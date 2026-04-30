package index_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	esopsclient "github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/index"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// fakeOptimizer captures the request the business layer issues and
// returns a canned response. Mirrors fakeStateChanger in shape so the
// two test files stay consistent with each other.
type fakeOptimizer struct {
	calls []types.IndexOptimizeRequest
	resp  types.IndexOptimizeResult
	err   error
}

func (f *fakeOptimizer) ForceMerge(_ context.Context, req types.IndexOptimizeRequest) (types.IndexOptimizeResult, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return types.IndexOptimizeResult{}, f.err
	}
	return f.resp, nil
}

func okOptimizeResp() types.IndexOptimizeResult {
	return types.IndexOptimizeResult{
		Action: "optimize", ShardsTotal: 3, ShardsSuccess: 3,
	}
}

func TestOptimize_rejectsEmptyPattern(t *testing.T) {
	insp := &fakeIndexInspector{}
	opt := &fakeOptimizer{resp: okOptimizeResp()}
	_, err := index.Optimize(context.Background(), insp, opt, index.OptimizeOptions{})
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
	if len(opt.calls) != 0 {
		t.Errorf("cluster contacted on validation failure: %+v", opt.calls)
	}
}

func TestOptimize_rejectsMatchAllPattern(t *testing.T) {
	for _, pattern := range []string{"*", "_all"} {
		insp := &fakeIndexInspector{}
		opt := &fakeOptimizer{resp: okOptimizeResp()}
		_, err := index.Optimize(context.Background(), insp, opt, index.OptimizeOptions{
			Pattern: pattern,
		})
		if !errors.Is(err, esopsclient.ErrBlastRadius) {
			t.Errorf("pattern %q: err = %v, want ErrBlastRadius", pattern, err)
		}
	}
}

func TestOptimize_rejectsNegativeMaxNumSegments(t *testing.T) {
	insp := &fakeIndexInspector{}
	opt := &fakeOptimizer{resp: okOptimizeResp()}
	_, err := index.Optimize(context.Background(), insp, opt, index.OptimizeOptions{
		Pattern: "logs-*", MaxNumSegments: -1,
	})
	if err == nil || !strings.Contains(err.Error(), "max-num-segments") {
		t.Errorf("expected error mentioning max-num-segments, got: %v", err)
	}
}

func TestOptimize_skipsClosedIndices(t *testing.T) {
	insp := &fakeIndexInspector{indices: []types.Index{
		{Name: "logs-2024", Status: "open"},
		{Name: "logs-2023", Status: "close"},
		{Name: "logs-2022", Status: "open"},
	}}
	opt := &fakeOptimizer{resp: okOptimizeResp()}
	got, err := index.Optimize(context.Background(), insp, opt, index.OptimizeOptions{
		Pattern: "logs-*",
	})
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	wantTarget := []string{"logs-2022", "logs-2024"}
	if !equalSlices(got.Indices, wantTarget) {
		t.Errorf("Indices = %v, want %v", got.Indices, wantTarget)
	}
	// The single closed index should be reflected in Failures.
	var foundClosedNote bool
	for _, msg := range got.Failures {
		if strings.Contains(msg, "logs-2023") && strings.Contains(msg, "closed") {
			foundClosedNote = true
		}
	}
	if !foundClosedNote {
		t.Errorf("expected closed-index skip note in Failures, got %v", got.Failures)
	}
	if len(opt.calls) != 1 {
		t.Fatalf("expected 1 forcemerge call, got %d", len(opt.calls))
	}
	wantPath := "logs-2022,logs-2024"
	if opt.calls[0].Index != wantPath {
		t.Errorf("forcemerge request Index = %q, want %q", opt.calls[0].Index, wantPath)
	}
}

func TestOptimize_blastRadius_refusesAt21WithDefault(t *testing.T) {
	rows := make([]types.Index, 21)
	for i := range rows {
		rows[i] = types.Index{Name: indexName(i), Status: "open"}
	}
	insp := &fakeIndexInspector{indices: rows}
	opt := &fakeOptimizer{resp: okOptimizeResp()}
	_, err := index.Optimize(context.Background(), insp, opt, index.OptimizeOptions{
		Pattern: "rotated-*",
	})
	if !errors.Is(err, esopsclient.ErrBlastRadius) {
		t.Errorf("err = %v, want ErrBlastRadius", err)
	}
	if len(opt.calls) != 0 {
		t.Errorf("cluster contacted past blast-radius limit: %+v", opt.calls)
	}
}

