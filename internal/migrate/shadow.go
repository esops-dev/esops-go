package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// Shadow-read verification defaults. TopK bounds the per-query hit
// window the diff runs over; concurrency caps the per-query fan-out
// across the query set (independent of the client's own max_in_flight);
// queryTimeout bounds a single replayed search so one pathological query
// can't stall the whole run.
const (
	shadowDefaultTopK         = 100
	shadowDefaultConcurrency  = 4
	shadowDefaultQueryTimeout = 10 * time.Second

	// scoreEpsilonAbs / scoreEpsilonRel bound when two _score values are
	// treated as different. ES/OS compute scores in float32, so even an
	// identical query on identical data can differ in the low bits once
	// it round-trips through JSON — and a single float32 ULP already
	// exceeds 1e-6 once scores climb past ~8 (BM25 routinely does). A
	// combined absolute+relative tolerance keeps that float noise from
	// being reported as a score divergence while still catching genuine
	// differences.
	scoreEpsilonAbs = 1e-6
	scoreEpsilonRel = 1e-5
)

// Shadow replays a set of named search queries against the source and
// target clusters and diffs the results. Doc-count parity (migrate
// verify) proves nothing about mapping drift, analyzer changes, or
// score-affecting configuration; shadow reads catch the silent
// regressions that surface as "the same query returns different hits
// after migration".
//
// Each query runs against both clusters concurrently (the two searches
// are independent); queries across the set fan out with req.Concurrency
// in flight. The diff compares the ordered top-K document IDs: set
// membership drives the per-query status, ordering does too but only for
// queries that carry an explicit sort (without one the cluster's tie
// order is non-deterministic across segment layouts and is not a
// regression), and score divergence is reported but only promotes to a
// mismatch under req.StrictScores.
//
// Auth/forbidden errors short-circuit the whole run — the operator's
// first reaction is "stop everything", not "let me read the per-row
// error". setFatal cancels the shared context so in-flight queries
// abort promptly rather than each running out its own timeout. Every
// other per-query failure lands as an error row.
func Shadow(ctx context.Context, src, tgt Endpoint, req types.MigrationShadowRequest) (types.MigrationShadowResult, error) {
	if err := validateShadowEndpoints(src, tgt); err != nil {
		return types.MigrationShadowResult{}, err
	}

	topK := req.TopK
	if topK <= 0 {
		topK = shadowDefaultTopK
	}
	concurrency := req.Concurrency
	if concurrency <= 0 {
		concurrency = shadowDefaultConcurrency
	}
	// A non-positive timeout falls back to the default rather than
	// running unbounded — symmetric with TopK/Concurrency above, so a
	// direct Shadow() caller (not just the CLI, which defaults the flag)
	// can't accidentally let one pathological query stall the run.
	queryTimeout := req.QueryTimeout
	if queryTimeout <= 0 {
		queryTimeout = shadowDefaultQueryTimeout
	}
	indices := client.SplitCSV(req.Indices)

	result := types.MigrationShadowResult{
		Action:       "migrate-verify-shadow",
		Source:       src.summary(),
		Target:       tgt.summary(),
		Indices:      req.Indices,
		TopK:         topK,
		StrictScores: req.StrictScores,
	}
	if len(req.Queries) == 0 {
		result.Status = types.MigrationStatusOK
		return result, nil
	}

	entries := make([]types.MigrationShadowEntry, len(req.Queries))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		fatalOnce sync.Once
		fatalErr  error
	)
	setFatal := func(err error) {
		fatalOnce.Do(func() {
			fatalErr = err
			cancel() // stop the rest of the fan-out promptly
		})
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	wg.Add(len(req.Queries))
	for i := range req.Queries {
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-runCtx.Done():
				entries[i] = types.MigrationShadowEntry{
					Name:   req.Queries[i].Name,
					Status: types.ShadowStatusError,
					Detail: "cancelled before query (a prior query failed fatally or the run was interrupted)",
				}
				return
			}
			defer func() { <-sem }()
			entries[i] = runShadowQuery(runCtx, src, tgt, req, indices, topK, queryTimeout, req.Queries[i], setFatal)
		}(i)
	}
	wg.Wait()

	if fatalErr != nil {
		return result, fatalErr
	}

	result.Queries = entries
	for _, e := range entries {
		switch e.Status {
		case types.ShadowStatusMismatch:
			result.Mismatches++
		case types.ShadowStatusError:
			result.Errors++
		}
	}
	if result.Mismatches > 0 || result.Errors > 0 {
		result.Status = types.MigrationStatusBlock
	} else {
		result.Status = types.MigrationStatusOK
	}
	return result, nil
}

