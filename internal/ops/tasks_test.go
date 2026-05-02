package ops_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeTasksInspector serves a canned slice and records the last filter
// it was called with, so tests can assert that ListTasks normalises the
// filter (comma-split, dedupe, trim) before forwarding to the cluster.
// Both Tasks and TasksStream share the same canned data so
// ListTasksStream tests can drive it identically.
type fakeTasksInspector struct {
	tasks     []types.Task
	err       error
	called    bool
	gotFilter types.TasksFilter
	callCount int
}

func (f *fakeTasksInspector) Tasks(_ context.Context, filter types.TasksFilter) ([]types.Task, error) {
	f.called = true
	f.callCount++
	f.gotFilter = filter
	if f.err != nil {
		return nil, f.err
	}
	return f.tasks, nil
}

func (f *fakeTasksInspector) TasksStream(_ context.Context, filter types.TasksFilter, yield func(types.Task) error) error {
	f.called = true
	f.callCount++
	f.gotFilter = filter
	if f.err != nil {
		return f.err
	}
	for _, t := range f.tasks {
		if err := yield(t); err != nil {
			return err
		}
	}
	return nil
}

// fakeTasksCanceller records every cancel call (multi-ID requests fan
// out client-side, so tests need to see each per-ID request) and
// optionally returns canned responses keyed by TaskID.
type fakeTasksCanceller struct {
	gotReqs    []types.TasksCancelRequest
	resp       types.TasksCancelResult            // returned when respByID has no entry
	respByID   map[string]types.TasksCancelResult // optional: per-ID canned response
	err        error
	errOnIndex int    // 0-based; 0 means "always", >0 means "fail on Nth call"
	errOnIDSet string // if set, return err only when TaskIDs[0]==this
	called     bool
}

func (f *fakeTasksCanceller) CancelTasks(_ context.Context, req types.TasksCancelRequest) (types.TasksCancelResult, error) {
	f.called = true
	f.gotReqs = append(f.gotReqs, req)
	if f.err != nil {
		switch {
		case f.errOnIDSet != "" && len(req.TaskIDs) == 1 && req.TaskIDs[0] == f.errOnIDSet:
			return types.TasksCancelResult{}, f.err
		case f.errOnIDSet == "" && f.errOnIndex == 0:
			return types.TasksCancelResult{}, f.err
		case f.errOnIDSet == "" && f.errOnIndex > 0 && len(f.gotReqs) == f.errOnIndex:
			return types.TasksCancelResult{}, f.err
		}
	}
	if f.respByID != nil && len(req.TaskIDs) == 1 {
		if r, ok := f.respByID[req.TaskIDs[0]]; ok {
			return r, nil
		}
	}
	return f.resp, nil
}

// gotReq returns the first recorded request, for tests that only fire
// one capability call (the single-ID and filter-mode paths).
func (f *fakeTasksCanceller) gotReq() types.TasksCancelRequest {
	if len(f.gotReqs) == 0 {
		return types.TasksCancelRequest{}
	}
	return f.gotReqs[0]
}

func sampleTask(id, action string, cancellable bool) types.Task {
	return types.Task{
		ID:               id,
		Node:             "node1",
		Type:             "transport",
		Action:           action,
		RunningTimeNanos: 1_500_000_000,
		Cancellable:      cancellable,
	}
}

func TestListTasks_passesFilterThrough(t *testing.T) {
	insp := &fakeTasksInspector{tasks: []types.Task{
		sampleTask("nodeA:1", "indices:data/write/reindex", true),
	}}
	got, err := ops.ListTasks(context.Background(), insp, types.TasksFilter{
		Actions:      []string{"indices:data/write/reindex"},
		Nodes:        []string{"nodeA"},
		ParentTaskID: "nodeX:99",
		Detailed:     true,
	})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(got) != 1 || got[0].ID != "nodeA:1" {
		t.Errorf("got %+v, want one task", got)
	}
	if !reflect.DeepEqual(insp.gotFilter.Actions, []string{"indices:data/write/reindex"}) {
		t.Errorf("Actions = %v, want passthrough", insp.gotFilter.Actions)
	}
	if insp.gotFilter.ParentTaskID != "nodeX:99" || !insp.gotFilter.Detailed {
		t.Errorf("filter not forwarded: %+v", insp.gotFilter)
	}
}

