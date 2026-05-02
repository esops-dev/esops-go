package ops_test

import (
	"context"
	"errors"
	"testing"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeRerouteExecutor records the request it was called with and
// returns a canned response. Same shape as the other capability fakes
// in this package.
type fakeRerouteExecutor struct {
	got    types.RerouteRequest
	result types.RerouteResult
	err    error
	called bool
}

func (f *fakeRerouteExecutor) Reroute(_ context.Context, req types.RerouteRequest) (types.RerouteResult, error) {
	f.called = true
	f.got = req
	return f.result, f.err
}

func moveCommand() types.RerouteCommand {
	return types.RerouteCommand{
		Action:   types.RerouteMove,
		Index:    "logs",
		Shard:    0,
		FromNode: "es01",
		ToNode:   "es02",
	}
}

func TestRebalance_passesRequestThrough(t *testing.T) {
	fake := &fakeRerouteExecutor{result: types.RerouteResult{Acknowledged: true}}

	req := types.RerouteRequest{
		Commands: []types.RerouteCommand{moveCommand()},
		DryRun:   true,
	}
	got, err := ops.Rebalance(context.Background(), fake, req)
	if err != nil {
		t.Fatalf("Rebalance: %v", err)
	}
	if !fake.called {
		t.Fatalf("adapter was not called")
	}
	if !fake.got.DryRun || len(fake.got.Commands) != 1 || fake.got.Commands[0].Action != types.RerouteMove {
		t.Errorf("adapter got %+v, want DryRun=true and one move command", fake.got)
	}
	if !got.Acknowledged {
		t.Errorf("got %+v, want Acknowledged=true", got)
	}
}

func TestRebalance_unacknowledgedReturnsBlocker(t *testing.T) {
	// acknowledged=false on a dry-run is the cluster's "I refuse" signal.
	// The business layer surfaces ErrDryRunBlocker so main.go maps it to
	// exit code 7, while still returning the result so the caller can
	// render the explanations[] block before exiting non-zero.
	fake := &fakeRerouteExecutor{result: types.RerouteResult{
		Acknowledged: false,
		Explanations: []types.RerouteExplanation{{Command: "move"}},
	}}
	result, err := ops.Rebalance(context.Background(), fake, types.RerouteRequest{
		Commands: []types.RerouteCommand{moveCommand()},
		DryRun:   true,
	})
	if !errors.Is(err, client.ErrDryRunBlocker) {
		t.Errorf("err = %v, want ErrDryRunBlocker", err)
	}
	if result.Acknowledged {
		t.Errorf("result.Acknowledged = true; expected the unack flag to round-trip for rendering")
	}
	if len(result.Explanations) != 1 {
		t.Errorf("result.Explanations not preserved (got %d, want 1)", len(result.Explanations))
	}
}

func TestRebalance_acknowledgedSucceeds(t *testing.T) {
	// Symmetric to the blocker test: ack=true means success — no error.
	fake := &fakeRerouteExecutor{result: types.RerouteResult{Acknowledged: true}}
	_, err := ops.Rebalance(context.Background(), fake, types.RerouteRequest{
		Commands: []types.RerouteCommand{moveCommand()},
	})
	if err != nil {
		t.Errorf("err = %v, want nil on acknowledged=true", err)
	}
}

func TestRebalance_propagatesError(t *testing.T) {
	sentinel := errors.New("boom")
	fake := &fakeRerouteExecutor{err: sentinel}
	_, err := ops.Rebalance(context.Background(), fake, types.RerouteRequest{
		Commands: []types.RerouteCommand{moveCommand()},
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestRebalance_rejectsEmptyRequest(t *testing.T) {
	fake := &fakeRerouteExecutor{}
	_, err := ops.Rebalance(context.Background(), fake, types.RerouteRequest{})
	if err == nil {
		t.Fatalf("expected error on empty request")
	}
	if fake.called {
		t.Errorf("adapter should not be called on validation failure")
	}
}

func TestRebalance_retryFailedAcceptsEmptyCommands(t *testing.T) {
	fake := &fakeRerouteExecutor{result: types.RerouteResult{Acknowledged: true}}
	_, err := ops.Rebalance(context.Background(), fake, types.RerouteRequest{
		RetryFailed: true,
	})
	if err != nil {
		t.Fatalf("Rebalance: %v", err)
	}
	if !fake.got.RetryFailed || len(fake.got.Commands) != 0 {
		t.Errorf("adapter got %+v, want RetryFailed=true and no commands", fake.got)
	}
}

func TestRebalance_retryFailedRejectsCommands(t *testing.T) {
	fake := &fakeRerouteExecutor{}
	_, err := ops.Rebalance(context.Background(), fake, types.RerouteRequest{
		RetryFailed: true,
		Commands:    []types.RerouteCommand{moveCommand()},
	})
	if err == nil {
		t.Fatalf("expected error: --retry-failed cannot be combined with another action")
	}
	if fake.called {
		t.Errorf("adapter should not be called on validation failure")
	}
}

func TestRebalance_moveValidation(t *testing.T) {
	cases := []struct {
		name string
		cmd  types.RerouteCommand
	}{
		{"missing index", types.RerouteCommand{Action: types.RerouteMove, FromNode: "a", ToNode: "b"}},
		{"missing from-node", types.RerouteCommand{Action: types.RerouteMove, Index: "logs", ToNode: "b"}},
		{"missing to-node", types.RerouteCommand{Action: types.RerouteMove, Index: "logs", FromNode: "a"}},
		{"same nodes", types.RerouteCommand{Action: types.RerouteMove, Index: "logs", FromNode: "a", ToNode: "a"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeRerouteExecutor{}
			_, err := ops.Rebalance(context.Background(), fake, types.RerouteRequest{
				Commands: []types.RerouteCommand{tc.cmd},
			})
			if err == nil {
				t.Errorf("expected error for %s", tc.name)
			}
			if fake.called {
				t.Errorf("adapter should not be called when validation fails")
			}
		})
	}
}

func TestRebalance_cancelValidation(t *testing.T) {
	fake := &fakeRerouteExecutor{}
	_, err := ops.Rebalance(context.Background(), fake, types.RerouteRequest{
		Commands: []types.RerouteCommand{{
			Action: types.RerouteCancel,
			Index:  "logs",
		}},
	})
	if err == nil {
		t.Fatalf("cancel without --from-node should fail validation")
	}
}

func TestRebalance_allocateReplicaValidation(t *testing.T) {
	fake := &fakeRerouteExecutor{}
	_, err := ops.Rebalance(context.Background(), fake, types.RerouteRequest{
		Commands: []types.RerouteCommand{{
			Action: types.RerouteAllocateReplica,
			Index:  "logs",
			Shard:  0,
		}},
	})
	if err == nil {
		t.Fatalf("allocate-replica without --to-node should fail validation")
	}
}

func TestRebalance_allocateEmptyPrimaryRequiresAcceptDataLoss(t *testing.T) {
	fake := &fakeRerouteExecutor{}
	_, err := ops.Rebalance(context.Background(), fake, types.RerouteRequest{
		Commands: []types.RerouteCommand{{
			Action: types.RerouteAllocateEmptyPrimary,
			Index:  "logs",
			Shard:  0,
			ToNode: "es01",
		}},
	})
	if err == nil {
		t.Fatalf("allocate-empty-primary without --accept-data-loss should fail validation")
	}
}

func TestRebalance_allocateStalePrimaryRequiresAcceptDataLoss(t *testing.T) {
	fake := &fakeRerouteExecutor{}
	_, err := ops.Rebalance(context.Background(), fake, types.RerouteRequest{
		Commands: []types.RerouteCommand{{
			Action: types.RerouteAllocateStalePrimary,
			Index:  "logs",
			Shard:  0,
			ToNode: "es01",
		}},
	})
	if err == nil {
		t.Fatalf("allocate-stale-primary without --accept-data-loss should fail validation")
	}
}

func TestRebalance_retryFailedAsCommandRejected(t *testing.T) {
	// retry_failed is a request-level flag, not a command. A caller
	// putting it in Commands should fail loudly rather than silently.
	fake := &fakeRerouteExecutor{}
	_, err := ops.Rebalance(context.Background(), fake, types.RerouteRequest{
		Commands: []types.RerouteCommand{{Action: types.RerouteRetryFailed}},
	})
	if err == nil {
		t.Fatalf("retry_failed in Commands should fail validation")
	}
}

func TestRebalance_unknownActionRejected(t *testing.T) {
	fake := &fakeRerouteExecutor{}
	_, err := ops.Rebalance(context.Background(), fake, types.RerouteRequest{
		Commands: []types.RerouteCommand{{Action: types.RerouteAction("warp_drive")}},
	})
	if err == nil {
		t.Fatalf("unknown action should fail validation")
	}
}

func TestParseRerouteAction(t *testing.T) {
	cases := []struct {
		in   string
		want types.RerouteAction
	}{
		{"move", types.RerouteMove},
		{"cancel", types.RerouteCancel},
		{"allocate-replica", types.RerouteAllocateReplica},
		{"allocate_replica", types.RerouteAllocateReplica},
		{"allocate-empty-primary", types.RerouteAllocateEmptyPrimary},
		{"allocate_empty_primary", types.RerouteAllocateEmptyPrimary},
		{"allocate-stale-primary", types.RerouteAllocateStalePrimary},
		{"allocate_stale_primary", types.RerouteAllocateStalePrimary},
		{"retry-failed", types.RerouteRetryFailed},
		{"retry_failed", types.RerouteRetryFailed},
		{"  MOVE  ", types.RerouteMove},
	}
	for _, tc := range cases {
		got, err := ops.ParseRerouteAction(tc.in)
		if err != nil {
			t.Errorf("ParseRerouteAction(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseRerouteAction(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseRerouteAction_rejectsEmpty(t *testing.T) {
	_, err := ops.ParseRerouteAction("")
	if err == nil {
		t.Fatalf("empty action should error")
	}
}

func TestParseRerouteAction_rejectsUnknown(t *testing.T) {
	_, err := ops.ParseRerouteAction("teleport")
	if err == nil {
		t.Fatalf("unknown action should error")
	}
}
