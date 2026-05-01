package snapshot_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dj-wasabi/esops-go/internal/snapshot"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// fakeStatusInspector implements just enough of SnapshotInspector for
// the wait tests: a script of statuses to return on each call. Other
// SnapshotInspector methods would never be invoked by WaitForCompletion
// but they're required by the interface.
type fakeStatusInspector struct {
	script []types.SnapshotStatus
	calls  int
}

func (f *fakeStatusInspector) Repositories(context.Context, types.SnapshotRepositoryFilter) ([]types.SnapshotRepository, error) {
	return nil, nil
}
func (f *fakeStatusInspector) Snapshots(context.Context, types.SnapshotFilter) ([]types.Snapshot, error) {
	return nil, nil
}
func (f *fakeStatusInspector) SnapshotsStream(context.Context, types.SnapshotFilter, func(types.Snapshot) error, func(string, error)) error {
	return nil
}
func (f *fakeStatusInspector) SnapshotStatus(_ context.Context, req types.SnapshotStatusRequest) (types.SnapshotStatus, error) {
	if f.calls >= len(f.script) {
		f.calls++
		// Stuck — return last entry.
		last := f.script[len(f.script)-1]
		last.Repository = req.Repository
		last.Snapshot = req.Snapshot
		return last, nil
	}
	out := f.script[f.calls]
	out.Repository = req.Repository
	out.Snapshot = req.Snapshot
	f.calls++
	return out, nil
}

// fakePruner is shared with prune_test.go in the same package.

func TestWaitForCompletion_stopsAtSuccess(t *testing.T) {
	ins := &fakeStatusInspector{script: []types.SnapshotStatus{
		{State: "STARTED", ShardsTotal: 5, ShardsStarted: 1},
		{State: "STARTED", ShardsTotal: 5, ShardsStarted: 4},
		{State: "SUCCESS", ShardsTotal: 5, ShardsDone: 5},
	}}
	pr := &fakePruner{}
	got, err := snapshot.WaitForCompletion(context.Background(), ins, pr, snapshot.WaitOptions{
		Repository: "s3", Snapshot: "x",
		PollInterval: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("WaitForCompletion: %v", err)
	}
	if got.State != "SUCCESS" {
		t.Errorf("final state = %q, want SUCCESS", got.State)
	}
	if ins.calls != 3 {
		t.Errorf("polls = %d, want 3", ins.calls)
	}
	if len(pr.calls) != 0 {
		t.Errorf("pruner called on success: %+v", pr.calls)
	}
}

func TestWaitForCompletion_stopsAtFailed(t *testing.T) {
	ins := &fakeStatusInspector{script: []types.SnapshotStatus{
		{State: "FAILED", ShardsFailed: 5},
	}}
	got, err := snapshot.WaitForCompletion(context.Background(), ins, nil, snapshot.WaitOptions{
		Repository: "s3", Snapshot: "x",
		PollInterval: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("WaitForCompletion: %v", err)
	}
	if got.State != "FAILED" {
		t.Errorf("final state = %q, want FAILED", got.State)
	}
}

func TestWaitForCompletion_cancelTriggersDeleteWhenOptedIn(t *testing.T) {
	// Script that never completes — cancellation is the only exit.
	ins := &fakeStatusInspector{script: []types.SnapshotStatus{
		{State: "STARTED"},
	}}
	pr := &fakePruner{}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := snapshot.WaitForCompletion(ctx, ins, pr, snapshot.WaitOptions{
		Repository:        "s3",
		Snapshot:          "x",
		PollInterval:      5 * time.Millisecond,
		CancelOnInterrupt: true,
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(pr.calls) != 1 {
		t.Fatalf("expected one DELETE call, got %d", len(pr.calls))
	}
	if pr.calls[0].Repository != "s3" || len(pr.calls[0].Snapshots) != 1 || pr.calls[0].Snapshots[0] != "x" {
		t.Errorf("DELETE call had wrong args: %+v", pr.calls[0])
	}
}

func TestWaitForCompletion_cancelWithoutOptInDoesNotDelete(t *testing.T) {
	ins := &fakeStatusInspector{script: []types.SnapshotStatus{
		{State: "STARTED"},
	}}
	pr := &fakePruner{}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := snapshot.WaitForCompletion(ctx, ins, pr, snapshot.WaitOptions{
		Repository:   "s3",
		Snapshot:     "x",
		PollInterval: 5 * time.Millisecond,
		// CancelOnInterrupt: false — operator opted out
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(pr.calls) != 0 {
		t.Errorf("pruner called without opt-in: %+v", pr.calls)
	}
}

func TestWaitForCompletion_missingMidWaitIsAnError(t *testing.T) {
	ins := &fakeStatusInspector{script: []types.SnapshotStatus{
		{MissingFromCluster: true},
	}}
	_, err := snapshot.WaitForCompletion(context.Background(), ins, nil, snapshot.WaitOptions{
		Repository: "s3", Snapshot: "x",
		PollInterval: 1 * time.Millisecond,
	})
	if err == nil {
		t.Fatalf("missing-mid-wait should error")
	}
}