func TestListTasks_normalisesCommaSeparated(t *testing.T) {
	// urfave/cli accepts both `--actions a,b` and `--actions a --actions b`.
	// Mirror the drain test idiom: comma-split + dedupe.
	insp := &fakeTasksInspector{}
	_, err := ops.ListTasks(context.Background(), insp, types.TasksFilter{
		Actions: []string{"a,b", "b", "  c  "},
		Nodes:   []string{" n1 ,n2", "n2"},
	})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if !reflect.DeepEqual(insp.gotFilter.Actions, []string{"a", "b", "c"}) {
		t.Errorf("Actions = %v, want [a b c]", insp.gotFilter.Actions)
	}
	if !reflect.DeepEqual(insp.gotFilter.Nodes, []string{"n1", "n2"}) {
		t.Errorf("Nodes = %v, want [n1 n2]", insp.gotFilter.Nodes)
	}
}

func TestListTasks_rejectsWhitespaceInsideToken(t *testing.T) {
	insp := &fakeTasksInspector{}
	_, err := ops.ListTasks(context.Background(), insp, types.TasksFilter{
		Actions: []string{"bad action"},
	})
	if err == nil {
		t.Fatalf("expected validation error for embedded whitespace")
	}
	if insp.called {
		t.Errorf("inspector should not be called when validation fails")
	}
}

