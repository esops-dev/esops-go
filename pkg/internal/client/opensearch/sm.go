package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// smInternalIDSuffix is what the SM plugin appends to every stored
// policy's `_id`: the operator PUTs "nightly" and the cluster stores
// "nightly-sm-policy". Both forms address the policy on the URL path,
// but only the bare name is what the operator typed. Stripping the
// suffix on the read path keeps the CLI consistent with ES SLM (which
// has no such suffix) and matches operator intent — they don't care
// about the plugin's internal naming scheme.
const smInternalIDSuffix = "-sm-policy"

// smPolicyEntryWire mirrors one row in the GET /_plugins/_sm/policies
// response. The plugin returns a {"policies":[{...}], "total_policies":N}
// envelope on the no-id form, and {"_id":"...","_seq_no":N,
// "_primary_term":N,"sm_policy":{...}} on the per-id form. We decode
// both shapes and surface them as SnapshotPolicy regardless. Mirrors
// ismPolicyEntryWire — the SM and ISM plugins evolved together and use
// the same response shell on most paths.
type smPolicyEntryWire struct {
	ID            string         `json:"_id"`
	SeqNo         int64          `json:"_seq_no"`
	PrimaryTerm   int64          `json:"_primary_term"`
	Policy        map[string]any `json:"sm_policy"`
	LastUpdatedMS int64          `json:"last_updated_time_millis"` // populated on some plugin versions
}

