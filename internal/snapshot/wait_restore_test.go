package snapshot_test

import (
	"context"
	"errors"
	"testing"
	"time"

	esopsclient "github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/snapshot"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// fakeRecoveryInspector returns a scripted sequence of RecoveryReport
// values to the poller. Each call advances the script index; once the
// script is exhausted, the last entry is returned indefinitely (so a
// "stuck" state is easy to model for the cancel test).
type fakeRecoveryInspector struct {
	script []types.RecoveryReport
	calls  int
	err    error
}

func (f *fakeRecoveryInspector) Recovery(_ context.Context, _ types.RecoveryRequest) (types.RecoveryReport, error) {
	if f.err != nil {
		return types.RecoveryReport{}, f.err
	}
	if len(f.script) == 0 {
		return types.RecoveryReport{}, nil
	}
	if f.calls >= len(f.script) {
		f.calls++
		return f.script[len(f.script)-1], nil
	}
	out := f.script[f.calls]
	f.calls++
	return out, nil
}

func indexAt(name string, stages ...string) types.IndexRecovery {
	shards := make([]types.ShardRecovery, len(stages))
	for i, st := range stages {
		shards[i] = types.ShardRecovery{ID: i, Stage: st, Type: "SNAPSHOT"}
	}
	return types.IndexRecovery{Index: name, Shards: shards}
}

func TestWaitForRestore_stopsWhenAllShardsDone(t *testing.T) {
	rec := &fakeRecoveryInspector{script: []types.RecoveryReport{
		{Indices: []types.IndexRecovery{indexAt("logs-1", "INDEX", "INDEX")}},
		{Indices: []types.IndexRecovery{indexAt("logs-1", "TRANSLOG", "INDEX")}},
		{Indices: []types.IndexRecovery{indexAt("logs-1", "DONE", "DONE")}},
	}}
	got, err := snapshot.WaitForRestore(context.Background(), rec, snapshot.RestoreWaitOptions{
		Repository:    "fs",
		Snapshot:      "x",
		TargetIndices: []string{"logs-1"},
		PollInterval:  1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("WaitForRestore: %v", err)
	}
	if rec.calls != 3 {
		t.Errorf("polls = %d, want 3", rec.calls)
	}
	_ = got
}

func TestWaitForRestore_waitsForAllTargetsToAppear(t *testing.T) {
	// First poll: only logs-1 is in the report (logs-2 still allocating).
	// Second poll: both indices, both DONE.
	rec := &fakeRecoveryInspector{script: []types.RecoveryReport{
		{Indices: []types.IndexRecovery{indexAt("logs-1", "DONE")}},
		{Indices: []types.IndexRecovery{
			indexAt("logs-1", "DONE"),
			indexAt("logs-2", "DONE"),
		}},
	}}
	_, err := snapshot.WaitForRestore(context.Background(), rec, snapshot.RestoreWaitOptions{
		Repository:    "fs",
		Snapshot:      "x",
		TargetIndices: []string{"logs-1", "logs-2"},
		PollInterval:  1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("WaitForRestore: %v", err)
	}
	if rec.calls != 2 {
		t.Errorf("polls = %d, want 2", rec.calls)
	}
}

func TestWaitForRestore_doesNotIssueClusterCancelOnContextCancel(t *testing.T) {
	// Restore can't be cleanly cancelled at the cluster — the poller
	// must NOT issue any cluster mutation on context cancellation.
	rec := &fakeRecoveryInspector{script: []types.RecoveryReport{
		{Indices: []types.IndexRecovery{indexAt("logs-1", "INDEX")}},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := snapshot.WaitForRestore(ctx, rec, snapshot.RestoreWaitOptions{
		Repository:    "fs",
		Snapshot:      "x",
		TargetIndices: []string{"logs-1"},
		PollInterval:  5 * time.Millisecond,
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	// The contract: there's no DELETE call, no _restore _cancel call,
	// no anything. The fake recovery inspector has no mutating
	// methods, so absence of mutation is implicit; this test pins
	// the contract by surviving with just RecoveryInspector.
}

func TestWaitForRestore_rejectsEmptyTargets(t *testing.T) {
	rec := &fakeRecoveryInspector{}
	_, err := snapshot.WaitForRestore(context.Background(), rec, snapshot.RestoreWaitOptions{
		Repository: "fs", Snapshot: "x",
		// TargetIndices: nil — should be rejected up-front.
		PollInterval: 1 * time.Millisecond,
	})
	if err == nil {
		t.Fatalf("expected error for missing target_indices")
	}
}

func TestWaitForRestore_authErrorIsFatal(t *testing.T) {
	rec := &fakeRecoveryInspector{err: errAuthForTest}
	_, err := snapshot.WaitForRestore(context.Background(), rec, snapshot.RestoreWaitOptions{
		Repository: "fs", Snapshot: "x",
		TargetIndices: []string{"logs-1"},
		PollInterval:  1 * time.Millisecond,
	})
	if err == nil {
		t.Fatalf("auth error should be fatal")
	}
}

// errAuthForTest satisfies errors.Is(err, client.ErrAuth) so the
// poller's auth-is-fatal branch trips.
var errAuthForTest = errors.Join(esopsclient.ErrAuth, errors.New("test 401"))