func TestListTasks_propagatesError(t *testing.T) {
	sentinel := errors.New("boom")
	insp := &fakeTasksInspector{err: sentinel}
	_, err := ops.ListTasks(context.Background(), insp, types.TasksFilter{})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestCancelTasks_byIDForwardsToCanceller(t *testing.T) {
	canceller := &fakeTasksCanceller{
		resp: types.TasksCancelResult{
			Cancelled: []types.Task{sampleTask("n1:42", "indices:data/write/reindex", true)},
		},
	}
	deps := ops.CancelTasksDeps{
		Inspector: &fakeTasksInspector{},
		Canceller: canceller,
	}
	got, err := ops.CancelTasks(context.Background(), deps, types.TasksCancelRequest{
		TaskIDs: []string{"n1:42"},
	}, false)
	if err != nil {
		t.Fatalf("CancelTasks: %v", err)
	}
	if !canceller.called || !reflect.DeepEqual(canceller.gotReq().TaskIDs, []string{"n1:42"}) {
		t.Errorf("canceller not called with task id: %+v", canceller.gotReq())
	}
	if got.Action != "cancel" || got.DryRun || !reflect.DeepEqual(got.TaskIDs, []string{"n1:42"}) {
		t.Errorf("result header wrong: %+v", got)
	}
	if len(got.Cancelled) != 1 {
		t.Errorf("Cancelled = %d, want 1", len(got.Cancelled))
	}
}

func TestCancelTasks_byFilterForwardsToCanceller(t *testing.T) {
	canceller := &fakeTasksCanceller{}
	deps := ops.CancelTasksDeps{
		Inspector: &fakeTasksInspector{},
		Canceller: canceller,
	}
	_, err := ops.CancelTasks(context.Background(), deps, types.TasksCancelRequest{
		Filter: types.TasksFilter{Actions: []string{"indices:data/write/reindex,*search*"}},
	}, false)
	if err != nil {
		t.Fatalf("CancelTasks: %v", err)
	}
	if !reflect.DeepEqual(canceller.gotReq().Filter.Actions, []string{"indices:data/write/reindex", "*search*"}) {
		t.Errorf("forwarded actions = %v, want comma-split slice", canceller.gotReq().Filter.Actions)
	}
}

func TestCancelTasks_rejectsBothIDAndFilter(t *testing.T) {
	deps := ops.CancelTasksDeps{
		Inspector: &fakeTasksInspector{},
		Canceller: &fakeTasksCanceller{},
	}
	_, err := ops.CancelTasks(context.Background(), deps, types.TasksCancelRequest{
		TaskIDs: []string{"n1:42"},
		Filter:  types.TasksFilter{Actions: []string{"foo"}},
	}, false)
	if err == nil {
		t.Fatalf("expected mutual-exclusion error")
	}
}

func TestCancelTasks_rejectsEmptyRequest(t *testing.T) {
	deps := ops.CancelTasksDeps{
		Inspector: &fakeTasksInspector{},
		Canceller: &fakeTasksCanceller{},
	}
	_, err := ops.CancelTasks(context.Background(), deps, types.TasksCancelRequest{}, false)
	if err == nil {
		t.Fatalf("expected refusal for empty request (would cancel everything)")
	}
}

func TestCancelTasks_rejectsMalformedTaskID(t *testing.T) {
	deps := ops.CancelTasksDeps{
		Inspector: &fakeTasksInspector{},
		Canceller: &fakeTasksCanceller{},
	}
	_, err := ops.CancelTasks(context.Background(), deps, types.TasksCancelRequest{
		TaskIDs: []string{"missing-colon"},
	}, false)
	if err == nil {
		t.Fatalf("expected validation error for malformed task ID")
	}
}

func TestCancelTasks_rejectsMalformedTaskIDInMultiBeforeAnyFanout(t *testing.T) {
	// A typo on the third of five IDs should reject up-front so the
	// first two don't get cancelled before the validation fires.
	canceller := &fakeTasksCanceller{}
	deps := ops.CancelTasksDeps{
		Inspector: &fakeTasksInspector{},
		Canceller: canceller,
	}
	_, err := ops.CancelTasks(context.Background(), deps, types.TasksCancelRequest{
		TaskIDs: []string{"n1:1", "n2:2", "bogus", "n4:4"},
	}, false)
	if err == nil {
		t.Fatalf("expected validation error for malformed task ID")
	}
	if canceller.called {
		t.Errorf("canceller must not be invoked when validation fails on any ID")
	}
}

func TestCancelTasks_dryRunListsThenSkipsCancel(t *testing.T) {
	insp := &fakeTasksInspector{tasks: []types.Task{
		sampleTask("n1:1", "indices:data/write/reindex", true),
		sampleTask("n2:2", "search", true),
	}}
	canceller := &fakeTasksCanceller{}
	deps := ops.CancelTasksDeps{Inspector: insp, Canceller: canceller}

	got, err := ops.CancelTasks(context.Background(), deps, types.TasksCancelRequest{
		Filter: types.TasksFilter{Actions: []string{"*"}},
	}, true)
	if err != nil {
		t.Fatalf("dry-run CancelTasks: %v", err)
	}
	if canceller.called {
		t.Errorf("canceller must not be called in dry-run")
	}
	if !insp.called {
		t.Errorf("inspector must be called in dry-run for the preview")
	}
	if !got.DryRun || got.Action != "cancel" {
		t.Errorf("result header wrong: %+v", got)
	}
	if len(got.Cancelled) != 2 {
		t.Errorf("Cancelled = %d, want 2 (preview)", len(got.Cancelled))
	}
}

func TestCancelTasks_dryRunBlocksOnNonCancellable(t *testing.T) {
	// A wildcard --actions that hits a force_merge task should fail
	// the dry-run with ErrDryRunBlocker so the operator sees the
	// mismatch before re-invoking without --dry-run.
	insp := &fakeTasksInspector{tasks: []types.Task{
		sampleTask("n1:1", "indices:admin/forcemerge", false),
	}}
	deps := ops.CancelTasksDeps{Inspector: insp, Canceller: &fakeTasksCanceller{}}

	got, err := ops.CancelTasks(context.Background(), deps, types.TasksCancelRequest{
		Filter: types.TasksFilter{Actions: []string{"*"}},
	}, true)
	if !errors.Is(err, client.ErrDryRunBlocker) {
		t.Errorf("err = %v, want ErrDryRunBlocker", err)
	}
	if len(got.Cancelled) != 1 {
		t.Errorf("Cancelled = %d, want the preview to still carry the row", len(got.Cancelled))
	}
}

func TestCancelTasks_dryRunByIDFindsMatch(t *testing.T) {
	insp := &fakeTasksInspector{tasks: []types.Task{
		sampleTask("n1:1", "indices:data/write/reindex", true),
		sampleTask("n2:2", "search", true),
	}}
	deps := ops.CancelTasksDeps{Inspector: insp, Canceller: &fakeTasksCanceller{}}
	got, err := ops.CancelTasks(context.Background(), deps, types.TasksCancelRequest{
		TaskIDs: []string{"n2:2"},
	}, true)
	if err != nil {
		t.Fatalf("CancelTasks dry-run: %v", err)
	}
	if len(got.Cancelled) != 1 || got.Cancelled[0].ID != "n2:2" {
		t.Errorf("preview = %+v, want exactly [n2:2]", got.Cancelled)
	}
}

func TestCancelTasks_dryRunByIDMissingErrors(t *testing.T) {
	insp := &fakeTasksInspector{tasks: []types.Task{
		sampleTask("n1:1", "search", true),
	}}
	deps := ops.CancelTasksDeps{Inspector: insp, Canceller: &fakeTasksCanceller{}}
	_, err := ops.CancelTasks(context.Background(), deps, types.TasksCancelRequest{
		TaskIDs: []string{"n9:99"},
	}, true)
	if err == nil {
		t.Fatalf("expected error when task ID is not in the listing")
	}
}

func TestCancelTasks_propagatesCancellerError(t *testing.T) {
	sentinel := errors.New("cluster refused")
	canceller := &fakeTasksCanceller{err: sentinel}
	deps := ops.CancelTasksDeps{Inspector: &fakeTasksInspector{}, Canceller: canceller}
	_, err := ops.CancelTasks(context.Background(), deps, types.TasksCancelRequest{
		TaskIDs: []string{"n1:42"},
	}, false)
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want cluster refused", err)
	}
}

