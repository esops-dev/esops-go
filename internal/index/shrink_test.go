package index_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dj-wasabi/esops-go/internal/index"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// fakeShrinker captures the request the business layer issues to the
// adapter and returns a canned response. Same shape as the other
// fakes in this package.
type fakeShrinker struct {
	calls []types.IndexShrinkRequest
	resp  types.IndexShrinkResult
	err   error
}

func (f *fakeShrinker) Shrink(_ context.Context, req types.IndexShrinkRequest) (types.IndexShrinkResult, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return types.IndexShrinkResult{}, f.err
	}
	r := f.resp
	if r.Source == "" {
		r.Source = req.Source
	}
	if r.Target == "" {
		r.Target = req.Target
	}
	return r, nil
}

func okShrinkResp() types.IndexShrinkResult {
	return types.IndexShrinkResult{
		Action: "shrink", Acknowledged: true, ShardsAcknowledged: true,
	}
}

func TestShrink_rejectsEmptySource(t *testing.T) {
	sh := &fakeShrinker{resp: okShrinkResp()}
	_, err := index.Shrink(context.Background(), sh, index.ShrinkOptions{
		Target: "logs-shrunk",
	})
	if err == nil || !strings.Contains(err.Error(), "source") {
		t.Errorf("expected error mentioning --source, got: %v", err)
	}
	if len(sh.calls) != 0 {
		t.Errorf("cluster contacted on validation failure: %+v", sh.calls)
	}
}

func TestShrink_rejectsEmptyTarget(t *testing.T) {
	sh := &fakeShrinker{resp: okShrinkResp()}
	_, err := index.Shrink(context.Background(), sh, index.ShrinkOptions{
		Source: "logs",
	})
	if err == nil || !strings.Contains(err.Error(), "target") {
		t.Errorf("expected error mentioning --target, got: %v", err)
	}
	if len(sh.calls) != 0 {
		t.Errorf("cluster contacted on validation failure: %+v", sh.calls)
	}
}

func TestShrink_rejectsIdenticalSourceAndTarget(t *testing.T) {
	sh := &fakeShrinker{resp: okShrinkResp()}
	_, err := index.Shrink(context.Background(), sh, index.ShrinkOptions{
		Source: "logs", Target: "logs",
	})
	if err == nil || !strings.Contains(err.Error(), "differ") {
		t.Errorf("expected error mentioning differ, got: %v", err)
	}
	if len(sh.calls) != 0 {
		t.Errorf("cluster contacted on validation failure: %+v", sh.calls)
	}
}

func TestShrink_rejectsPatternishNames(t *testing.T) {
	cases := []struct {
		name           string
		source, target string
	}{
		{"wildcard source", "logs-*", "logs-shrunk"},
		{"wildcard target", "logs", "logs-*"},
		{"match-all source", "*", "logs-shrunk"},
		{"_all target", "logs", "_all"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sh := &fakeShrinker{resp: okShrinkResp()}
			_, err := index.Shrink(context.Background(), sh, index.ShrinkOptions{
				Source: c.source, Target: c.target,
			})
			if err == nil {
				t.Errorf("expected pattern rejection for source=%q target=%q", c.source, c.target)
			}
			if len(sh.calls) != 0 {
				t.Errorf("cluster contacted on validation failure: %+v", sh.calls)
			}
		})
	}
}

func TestShrink_rejectsNegativeShards(t *testing.T) {
	sh := &fakeShrinker{resp: okShrinkResp()}
	_, err := index.Shrink(context.Background(), sh, index.ShrinkOptions{
		Source: "logs", Target: "logs-shrunk", TargetShards: -1,
	})
	if err == nil || !strings.Contains(err.Error(), "shards") {
		t.Errorf("expected error mentioning shards, got: %v", err)
	}
}

func TestShrink_rejectsNegativeReplicas(t *testing.T) {
	sh := &fakeShrinker{resp: okShrinkResp()}
	neg := -1
	_, err := index.Shrink(context.Background(), sh, index.ShrinkOptions{
		Source: "logs", Target: "logs-shrunk", TargetReplicas: &neg,
	})
	if err == nil || !strings.Contains(err.Error(), "replicas") {
		t.Errorf("expected error mentioning replicas, got: %v", err)
	}
}

func TestShrink_dryRun_doesNotCallCluster(t *testing.T) {
	sh := &fakeShrinker{resp: okShrinkResp()}
	got, err := index.Shrink(context.Background(), sh, index.ShrinkOptions{
		Source: "logs", Target: "logs-shrunk", TargetShards: 1, DryRun: true,
	})
	if err != nil {
		t.Fatalf("Shrink: %v", err)
	}
	if !got.DryRun {
		t.Errorf("DryRun = false on result, want true")
	}
	if !got.Acknowledged {
		t.Errorf("dry-run should ack synthetically (got %+v)", got)
	}
	if got.Source != "logs" || got.Target != "logs-shrunk" {
		t.Errorf("Source/Target = %q/%q, want logs/logs-shrunk", got.Source, got.Target)
	}
	if got.TargetShards != 1 {
		t.Errorf("TargetShards = %d, want 1", got.TargetShards)
	}
	if len(sh.calls) != 0 {
		t.Errorf("dry-run contacted cluster: %+v", sh.calls)
	}
}

func TestShrink_passesKnobsThrough(t *testing.T) {
	sh := &fakeShrinker{resp: okShrinkResp()}
	replicas := 0
	copySettings := true
	_, err := index.Shrink(context.Background(), sh, index.ShrinkOptions{
		Source:              "logs",
		Target:              "logs-shrunk",
		TargetShards:        1,
		TargetReplicas:      &replicas,
		CopySettings:        &copySettings,
		WaitForActiveShards: "all",
	})
	if err != nil {
		t.Fatalf("Shrink: %v", err)
	}
	if len(sh.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(sh.calls))
	}
	got := sh.calls[0]
	if got.Source != "logs" || got.Target != "logs-shrunk" {
		t.Errorf("Source/Target = %q/%q", got.Source, got.Target)
	}
	if got.TargetShards != 1 {
		t.Errorf("TargetShards = %d, want 1", got.TargetShards)
	}
	if got.TargetReplicas == nil || *got.TargetReplicas != 0 {
		t.Errorf("TargetReplicas = %v, want pointer to 0", got.TargetReplicas)
	}
	if got.CopySettings == nil || *got.CopySettings != true {
		t.Errorf("CopySettings = %v, want pointer to true", got.CopySettings)
	}
	if got.WaitForActiveShards != "all" {
		t.Errorf("WaitForActiveShards = %q, want 'all'", got.WaitForActiveShards)
	}
}

func TestShrink_propagatesAdapterError(t *testing.T) {
	sentinel := errors.New("boom")
	sh := &fakeShrinker{err: sentinel}
	_, err := index.Shrink(context.Background(), sh, index.ShrinkOptions{
		Source: "logs", Target: "logs-shrunk",
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}
