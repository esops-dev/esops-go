package migrate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// verifyConcurrency caps the per-index fan-out across the resolved
// pair set. Verifying a 1k-index reindex used to issue 2k sequential
// /_count calls — most of the wall time was network round-trips. A
// modest fan-out keeps the total count bounded without overwhelming
// the cluster's coordinating node. 8 in flight is the same default
// /_search slice cluster-side schedulers prefer; safe on small
// clusters, useful on large ones.
const verifyConcurrency = 8

// Verify compares per-index doc counts across the source and target
// clusters. The pairing is name-for-name unless --to-suffix or
// --to-index is given:
//
//   - --to-suffix appends a suffix to the source name (mirrors
//     `migrate reindex --to-suffix`)
//   - --to-index pins the target side to a literal name (mirrors
//     `migrate reindex --to-index`). Only valid when the source-side
//     resolution returns one index.
//
// /<index>/_count is the cluster's authoritative count (segment-flushed,
// not /_cat/indices' refresh-cached column) so verify reflects the
// state at the moment the call lands. The source and target counts for
// one index pair run concurrently (they're independent); pairs across
// the resolved set fan out with verifyConcurrency in flight.
func Verify(ctx context.Context, src, tgt Endpoint, req types.MigrationVerifyRequest) (types.MigrationVerifyResult, error) {
	if err := validateVerifyEndpoints(src, tgt); err != nil {
		return types.MigrationVerifyResult{}, err
	}
	if req.TargetSuffix != "" && req.TargetIndexLiteral != "" {
		return types.MigrationVerifyResult{}, client.UsageError("--to-suffix and --to-index are mutually exclusive")
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
	if req.TargetIndexLiteral != "" && len(indices) > 1 {
		return result, client.UsageError("--to-index requires a single source index; --indices=%q resolved to %d", req.Indices, len(indices))
	}

	sort.SliceStable(indices, func(i, j int) bool { return indices[i].Name < indices[j].Name })

	// Build the worklist up-front so the fan-out can write into a
	// pre-sized slice and we don't lose ordering when goroutines race.
	entries := make([]types.MigrationVerifyEntry, len(indices))
	for i, ix := range indices {
		entries[i].SourceIndex = ix.Name
		entries[i].TargetIndex = resolveVerifyTargetIndex(req, ix.Name, len(indices) == 1)
	}

	// Fatal classes (auth/forbidden) abort the whole run; the rest land
	// as per-row blockers. fatalErr is set once under fatalOnce so the
	// surfaced error is the first one observed.
	var (
		fatalOnce sync.Once
		fatalErr  error
	)
	setFatal := func(err error) {
		fatalOnce.Do(func() { fatalErr = err })
	}

	sem := make(chan struct{}, verifyConcurrency)
	var wg sync.WaitGroup
	wg.Add(len(entries))
	for i := range entries {
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				entries[i].Status = types.MigrationStatusBlock
				entries[i].Detail = "context cancelled before count"
				return
			}
			defer func() { <-sem }()

			fillVerifyEntry(ctx, src, tgt, &entries[i], setFatal)
		}(i)
	}
	wg.Wait()

	if fatalErr != nil {
		return result, fatalErr
	}

	result.Indices = entries
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

// fillVerifyEntry runs the source and target /_count calls concurrently
// for one pair, then folds the outcome into the entry's Status/Detail/
// Diff. Auth/forbidden errors short-circuit through setFatal because
// the operator's first reaction is "stop everything", not "let me see
// the per-row block".
func fillVerifyEntry(ctx context.Context, src, tgt Endpoint, entry *types.MigrationVerifyEntry, setFatal func(error)) {
	type countResp struct {
		res types.CountResult
		err error
	}
	var (
		srcCh = make(chan countResp, 1)
		tgtCh = make(chan countResp, 1)
	)
	go func() {
		res, err := src.DocCount.Count(ctx, types.CountRequest{Indices: []string{entry.SourceIndex}})
		srcCh <- countResp{res, err}
	}()
	go func() {
		res, err := tgt.DocCount.Count(ctx, types.CountRequest{
			Indices:           []string{entry.TargetIndex},
			IgnoreUnavailable: true,
		})
		tgtCh <- countResp{res, err}
	}()
	srcResp := <-srcCh
	tgtResp := <-tgtCh

	if srcResp.err != nil {
		if errors.Is(srcResp.err, client.ErrAuth) || errors.Is(srcResp.err, client.ErrForbidden) {
			setFatal(srcResp.err)
			return
		}
		entry.Status = types.MigrationStatusBlock
		entry.Detail = fmt.Sprintf("source count failed: %v", srcResp.err)
		return
	}
	entry.SourceCount = srcResp.res.Count

	if tgtResp.err != nil {
		if errors.Is(tgtResp.err, client.ErrAuth) || errors.Is(tgtResp.err, client.ErrForbidden) {
			setFatal(tgtResp.err)
			return
		}
		entry.Status = types.MigrationStatusBlock
		entry.Detail = fmt.Sprintf("target count failed: %v", tgtResp.err)
		return
	}
	entry.TargetCount = tgtResp.res.Count
	entry.Diff = entry.SourceCount - entry.TargetCount

	switch {
	case entry.Diff == 0 && shardsClean(srcResp.res.Shards) && shardsClean(tgtResp.res.Shards):
		entry.Status = types.MigrationStatusOK
	case entry.Diff == 0 && (!shardsClean(srcResp.res.Shards) || !shardsClean(tgtResp.res.Shards)):
		entry.Status = types.MigrationStatusWarn
		entry.Detail = "counts match but a shard set was incomplete (failed/skipped > 0 or successful < total)"
	case entry.Diff != 0:
		entry.Status = types.MigrationStatusBlock
		if entry.Diff > 0 {
			entry.Detail = fmt.Sprintf("target is short by %d documents", entry.Diff)
		} else {
			entry.Detail = fmt.Sprintf("target has %d documents more than source (writes after reindex?)", -entry.Diff)
		}
	}
}

// resolveVerifyTargetIndex mirrors resolveTargetIndex (used by reindex)
// so a verify-after-reindex run produces the same pairing the reindex
// did. Literal --to-index wins over --to-suffix; both win over "use the
// source name as-is".
func resolveVerifyTargetIndex(req types.MigrationVerifyRequest, sourceName string, single bool) string {
	if req.TargetIndexLiteral != "" && single {
		return req.TargetIndexLiteral
	}
	if req.TargetSuffix != "" {
		return sourceName + req.TargetSuffix
	}
	return sourceName
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
// response indicates a complete, all-successful read. A failed or
// skipped shard means the count is across a partial set; a mismatch
// between Total and Successful (e.g. a frozen index whose count touched
// no shards) is the same situation under a different name. Equality
// between source and target counts in either case is not a
// verification, it's a coincidence the operator should look at.
func shardsClean(s types.CountShardsInfo) bool {
	if s.Failed != 0 || s.Skipped != 0 {
		return false
	}
	// Total==0 with Successful==0 is the zero value — happens when the
	// cluster never populated the block. Treat that as "no signal" so a
	// count whose response omits the block doesn't get downgraded to
	// warn purely on shape.
	if s.Total == 0 && s.Successful == 0 {
		return true
	}
	return s.Successful == s.Total
}
