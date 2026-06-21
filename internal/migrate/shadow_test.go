package migrate_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	esopsmigrate "github.com/esops-dev/esops-go/internal/migrate"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeSearcher serves a canned SearchResult and records the requests it
// saw. Per-name overrides let a test return different results from the
// source and target sides of a pair.
type fakeSearcher struct {
	result types.SearchResult
	err    error

	mu      sync.Mutex
	gotReqs []types.SearchRequest
}

func (f *fakeSearcher) Search(_ context.Context, req types.SearchRequest) (types.SearchResult, error) {
	f.mu.Lock()
	f.gotReqs = append(f.gotReqs, req)
	f.mu.Unlock()
	if f.err != nil {
		return types.SearchResult{}, f.err
	}
	return f.result, nil
}

func hits(pairs ...any) types.SearchResult {
	var r types.SearchResult
	for i := 0; i < len(pairs); i += 2 {
		r.Hits = append(r.Hits, types.SearchResultHit{
			Index: "idx",
			ID:    pairs[i].(string),
			Score: pairs[i+1].(float64),
		})
	}
	r.TotalHits = int64(len(r.Hits))
	return r
}

func mkShadowEndpoints(src, tgt types.SearchResult) (esopsmigrate.Endpoint, esopsmigrate.Endpoint, *fakeSearcher, *fakeSearcher) {
	srcS := &fakeSearcher{result: src}
	tgtS := &fakeSearcher{result: tgt}
	srcEP := esopsmigrate.Endpoint{Context: "src", URL: "http://es-src:9200", Info: types.ClusterInfo{Dialect: types.DialectElasticsearch}, Search: srcS}
	tgtEP := esopsmigrate.Endpoint{Context: "tgt", URL: "http://es-tgt:9200", Info: types.ClusterInfo{Dialect: types.DialectOpenSearch}, Search: tgtS}
	return srcEP, tgtEP, srcS, tgtS
}

func oneQuery() []types.ShadowQuery {
	return []types.ShadowQuery{{Name: "q1", Body: json.RawMessage(`{"query":{"match_all":{}}}`)}}
}

func sortedQuery() []types.ShadowQuery {
	return []types.ShadowQuery{{Name: "q1", Body: json.RawMessage(`{"query":{"match_all":{}},"sort":[{"ts":"asc"}]}`)}}
}

func TestShadow_IdenticalHitsOK(t *testing.T) {
	same := hits("a", 1.0, "b", 0.9, "c", 0.8)
	src, tgt, _, _ := mkShadowEndpoints(same, same)

	res, err := esopsmigrate.Shadow(context.Background(), src, tgt, types.MigrationShadowRequest{Queries: oneQuery()})
	if err != nil {
		t.Fatalf("Shadow: %v", err)
	}
	if res.Status != types.MigrationStatusOK {
		t.Errorf("status = %q, want ok", res.Status)
	}
	e := res.Queries[0]
	if e.Status != types.ShadowStatusOK || e.SetDiff != 0 || e.OrderDiff != 0 {
		t.Errorf("entry = %+v, want ok/0/0", e)
	}
	if e.SourceHits != 3 || e.TargetHits != 3 {
		t.Errorf("hits = %d/%d, want 3/3", e.SourceHits, e.TargetHits)
	}
}

func TestShadow_SetDifferenceMismatches(t *testing.T) {
	src, tgt, _, _ := mkShadowEndpoints(
		hits("a", 1.0, "b", 0.9),
		hits("a", 1.0, "z", 0.9), // b vs z
	)
	res, err := esopsmigrate.Shadow(context.Background(), src, tgt, types.MigrationShadowRequest{Queries: oneQuery()})
	if err != nil {
		t.Fatalf("Shadow: %v", err)
	}
	e := res.Queries[0]
	if e.Status != types.ShadowStatusMismatch {
		t.Errorf("status = %q, want mismatch", e.Status)
	}
	if e.SetDiff != 2 { // b only in src, z only in tgt
		t.Errorf("set_diff = %d, want 2", e.SetDiff)
	}
	if res.Mismatches != 1 || res.Status != types.MigrationStatusBlock {
		t.Errorf("result = %+v, want 1 mismatch / block", res)
	}
}

func TestShadow_OrderDifferenceMismatchesWithSort(t *testing.T) {
	src, tgt, _, _ := mkShadowEndpoints(
		hits("a", 1.0, "b", 0.9, "c", 0.8),
		hits("b", 1.0, "a", 0.9, "c", 0.8), // a,b swapped
	)
	res, err := esopsmigrate.Shadow(context.Background(), src, tgt, types.MigrationShadowRequest{Queries: sortedQuery()})
	if err != nil {
		t.Fatalf("Shadow: %v", err)
	}
	e := res.Queries[0]
	if e.SetDiff != 0 {
		t.Errorf("set_diff = %d, want 0 (same set)", e.SetDiff)
	}
	if e.OrderDiff == 0 || e.Status != types.ShadowStatusMismatch {
		t.Errorf("expected order mismatch for a sorted query, got %+v", e)
	}
}

