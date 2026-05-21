package index

import (
	"context"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// ShrinkOptions are the knobs the CLI layer hands to Shrink. Source
// and Target are required and must be literal index names — patterns
// are not supported on the shrink path. The cluster's shrink endpoint
// only accepts a single source/target pair, and the typical operator
// flow (block-for-writes, allocate-to-one-node, then shrink) is per-
// index by design.
//
// TargetShards is the requested primary count on the new index. The
// cluster requires it to be a factor of the source's primary count
// (4→2, 4→1, 8→4, …); the cluster validates this and we surface its
// error verbatim. 0 leaves the cluster default in place (one primary).
//
// TargetReplicas is a *int so "set replicas to 0" is expressible
// distinctly from "leave the cluster default" — the typical post-
// shrink flow is shrink with replicas=0 to keep the operation cheap,
// then bump replicas back up afterwards.
//
// CopySettings mirrors the cluster's ?copy_settings=true. DryRun is
// the global flag — when true, the cluster is not contacted; the
// function returns a simulated result reflecting what would have
// been sent.
type ShrinkOptions struct {
	Source              string
	Target              string
	TargetShards        int
	TargetReplicas      *int
	CopySettings        *bool
	DryRun              bool
	WaitForActiveShards string
}

// Shrink is the business-layer entry point for `esops index shrink`.
// Validation:
//
//  1. Source and Target are required and must be different.
//  2. Target must not look like a pattern — bare "*", "_all", and any
//     name containing a wildcard glyph are refused. The cluster would
//     refuse those too, but failing client-side keeps the message
//     specific and the request out of the cluster's audit log.
//  3. TargetShards must be >= 0; the cluster validates the
//     "is a factor of source shards" requirement.
//
// The cluster has no native dry-run for shrink, so dry-run is
// implemented client-side as a "validate + report" path that returns
// a synthetic Acknowledged=true without contacting the cluster. The
// pre-flight requirements (source read-only, primaries co-located)
// are policy decisions the operator makes before invoking shrink and
// the cluster surfaces the canonical error if either is missing —
// esops does not flip those settings automatically.
func Shrink(ctx context.Context, sh client.IndexShrinker, opts ShrinkOptions) (types.IndexShrinkResult, error) {
	if err := validateShrinkOptions(opts); err != nil {
		return types.IndexShrinkResult{}, err
	}

	if opts.DryRun {
		return types.IndexShrinkResult{
			Action:             "shrink",
			DryRun:             true,
			Source:             opts.Source,
			Target:             opts.Target,
			Acknowledged:       true,
			ShardsAcknowledged: true,
			TargetShards:       opts.TargetShards,
		}, nil
	}

	req := types.IndexShrinkRequest{
		Source:              opts.Source,
		Target:              opts.Target,
		TargetShards:        opts.TargetShards,
		TargetReplicas:      opts.TargetReplicas,
		CopySettings:        opts.CopySettings,
		WaitForActiveShards: opts.WaitForActiveShards,
	}
	return sh.Shrink(ctx, req)
}

// validateShrinkOptions catches the foot-guns scripts hit most: empty
// source/target, identical names, pattern-y target names, and a
// negative shard count. Source naming with a wildcard *is* allowed
// because the cluster accepts it on the shrink path (resolving to a
// single index when the pattern is unambiguous), but in practice
// operators name a literal source — same constraint we apply to
// Target keeps the story symmetrical and the message specific.
func validateShrinkOptions(opts ShrinkOptions) error {
	source := strings.TrimSpace(opts.Source)
	target := strings.TrimSpace(opts.Target)
	if source == "" {
		return client.UsageError("--source is required")
	}
	if target == "" {
		return client.UsageError("--target is required")
	}
	if source == target {
		return client.UsageError("--source and --target must differ (got %q for both)", source)
	}
	if client.IsPatternish(source) {
		return client.UsageError("--source must be a literal index name, not a pattern (got %q)", source)
	}
	if client.IsPatternish(target) {
		return client.UsageError("--target must be a literal index name, not a pattern (got %q)", target)
	}
	if opts.TargetShards < 0 {
		return client.UsageError("--shards must be >= 0 (got %d)", opts.TargetShards)
	}
	if opts.TargetReplicas != nil && *opts.TargetReplicas < 0 {
		return client.UsageError("--replicas must be >= 0 (got %d)", *opts.TargetReplicas)
	}
	return nil
}
