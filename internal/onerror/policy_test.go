package onerror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/esops-dev/esops-go/internal/audit"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in      string
		want    types.OnErrorPolicy
		wantErr bool
	}{
		{"", types.OnErrorAbort, false},
		{"abort", types.OnErrorAbort, false},
		{"Abort", types.OnErrorAbort, false},
		{"  abort  ", types.OnErrorAbort, false},
		{"skip", types.OnErrorSkip, false},
		{"SKIP", types.OnErrorSkip, false},
		{"rollback", types.OnErrorRollback, false},
		{"continue", "", true},
		{"abort,skip", "", true},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("Parse(%q): want error, got nil", c.in)
				continue
			}
			if !errors.Is(err, client.ErrUsage) {
				t.Errorf("Parse(%q): want ErrUsage, got %v", c.in, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q): unexpected error %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("Parse(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRefuseRollback(t *testing.T) {
	err := RefuseRollback("snapshot prune")
	if err == nil {
		t.Fatal("RefuseRollback returned nil")
	}
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("RefuseRollback should wrap ErrUsage, got %v", err)
	}
}

func TestRunAbortPolicyStopsOnFailure(t *testing.T) {
	items := []string{"a", "b", "c"}
	wantErr := errors.New("boom on b")
	calls := []string{}
	step := func(_ context.Context, s string) error {
		calls = append(calls, s)
		if s == "b" {
			return wantErr
		}
		return nil
	}

	sum, err := Run(context.Background(), items, identity, step, nil, types.OnErrorAbort)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run abort: got err %v, want %v", err, wantErr)
	}
	if len(calls) != 2 {
		t.Errorf("Run abort: expected 2 calls (a, b), got %v", calls)
	}
	if sum.Succeeded != 1 || sum.Failed != 1 {
		t.Errorf("Run abort: counts succeeded=%d failed=%d, want 1/1", sum.Succeeded, sum.Failed)
	}
	if len(sum.Items) != 2 {
		t.Errorf("Run abort: expected 2 items in summary, got %d", len(sum.Items))
	}
}

func TestRunSkipPolicyContinuesPastFailures(t *testing.T) {
	items := []string{"a", "b", "c", "d"}
	calls := 0
	step := func(_ context.Context, s string) error {
		calls++
		if s == "b" || s == "d" {
			return fmt.Errorf("fail %s", s)
		}
		return nil
	}

	sum, err := Run(context.Background(), items, identity, step, nil, types.OnErrorSkip)
	if err != nil {
		t.Fatalf("Run skip: unexpected error %v", err)
	}
	if calls != 4 {
		t.Errorf("Run skip: expected 4 step calls, got %d", calls)
	}
	if sum.Succeeded != 2 || sum.Failed != 2 {
		t.Errorf("Run skip: counts succeeded=%d failed=%d, want 2/2", sum.Succeeded, sum.Failed)
	}
	// SummaryError must report non-zero exit because failures happened.
	if SummaryError(sum) == nil {
		t.Error("SummaryError: expected non-nil for skip with failures, got nil")
	}
}

func TestRunSkipPolicyAllSucceedReturnsNilSummaryError(t *testing.T) {
	items := []string{"a", "b"}
	step := func(_ context.Context, _ string) error { return nil }

	sum, err := Run(context.Background(), items, identity, step, nil, types.OnErrorSkip)
	if err != nil {
		t.Fatalf("Run skip: unexpected error %v", err)
	}
	if sum.Succeeded != 2 || sum.Failed != 0 {
		t.Errorf("Run skip: counts succeeded=%d failed=%d, want 2/0", sum.Succeeded, sum.Failed)
	}
	if SummaryError(sum) != nil {
		t.Error("SummaryError: expected nil for all-success, got non-nil")
	}
}

func TestRunRollbackPolicyUndoesPriorItems(t *testing.T) {
	items := []string{"a", "b", "c", "d"}
	wantErr := errors.New("c failed")
	applied := []string{}
	rolled := []string{}
	step := func(_ context.Context, s string) error {
		if s == "c" {
			return wantErr
		}
		applied = append(applied, s)
		return nil
	}
	rb := func(_ context.Context, s string) error {
		rolled = append(rolled, s)
		return nil
	}

	sum, err := Run(context.Background(), items, identity, step, rb, types.OnErrorRollback)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run rollback: got err %v, want %v", err, wantErr)
	}
	// "a" then "b" were applied; rollback walks reverse-order.
	if got := fmt.Sprintf("%v", rolled); got != "[b a]" {
		t.Errorf("Run rollback: rolled-back order %v, want [b a]", rolled)
	}
	// "d" must not have been attempted.
	if sum.Failed != 1 || sum.RolledBack != 2 || sum.Succeeded != 0 {
		t.Errorf("Run rollback: counts succeeded=%d failed=%d rolled-back=%d, want 0/1/2",
			sum.Succeeded, sum.Failed, sum.RolledBack)
	}
	if len(sum.Items) != 3 {
		t.Errorf("Run rollback: expected 3 items (a, b, c), got %d", len(sum.Items))
	}
}