func TestShadow_OrderDifferenceIgnoredWithoutSort(t *testing.T) {
	// Same hit set, different order, but the query has no sort — tie
	// order is non-deterministic across clusters, so an order difference
	// is reported but does NOT fail the check.
	src, tgt, _, _ := mkShadowEndpoints(
		hits("a", 1.0, "b", 1.0, "c", 1.0),
		hits("b", 1.0, "a", 1.0, "c", 1.0),
	)
	res, err := esopsmigrate.Shadow(context.Background(), src, tgt, types.MigrationShadowRequest{Queries: oneQuery()})
	if err != nil {
		t.Fatalf("Shadow: %v", err)
	}
	e := res.Queries[0]
	if e.OrderDiff == 0 {
		t.Errorf("order_diff should still be reported, got 0")
	}
	if e.Status != types.ShadowStatusOK {
		t.Errorf("status = %q, want ok (no sort → order diff is not a regression)", e.Status)
	}
}

func TestShadow_SetDiffDeduplicatesRepeatedIDs(t *testing.T) {
	// A duplicate index+id in one window must not double-count: the
	// symmetric set difference here is 1 (only z is unique to target).
	src, tgt, _, _ := mkShadowEndpoints(
		hits("a", 1.0, "a", 1.0),
		hits("a", 1.0, "z", 0.9),
	)
	res, err := esopsmigrate.Shadow(context.Background(), src, tgt, types.MigrationShadowRequest{Queries: oneQuery()})
	if err != nil {
		t.Fatalf("Shadow: %v", err)
	}
	if got := res.Queries[0].SetDiff; got != 1 {
		t.Errorf("set_diff = %d, want 1 (sets, not slices)", got)
	}
}

func TestShadow_LargeScoresWithinToleranceNotFlagged(t *testing.T) {
	// Two float32 scores that differ only by round-trip noise at a large
	// magnitude must not register as a score divergence under strict.
	src, tgt, _, _ := mkShadowEndpoints(
		hits("a", 12345.6789),
		hits("a", 12345.6790),
	)
	res, err := esopsmigrate.Shadow(context.Background(), src, tgt, types.MigrationShadowRequest{
		Queries:      oneQuery(),
		StrictScores: true,
	})
	if err != nil {
		t.Fatalf("Shadow: %v", err)
	}
	if got := res.Queries[0].ScoreDiff; got != 0 {
		t.Errorf("score_diff = %d, want 0 (within relative tolerance)", got)
	}
}

func TestShadow_ScoreDiffReportedButNotFailingByDefault(t *testing.T) {
	src, tgt, _, _ := mkShadowEndpoints(
		hits("a", 1.0, "b", 0.9),
		hits("a", 2.5, "b", 0.9), // a's score differs
	)
	res, err := esopsmigrate.Shadow(context.Background(), src, tgt, types.MigrationShadowRequest{Queries: oneQuery()})
	if err != nil {
		t.Fatalf("Shadow: %v", err)
	}
	e := res.Queries[0]
	if e.ScoreDiff != 1 {
		t.Errorf("score_diff = %d, want 1", e.ScoreDiff)
	}
	if e.Status != types.ShadowStatusOK {
		t.Errorf("status = %q, want ok (score drift is not a failure by default)", e.Status)
	}
}

func TestShadow_StrictScoresPromotesToMismatch(t *testing.T) {
	src, tgt, _, _ := mkShadowEndpoints(
		hits("a", 1.0, "b", 0.9),
		hits("a", 2.5, "b", 0.9),
	)
	res, err := esopsmigrate.Shadow(context.Background(), src, tgt, types.MigrationShadowRequest{
		Queries:      oneQuery(),
		StrictScores: true,
	})
	if err != nil {
		t.Fatalf("Shadow: %v", err)
	}
	if res.Queries[0].Status != types.ShadowStatusMismatch {
		t.Errorf("status = %q, want mismatch under --strict-scores", res.Queries[0].Status)
	}
}