// ListSnapshotPolicies implements client.SnapshotPolicyInspector via
// GET /_plugins/_sm/policies. The plugin does not accept comma-
// separated names on this endpoint; multi-name requests fan out to
// per-id GETs (mirrors ISM). Empty input lists every policy.
func (c *Client) ListSnapshotPolicies(ctx context.Context, names []string) ([]types.SnapshotPolicy, error) {
	switch len(names) {
	case 0:
		return c.listAllSMPolicies(ctx)
	case 1:
		p, err := c.getOneSMPolicy(ctx, names[0])
		if err != nil {
			return nil, err
		}
		return []types.SnapshotPolicy{p}, nil
	default:
		// Sequential here: SM policy lists are single-digit in the wild,
		// fan-out parallelism matters less than for ISM (which sees
		// thousands of indices).
		out := make([]types.SnapshotPolicy, 0, len(names))
		for _, name := range names {
			p, err := c.getOneSMPolicy(ctx, name)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
		return out, nil
	}
}

// GetSnapshotPolicy implements client.SnapshotPolicyInspector for the
// single-name lookup. The plugin's own 404 surfaces as ErrNotFound
// (wrapped) so the business layer can produce a precise message.
func (c *Client) GetSnapshotPolicy(ctx context.Context, name string) (types.SnapshotPolicy, error) {
	return c.getOneSMPolicy(ctx, name)
}

func (c *Client) listAllSMPolicies(ctx context.Context) ([]types.SnapshotPolicy, error) {
	body, status, err := c.doISMRequest(ctx, http.MethodGet, "/_plugins/_sm/policies", nil)
	if err != nil {
		return nil, err
	}
	if err := classifyISMStatus("sm list_policies", status, body); err != nil {
		return nil, err
	}
	var wire struct {
		Policies      []smPolicyEntryWire `json:"policies"`
		TotalPolicies int                 `json:"total_policies"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, errors.Join(errors.New("decoding sm list_policies response"), err)
	}
	out := make([]types.SnapshotPolicy, 0, len(wire.Policies))
	for _, p := range wire.Policies {
		out = append(out, smPolicyFromWire(p))
	}
	return out, nil
}

func (c *Client) getOneSMPolicy(ctx context.Context, name string) (types.SnapshotPolicy, error) {
	body, status, err := c.doISMRequest(ctx, http.MethodGet, "/_plugins/_sm/policies/"+name, nil)
	if err != nil {
		return types.SnapshotPolicy{}, err
	}
	if err := classifyISMStatus("sm get_policy", status, body); err != nil {
		return types.SnapshotPolicy{}, err
	}
	var wire smPolicyEntryWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return types.SnapshotPolicy{}, errors.Join(errors.New("decoding sm get_policy response"), err)
	}
	if wire.ID == "" {
		// The per-id form always carries _id back. Empty here means the
		// plugin returned an unexpected shape — surface the requested
		// name so the result stays interpretable.
		wire.ID = name
	}
	return smPolicyFromWire(wire), nil
}

// PutSnapshotPolicy implements client.SnapshotPolicyManager via the SM
// plugin's split create-vs-update surface. The plugin's API treats the
// two operations as distinct verbs:
//
//	POST /_plugins/_sm/policies/<name>                                   creates
//	PUT  /_plugins/_sm/policies/<name>?if_seq_no=N&if_primary_term=M     updates
//
// POST without tokens 409s when the policy already exists; PUT without
// tokens 400s with "Sequence number and primary term must be provided
// when updating" — the plugin doesn't accept an unconditional PUT at
// all. The CLI surface is "here's my body, install it (create or
// update)", so the adapter implements that intent: POST first, and on
// conflict fetch the current tokens and PUT with them. The single
// extra round-trip on update is the cost of preserving the operator's
// "create or update" semantics across both products' policy verbs.
func (c *Client) PutSnapshotPolicy(ctx context.Context, req types.SnapshotPolicyPutRequest) (types.SnapshotPolicyPutResult, error) {
	body, status, err := c.doISMRequest(ctx, http.MethodPost, "/_plugins/_sm/policies/"+req.Name, bytes.NewReader(req.Body))
	if err != nil {
		return types.SnapshotPolicyPutResult{}, err
	}
	if status == http.StatusConflict {
		// Policy already exists — fetch the current concurrency tokens
		// and re-issue as a conditional PUT. This is the create-or-
		// update path operators expect from a single CLI verb.
		seqNo, primaryTerm, err := c.fetchSMTokens(ctx, req.Name)
		if err != nil {
			return types.SnapshotPolicyPutResult{}, fmt.Errorf("sm put_policy %q: refreshing concurrency tokens: %w", req.Name, err)
		}
		path := fmt.Sprintf("/_plugins/_sm/policies/%s?if_seq_no=%d&if_primary_term=%d",
			req.Name, seqNo, primaryTerm,
		)
		body, status, err = c.doISMRequest(ctx, http.MethodPut, path, bytes.NewReader(req.Body))
		if err != nil {
			return types.SnapshotPolicyPutResult{}, err
		}
	}
	if err := classifyISMStatus("sm put_policy", status, body); err != nil {
		return types.SnapshotPolicyPutResult{}, err
	}

	// classifyISMStatus already covers the only failure modes that
	// matter to the operator (auth, conflict, malformed body, plugin
	// rejection). The cluster's success body echoes the stored policy
	// with fresh _seq_no / _primary_term — useful for a chained edit,
	// but `policy get` retrieves them on demand and the put result
	// shape stays narrow. Skipping the decode shaves an unmarshal
	// over a multi-KB response on every put.
	return types.SnapshotPolicyPutResult{
		Action:       "policy-put",
		Name:         req.Name,
		Acknowledged: true,
	}, nil
}

// fetchSMTokens reads the SM plugin's optimistic-concurrency tokens
// for a single policy. We can't reuse smPolicyFromWire here — the
// neutral SnapshotPolicy view only carries the inner `sm_policy`
// block in Definition, while the tokens (`_seq_no` / `_primary_term`)
// live alongside it at the wire root. Decoding straight into
// smPolicyEntryWire pulls the right fields without leaking SM-
// specifics into the public API.
//
// The cluster's own 404 (policy went missing between our POST and
// this re-fetch) surfaces as ErrNotFound — extremely rare race, but
// the operator sees a precise message rather than a generic 4xx.
func (c *Client) fetchSMTokens(ctx context.Context, name string) (seqNo int64, primaryTerm int64, err error) {
	body, status, err := c.doISMRequest(ctx, http.MethodGet, "/_plugins/_sm/policies/"+name, nil)
	if err != nil {
		return 0, 0, err
	}
	if err := classifyISMStatus("sm get_policy (token refresh)", status, body); err != nil {
		return 0, 0, err
	}
	var wire smPolicyEntryWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return 0, 0, errors.Join(errors.New("decoding sm get_policy (token refresh) response"), err)
	}
	return wire.SeqNo, wire.PrimaryTerm, nil
}

// DeleteSnapshotPolicy implements client.SnapshotPolicyManager via
// DELETE /_plugins/_sm/policies/<name>. The plugin acks with the
// same {"_index":".opendistro-...","_id":"...","result":"deleted",...}
// envelope ISM uses; we surface "result == deleted" as Acknowledged.
func (c *Client) DeleteSnapshotPolicy(ctx context.Context, req types.SnapshotPolicyDeleteRequest) (types.SnapshotPolicyDeleteResult, error) {
	body, status, err := c.doISMRequest(ctx, http.MethodDelete, "/_plugins/_sm/policies/"+req.Name, nil)
	if err != nil {
		return types.SnapshotPolicyDeleteResult{}, err
	}
	if err := classifyISMStatus("sm delete_policy", status, body); err != nil {
		return types.SnapshotPolicyDeleteResult{}, err
	}
	var wire struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return types.SnapshotPolicyDeleteResult{}, errors.Join(errors.New("decoding sm delete_policy response"), err)
	}
	return types.SnapshotPolicyDeleteResult{
		Action:       "policy-delete",
		Name:         req.Name,
		Acknowledged: wire.Result == "deleted",
	}, nil
}

// ExecuteSnapshotPolicy implements client.SnapshotPolicyManager via
// POST /_plugins/_sm/policies/<name>/_start. Note the semantic
// divergence from ES SLM `_execute`: OS SM `_start` toggles the
// policy's `enabled` flag on; the snapshot lands on the next scheduled
// cron tick rather than immediately. SnapshotName is therefore left
// empty in the result — `snapshot list --repository <repo>` shortly
// after the next tick is the way to confirm a snapshot landed.
//
// The plugin returns {"acknowledged":true} on success. Returning
// ErrUnsupported on `_start`-against-an-already-running policy isn't
// the right answer here — the plugin treats it as a no-op and acks
// regardless, so esops mirrors that and surfaces the cluster's reason
// only when the call genuinely fails.
func (c *Client) ExecuteSnapshotPolicy(ctx context.Context, req types.SnapshotPolicyExecuteRequest) (types.SnapshotPolicyExecuteResult, error) {
	body, status, err := c.doISMRequest(ctx, http.MethodPost, "/_plugins/_sm/policies/"+req.Name+"/_start", nil)
	if err != nil {
		return types.SnapshotPolicyExecuteResult{}, err
	}
	if err := classifyISMStatus("sm start_policy", status, body); err != nil {
		return types.SnapshotPolicyExecuteResult{}, err
	}
	var wire struct {
		Acknowledged bool `json:"acknowledged"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return types.SnapshotPolicyExecuteResult{}, errors.Join(errors.New("decoding sm start_policy response"), err)
	}
	if !wire.Acknowledged {
		return types.SnapshotPolicyExecuteResult{}, fmt.Errorf("sm start_policy %q: cluster did not acknowledge", req.Name)
	}
	return types.SnapshotPolicyExecuteResult{
		Action:       "policy-execute",
		Name:         req.Name,
		Acknowledged: true,
	}, nil
}

// smPolicyFromWire flattens an SM list/get entry into the neutral
// SnapshotPolicy view. Definition carries the cluster's `sm_policy`
// block as a map[string]any so renderers can pretty-print it without
// a re-marshal/decode round-trip.
//
// Repository is plucked from `snapshot_config.repository`; Schedule
// from `creation.schedule.cron.expression` when present. Both fall
// back to empty strings rather than synthesising defaults — the
// neutral view says "the cluster did not advertise this", and the
// renderer prints "-".
//
// Name is normalised by stripping the plugin's `-sm-policy` suffix:
// "nightly-sm-policy" → "nightly". Operators put the bare name and
// expect to see it back; the suffix is an internal-id detail that
// would otherwise force them to learn it.
func smPolicyFromWire(p smPolicyEntryWire) types.SnapshotPolicy {
	out := types.SnapshotPolicy{
		Name:               strings.TrimSuffix(p.ID, smInternalIDSuffix),
		ModifiedDateMillis: p.LastUpdatedMS,
		Definition:         p.Policy,
	}
	if p.Policy == nil {
		return out
	}
	if cfg, ok := p.Policy["snapshot_config"].(map[string]any); ok {
		if repo, ok := cfg["repository"].(string); ok {
			out.Repository = repo
		}
	}
	if cre, ok := p.Policy["creation"].(map[string]any); ok {
		if sched, ok := cre["schedule"].(map[string]any); ok {
			if cron, ok := sched["cron"].(map[string]any); ok {
				if expr, ok := cron["expression"].(string); ok {
					out.Schedule = expr
				}
			}
		}
	}
	if enabled, ok := p.Policy["enabled"].(bool); ok {
		out.Enabled = enabled
	} else {
		// SM defaults to enabled when the field is omitted on PUT.
		out.Enabled = true
	}
	return out
}

// Compile-time guarantee that this Client satisfies the SnapshotPolicy
// surface. The ES adapter implements the same interface against
// /_slm/policy; the factory wires whichever the dialect resolves to.
var (
	_ esopsclient.SnapshotPolicyInspector = (*Client)(nil)
	_ esopsclient.SnapshotPolicyManager   = (*Client)(nil)
)