func TestRunRollbackPolicyRecordsRollbackFailure(t *testing.T) {
	items := []string{"a", "b", "c"}
	step := func(_ context.Context, s string) error {
		if s == "c" {
			return errors.New("c failed")
		}
		return nil
	}
	rbErr := errors.New("undo failed for b")
	rb := func(_ context.Context, s string) error {
		if s == "b" {
			return rbErr
		}
		return nil
	}

	sum, err := Run(context.Background(), items, identity, step, rb, types.OnErrorRollback)
	if err == nil {
		t.Fatal("Run rollback: expected triggering error, got nil")
	}
	if sum.RollbackFailed != 1 || sum.RolledBack != 1 || sum.Failed != 1 {
		t.Errorf("Run rollback: counts failed=%d rolled-back=%d rollback-failed=%d, want 1/1/1",
			sum.Failed, sum.RolledBack, sum.RollbackFailed)
	}
}

func TestRunRollbackPolicyRefusesWithoutRollbackFunc(t *testing.T) {
	_, err := Run(context.Background(), []string{"a"}, identity,
		func(_ context.Context, _ string) error { return nil }, nil, types.OnErrorRollback)
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Errorf("Run rollback without rb: expected ErrUsage, got %v", err)
	}
}

func TestRunRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, []string{"a", "b"}, identity,
		func(_ context.Context, _ string) error { return nil }, nil, types.OnErrorSkip)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run with cancelled ctx: got %v, want context.Canceled", err)
	}
}

func TestExitErrorWrapsSummary(t *testing.T) {
	sum := types.OnErrorSummary{
		Policy: types.OnErrorSkip,
		Failed: 2,
	}
	err := SummaryError(sum)
	if err == nil {
		t.Fatal("SummaryError returned nil for non-zero failures")
	}
	if !IsExitError(err) {
		t.Errorf("IsExitError: expected true for SummaryError, got false")
	}
	var ex *ExitError
	if !errors.As(err, &ex) {
		t.Fatal("errors.As: ExitError target not matched")
	}
	if ex.Summary.Failed != 2 {
		t.Errorf("ExitError.Summary.Failed = %d, want 2", ex.Summary.Failed)
	}
}

func identity(s string) string { return s }

// TestRunWritesAuditSummary verifies that onerror.Run pushes the
// final per-item tallies into the audit record on every exit path —
// here exercised by a mixed succeed/fail run under --on-error skip.
// The spec requires multi-item commands to record
// succeeded/failed/rolled-back counts in the audit summary; this is
// the single-point assertion that the deferred audit.Summary call
// actually fires.
func TestRunWritesAuditSummary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	audit.Reset()
	audit.Init(path)
	audit.Begin("index optimize", []string{"esops", "index", "optimize", "logs-*"}, "local-es", "http://localhost:9200", false)
	defer audit.Reset()

	step := func(_ context.Context, item string) error {
		if item == "fail" {
			return fmt.Errorf("boom")
		}
		return nil
	}
	_, err := Run(context.Background(), []string{"ok-1", "fail", "ok-2"}, identity, step, nil, types.OnErrorSkip)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	audit.Finish(1, "on-error-policy")

	data, err := os.ReadFile(path) // #nosec G304 -- test-local path under TempDir
	if err != nil {
		t.Fatalf("read audit file: %v", err)
	}
	var rec struct {
		Summary map[string]any `json:"summary"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("unmarshal audit record: %v", err)
	}
	if rec.Summary == nil {
		t.Fatal("audit record carried no summary")
	}
	if rec.Summary["policy"] != "skip" {
		t.Errorf("summary.policy = %v, want skip", rec.Summary["policy"])
	}
	if rec.Summary["succeeded"].(float64) != 2 {
		t.Errorf("summary.succeeded = %v, want 2", rec.Summary["succeeded"])
	}
	if rec.Summary["failed"].(float64) != 1 {
		t.Errorf("summary.failed = %v, want 1", rec.Summary["failed"])
	}
}