func TestShadow_PerQueryErrorRow(t *testing.T) {
	src, tgt, _, tgtS := mkShadowEndpoints(hits("a", 1.0), hits("a", 1.0))
	tgtS.err = errors.New("boom")

	res, err := esopsmigrate.Shadow(context.Background(), src, tgt, types.MigrationShadowRequest{Queries: oneQuery()})
	if err != nil {
		t.Fatalf("Shadow returned fatal err for a non-fatal failure: %v", err)
	}
	if res.Errors != 1 || res.Queries[0].Status != types.ShadowStatusError {
		t.Errorf("expected one error row, got %+v", res)
	}
	if res.Status != types.MigrationStatusBlock {
		t.Errorf("status = %q, want block", res.Status)
	}
}

func TestShadow_AuthErrorIsFatal(t *testing.T) {
	src, tgt, srcS, _ := mkShadowEndpoints(hits("a", 1.0), hits("a", 1.0))
	srcS.err = client.ErrAuth

	_, err := esopsmigrate.Shadow(context.Background(), src, tgt, types.MigrationShadowRequest{Queries: oneQuery()})
	if !errors.Is(err, client.ErrAuth) {
		t.Fatalf("expected fatal ErrAuth, got %v", err)
	}
}

func TestShadow_ForwardsTopKAndIndices(t *testing.T) {
	src, tgt, srcS, _ := mkShadowEndpoints(hits("a", 1.0), hits("a", 1.0))

	if _, err := esopsmigrate.Shadow(context.Background(), src, tgt, types.MigrationShadowRequest{
		Queries: oneQuery(),
		Indices: "logs-*,events-*",
		TopK:    25,
	}); err != nil {
		t.Fatalf("Shadow: %v", err)
	}
	if len(srcS.gotReqs) != 1 {
		t.Fatalf("expected 1 source request, got %d", len(srcS.gotReqs))
	}
	got := srcS.gotReqs[0]
	if got.Size != 25 {
		t.Errorf("Size = %d, want 25", got.Size)
	}
	if len(got.Indices) != 2 || got.Indices[0] != "logs-*" || got.Indices[1] != "events-*" {
		t.Errorf("Indices = %v, want [logs-* events-*]", got.Indices)
	}
	if !got.IgnoreUnavailable {
		t.Errorf("expected IgnoreUnavailable=true on the shadow search")
	}
}

func TestShadow_IncludeBodyToggle(t *testing.T) {
	src, tgt, _, _ := mkShadowEndpoints(hits("a", 1.0), hits("a", 1.0))

	off, _ := esopsmigrate.Shadow(context.Background(), src, tgt, types.MigrationShadowRequest{Queries: oneQuery()})
	if off.Queries[0].Body != nil {
		t.Errorf("body should be redacted (nil) by default, got %s", off.Queries[0].Body)
	}

	on, _ := esopsmigrate.Shadow(context.Background(), src, tgt, types.MigrationShadowRequest{Queries: oneQuery(), IncludeBody: true})
	if len(on.Queries[0].Body) == 0 {
		t.Errorf("body should be present with IncludeBody=true")
	}
}

// blockingSearcher blocks until its context is cancelled, then returns
// the classified transport error the real adapter would (deadline →
// ErrUnreachable-wrapped), so the timeout path can be exercised.
type blockingSearcher struct{}

func (blockingSearcher) Search(ctx context.Context, _ types.SearchRequest) (types.SearchResult, error) {
	<-ctx.Done()
	return types.SearchResult{}, fmt.Errorf("%w: %v", client.ErrUnreachable, ctx.Err())
}

func TestShadow_PerQueryTimeoutSurfacesAsTimeoutNotUnreachable(t *testing.T) {
	src := esopsmigrate.Endpoint{Context: "src", Info: types.ClusterInfo{Dialect: types.DialectElasticsearch}, Search: blockingSearcher{}}
	tgt := esopsmigrate.Endpoint{Context: "tgt", Info: types.ClusterInfo{Dialect: types.DialectOpenSearch}, Search: blockingSearcher{}}

	res, err := esopsmigrate.Shadow(context.Background(), src, tgt, types.MigrationShadowRequest{
		Queries:      oneQuery(),
		QueryTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Shadow: %v", err)
	}
	e := res.Queries[0]
	if e.Status != types.ShadowStatusError {
		t.Fatalf("status = %q, want error", e.Status)
	}
	if !strings.Contains(e.Detail, "timed out") {
		t.Errorf("detail = %q, want a 'timed out' message (not 'unreachable')", e.Detail)
	}
}

func TestShadow_MissingSearchCapabilityIsWiringError(t *testing.T) {
	src, tgt, _, _ := mkShadowEndpoints(hits("a", 1.0), hits("a", 1.0))
	src.Search = nil

	_, err := esopsmigrate.Shadow(context.Background(), src, tgt, types.MigrationShadowRequest{Queries: oneQuery()})
	if err == nil {
		t.Fatal("expected a wiring error when SearchExecutor is nil")
	}
}
