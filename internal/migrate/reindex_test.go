package migrate_test

import (
	"context"
	"errors"
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
	if f.statusErr != nil {
		return types.ReindexTaskStatus{}, f.statusErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
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
