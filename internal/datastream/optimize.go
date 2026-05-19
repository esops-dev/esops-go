package datastream

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// OptimizeOptions narrows a `datastream optimize` call. Pattern is the
// operator-supplied data-stream name (or comma-joined list, possibly
// with patterns). IncludeWriteIndex opts in to force-merging the
// stream's current write index — default-off because the write index
// is by definition still receiving writes, and force-merging a live
// index is expensive and rarely what an operator wants. The remaining
// knobs forward to IndexOptimizer.ForceMerge.
type OptimizeOptions struct {
	Pattern            string
	MaxIndices         int
	DryRun             bool
	MaxNumSegments     int
	OnlyExpungeDeletes bool
	Flush              *bool
	IgnoreUnavailable  bool
	IncludeWriteIndex  bool
}

// DefaultMaxOptimizeBackings is the blast-radius cap for `datastream
// optimize`. Higher than DefaultMaxStreams because force-merge is
// idempotent and read-only-from-the-data's-perspective — repeat
// invocations don't compound damage the way deletes do — but lower
// than the index command's 20 so an operator who points the verb at
// every stream in their cluster has to acknowledge the size of the
// resolved sweep.
const DefaultMaxOptimizeBackings = 50

// Optimize is the business-layer entry point for `datastream optimize`.
// It:
//
//  1. Validates the pattern (non-empty; rejects bare "*" / "_all").
//  2. Resolves the pattern via DataStreamInspector to a backing-index
//     list, and the stream's write index per row. The write index is
//     `Indices[len-1]`; when a stream has zero backings (transient
//     state mid-bootstrap) WriteIndex is empty and the skip check
//     becomes a no-op.
//  3. Calls IndicesNamesStatus to partition the resolved backings by
//     open/closed status. Closed backings are skipped because
//     /_forcemerge returns 400 (index_closed_exception) for them on
//     both dialects — pre-flighting client-side keeps the call from
//     failing the whole sweep when a single cold-storage backing is
//     closed. Cluster refuses /_forcemerge on the active write index
//     of a data stream on Elasticsearch (and is never intended on
//     OpenSearch), so the write index is also skipped unless
//     IncludeWriteIndex.
//  4. Enforces the blast-radius limit on the resolved target list.
//  5. On dry-run, returns the resolved would-be merge without firing
//     the call. The cluster has no native dry-run for /_forcemerge.
//  6. Otherwise calls the IndexOptimizer with the resolved index list
//     and returns its response enriched with the resolved Indices.
func Optimize(ctx context.Context, inspector client.DataStreamInspector, indexes client.IndexInspector, opt client.IndexOptimizer, opts OptimizeOptions) (types.IndexOptimizeResult, error) {
	if err := validateOptimizePattern(opts.Pattern); err != nil {
		return types.IndexOptimizeResult{}, err
	}
	if opts.MaxNumSegments < 0 {
		return types.IndexOptimizeResult{}, client.UsageError("--max-num-segments must be >= 0 (got %d)", opts.MaxNumSegments)
	}

	rows, err := inspector.DataStreams(ctx, types.DataStreamFilter{
		Names: client.SplitCSV(opts.Pattern),
	})
	if err != nil {
		return types.IndexOptimizeResult{}, err
	}

	// Collect every backing across matched streams plus the set of
	// names that are the write index for their stream. WriteIndex is
	// the last entry of Indices on every adapter, and stays empty when
	// the stream has zero backings — the skip check below tolerates
	// that as a no-op.
	var backings []string
	writeIndexes := make(map[string]struct{})
	for _, r := range rows {
		if r.WriteIndex != "" {
			writeIndexes[r.WriteIndex] = struct{}{}
		}
		for _, b := range r.Indices {
			backings = append(backings, b.Name)
		}
	}
	sort.Strings(backings)

	target, writeSkipped, closedSkipped, err := partitionBackings(ctx, indexes, backings, writeIndexes, opts.IncludeWriteIndex)
	if err != nil {
		return types.IndexOptimizeResult{}, err
	}

	limit := opts.MaxIndices
	if limit <= 0 {
		limit = DefaultMaxOptimizeBackings
	}
	if len(target) > limit {
		return types.IndexOptimizeResult{}, fmt.Errorf(
			"%w: pattern %q resolves to %d backing indices to optimize (limit %d); pass --max-indices N to override",
			client.ErrBlastRadius, opts.Pattern, len(target), limit,
		)
	}

	result := types.IndexOptimizeResult{
		Action:         "datastream-optimize",
		DryRun:         opts.DryRun,
		Indices:        target,
		MaxNumSegments: opts.MaxNumSegments,
	}
	for _, name := range writeSkipped {
		result.Failures = append(result.Failures,
			fmt.Sprintf("%s: skipped (write index — pass --include-write-index to force-merge it)", name))
	}
	for _, name := range closedSkipped {
		result.Failures = append(result.Failures,
			fmt.Sprintf("%s: skipped (closed backing index — open it first to optimize)", name))
	}

	if len(target) == 0 || opts.DryRun {
		return result, nil
	}

	cReq := types.IndexOptimizeRequest{
		Indices:            target,
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

// partitionBackings classifies every resolved backing name into one of
// three buckets: target (open, not write-blocked), writeSkipped (the
// active write index of its stream, when IncludeWriteIndex is false),
// closedSkipped (the cluster reports status=close, where /_forcemerge
// returns a 400). The write check comes first so a backing that
// happens to be both write *and* closed (extremely rare) only appears
// once, under the write-index reason — which is the more relevant
// signal for the operator's next action.
//
// indexes may be nil; in that case the function does no status
// lookup and lets every non-write backing through. Tests use this to
// avoid threading a fake IndexInspector through every case.
func partitionBackings(ctx context.Context, indexes client.IndexInspector, backings []string, writeIndexes map[string]struct{}, includeWriteIndex bool) (target, writeSkipped, closedSkipped []string, err error) {
	if len(backings) == 0 {
		return nil, nil, nil, nil
	}
	statusByName := make(map[string]string, len(backings))
	if indexes != nil {
		// Hidden:true on the filter because all data-stream backing
		// indices are dot-prefixed; without it /_cat/indices drops
		// every row before the partition runs.
		rows, err := indexes.IndicesNamesStatus(ctx, types.IndexFilter{
			Index:  strings.Join(backings, ","),
			Hidden: true,
		})
		if err != nil {
			return nil, nil, nil, err
		}
		for _, r := range rows {
			statusByName[r.Name] = r.Status
		}
	}
	for _, name := range backings {
		if _, isWrite := writeIndexes[name]; isWrite && !includeWriteIndex {
			writeSkipped = append(writeSkipped, name)
			continue
		}
		if !strings.EqualFold(statusByName[name], "close") {
			target = append(target, name)
			continue
		}
		closedSkipped = append(closedSkipped, name)
	}
	sort.Strings(target)
	sort.Strings(writeSkipped)
	sort.Strings(closedSkipped)
	return target, writeSkipped, closedSkipped, nil
}

func validateOptimizePattern(pattern string) error {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return client.UsageError("--name is required (a comma-separated list or pattern)")
	}
	switch pattern {
	case "*", "_all":
		return fmt.Errorf("%w: refusing pattern %q — it would match every data stream; name a narrower pattern", client.ErrBlastRadius, pattern)
	}
	return nil
}
