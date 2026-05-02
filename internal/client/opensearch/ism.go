package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// ismPolicyEntryWire mirrors one row in the GET /_plugins/_ism/policies
// response. The plugin returns a {"policies":[{...}], "total_policies":N}
// envelope on the no-id form, and {"_id":"...","_seq_no":N,
// "_primary_term":N,"policy":{...}} on the per-id form. We decode both
// shapes and surface them as ISMPolicy regardless.
type ismPolicyEntryWire struct {
	ID          string         `json:"_id"`
	SeqNo       int64          `json:"_seq_no"`
	PrimaryTerm int64          `json:"_primary_term"`
	Policy      map[string]any `json:"policy"`
}

// ListPolicies implements client.ISMManager via
// GET /_plugins/_ism/policies[/<id>]. An empty id list hits the
// no-id form; a single id hits the per-id form. The plugin does not
// support comma-separated ids on this endpoint, so multi-id requests
// fan out to one HTTP call per id — kept sequential because policy
// lists in the wild are single-digit and the parallel orchestration
// would dwarf the latency.
func (c *Client) ListPolicies(ctx context.Context, ids []string) ([]types.ISMPolicy, error) {
	switch len(ids) {
	case 0:
		return c.listAllISMPolicies(ctx)
	case 1:
		p, err := c.getOneISMPolicy(ctx, ids[0])
		if err != nil {
			return nil, err
		}
		return []types.ISMPolicy{p}, nil
	default:
		// The plugin's GET /_plugins/_ism/policies/<id> endpoint takes
		// a single id, so multi-id queries fan out. Run them
		// concurrently with a small parallelism cap so a 50-id query
		// doesn't open 50 simultaneous connections to the cluster.
		// Any single failure cancels the rest via ctx.
		return c.fanOutGetISMPolicies(ctx, ids)
	}
}

// fanOutGetISMPolicies fetches the named policies concurrently with a
// bounded worker pool. Output preserves the input id order so a
// captured stdout is deterministic across runs. The first error
// returned by any worker cancels the remaining requests.
func (c *Client) fanOutGetISMPolicies(ctx context.Context, ids []string) ([]types.ISMPolicy, error) {
	const maxParallel = 4
	parallel := maxParallel
	if len(ids) < parallel {
		parallel = len(ids)
	}

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make([]types.ISMPolicy, len(ids))
	errs := make([]error, len(ids))
	jobs := make(chan int)

	var wg sync.WaitGroup
	for w := 0; w < parallel; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				p, err := c.getOneISMPolicy(cctx, ids[i])
				if err != nil {
					errs[i] = err
					cancel()
					return
				}
				results[i] = p
			}
		}()
	}
dispatch:
	for i := range ids {
		select {
		case <-cctx.Done():
			// One worker hit an error and cancelled the context (or the
			// caller's ctx fired). Stop dispatching; close(jobs) below
			// drains the workers cleanly. Bare `break` would only exit
			// the select — staticcheck SA4011 catches that.
			break dispatch
		case jobs <- i:
		}
	}
	close(jobs)
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

// GetPolicy implements client.ISMManager for the single-id lookup. The
// plugin's own 404 surfaces as ErrNotFound (wrapped) so the business
// layer can produce a precise message without scanning a 1-element list.
func (c *Client) GetPolicy(ctx context.Context, id string) (types.ISMPolicy, error) {
	return c.getOneISMPolicy(ctx, id)
}