func TestCancelTasks_multiIDFanout(t *testing.T) {
	// Three IDs → three POSTs. The aggregated result echoes every ID
	// the operator passed, and Cancelled / NodeFailures are unions
	// across the per-call results.
	canceller := &fakeTasksCanceller{
		respByID: map[string]types.TasksCancelResult{
			"n1:1": {Cancelled: []types.Task{sampleTask("n1:1", "search", true)}},
			"n2:2": {Cancelled: []types.Task{sampleTask("n2:2", "search", true)}},
			"n3:3": {Cancelled: []types.Task{sampleTask("n3:3", "search", true)}, NodeFailures: []string{"n3: master not available"}},
		},
	}
	deps := ops.CancelTasksDeps{Inspector: &fakeTasksInspector{}, Canceller: canceller}
	got, err := ops.CancelTasks(context.Background(), deps, types.TasksCancelRequest{
		TaskIDs: []string{"n1:1", "n2:2", "n3:3"},
	}, false)
	if err != nil {
		t.Fatalf("CancelTasks: %v", err)
	}
	if len(canceller.gotReqs) != 3 {
		t.Errorf("expected 3 capability calls, got %d", len(canceller.gotReqs))
	}
	for i, want := range []string{"n1:1", "n2:2", "n3:3"} {
		if !reflect.DeepEqual(canceller.gotReqs[i].TaskIDs, []string{want}) {
			t.Errorf("call[%d] TaskIDs = %v, want [%s]", i, canceller.gotReqs[i].TaskIDs, want)
		}
	}
	if !reflect.DeepEqual(got.TaskIDs, []string{"n1:1", "n2:2", "n3:3"}) {
		t.Errorf("aggregated TaskIDs = %v, want [n1:1 n2:2 n3:3]", got.TaskIDs)
	}
	if len(got.Cancelled) != 3 {
		t.Errorf("aggregated Cancelled = %d, want 3", len(got.Cancelled))
	}
	if !reflect.DeepEqual(got.NodeFailures, []string{"n3: master not available"}) {
		t.Errorf("aggregated NodeFailures = %v, want one entry from n3", got.NodeFailures)
	}
}

