package migrate_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	esopsmigrate "github.com/esops-dev/esops-go/internal/migrate"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeReindexer captures the SubmitReindex calls and serves
// pre-canned task statuses. The status slice is consumed in order;
// the last entry repeats indefinitely so a poll loop can run for as
// many iterations as the test sets up.
type fakeReindexer struct {
	mu       sync.Mutex
	submits  []types.ReindexSubmitRequest
	submitFn func(types.ReindexSubmitRequest) (types.ReindexSubmit, error)

	statusCalls int
	statuses    []types.ReindexTaskStatus
	statusErr   error
}

func (f *fakeReindexer) SubmitReindex(_ context.Context, req types.ReindexSubmitRequest) (types.ReindexSubmit, error) {
	f.mu.Lock()
	f.submits = append(f.submits, req)
	f.mu.Unlock()
	if f.submitFn != nil {
		return f.submitFn(req)
	}
	return types.ReindexSubmit{
		Action:      "migrate-reindex",
		TaskID:      "task-" + req.SourceIndex,
		SourceIndex: req.SourceIndex,
		TargetIndex: req.TargetIndex,
	}, nil
}

func (f *fakeReindexer) ReindexTaskStatus(_ context.Context, id string) (types.ReindexTaskStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusErr != nil {
		f.statusCalls++
		return types.ReindexTaskStatus{}, f.statusErr
	}
	idx := f.statusCalls
	if idx >= len(f.statuses) {
		idx = len(f.statuses) - 1
	}
	f.statusCalls++
	if idx < 0 {
		return types.ReindexTaskStatus{TaskID: id, Completed: true}, nil
	}
	out := f.statuses[idx]
	out.TaskID = id
	return out, nil
}

func mkReindexEndpoints(t *testing.T, ix []types.Index, r *fakeReindexer) (esopsmigrate.Endpoint, esopsmigrate.Endpoint) {
	t.Helper()
	// Resumability persists task state under XDG_STATE_HOME; isolate
	// per-test so a real run on the developer's box stays unaffected.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	src := mkSrc(t,
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-src:9200",
		&fakeIndices{indices: ix}, &fakeMappings{})
	tgt := esopsmigrate.Endpoint{
		Context: "tgt",
		URL:     "http://es-tgt:9200",
		Info:    types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		Reindex: r,
	}
	return src, tgt
}