func TestOptimize_blastRadius_overrideLetsItThrough(t *testing.T) {
	rows := make([]types.Index, 25)
	for i := range rows {
		rows[i] = types.Index{Name: indexName(i), Status: "open"}
	}
	insp := &fakeIndexInspector{indices: rows}
	opt := &fakeOptimizer{resp: okOptimizeResp()}
	_, err := index.Optimize(context.Background(), insp, opt, index.OptimizeOptions{
		Pattern: "rotated-*", MaxIndices: 50,
	})
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if len(opt.calls) != 1 {
		t.Errorf("expected 1 forcemerge call with override, got %d", len(opt.calls))
	}
}

func TestOptimize_dryRun_doesNotCallCluster(t *testing.T) {
	insp := &fakeIndexInspector{indices: []types.Index{
		{Name: "logs-2024", Status: "open"},
	}}
	opt := &fakeOptimizer{resp: okOptimizeResp()}
	got, err := index.Optimize(context.Background(), insp, opt, index.OptimizeOptions{
		Pattern: "logs-*", DryRun: true, MaxNumSegments: 1,
	})
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if !got.DryRun {
		t.Errorf("DryRun = false on result, want true")
	}
	if got.MaxNumSegments != 1 {
		t.Errorf("MaxNumSegments = %d, want 1", got.MaxNumSegments)
	}
	if len(opt.calls) != 0 {
		t.Errorf("dry-run contacted cluster: %+v", opt.calls)
	}
	if len(got.Indices) != 1 || got.Indices[0] != "logs-2024" {
		t.Errorf("Indices = %v, want [logs-2024]", got.Indices)
	}
}

func TestOptimize_emptyMatchIsNoOp(t *testing.T) {
	insp := &fakeIndexInspector{indices: nil}
	opt := &fakeOptimizer{resp: okOptimizeResp()}
	got, err := index.Optimize(context.Background(), insp, opt, index.OptimizeOptions{
		Pattern: "missing-*",
	})
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if len(got.Indices) != 0 {
		t.Errorf("expected 0 indices, got %d", len(got.Indices))
	}
	if len(opt.calls) != 0 {
		t.Errorf("empty match contacted cluster: %+v", opt.calls)
	}
}

func TestOptimize_propagatesAdapterError(t *testing.T) {
	sentinel := errors.New("boom")
	insp := &fakeIndexInspector{indices: []types.Index{
		{Name: "logs", Status: "open"},
	}}
	opt := &fakeOptimizer{err: sentinel}
	_, err := index.Optimize(context.Background(), insp, opt, index.OptimizeOptions{
		Pattern: "logs",
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

func TestOptimize_passesKnobsThrough(t *testing.T) {
	insp := &fakeIndexInspector{indices: []types.Index{
		{Name: "logs", Status: "open"},
	}}
	opt := &fakeOptimizer{resp: okOptimizeResp()}
	flush := false
	_, err := index.Optimize(context.Background(), insp, opt, index.OptimizeOptions{
		Pattern:            "logs",
		MaxNumSegments:     1,
		OnlyExpungeDeletes: true,
		Flush:              &flush,
		IgnoreUnavailable:  true,
	})
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if len(opt.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(opt.calls))
	}
	got := opt.calls[0]
	if got.MaxNumSegments != 1 {
		t.Errorf("MaxNumSegments = %d, want 1", got.MaxNumSegments)
	}
	if !got.OnlyExpungeDeletes {
		t.Errorf("OnlyExpungeDeletes = false, want true")
	}
	if got.Flush == nil || *got.Flush != false {
		t.Errorf("Flush = %v, want pointer to false", got.Flush)
	}
	if !got.IgnoreUnavailable {
		t.Errorf("IgnoreUnavailable = false, want true")
	}
}

func TestOptimize_propagatesShardFailures(t *testing.T) {
	insp := &fakeIndexInspector{indices: []types.Index{
		{Name: "logs", Status: "open"},
	}}
	opt := &fakeOptimizer{resp: types.IndexOptimizeResult{
		Action: "optimize", ShardsTotal: 3, ShardsSuccess: 2, ShardsFailed: 1,
		Failures: []string{"logs[0] failed: io_exception: disk full"},
	}}
	got, err := index.Optimize(context.Background(), insp, opt, index.OptimizeOptions{
		Pattern: "logs",
	})
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if got.ShardsFailed != 1 {
		t.Errorf("ShardsFailed = %d, want 1", got.ShardsFailed)
	}
	if len(got.Failures) != 1 {
		t.Errorf("Failures = %v, want 1 entry", got.Failures)
	}
}