func TestCancelTasks_multiIDCommaSplit(t *testing.T) {
	// `cancel id1,id2 id3` is the same as `cancel id1 id2 id3`.
	canceller := &fakeTasksCanceller{}
	deps := ops.CancelTasksDeps{Inspector: &fakeTasksInspector{}, Canceller: canceller}
	got, err := ops.CancelTasks(context.Background(), deps, types.TasksCancelRequest{
		TaskIDs: []string{"n1:1,n2:2", "n3:3"},
	}, false)
	if err != nil {
		t.Fatalf("CancelTasks: %v", err)
	}
	if len(canceller.gotReqs) != 3 {
		t.Errorf("expected 3 capability calls (comma-split), got %d", len(canceller.gotReqs))
	}
	if !reflect.DeepEqual(got.TaskIDs, []string{"n1:1", "n2:2", "n3:3"}) {
		t.Errorf("TaskIDs = %v, want [n1:1 n2:2 n3:3]", got.TaskIDs)
	}
}

func TestCancelTasks_multiIDDedupes(t *testing.T) {
	// Repeated IDs should collapse to one POST per unique ID — same
	// guarantee the drain --node path provides.
	canceller := &fakeTasksCanceller{}
	deps := ops.CancelTasksDeps{Inspector: &fakeTasksInspector{}, Canceller: canceller}
	_, err := ops.CancelTasks(context.Background(), deps, types.TasksCancelRequest{
		TaskIDs: []string{"n1:1", "n1:1", "n2:2"},
	}, false)
	if err != nil {
		t.Fatalf("CancelTasks: %v", err)
	}
	if len(canceller.gotReqs) != 2 {
		t.Errorf("expected 2 calls after dedupe, got %d", len(canceller.gotReqs))
	}
}

func TestCancelTasks_multiIDPartialFailure(t *testing.T) {
	// First two cancels succeed, the third fails: the aggregated
	// result carries the partial success and the error wraps the
	// failing ID so the operator sees both.
	sentinel := errors.New("cluster 503")
	canceller := &fakeTasksCanceller{
		respByID: map[string]types.TasksCancelResult{
			"n1:1": {Cancelled: []types.Task{sampleTask("n1:1", "search", true)}},
			"n2:2": {Cancelled: []types.Task{sampleTask("n2:2", "search", true)}},
		},
		err:        sentinel,
		errOnIDSet: "n3:3",
	}
	deps := ops.CancelTasksDeps{Inspector: &fakeTasksInspector{}, Canceller: canceller}
	got, err := ops.CancelTasks(context.Background(), deps, types.TasksCancelRequest{
		TaskIDs: []string{"n1:1", "n2:2", "n3:3"},
	}, false)
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want it to wrap %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), "n3:3") {
		t.Errorf("err = %v, want it to name the failing id n3:3", err)
	}
	if len(got.Cancelled) != 2 {
		t.Errorf("aggregated Cancelled = %d, want 2 (partial success)", len(got.Cancelled))
	}
}

func TestCancelTasks_dryRunByMultipleIDs(t *testing.T) {
	insp := &fakeTasksInspector{tasks: []types.Task{
		sampleTask("n1:1", "search", true),
		sampleTask("n2:2", "indices:data/write/reindex", true),
		sampleTask("n3:3", "indices:admin/forcemerge", false),
	}}
	deps := ops.CancelTasksDeps{Inspector: insp, Canceller: &fakeTasksCanceller{}}
	got, err := ops.CancelTasks(context.Background(), deps, types.TasksCancelRequest{
		TaskIDs: []string{"n1:1", "n2:2"},
	}, true)
	if err != nil {
		t.Fatalf("CancelTasks dry-run: %v", err)
	}
	if len(got.Cancelled) != 2 {
		t.Errorf("preview = %d, want exactly 2", len(got.Cancelled))
	}
}

