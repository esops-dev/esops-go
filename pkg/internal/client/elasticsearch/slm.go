package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/elastic/go-elasticsearch/v9/esapi"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// slmPolicyEntryWire mirrors one entry in the GET /_slm/policy[/<name>]
// response. The cluster keys the response by policy name; we decode the
// value into this struct and surface Name from the map key. Stats blocks
// (`stats`, `policy_stats`) are intentionally dropped — `policy get`
// surfaces operator-facing metadata, the cluster's per-execution
// counters belong in `_slm/stats` if a future command exposes them.
type slmPolicyEntryWire struct {
	Version          int64                       `json:"version"`
	ModifiedDate     string                      `json:"modified_date"`
	ModifiedDateMS   int64                       `json:"modified_date_millis"`
	Policy           slmPolicyDefinitionWire     `json:"policy"`
	LastSuccess      *slmPolicyExecutionWire     `json:"last_success,omitempty"`
	LastFailure      *slmPolicyExecutionWire     `json:"last_failure,omitempty"`
	NextExecutionMS  int64                       `json:"next_execution_millis"`
	NextExecutionISO string                      `json:"next_execution"`
	InProgress       *slmPolicyInProgressWire    `json:"in_progress,omitempty"`
}

// slmPolicyDefinitionWire is the inner `policy` block. We carry a
// generic map for the renderer + a few typed projections so the table
// view doesn't have to type-assert every cell.
type slmPolicyDefinitionWire struct {
	Name       string         `json:"name"`
	Schedule   string         `json:"schedule"`
	Repository string         `json:"repository"`
	Config     map[string]any `json:"config,omitempty"`
	Retention  map[string]any `json:"retention,omitempty"`
}

type slmPolicyExecutionWire struct {
	SnapshotName string `json:"snapshot_name"`
	TimeMS       int64  `json:"time_in_millis"`
}

type slmPolicyInProgressWire struct {
	Name      string `json:"name"`
	UUID      string `json:"uuid"`
	StartedMS int64  `json:"start_time_millis"`
}

// ListSnapshotPolicies implements client.SnapshotPolicyInspector via
// GET /_slm/policy[/<names>]. Empty names returns every policy. The
// cluster honours comma-separated names in the path segment, matching
// the ES ILM behaviour, so multi-name fans out in a single round-trip.
func (c *Client) ListSnapshotPolicies(ctx context.Context, names []string) ([]types.SnapshotPolicy, error) {
	gl := c.api.SlmGetLifecycle
	opts := []func(*esapi.SlmGetLifecycleRequest){gl.WithContext(ctx)}
	if len(names) > 0 {
		opts = append(opts, gl.WithPolicyID(strings.Join(names, ",")))
	}

	resp, err := c.api.SlmGetLifecycle(opts...)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("slm get_lifecycle", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var wire map[string]slmPolicyEntryWire
	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "slm get_lifecycle")
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding slm get_lifecycle response"), err)
	}

	out := make([]types.SnapshotPolicy, 0, len(wire))
	for name, p := range wire {
		out = append(out, slmPolicyFromWire(name, p))
	}
	return out, nil
}

// GetSnapshotPolicy implements client.SnapshotPolicyInspector for the
// single-name lookup. Returns ErrNotFound (wrapped) when the cluster
// 404s — ES SLM 404s on a missing policy id rather than returning an
// empty map, matching the ILM convention.
func (c *Client) GetSnapshotPolicy(ctx context.Context, name string) (types.SnapshotPolicy, error) {
	rows, err := c.ListSnapshotPolicies(ctx, []string{name})
	if err != nil {
		return types.SnapshotPolicy{}, err
	}
	for _, p := range rows {
		if p.Name == name {
			return p, nil
		}
	}
	return types.SnapshotPolicy{}, fmt.Errorf("%w: slm policy %q", esopsclient.ErrNotFound, name)
}

