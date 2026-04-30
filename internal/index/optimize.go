package index

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// OptimizeOptions are the knobs the CLI layer hands to Optimize. The
// shape mirrors StateChangeOptions so commands stay symmetrical: the
// pattern + blast-radius cap belong here, the cluster-side knobs
// (max_num_segments, only_expunge_deletes, flush) sit alongside them.
//
// MaxNumSegments is the canonical "merge to N segments" knob; 0 leaves
// the cluster's dynamic default in place. The typical value for a
// sealed-archive merge is 1.
//
// OnlyExpungeDeletes is the cheap variant that only rewrites segments
// holding tombstoned docs — appropriate after a delete-by-query sweep.
// Flush is a *bool so "I want flush=false" is expressible without
// conflating it with the zero value (where the cluster default of
// true should be preserved).
type OptimizeOptions struct {
	Pattern            string
	MaxIndices         int
	DryRun             bool
	MaxNumSegments     int
	OnlyExpungeDeletes bool
	Flush              *bool
	IgnoreUnavailable  bool
}

// Optimize is the business-layer entry point for `esops index optimize`.
// It:
//
//  1. Validates the pattern (non-empty; rejects bare "*" / "_all").
//  2. Resolves the pattern via IndexInspector and partitions matches
//     into "open" (eligible) and "closed" (skipped — force-merging a
//     closed index is a 400 on both dialects).
//  3. Enforces the blast-radius limit on the eligible set: more than
//     MaxIndices (default DefaultMaxIndices) returns ErrBlastRadius.
//  4. On dry-run, returns the resolved would-be merge without firing
//     the call. The cluster has no native dry-run for /_forcemerge.
//  5. Otherwise calls the IndexOptimizer with the resolved name list
//     and returns its response enriched with the resolved Indices.
//
// When the resolved pattern matches nothing, the function returns a
// no-op IndexOptimizeResult (zero shards, empty Indices) without
// hitting the cluster — useful so a pattern that matched yesterday
// but is empty today doesn't fire a wasted call.
func Optimize(ctx context.Context, inspector client.IndexInspector, opt client.IndexOptimizer, opts OptimizeOptions) (types.IndexOptimizeResult, error) {
	if err := validateOptimizeOptions(opts); err != nil {
		return types.IndexOptimizeResult{}, err
	}

	indices, err := inspector.Indices(ctx, types.IndexFilter{
		Index:  opts.Pattern,
		Hidden: true,
	})
	if err != nil {
		return types.IndexOptimizeResult{}, err
	}

	target, skipped := partitionByOpenStatus(indices)
	limit := opts.MaxIndices
	if limit <= 0 {
		limit = DefaultMaxIndices
	}
	if len(target) > limit {
		return types.IndexOptimizeResult{}, fmt.Errorf(
			"%w: pattern %q resolves to %d open indices to optimize (limit %d); pass --max-indices N to override",
			client.ErrBlastRadius, opts.Pattern, len(target), limit,
		)
	}

	result := types.IndexOptimizeResult{
		Action:         "optimize",
		DryRun:         opts.DryRun,
		Indices:        target,
		MaxNumSegments: opts.MaxNumSegments,
	}
	// Closed indices are surfaced via Failures so the operator sees
	// why a pattern that named them produced no merge — closed indices
	// can't be force-merged on either dialect, and naming the skip in
	// the rendered output keeps the resolved view honest.
	for _, name := range skipped {
		result.Failures = append(result.Failures, fmt.Sprintf("%s: skipped (closed index — open it first to optimize)", name))
	}

	if len(target) == 0 || opts.DryRun {
		return result, nil
	}

	cReq := types.IndexOptimizeRequest{
		Index:              strings.Join(target, ","),
		MaxNumSegments:     opts.MaxNumSegments,
		OnlyExpungeDeletes: opts.OnlyExpungeDeletes,
		Flush:              opts.Flush,
		IgnoreUnavailable:  opts.IgnoreUnavailable,
	}
	resp, err := opt.ForceMerge(ctx, cReq)
	if err != nil {
		return types.IndexOptimizeResult{}, err
	}
	result.ShardsTotal = resp.ShardsTotal
	result.ShardsSuccess = resp.ShardsSuccess
	result.ShardsFailed = resp.ShardsFailed
	result.Failures = append(result.Failures, resp.Failures...)
	return result, nil
}

// validateOptimizeOptions guards against the common foot-guns: an
// empty --index, a wildcard so broad it would touch every index in
// the cluster, and a negative max-num-segments value that the cluster
// would reject anyway. Catching the last one client-side keeps the
// error message specific.
func validateOptimizeOptions(opts OptimizeOptions) error {
	pattern := strings.TrimSpace(opts.Pattern)
	if pattern == "" {
		return fmt.Errorf("--index is required (a comma-separated list or pattern)")
	}
	switch pattern {
	case "*", "_all":
		return fmt.Errorf("%w: refusing pattern %q — it would match every index; name a narrower pattern", client.ErrBlastRadius, pattern)
	}
	if opts.MaxNumSegments < 0 {
		return fmt.Errorf("--max-num-segments must be >= 0 (got %d)", opts.MaxNumSegments)
	}
	return nil
}

// partitionByOpenStatus splits the resolved /_cat/indices view into:
//   - target: open indices (eligible for force-merge)
//   - skipped: closed indices (the cluster rejects /_forcemerge on a
//     closed index with a 400; we surface the skip rather than letting
//     the request fail mid-flight)
//
// Both lists are sorted alphabetically so rendered output is stable.
func partitionByOpenStatus(indices []types.Index) (target, skipped []string) {
	for _, ix := range indices {
		if strings.EqualFold(ix.Status, "open") {
			target = append(target, ix.Name)
		} else {
			skipped = append(skipped, ix.Name)
		}
	}
	sort.Strings(target)
	sort.Strings(skipped)
	return target, skipped
}