func TestReindex_DryRunDoesNotSubmit(t *testing.T) {
	r := &fakeReindexer{}
	src, tgt := mkReindexEndpoints(t, []types.Index{{Name: "products"}}, r)

	res, err := esopsmigrate.Reindex(context.Background(), src, tgt, types.MigrationReindexRequest{
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.DryRun {
		t.Errorf("DryRun flag did not propagate to result")
	}
	if len(r.submits) != 0 {
		t.Errorf("dry-run should not have submitted; got %d", len(r.submits))
	}
	if res.Status != types.MigrationStatusOK {
		t.Errorf("dry-run status: got %q, want ok", res.Status)
	}
}

func TestReindex_FanOutPerSourceIndex(t *testing.T) {
	r := &fakeReindexer{
		statuses: []types.ReindexTaskStatus{
			{Completed: true, Total: 100, Created: 100},
		},
	}
	indices := []types.Index{
		{Name: "logs"},
		{Name: "products"},
	}
	src, tgt := mkReindexEndpoints(t, indices, r)

	res, err := esopsmigrate.Reindex(context.Background(), src, tgt, types.MigrationReindexRequest{
		TargetSuffix: "-v2",
		PollInterval: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(r.submits) != 2 {
		t.Fatalf("expected 2 submissions, got %d", len(r.submits))
	}
	wantTargets := map[string]string{"logs": "logs-v2", "products": "products-v2"}
	for _, s := range r.submits {
		if wantTargets[s.SourceIndex] != s.TargetIndex {
			t.Errorf("source %q -> target %q, want %q", s.SourceIndex, s.TargetIndex, wantTargets[s.SourceIndex])
		}
		if s.SourceRemoteHost != "http://es-src:9200" {
			t.Errorf("source.remote.host: got %q, want http://es-src:9200", s.SourceRemoteHost)
		}
	}
	if res.Status != types.MigrationStatusOK || len(res.Indices) != 2 {
		t.Errorf("expected ok with 2 entries, got status=%q len=%d", res.Status, len(res.Indices))
	}
}

func TestReindex_FailuresMarkBlock(t *testing.T) {
	r := &fakeReindexer{
		statuses: []types.ReindexTaskStatus{
			{Completed: true, Total: 100, Failures: []string{"products[42]: mapper_parsing_exception: bad data"}},
		},
	}
	src, tgt := mkReindexEndpoints(t, []types.Index{{Name: "products"}}, r)

	res, err := esopsmigrate.Reindex(context.Background(), src, tgt, types.MigrationReindexRequest{
		PollInterval: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != types.MigrationStatusBlock {
		t.Fatalf("expected block on failure, got %q", res.Status)
	}
	if res.Blockers != 1 {
		t.Errorf("expected 1 blocker, got %d", res.Blockers)
	}
}

func TestReindex_VersionConflictsWithProceedWarn(t *testing.T) {
	r := &fakeReindexer{
		statuses: []types.ReindexTaskStatus{
			{Completed: true, Total: 100, Created: 80, VersionConflicts: 20},
		},
	}
	src, tgt := mkReindexEndpoints(t, []types.Index{{Name: "logs"}}, r)

	res, err := esopsmigrate.Reindex(context.Background(), src, tgt, types.MigrationReindexRequest{
		Conflicts:    "proceed",
		PollInterval: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != types.MigrationStatusWarn {
		t.Errorf("expected warn on proceed+conflicts, got %q", res.Status)
	}
}

func TestReindex_SubmitAuthErrorPropagates(t *testing.T) {
	r := &fakeReindexer{
		submitFn: func(_ types.ReindexSubmitRequest) (types.ReindexSubmit, error) {
			return types.ReindexSubmit{}, client.ErrAuth
		},
	}
	src, tgt := mkReindexEndpoints(t, []types.Index{{Name: "logs"}}, r)

	_, err := esopsmigrate.Reindex(context.Background(), src, tgt, types.MigrationReindexRequest{
		PollInterval: 1 * time.Millisecond,
	})
	if !errors.Is(err, client.ErrAuth) {
		t.Fatalf("expected ErrAuth, got %v", err)
	}
}

func TestReindex_SubmitGenericErrorPerIndexBlock(t *testing.T) {
	r := &fakeReindexer{
		submitFn: func(req types.ReindexSubmitRequest) (types.ReindexSubmit, error) {
			if req.SourceIndex == "logs" {
				return types.ReindexSubmit{}, errors.New("simulated failure")
			}
			return types.ReindexSubmit{
				Action: "migrate-reindex", TaskID: "task-" + req.SourceIndex,
				SourceIndex: req.SourceIndex, TargetIndex: req.TargetIndex,
			}, nil
		},
		statuses: []types.ReindexTaskStatus{{Completed: true, Total: 1, Created: 1}},
	}
	indices := []types.Index{{Name: "logs"}, {Name: "products"}}
	src, tgt := mkReindexEndpoints(t, indices, r)

	res, err := esopsmigrate.Reindex(context.Background(), src, tgt, types.MigrationReindexRequest{
		PollInterval: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// One source failed (block), one succeeded (ok). Overall blocks.
	if res.Status != types.MigrationStatusBlock {
		t.Fatalf("expected block, got %q", res.Status)
	}
	if res.Blockers != 1 {
		t.Errorf("expected 1 blocker, got %d", res.Blockers)
	}
}

func TestReindex_ToIndexRequiresSingleSource(t *testing.T) {
	r := &fakeReindexer{}
	indices := []types.Index{{Name: "logs"}, {Name: "products"}}
	src, tgt := mkReindexEndpoints(t, indices, r)

	_, err := esopsmigrate.Reindex(context.Background(), src, tgt, types.MigrationReindexRequest{
		TargetIndexLiteral: "logs-v2",
	})
	if !errors.Is(err, client.ErrUsage) {
		t.Fatalf("expected ErrUsage, got %v", err)
	}
}

func TestReindex_ConflictsValidation(t *testing.T) {
	r := &fakeReindexer{}
	src, tgt := mkReindexEndpoints(t, []types.Index{{Name: "logs"}}, r)

	_, err := esopsmigrate.Reindex(context.Background(), src, tgt, types.MigrationReindexRequest{
		Conflicts: "ignore", // not a valid value
	})
	if !errors.Is(err, client.ErrUsage) {
		t.Fatalf("expected ErrUsage on bad conflicts value, got %v", err)
	}
}

func TestReindex_AuthHeadersPropagateToSubmit(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	r := &fakeReindexer{
		statuses: []types.ReindexTaskStatus{{Completed: true}},
	}
	src := mkSrc(t,
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-src:9200",
		&fakeIndices{indices: []types.Index{{Name: "logs"}}},
		&fakeMappings{})
	src.Auth = esopsmigrate.EndpointAuth{Username: "u", Password: "p"}
	tgt := esopsmigrate.Endpoint{
		Context: "tgt",
		URL:     "http://es-tgt:9200",
		Info:    types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		Reindex: r,
	}

	_, err := esopsmigrate.Reindex(context.Background(), src, tgt, types.MigrationReindexRequest{
		PollInterval: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(r.submits) != 1 {
		t.Fatalf("expected one submission, got %d", len(r.submits))
	}
	if r.submits[0].SourceUsername != "u" || r.submits[0].SourcePassword != "p" {
		t.Errorf("source basic-auth not forwarded: got user=%q pass=%q", r.submits[0].SourceUsername, r.submits[0].SourcePassword)
	}
}

func TestReindex_NoSourceIndicesReturnsOK(t *testing.T) {
	r := &fakeReindexer{}
	src, tgt := mkReindexEndpoints(t, nil, r)

	res, err := esopsmigrate.Reindex(context.Background(), src, tgt, types.MigrationReindexRequest{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != types.MigrationStatusOK || len(res.Indices) != 0 {
		t.Errorf("empty source: got status=%q indices=%d", res.Status, len(res.Indices))
	}
	if len(r.submits) != 0 {
		t.Errorf("expected no submissions, got %d", len(r.submits))
	}
}

// fakeTasksCanceller captures /_tasks/<id>/_cancel calls so the SIGINT
// path can be asserted without a live cluster.
type fakeTasksCanceller struct {
	mu     sync.Mutex
	calls  []types.TasksCancelRequest
	result types.TasksCancelResult
	err    error
}

func (f *fakeTasksCanceller) CancelTasks(_ context.Context, req types.TasksCancelRequest) (types.TasksCancelResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	return f.result, f.err
}

func TestReindex_SIGINTCancelsRemoteTask(t *testing.T) {
	r := &fakeReindexer{
		statuses: []types.ReindexTaskStatus{
			{Completed: false, Total: 100, Created: 0},
		},
	}
	src, tgt := mkReindexEndpoints(t, []types.Index{{Name: "logs"}}, r)
	cancelStub := &fakeTasksCanceller{}
	tgt.TasksCancel = cancelStub

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after the first poll has happened.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := esopsmigrate.Reindex(ctx, src, tgt, types.MigrationReindexRequest{
		PollInterval: 1 * time.Millisecond,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if len(cancelStub.calls) != 1 {
		t.Fatalf("expected one /_tasks cancel, got %d", len(cancelStub.calls))
	}
	if got := cancelStub.calls[0].TaskIDs; len(got) != 1 || got[0] != "task-logs" {
		t.Errorf("cancel target: got %v, want [task-logs]", got)
	}
}

func TestReindex_PollNotFoundShortCircuits(t *testing.T) {
	r := &fakeReindexer{
		statusErr: client.ErrNotFound,
	}
	src, tgt := mkReindexEndpoints(t, []types.Index{{Name: "logs"}}, r)

	res, err := esopsmigrate.Reindex(context.Background(), src, tgt, types.MigrationReindexRequest{
		PollInterval: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != types.MigrationStatusBlock || res.Blockers != 1 {
		t.Fatalf("expected single block on ErrNotFound, got %+v", res)
	}
	if got := res.Indices[0].Detail; got == "" || !strings.Contains(got, "no longer exists") {
		t.Errorf("expected detail to mention the missing task, got %q", got)
	}
	// Crucially, ErrNotFound must NOT loop forever — exactly one poll
	// call, then we abort.
	if r.statusCalls != 1 {
		t.Errorf("expected 1 poll attempt on ErrNotFound, got %d", r.statusCalls)
	}
}

// TestReindex_PartialBlockPersistsStatusAcrossResume — a partial run
// that produces a per-row block must NOT silently upgrade to OK on
// `--resume`. The orchestrator persists Status+Detail alongside the
// task ID; resume replays them verbatim.
func TestReindex_PartialBlockPersistsStatusAcrossResume(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	r := &fakeReindexer{
		statuses: []types.ReindexTaskStatus{
			{Completed: true, Total: 100, Failures: []string{"products[42]: mapper_parsing_exception: bad data"}},
		},
	}
	src, tgt := mkReindexEndpoints(t, []types.Index{{Name: "products"}}, r)
	src.Context = "src-partial"
	tgt.Context = "tgt-partial"

	// First run: poll completes with a failure -> per-row block.
	res, err := esopsmigrate.Reindex(context.Background(), src, tgt, types.MigrationReindexRequest{
		PollInterval: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("first run err: %v", err)
	}
	if res.Status != types.MigrationStatusBlock || res.Indices[0].Status != types.MigrationStatusBlock {
		t.Fatalf("first run should block, got %+v", res)
	}
	originalDetail := res.Indices[0].Detail

	// State should still be on disk (block status doesn't clear it).
	state, loadErr := esopsmigrate.LoadReindexState("src-partial", "tgt-partial", "", "")
	if loadErr != nil {
		t.Fatalf("expected state preserved on block, got err=%v", loadErr)
	}
	if len(state.Entries) != 1 || state.Entries[0].Status != types.MigrationStatusBlock {
		t.Fatalf("state did not persist block status: %+v", state.Entries)
	}

	// Resume: must report block, not OK.
	resumed, err := esopsmigrate.Reindex(context.Background(), src, tgt, types.MigrationReindexRequest{
		Resume:       true,
		PollInterval: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("resume err: %v", err)
	}
	if resumed.Status != types.MigrationStatusBlock {
		t.Errorf("resume should preserve block status, got %q", resumed.Status)
	}
	if resumed.Indices[0].Status != types.MigrationStatusBlock {
		t.Errorf("per-row status: got %q, want block", resumed.Indices[0].Status)
	}
	if resumed.Indices[0].Detail != originalDetail {
		t.Errorf("per-row detail not replayed:\n got %q\nwant %q", resumed.Indices[0].Detail, originalDetail)
	}
}

// TestReindex_ResumeLegacyStateDefaultsToOK — older state files
// (schema-1 entries written before the Status field landed) carry an
// empty Status. The reader must keep treating those as the prior
// "previously completed: OK" behaviour for backwards compatibility.
func TestReindex_ResumeLegacyStateDefaultsToOK(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := esopsmigrate.SaveReindexState(esopsmigrate.ReindexState{
		Action:        "migrate-reindex",
		SourceContext: "src-legacy",
		TargetContext: "tgt-legacy",
		StartedAt:     time.Now().UTC(),
		Entries: []esopsmigrate.ReindexStateEntry{
			{SourceIndex: "logs", TargetIndex: "logs", TaskID: "task-logs", Completed: true},
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	src := mkSrc(t,
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-src:9200", &fakeIndices{indices: nil}, &fakeMappings{})
	src.Context = "src-legacy"
	tgt := esopsmigrate.Endpoint{
		Context: "tgt-legacy",
		URL:     "http://es-tgt:9200",
		Info:    types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		Reindex: &fakeReindexer{},
	}

	res, err := esopsmigrate.Reindex(context.Background(), src, tgt, types.MigrationReindexRequest{
		Resume:       true,
		PollInterval: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("resume err: %v", err)
	}
	if res.Status != types.MigrationStatusOK || res.Indices[0].Status != types.MigrationStatusOK {
		t.Errorf("legacy state should resume as OK, got status=%q row=%q", res.Status, res.Indices[0].Status)
	}
	if res.Indices[0].Detail != "previously completed" {
		t.Errorf("legacy detail: got %q", res.Indices[0].Detail)
	}
}

func TestReindex_ResumeContinuesPersistedTask(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	// Seed state as if a prior run had submitted but not finished.
	state := esopsmigrate.ReindexState{
		Action:        "migrate-reindex",
		SourceContext: "src",
		TargetContext: "tgt",
		TargetSuffix:  "-v2",
		StartedAt:     time.Now().UTC(),
		Entries: []esopsmigrate.ReindexStateEntry{
			{SourceIndex: "logs", TargetIndex: "logs-v2", TaskID: "task-logs"},
		},
	}
	if err := esopsmigrate.SaveReindexState(state); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	r := &fakeReindexer{
		statuses: []types.ReindexTaskStatus{
			{Completed: true, Total: 100, Created: 100},
		},
	}
	// Note: src.Indices is intentionally empty; resume should NOT
	// list source indices (it works off the prior state).
	src := mkSrc(t,
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-src:9200",
		&fakeIndices{indices: nil}, &fakeMappings{})
	src.Context = "src"
	tgt := esopsmigrate.Endpoint{
		Context: "tgt",
		URL:     "http://es-tgt:9200",
		Info:    types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		Reindex: r,
	}

	res, err := esopsmigrate.Reindex(context.Background(), src, tgt, types.MigrationReindexRequest{
		Resume:       true,
		TargetSuffix: "-v2",
		PollInterval: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != types.MigrationStatusOK || len(res.Indices) != 1 {
		t.Fatalf("resume status: got %+v", res)
	}
	if len(r.submits) != 0 {
		t.Errorf("resume must not submit new reindexes; got %d", len(r.submits))
	}
	// Successful resume should clear the state file.
	if _, err := esopsmigrate.LoadReindexState("src", "tgt", "-v2", ""); !errors.Is(err, esopsmigrate.ErrNoReindexState) {
		t.Errorf("expected state cleared after successful resume, got err=%v", err)
	}
}

func TestReindex_ResumeWithNoStateIsUsageError(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	r := &fakeReindexer{}
	src, tgt := mkReindexEndpoints(t, nil, r)
	src.Context = "src-empty"
	tgt.Context = "tgt-empty"

	_, err := esopsmigrate.Reindex(context.Background(), src, tgt, types.MigrationReindexRequest{
		Resume: true,
	})
	if !errors.Is(err, client.ErrUsage) {
		t.Fatalf("expected ErrUsage on missing state, got %v", err)
	}
}
