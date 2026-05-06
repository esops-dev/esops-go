package migrate

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// Verify compares per-index doc counts across the source and target
// clusters. The pairing is name-for-name unless --to-suffix is given,
// in which case the target name is "<source-name><suffix>" — the same
// mapping as `migrate reindex --to-suffix`.
//
// /<index>/_count is the cluster's authoritative count (segment-flushed,
// not /_cat/indices' refresh-cached column) so verify reflects the
// state at the moment the call lands. Both source and target counts
// are taken in close sequence — there is still a window where writes
// to the source between the two calls land in the diff, but that's
// expected: cutover is what closes that window for real.
func Verify(ctx context.Context, src, tgt Endpoint, req types.MigrationVerifyRequest) (types.MigrationVerifyResult, error) {
	if err := validateVerifyEndpoints(src, tgt); err != nil {
		return types.MigrationVerifyResult{}, err
	}

	indices, err := src.Indices.Indices(ctx, types.IndexFilter{
		Index:  req.Indices,
		Status: "open",
	})
	if err != nil {
		return types.MigrationVerifyResult{}, fmt.Errorf("listing source indices: %w", err)
	}

	result := types.MigrationVerifyResult{
		Action: "migrate-verify",
		Source: src.summary(),
		Target: tgt.summary(),
	}
	if len(indices) == 0 {
		result.Status = types.MigrationStatusOK
		return result, nil
	}

	sort.SliceStable(indices, func(i, j int) bool { return indices[i].Name < indices[j].Name })

	for _, ix := range indices {
		entry := types.MigrationVerifyEntry{
			SourceIndex: ix.Name,
			TargetIndex: ix.Name + req.TargetSuffix,
		}

		srcCount, srcErr := src.DocCount.Count(ctx, types.CountRequest{Indices: []string{entry.SourceIndex}})
		if srcErr != nil {
			if errors.Is(srcErr, client.ErrAuth) || errors.Is(srcErr, client.ErrForbidden) {
				return result, srcErr
			}
			entry.Status = types.MigrationStatusBlock
			entry.Detail = fmt.Sprintf("source count failed: %v", srcErr)
			result.Indices = append(result.Indices, entry)
			continue
		}
		entry.SourceCount = srcCount.Count

		tgtCount, tgtErr := tgt.DocCount.Count(ctx, types.CountRequest{
			Indices:           []string{entry.TargetIndex},
			IgnoreUnavailable: true,
		})
		if tgtErr != nil {
			if errors.Is(tgtErr, client.ErrAuth) || errors.Is(tgtErr, client.ErrForbidden) {
				return result, tgtErr
			}
			entry.Status = types.MigrationStatusBlock
			entry.Detail = fmt.Sprintf("target count failed: %v", tgtErr)
			result.Indices = append(result.Indices, entry)
			continue
		}
		entry.TargetCount = tgtCount.Count
		entry.Diff = entry.SourceCount - entry.TargetCount

		switch {
		case entry.Diff == 0 && shardsClean(srcCount.Shards) && shardsClean(tgtCount.Shards):
			entry.Status = types.MigrationStatusOK
		case entry.Diff == 0 && (!shardsClean(srcCount.Shards) || !shardsClean(tgtCount.Shards)):
			// Counts match but the cluster reported partial shard
			// coverage on at least one side — surface as a warn so
			// the operator knows the equality is on a partial set.
			entry.Status = types.MigrationStatusWarn
			entry.Detail = "counts match but a shard set was incomplete (failed/skipped > 0)"
		case entry.Diff != 0:
			entry.Status = types.MigrationStatusBlock
			if entry.Diff > 0 {
				entry.Detail = fmt.Sprintf("target is short by %d documents", entry.Diff)
			} else {
				entry.Detail = fmt.Sprintf("target has %d documents more than source (writes after reindex?)", -entry.Diff)
			}
		}
		result.Indices = append(result.Indices, entry)
	}

	for _, e := range result.Indices {
		switch e.Status {
		case types.MigrationStatusBlock:
			result.Blockers++
		case types.MigrationStatusWarn:
			result.Warnings++
		}
	}
	switch {
	case result.Blockers > 0:
		result.Status = types.MigrationStatusBlock
	case result.Warnings > 0:
		result.Status = types.MigrationStatusWarn
	default:
		result.Status = types.MigrationStatusOK
	}
	return result, nil
}

// validateVerifyEndpoints checks the wiring matches the call needs.
// Both clusters need DocCount and IndexInspector.
func validateVerifyEndpoints(src, tgt Endpoint) error {
	if src.Indices == nil {
		return errors.New("source endpoint missing IndexInspector — wiring bug")
	}
	if src.DocCount == nil {
		return errors.New("source endpoint missing DocCounter — wiring bug")
	}
	if tgt.DocCount == nil {
		return errors.New("target endpoint missing DocCounter — wiring bug")
	}
	return nil
}

// shardsClean reports whether the cluster's _shards block on a count
// response indicates a complete, non-skipped read. A failed or skipped
// shard means the count is across a partial set — equality between
// source and target counts in that case is not a verification, it's a
// coincidence the operator should look at.
func shardsClean(s types.CountShardsInfo) bool {
	return s.Failed == 0 && s.Skipped == 0
}