// runShadowQuery replays one query against both clusters and folds the
// outcome into an entry. The per-query timeout is applied via a child
// context so a single slow search bounds itself without affecting the
// rest of the run.
func runShadowQuery(ctx context.Context, src, tgt Endpoint, req types.MigrationShadowRequest, indices []string, topK int, queryTimeout time.Duration, q types.ShadowQuery, setFatal func(error)) types.MigrationShadowEntry {
	entry := types.MigrationShadowEntry{Name: q.Name}
	if req.IncludeBody {
		entry.Body = types.RawJSON(q.Body)
	}

	qctx := ctx
	if queryTimeout > 0 {
		var cancel context.CancelFunc
		qctx, cancel = context.WithTimeout(ctx, queryTimeout)
		defer cancel()
	}

	searchReq := types.SearchRequest{
		Indices:           indices,
		Body:              q.Body,
		Size:              topK,
		IgnoreUnavailable: true,
	}

	type sres struct {
		res types.SearchResult
		err error
	}
	srcCh := make(chan sres, 1)
	tgtCh := make(chan sres, 1)
	go func() { r, e := src.Search.Search(qctx, searchReq); srcCh <- sres{r, e} }()
	go func() { r, e := tgt.Search.Search(qctx, searchReq); tgtCh <- sres{r, e} }()
	srcResp := <-srcCh
	tgtResp := <-tgtCh

	// A fired per-query deadline shows up as the qctx error, not as a
	// wrapped context.DeadlineExceeded on the returned error: the
	// transport classifier flattens the cause to a string (ErrUnreachable
	// + %v), so errors.Is on the search error can't see it. Reading
	// qctx.Err() is the reliable signal — and it's DeadlineExceeded only
	// for the per-query timeout (a fatal/interrupt cancel surfaces as
	// context.Canceled), so we don't mislabel an aborted query.
	timedOut := errors.Is(qctx.Err(), context.DeadlineExceeded)
	if srcResp.err != nil {
		return shadowErrorEntry(entry, "source", srcResp.err, queryTimeout, timedOut, setFatal)
	}
	if tgtResp.err != nil {
		return shadowErrorEntry(entry, "target", tgtResp.err, queryTimeout, timedOut, setFatal)
	}

	entry.SourceHits = srcResp.res.TotalHits
	entry.TargetHits = tgtResp.res.TotalHits
	entry.SourceTopK = toShadowHits(srcResp.res.Hits)
	entry.TargetTopK = toShadowHits(tgtResp.res.Hits)

	d := compareShadow(srcResp.res.Hits, tgtResp.res.Hits, bodyHasSort(q.Body), req.StrictScores)
	entry.SetDiff = d.setDiff
	entry.OrderDiff = d.orderDiff
	entry.ScoreDiff = d.scoreDiff
	if d.mismatch {
		entry.Status = types.ShadowStatusMismatch
		entry.Detail = d.detail
	} else {
		entry.Status = types.ShadowStatusOK
	}
	return entry
}

// shadowErrorEntry classifies a per-query search failure into an error
// row. Auth/forbidden short-circuit the whole run via setFatal; a
// per-query timeout (timedOut) is surfaced as a timeout rather than the
// "unreachable" the transport layer reports for a cancelled context, so
// the operator doesn't chase a connectivity problem that isn't there.
func shadowErrorEntry(entry types.MigrationShadowEntry, side string, err error, timeout time.Duration, timedOut bool, setFatal func(error)) types.MigrationShadowEntry {
	if isFatalShadowErr(err) {
		setFatal(err)
	}
	entry.Status = types.ShadowStatusError
	if timedOut {
		entry.Detail = fmt.Sprintf("%s search timed out after %s", side, timeout)
	} else {
		entry.Detail = fmt.Sprintf("%s search failed: %v", side, err)
	}
	return entry
}

// shadowDiff is the computed comparison between one query's source and
// target hit windows.
type shadowDiff struct {
	setDiff   int
	orderDiff int
	scoreDiff int
	mismatch  bool
	detail    string
}

