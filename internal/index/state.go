package index

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// DefaultMaxIndices is the blast-radius cap for destructive index
// operations resolved from a pattern. `index close` (and friends)
// refuse when the resolved match exceeds this threshold without an
// explicit --max-indices opt-in. Centralised here so the CLI flag's
// --help and the business rule stay in sync.
const DefaultMaxIndices = 20

// StateChangeOptions are the knobs the CLI layer hands to ChangeState.
// Pattern is required (an unbounded `*` is a footgun the cluster will
// happily accept; refusing it client-side is part of the safety story).
// MaxIndices is the operator's opt-in past the default 20-index cap; 0
// keeps the default in force.
//
// DryRun is the global --dry-run flag: when true, the cluster is not
// contacted; the function returns the resolved would-be transition.
//
// IgnoreUnavailable / WaitForActiveShards mirror the cluster's params
// of the same name and are passed through to the adapter on the live
// path. They are not applied to the dry-run path — dry-run answers
// "what would this match in the current cluster state" and that
// answer doesn't depend on either knob.
type StateChangeOptions struct {
	Action              types.IndexStateAction
	Pattern             string
	MaxIndices          int
	DryRun              bool
	IgnoreUnavailable   bool
	WaitForActiveShards string
}

// ChangeState is the business-layer entry point for `esops index open`
// and `esops index close`. It:
//
//  1. Validates the pattern (non-empty; rejects bare "*" / "_all").
//  2. Resolves the pattern via IndexInspector and partitions matches
//     into "needs to change" and "already in target state".
//  3. Enforces the blast-radius limit on the would-change set: more
//     than MaxIndices (default DefaultMaxIndices) returns ErrBlastRadius.
//  4. On dry-run, returns the resolved view with Acknowledged=true (a
//     synthetic "yes, this would have worked") and DryRun=true.
//  5. Otherwise calls the IndexStateChanger and returns its response
//     enriched with the resolved Indices/Skipped lists.
//
// When the resolved pattern matches nothing, the function returns a
// no-op IndexStateChange (Acknowledged=true, empty Indices) without
// hitting the cluster — useful so a pattern that matched yesterday
// but is empty today returns success rather than a 404 spike.
func ChangeState(ctx context.Context, inspector client.IndexInspector, changer client.IndexStateChanger, opts StateChangeOptions) (types.IndexStateChange, error) {
	if err := validateStateChangeOptions(opts); err != nil {
		return types.IndexStateChange{}, err
	}

	rows, err := inspector.IndicesNamesStatus(ctx, types.IndexFilter{
		Index:  opts.Pattern,
		Hidden: true, // closed indices and dot-prefixed system indices both
		// need to be visible — the operator named them in the pattern.
	})
	if err != nil {
		return types.IndexStateChange{}, err
	}

	target, skipped := partitionByTargetState(rows, opts.Action)
	limit := opts.MaxIndices
	if limit <= 0 {
		limit = DefaultMaxIndices
	}
	if len(target) > limit {
		return types.IndexStateChange{}, fmt.Errorf(
			"%w: pattern %q resolves to %d indices to %s (limit %d); pass --max-indices N to override",
			client.ErrBlastRadius, opts.Pattern, len(target), opts.Action, limit,
		)
	}

	result := types.IndexStateChange{
		Action:  opts.Action,
		DryRun:  opts.DryRun,
		Indices: target,
		Skipped: skipped,
	}

	// No-op fast path: nothing to change. Both branches converge here —
	// dry-run and live both return acknowledged=true so scripts can
	// treat "everything was already in state" as success.
	if len(target) == 0 {
		result.Acknowledged = true
		result.ShardsAcknowledged = true
		return result, nil
	}

	if opts.DryRun {
		result.Acknowledged = true
		result.ShardsAcknowledged = true
		return result, nil
	}

	// Send the resolved name list to the cluster, not the original
	// pattern. The pattern may also include closed indices we'd skip;
	// passing the explicit list keeps the cluster's view aligned with
	// what the operator sees in the result.
	cReq := types.IndexStateChangeRequest{
		Indices:             target,
		IgnoreUnavailable:   opts.IgnoreUnavailable,
		WaitForActiveShards: opts.WaitForActiveShards,
	}

	var changed types.IndexStateChange
	switch opts.Action {
	case types.IndexStateOpen:
		changed, err = changer.OpenIndex(ctx, cReq)
	case types.IndexStateClose:
		changed, err = changer.CloseIndex(ctx, cReq)
	default:
		return types.IndexStateChange{}, client.UsageError("unsupported action %q", opts.Action)
	}
	if err != nil {
		return types.IndexStateChange{}, err
	}
	result.Acknowledged = changed.Acknowledged
	result.ShardsAcknowledged = changed.ShardsAcknowledged
	return result, nil
}

// validateStateChangeOptions guards against the two foot-guns scripts
// hit most: an empty --index (where urfave's required:true is the
// shallow check, but we want a clear message) and a wildcard so broad
// it would match every index in the cluster. The latter has to go
// through the explicit override.
func validateStateChangeOptions(opts StateChangeOptions) error {
	if opts.Action != types.IndexStateOpen && opts.Action != types.IndexStateClose {
		return client.UsageError("unsupported action %q (expected: open, close)", opts.Action)
	}
	pattern := strings.TrimSpace(opts.Pattern)
	if pattern == "" {
		return client.UsageError("--index is required (a comma-separated list or pattern)")
	}
	switch pattern {
	case "*", "_all":
		return fmt.Errorf("%w: refusing pattern %q — it would match every index; name a narrower pattern", client.ErrBlastRadius, pattern)
	}
	return nil
}

// partitionByTargetState splits the resolved /_cat/indices view into:
//   - target: indices that need to change to reach the requested state
//     (open → currently closed; close → currently open)
//   - skipped: indices already in the target state
//
// Both lists are sorted alphabetically so the rendered output (and
// captured JSON) is stable regardless of cluster response order.
func partitionByTargetState(rows []types.IndexNameStatus, action types.IndexStateAction) (target, skipped []string) {
	want := wantPriorState(action)
	target = make([]string, 0, len(rows))
	skipped = make([]string, 0, len(rows))
	for _, r := range rows {
		switch strings.ToLower(r.Status) {
		case want:
			target = append(target, r.Name)
		default:
			skipped = append(skipped, r.Name)
		}
	}
	sort.Strings(target)
	sort.Strings(skipped)
	return target, skipped
}

// wantPriorState returns the cluster status we expect to find on
// indices that *should* change for the requested action. Inverse of
// the action: opening transitions from "close"; closing from "open".
func wantPriorState(a types.IndexStateAction) string {
	switch a {
	case types.IndexStateOpen:
		return "close"
	case types.IndexStateClose:
		return "open"
	}
	return ""
}

// ParseStateAction maps a CLI subcommand verb ("open"/"close") to a
// typed IndexStateAction. Keeps the CLI wiring symmetrical with
// ParseSort/ValidateHealth/ValidateStatus in this package.
func ParseStateAction(s string) (types.IndexStateAction, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "open":
		return types.IndexStateOpen, nil
	case "close":
		return types.IndexStateClose, nil
	default:
		return "", client.UsageError("unknown index state action %q (expected: open, close)", s)
	}
}