// PutSnapshotPolicy implements client.SnapshotPolicyManager via
// PUT /_slm/policy/<name>. Body is forwarded verbatim — the cluster
// validates the inner shape (schedule, name pattern, repository,
// config, retention). The wire response is {"acknowledged":true} on
// success.
func (c *Client) PutSnapshotPolicy(ctx context.Context, req types.SnapshotPolicyPutRequest) (types.SnapshotPolicyPutResult, error) {
	pl := c.api.SlmPutLifecycle
	opts := []func(*esapi.SlmPutLifecycleRequest){pl.WithContext(ctx)}

	resp, err := c.api.SlmPutLifecycle(bytes.NewReader(req.Body), req.Name, opts...)
	if err != nil {
		return types.SnapshotPolicyPutResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("slm put_lifecycle", resp.StatusCode, resp.Body); err != nil {
		return types.SnapshotPolicyPutResult{}, err
	}

	var wire struct {
		Acknowledged bool `json:"acknowledged"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.SnapshotPolicyPutResult{}, errors.Join(errors.New("decoding slm put_lifecycle response"), err)
	}
	return types.SnapshotPolicyPutResult{
		Action:       "policy-put",
		Name:         req.Name,
		Acknowledged: wire.Acknowledged,
	}, nil
}

// DeleteSnapshotPolicy implements client.SnapshotPolicyManager via
// DELETE /_slm/policy/<name>. The cluster's only failure mode here is
// "policy not found"; we surface it as ErrNotFound so the CLI exits
// cleanly rather than reporting a generic 4xx.
func (c *Client) DeleteSnapshotPolicy(ctx context.Context, req types.SnapshotPolicyDeleteRequest) (types.SnapshotPolicyDeleteResult, error) {
	dl := c.api.SlmDeleteLifecycle
	resp, err := c.api.SlmDeleteLifecycle(req.Name, dl.WithContext(ctx))
	if err != nil {
		return types.SnapshotPolicyDeleteResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("slm delete_lifecycle", resp.StatusCode, resp.Body); err != nil {
		return types.SnapshotPolicyDeleteResult{}, err
	}

	var wire struct {
		Acknowledged bool `json:"acknowledged"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.SnapshotPolicyDeleteResult{}, errors.Join(errors.New("decoding slm delete_lifecycle response"), err)
	}
	return types.SnapshotPolicyDeleteResult{
		Action:       "policy-delete",
		Name:         req.Name,
		Acknowledged: wire.Acknowledged,
	}, nil
}

// ExecuteSnapshotPolicy implements client.SnapshotPolicyManager via
// POST /_slm/policy/<name>/_execute. The cluster synthesises a one-off
// snapshot from the policy template and returns its name in
// `snapshot_name`. The CLI surfaces the synthesised name so an
// operator can `snapshot list` it shortly afterwards.
func (c *Client) ExecuteSnapshotPolicy(ctx context.Context, req types.SnapshotPolicyExecuteRequest) (types.SnapshotPolicyExecuteResult, error) {
	el := c.api.SlmExecuteLifecycle
	resp, err := c.api.SlmExecuteLifecycle(req.Name, el.WithContext(ctx))
	if err != nil {
		return types.SnapshotPolicyExecuteResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("slm execute_lifecycle", resp.StatusCode, resp.Body); err != nil {
		return types.SnapshotPolicyExecuteResult{}, err
	}

	var wire struct {
		SnapshotName string `json:"snapshot_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.SnapshotPolicyExecuteResult{}, errors.Join(errors.New("decoding slm execute_lifecycle response"), err)
	}
	return types.SnapshotPolicyExecuteResult{
		Action:       "policy-execute",
		Name:         req.Name,
		SnapshotName: wire.SnapshotName,
		Acknowledged: true,
	}, nil
}

// slmPolicyFromWire flattens an SLM list entry into the neutral
// SnapshotPolicy view. Definition carries the cluster's `policy` block
// as a map[string]any so renderers can pretty-print it without a
// re-marshal/decode round-trip.
func slmPolicyFromWire(name string, p slmPolicyEntryWire) types.SnapshotPolicy {
	def := map[string]any{}
	if p.Policy.Name != "" {
		def["name"] = p.Policy.Name
	}
	if p.Policy.Schedule != "" {
		def["schedule"] = p.Policy.Schedule
	}
	if p.Policy.Repository != "" {
		def["repository"] = p.Policy.Repository
	}
	if len(p.Policy.Config) > 0 {
		def["config"] = p.Policy.Config
	}
	if len(p.Policy.Retention) > 0 {
		def["retention"] = p.Policy.Retention
	}

	out := types.SnapshotPolicy{
		Name:                name,
		Repository:          p.Policy.Repository,
		Schedule:            p.Policy.Schedule,
		Enabled:             true, // ES SLM has no per-policy enable/disable; treat present as enabled.
		Version:             p.Version,
		ModifiedDateMillis:  p.ModifiedDateMS,
		NextExecutionMillis: p.NextExecutionMS,
		Definition:          def,
	}
	if p.LastSuccess != nil {
		out.LastSuccessMillis = p.LastSuccess.TimeMS
	}
	if p.LastFailure != nil {
		out.LastFailureMillis = p.LastFailure.TimeMS
	}
	return out
}

// Compile-time guarantee that this Client satisfies the SnapshotPolicy
// surface. The OS adapter implements the same interface against
// /_plugins/_sm/policies; the factory wires whichever the dialect
// resolves to.
var (
	_ esopsclient.SnapshotPolicyInspector = (*Client)(nil)
	_ esopsclient.SnapshotPolicyManager   = (*Client)(nil)
)