// compareShadow diffs two ordered hit windows. Hits are keyed on
// index+id (not id alone) so a multi-index pattern with the same _id in
// two indices doesn't collapse into one. setDiff is the size of the
// symmetric difference of the two id sets; orderDiff counts positions
// where the ids common to both appear in a different relative order;
// scoreDiff counts common ids whose scores differ beyond the tolerance.
//
// A mismatch is declared on any set difference. Order differences count
// only when the query carries an explicit sort (hasSort): without one,
// the cluster's tie-breaking order is non-deterministic across segment
// layouts and a difference is expected, not a regression. Score
// divergence contributes only when strictScores is set — across-version
// and analyzer-driven score drift is expected and not, by itself, a
// regression.
func compareShadow(srcHits, tgtHits []types.SearchResultHit, hasSort, strictScores bool) shadowDiff {
	srcKeys := shadowHitKeys(srcHits)
	tgtKeys := shadowHitKeys(tgtHits)
	srcSet := shadowKeySet(srcKeys)
	tgtSet := shadowKeySet(tgtKeys)

	// Symmetric difference over the *sets* so a duplicate index+id in one
	// window (legal across an alias) isn't double-counted.
	setDiff := 0
	for k := range srcSet {
		if _, ok := tgtSet[k]; !ok {
			setDiff++
		}
	}
	for k := range tgtSet {
		if _, ok := srcSet[k]; !ok {
			setDiff++
		}
	}

	// Order: restrict each side to the keys common to both, preserving
	// each side's order, then compare element-wise. Both sequences hold
	// the same set, so equal length — a positional disagreement is a
	// genuine reordering.
	commonSrc := shadowCommon(srcKeys, tgtSet)
	commonTgt := shadowCommon(tgtKeys, srcSet)
	orderDiff := 0
	for i := 0; i < min(len(commonSrc), len(commonTgt)); i++ {
		if commonSrc[i] != commonTgt[i] {
			orderDiff++
		}
	}

	scoreDiff := 0
	srcScores := shadowKeyScores(srcHits)
	tgtScores := shadowKeyScores(tgtHits)
	for k := range srcSet {
		if _, ok := tgtSet[k]; !ok {
			continue
		}
		if scoresDiffer(srcScores[k], tgtScores[k]) {
			scoreDiff++
		}
	}

	d := shadowDiff{setDiff: setDiff, orderDiff: orderDiff, scoreDiff: scoreDiff}
	var reasons []string
	if setDiff > 0 {
		reasons = append(reasons, fmt.Sprintf("%d id(s) differ between the top-K sets", setDiff))
	}
	if orderDiff > 0 && hasSort {
		reasons = append(reasons, fmt.Sprintf("%d common id(s) in a different order", orderDiff))
	}
	if strictScores && scoreDiff > 0 {
		reasons = append(reasons, fmt.Sprintf("%d common id(s) with diverging scores", scoreDiff))
	}
	if len(reasons) > 0 {
		d.mismatch = true
		d.detail = strings.Join(reasons, "; ")
	}
	return d
}

// scoresDiffer reports whether two _score values are meaningfully
// different, using a combined absolute + relative tolerance so float32
// round-trip noise on large scores is not mistaken for a divergence.
func scoresDiffer(a, b float64) bool {
	diff := math.Abs(a - b)
	if diff <= scoreEpsilonAbs {
		return false
	}
	return diff > scoreEpsilonRel*math.Max(math.Abs(a), math.Abs(b))
}

// bodyHasSort reports whether the operator's search body carries a
// top-level `sort`. Order comparison is only meaningful for a query that
// pins its ordering; a match_all (or any query relying on score ties)
// orders ties by internal doc id, which two clusters with different
// segment layouts will disagree on without that being a regression.
func bodyHasSort(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return false
	}
	_, ok := body["sort"]
	return ok
}

func shadowHitKeys(hits []types.SearchResultHit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Index + "\x00" + h.ID
	}
	return out
}

func shadowKeySet(keys []string) map[string]struct{} {
	out := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		out[k] = struct{}{}
	}
	return out
}

func shadowKeyScores(hits []types.SearchResultHit) map[string]float64 {
	out := make(map[string]float64, len(hits))
	for _, h := range hits {
		out[h.Index+"\x00"+h.ID] = h.Score
	}
	return out
}

func shadowCommon(keys []string, other map[string]struct{}) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if _, ok := other[k]; ok {
			out = append(out, k)
		}
	}
	return out
}

func toShadowHits(hits []types.SearchResultHit) []types.ShadowHit {
	if len(hits) == 0 {
		return nil
	}
	out := make([]types.ShadowHit, len(hits))
	for i, h := range hits {
		out[i] = types.ShadowHit{ID: h.ID, Score: h.Score}
	}
	return out
}

// isFatalShadowErr reports whether a per-query error should abort the
// whole run rather than land as an error row. Auth/forbidden mean every
// remaining query would fail the same way, so we stop and surface the
// classified error for the right exit code.
func isFatalShadowErr(err error) bool {
	return errors.Is(err, client.ErrAuth) || errors.Is(err, client.ErrForbidden)
}

// validateShadowEndpoints checks both clusters carry a SearchExecutor.
func validateShadowEndpoints(src, tgt Endpoint) error {
	if src.Search == nil {
		return errors.New("source endpoint missing SearchExecutor — wiring bug")
	}
	if tgt.Search == nil {
		return errors.New("target endpoint missing SearchExecutor — wiring bug")
	}
	return nil
}
