package snapshot

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// PolicyListOptions narrows a `snapshot policy list` call. Names is a
// repeatable filter forwarded to the inspector; on ES the cluster
// honours a comma-joined name list at the path segment, on OS the
// adapter fans out per-id. Empty names returns every policy.
type PolicyListOptions struct {
	Names []string
}

// ListPolicies returns the cluster's snapshot policies, sorted by name
// for deterministic output. Pass-through over the capability today;
// kept as its own function so a future filter (e.g. by repository name
// or in-progress flag) lands in one place.
func ListPolicies(ctx context.Context, c client.SnapshotPolicyInspector, opts PolicyListOptions) ([]types.SnapshotPolicy, error) {
	rows, err := c.ListSnapshotPolicies(ctx, opts.Names)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

// GetPolicy fetches a single policy by name. The capability returns
// ErrNotFound (wrapped) on a 404 so the CLI can exit with a clean
// message; everything else surfaces verbatim.
func GetPolicy(ctx context.Context, c client.SnapshotPolicyInspector, name string) (types.SnapshotPolicy, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return types.SnapshotPolicy{}, client.UsageError("--name is required")
	}
	if client.IsPatternish(name) {
		return types.SnapshotPolicy{}, client.UsageError("--name must be a literal policy name, not a pattern (got %q)", name)
	}
	return c.GetSnapshotPolicy(ctx, name)
}

// PolicyPutOptions are the knobs the CLI hands to PutPolicy. Body is
// the raw JSON payload — the cluster validates the inner shape (ES
// expects schedule/name/repository/config; OS expects creation/
// deletion/snapshot_config). DryRun does not contact the cluster; the
// cluster has no native dry-run for either dialect's policy PUT, so
// dry-run is a client-side "validate + report" path.
type PolicyPutOptions struct {
	Name   string
	Body   []byte
	DryRun bool
}

// PutPolicy is the business-layer entry point for `snapshot policy
// put`. Validates the name is a literal, the body is non-empty and
// parses as a JSON object, then forwards to the capability.
func PutPolicy(ctx context.Context, c client.SnapshotPolicyManager, opts PolicyPutOptions) (types.SnapshotPolicyPutResult, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return types.SnapshotPolicyPutResult{}, client.UsageError("--name is required")
	}
	if client.IsPatternish(name) {
		return types.SnapshotPolicyPutResult{}, client.UsageError("--name must be a literal policy name, not a pattern (got %q)", name)
	}
	if len(opts.Body) == 0 {
		return types.SnapshotPolicyPutResult{}, client.UsageError("policy body is required (use --body or --from-file)")
	}
	if err := validateJSONObject(opts.Body); err != nil {
		return types.SnapshotPolicyPutResult{}, client.UsageError("policy body: %v", err)
	}

	if opts.DryRun {
		return types.SnapshotPolicyPutResult{
			Action:       "policy-put",
			DryRun:       true,
			Name:         name,
			Acknowledged: true,
		}, nil
	}

	result, err := c.PutSnapshotPolicy(ctx, types.SnapshotPolicyPutRequest{
		Name: name,
		Body: opts.Body,
	})
	if err != nil {
		return types.SnapshotPolicyPutResult{}, err
	}
	return result, nil
}

// PolicyDeleteOptions are the knobs the CLI hands to DeletePolicy.
// Name is a literal policy name — neither product accepts patterns on
// the delete path, mirrored client-side so a typo doesn't reach the
// cluster as an empty path segment.
type PolicyDeleteOptions struct {
	Name   string
	DryRun bool
}

// DeletePolicy is the business-layer entry point for `snapshot policy
// delete`. Refuses patterns up-front and short-circuits on dry-run.
func DeletePolicy(ctx context.Context, c client.SnapshotPolicyManager, opts PolicyDeleteOptions) (types.SnapshotPolicyDeleteResult, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return types.SnapshotPolicyDeleteResult{}, client.UsageError("--name is required")
	}
	if client.IsPatternish(name) {
		return types.SnapshotPolicyDeleteResult{}, client.UsageError("--name must be a literal policy name, not a pattern (got %q)", name)
	}

	if opts.DryRun {
		return types.SnapshotPolicyDeleteResult{
			Action:       "policy-delete",
			DryRun:       true,
			Name:         name,
			Acknowledged: true,
		}, nil
	}

	return c.DeleteSnapshotPolicy(ctx, types.SnapshotPolicyDeleteRequest{Name: name})
}

// PolicyExecuteOptions are the knobs the CLI hands to ExecutePolicy.
// Name is required and literal. DryRun short-circuits without contacting
// the cluster — neither dialect has a native dry-run for the
// execute path, so this is a "validate + report" stub.
type PolicyExecuteOptions struct {
	Name   string
	DryRun bool
}

// ExecutePolicy is the business-layer entry point for `snapshot policy
// execute`. The cluster-side semantics differ by dialect (ES SLM
// synthesises an immediate snapshot; OS SM toggles enabled and waits
// for the next cron tick) — the result surface preserves whatever the
// cluster returned, including the synthesised snapshot name on ES.
func ExecutePolicy(ctx context.Context, c client.SnapshotPolicyManager, opts PolicyExecuteOptions) (types.SnapshotPolicyExecuteResult, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return types.SnapshotPolicyExecuteResult{}, client.UsageError("--name is required")
	}
	if client.IsPatternish(name) {
		return types.SnapshotPolicyExecuteResult{}, client.UsageError("--name must be a literal policy name, not a pattern (got %q)", name)
	}

	if opts.DryRun {
		return types.SnapshotPolicyExecuteResult{
			Action:       "policy-execute",
			DryRun:       true,
			Name:         name,
			Acknowledged: true,
		}, nil
	}

	return c.ExecuteSnapshotPolicy(ctx, types.SnapshotPolicyExecuteRequest{Name: name})
}

// validateJSONObject returns an error if body is not a top-level JSON
// object. Both ES and OS reject non-object bodies on the policy PUT
// endpoints; a client-side check turns the cluster's terse 400 into a
// precise message before the request lands in the audit log.
func validateJSONObject(body []byte) error {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return err
	}
	if _, ok := v.(map[string]any); !ok {
		return client.UsageError("expected a JSON object")
	}
	return nil
}