func TestCancelTasks_dryRunByMultipleIDsBlocksOnNonCancellable(t *testing.T) {
	// One of the IDs targets a force_merge — dry-run blocks for it.
	insp := &fakeTasksInspector{tasks: []types.Task{
		sampleTask("n1:1", "search", true),
		sampleTask("n2:2", "indices:admin/forcemerge", false),
	}}
	deps := ops.CancelTasksDeps{Inspector: insp, Canceller: &fakeTasksCanceller{}}
	_, err := ops.CancelTasks(context.Background(), deps, types.TasksCancelRequest{
		TaskIDs: []string{"n1:1", "n2:2"},
	}, true)
	if !errors.Is(err, client.ErrDryRunBlocker) {
		t.Errorf("err = %v, want ErrDryRunBlocker", err)
	}
}

func TestCancelTasks_dryRunByMultipleIDsReportsAllMissing(t *testing.T) {
	insp := &fakeTasksInspector{tasks: []types.Task{
		sampleTask("n1:1", "search", true),
	}}
	deps := ops.CancelTasksDeps{Inspector: insp, Canceller: &fakeTasksCanceller{}}
	_, err := ops.CancelTasks(context.Background(), deps, types.TasksCancelRequest{
		TaskIDs: []string{"missing1:1", "missing2:2"},
	}, true)
	if err == nil {
		t.Fatalf("expected error naming both missing IDs")
	}
	if !strings.Contains(err.Error(), "missing1:1") || !strings.Contains(err.Error(), "missing2:2") {
		t.Errorf("err = %v, want it to name both missing IDs", err)
	}
}

func TestListTasksStream_yieldsAllRowsAndForwardsFilter(t *testing.T) {
	// Stream-mode tests cover the same forward + normalise contract as
	// the buffered ListTasks: filter normalisation runs first, the
	// inspector receives the cleaned filter, and yield is invoked once
	// per row.
	insp := &fakeTasksInspector{tasks: []types.Task{
		sampleTask("nodeA:1", "search", true),
		sampleTask("nodeB:5", "indices:admin/forcemerge", false),
	}}
	var got []string
	err := ops.ListTasksStream(context.Background(), insp, types.TasksFilter{
		Actions: []string{"a, b"},
	}, func(t types.Task) error {
		got = append(got, t.ID)
		return nil
	})
	if err != nil {
		t.Fatalf("ListTasksStream: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("yielded %d rows, want 2", len(got))
	}
	if !reflect.DeepEqual(insp.gotFilter.Actions, []string{"a", "b"}) {
		t.Errorf("filter not normalised on the stream path: %v", insp.gotFilter.Actions)
	}
}

func TestListTasksStream_yieldErrorStopsIteration(t *testing.T) {
	insp := &fakeTasksInspector{tasks: []types.Task{
		sampleTask("nodeA:1", "search", true),
		sampleTask("nodeB:5", "search", true),
		sampleTask("nodeC:9", "search", true),
	}}
	stop := errors.New("stop")
	var seen int
	err := ops.ListTasksStream(context.Background(), insp, types.TasksFilter{}, func(t types.Task) error {
		seen++
		if seen == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Errorf("err = %v, want stop", err)
	}
	if seen != 2 {
		t.Errorf("seen = %d, want 2 (yield error must short-circuit)", seen)
	}
}

func TestListTasksStream_rejectsWhitespaceInsideToken(t *testing.T) {
	insp := &fakeTasksInspector{}
	err := ops.ListTasksStream(context.Background(), insp, types.TasksFilter{
		Actions: []string{"bad action"},
	}, func(types.Task) error { return nil })
	if err == nil {
		t.Fatalf("expected validation error for embedded whitespace")
	}
	if insp.called {
		t.Errorf("inspector should not be called when filter validation fails")
	}
}