func (c *Client) listAllISMPolicies(ctx context.Context) ([]types.ISMPolicy, error) {
	body, status, err := c.doISMRequest(ctx, http.MethodGet, "/_plugins/_ism/policies", nil)
	if err != nil {
		return nil, err
	}
	if err := classifyISMStatus("ism list_policies", status, body); err != nil {
		return nil, err
	}
	var wire struct {
		Policies      []ismPolicyEntryWire `json:"policies"`
		TotalPolicies int                  `json:"total_policies"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, errors.Join(errors.New("decoding ism list_policies response"), err)
	}
	out := make([]types.ISMPolicy, 0, len(wire.Policies))
	for _, p := range wire.Policies {
		out = append(out, types.ISMPolicy{
			ID:          p.ID,
			SeqNo:       p.SeqNo,
			PrimaryTerm: p.PrimaryTerm,
			Definition:  p.Policy,
		})
	}
	return out, nil
}

func (c *Client) getOneISMPolicy(ctx context.Context, id string) (types.ISMPolicy, error) {
	body, status, err := c.doISMRequest(ctx, http.MethodGet, "/_plugins/_ism/policies/"+id, nil)
	if err != nil {
		return types.ISMPolicy{}, err
	}
	if err := classifyISMStatus("ism get_policy", status, body); err != nil {
		return types.ISMPolicy{}, err
	}
	var wire ismPolicyEntryWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return types.ISMPolicy{}, errors.Join(errors.New("decoding ism get_policy response"), err)
	}
	if wire.ID == "" {
		// The single-id form always carries _id back. Empty here means
		// the plugin returned an unexpected shape — surface what we
		// know.
		wire.ID = id
	}
	return types.ISMPolicy{
		ID:          wire.ID,
		SeqNo:       wire.SeqNo,
		PrimaryTerm: wire.PrimaryTerm,
		Definition:  wire.Policy,
	}, nil
}

// PutPolicy implements client.ISMManager via
// PUT /_plugins/_ism/policies/<id>[?if_seq_no=N&if_primary_term=M].
// When SeqNo and PrimaryTerm are both zero, the request posts
// unconditionally — the create-or-overwrite path. When both are set,
// the plugin refuses with a 409 if the policy has been concurrently
// modified.
func (c *Client) PutPolicy(ctx context.Context, req types.ISMPolicyPutRequest) (types.ISMPolicyPutResult, error) {
	path := "/_plugins/_ism/policies/" + req.ID
	if req.SeqNo > 0 && req.PrimaryTerm > 0 {
		path += "?if_seq_no=" + strconv.FormatInt(req.SeqNo, 10) +
			"&if_primary_term=" + strconv.FormatInt(req.PrimaryTerm, 10)
	}
	body, status, err := c.doISMRequest(ctx, http.MethodPut, path, bytes.NewReader(req.Body))
	if err != nil {
		return types.ISMPolicyPutResult{}, err
	}
	if err := classifyISMStatus("ism put_policy", status, body); err != nil {
		return types.ISMPolicyPutResult{}, err
	}

	// On success the plugin returns the stored policy with fresh
	// _seq_no / _primary_term. Surface them so a chained edit can pick
	// up the new tokens without a follow-up GET.
	var wire ismPolicyEntryWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return types.ISMPolicyPutResult{}, errors.Join(errors.New("decoding ism put_policy response"), err)
	}
	return types.ISMPolicyPutResult{
		Action:      "ism-policy-put",
		ID:          req.ID,
		SeqNo:       wire.SeqNo,
		PrimaryTerm: wire.PrimaryTerm,
	}, nil
}

// DeletePolicy implements client.ISMManager via
// DELETE /_plugins/_ism/policies/<id>. The plugin refuses (typically
// 409) when the policy is still attached to managed indices; that error
// surfaces verbatim.
func (c *Client) DeletePolicy(ctx context.Context, req types.ISMPolicyDeleteRequest) (types.ISMPolicyDeleteResult, error) {
	body, status, err := c.doISMRequest(ctx, http.MethodDelete, "/_plugins/_ism/policies/"+req.ID, nil)
	if err != nil {
		return types.ISMPolicyDeleteResult{}, err
	}
	if err := classifyISMStatus("ism delete_policy", status, body); err != nil {
		return types.ISMPolicyDeleteResult{}, err
	}
	// The plugin returns {"_index":".opendistro-ism-config","_id":"...",
	// "result":"deleted","forced_refresh":true,...}. We surface the
	// "result == deleted" check as OK so a captured JSON record is
	// self-explanatory.
	var wire struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return types.ISMPolicyDeleteResult{}, errors.Join(errors.New("decoding ism delete_policy response"), err)
	}
	return types.ISMPolicyDeleteResult{
		Action: "ism-policy-delete",
		ID:     req.ID,
		OK:     strings.EqualFold(wire.Result, "deleted"),
	}, nil
}

// ExplainPolicy implements client.ISMManager via
// GET /_plugins/_ism/explain[/<index>][?show_policy=true]. The plugin
// returns a map keyed by index name plus a top-level "total_managed_indices"
// field; we decode the map and drop the counter.
//
// Indices reported with `enabled:false` and no managed_index_meta_data
// are surfaced as Managed=false so the operator scanning a pattern sees
// the gap rather than missing the index entirely.
func (c *Client) ExplainPolicy(ctx context.Context, req types.ISMExplainRequest) ([]types.ISMExplainEntry, error) {
	path := "/_plugins/_ism/explain"
	if req.Index != "" {
		path += "/" + req.Index
	}
	if req.ShowPolicy {
		path += "?show_policy=true"
	}

	body, status, err := c.doISMRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if err := classifyISMStatus("ism explain", status, body); err != nil {
		return nil, err
	}

	// Decode into a generic map first because the response mixes per-
	// index entries (object values) with a top-level
	// "total_managed_indices" int. Keys we don't recognise are dropped.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.Join(errors.New("decoding ism explain response"), err)
	}

	out := make([]types.ISMExplainEntry, 0, len(raw))
	for name, payload := range raw {
		if name == "total_managed_indices" {
			continue
		}
		var entry ismExplainEntryWire
		if err := json.Unmarshal(payload, &entry); err != nil {
			// A non-object value (the plugin sometimes embeds a count
			// per index name on errors) — skip rather than failing the
			// whole explain.
			continue
		}
		e := types.ISMExplainEntry{
			Index:    name,
			PolicyID: entry.PolicyID,
			Enabled:  entry.Enabled,
			Policy:   entry.Policy,
		}
		// Managed = the plugin tracks state for this index. The
		// presence of policy_seq_no on the metadata is the canonical
		// signal; we treat any populated meta block as "managed".
		if entry.Meta != nil {
			e.Managed = true
			if s, ok := entry.Meta["state"].(map[string]any); ok {
				if name, ok := s["name"].(string); ok {
					e.State = name
				}
			}
			if a, ok := entry.Meta["action"].(map[string]any); ok {
				if name, ok := a["name"].(string); ok {
					e.Action = name
				}
				if step, ok := a["step"].(map[string]any); ok {
					if name, ok := step["name"].(string); ok {
						e.Step = name
					}
				}
			}
			if info, ok := entry.Meta["info"].(map[string]any); ok {
				e.StepInfo = info
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// ExplainPolicyStream implements client.ISMManager. Streams the ISM
// explain envelope: {"index1": {...}, "index2": {...},
// "total_managed_indices": N}. Decodes one entry at a time so a
// `--show-policy` invocation against a multi-thousand-index fleet
// doesn't have to buffer tens of MB into memory before printing.
func (c *Client) ExplainPolicyStream(ctx context.Context, req types.ISMExplainRequest, yield func(types.ISMExplainEntry) error) error {
	path := "/_plugins/_ism/explain"
	if req.Index != "" {
		path += "/" + req.Index
	}
	if req.ShowPolicy {
		path += "?show_policy=true"
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(clientutil.NewBoundedReader(resp.Body, 64*1024, "ism explain (stream) error"))
		return classifyISMStatus("ism explain (stream)", resp.StatusCode, errBody)
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "ism explain (stream)")
	dec := json.NewDecoder(body)
	if tok, err := dec.Token(); err != nil {
		return errors.Join(errors.New("decoding ism explain (stream) response"), err)
	} else if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("ism explain (stream): expected JSON object, got %v", tok)
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return errors.Join(errors.New("decoding ism explain (stream) key"), err)
		}
		name, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("ism explain (stream): expected string key, got %v", keyTok)
		}
		if name == "total_managed_indices" {
			// Skip the counter.
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return errors.Join(errors.New("skipping ism explain (stream) total_managed_indices"), err)
			}
			continue
		}
		var entry ismExplainEntryWire
		if err := dec.Decode(&entry); err != nil {
			// A non-object value (the plugin sometimes embeds a per-index
			// counter on errors) — skip rather than failing the whole
			// stream. Mirrors the buffered ExplainPolicy behaviour.
			continue
		}
		e := types.ISMExplainEntry{
			Index:    name,
			PolicyID: entry.PolicyID,
			Enabled:  entry.Enabled,
			Policy:   entry.Policy,
		}
		if entry.Meta != nil {
			e.Managed = true
			if s, ok := entry.Meta["state"].(map[string]any); ok {
				if n, ok := s["name"].(string); ok {
					e.State = n
				}
			}
			if a, ok := entry.Meta["action"].(map[string]any); ok {
				if n, ok := a["name"].(string); ok {
					e.Action = n
				}
				if step, ok := a["step"].(map[string]any); ok {
					if n, ok := step["name"].(string); ok {
						e.Step = n
					}
				}
			}
			if info, ok := entry.Meta["info"].(map[string]any); ok {
				e.StepInfo = info
			}
		}
		if err := yield(e); err != nil {
			return err
		}
	}
	return nil
}

// ismExplainEntryWire is the per-index payload from the explain endpoint.
// The plugin uses a deeply nested layout: "policy_id" and "enabled" sit
// at the top of each index entry, while the runtime state hangs off
// "managed_index_meta_data". We carry meta as a generic map and pluck
// the operator-facing fields in the caller — the inner shape evolves
// across plugin versions and a tightly-typed wire would break on every
// minor.
type ismExplainEntryWire struct {
	Index    string         `json:"index"`
	PolicyID string         `json:"policy_id"`
	Enabled  bool           `json:"enabled"`
	Policy   map[string]any `json:"policy,omitempty"`
	Meta     map[string]any `json:"managed_index_meta_data"`
}

// doISMRequest is the raw-HTTP escape hatch ISM needs because the
// plugin's admin API has no typed helper in opensearch-go/v4. Body and
// status come back paired so the caller can either decode JSON or
// surface a non-2xx with the cluster's reason in the message. The
// response body is bounded by DefaultMaxResponseBytes so a runaway
// `ism explain --show-policy` against a multi-thousand-index fleet
// fails clean instead of buffering hundreds of MB into the operator's
// heap.
func (c *Client) doISMRequest(ctx context.Context, method, path string, body io.Reader) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.api.Client.Perform(req)
	if err != nil {
		return nil, 0, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	op := method + " " + path
	respBody, err := io.ReadAll(clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, op))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return respBody, resp.StatusCode, nil
}

// classifyISMStatus mirrors ClassifyStatus but is local so the import
// surface stays small and so a future ISM-specific error envelope (the
// plugin sometimes wraps errors in a different shape) is a per-package
// fix rather than a shared change.
func classifyISMStatus(op string, status int, body []byte) error {
	switch status {
	case http.StatusUnauthorized:
		return esopsclient.ErrAuth
	case http.StatusForbidden:
		return esopsclient.ErrForbidden
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s returned HTTP 404: %s", esopsclient.ErrNotFound, op, formatISMError(body))
	}
	if status >= 400 {
		return fmt.Errorf("%s returned HTTP %d: %s", op, status, formatISMError(body))
	}
	return nil
}

// formatISMError extracts the load-bearing reason from an ISM error
// body. The plugin uses the same {error:{type,reason}} shape as the
// core cluster on most paths, with the occasional plain-string variant.
func formatISMError(body []byte) string {
	if len(body) == 0 {
		return "(empty response body)"
	}
	var typed struct {
		Error struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &typed); err == nil {
		switch {
		case typed.Error.Reason != "":
			return typed.Error.Reason
		case typed.Message != "":
			return typed.Message
		}
	}
	s := strings.TrimSpace(string(body))
	const max = 200
	if len(s) > max {
		s = s[:max-1] + "…"
	}
	return s
}

// Compile-time guarantee that this Client satisfies the ISM capability.
// ILM intentionally not implemented — see CLAUDE.md §3 and the factory.
var _ esopsclient.ISMManager = (*Client)(nil)
