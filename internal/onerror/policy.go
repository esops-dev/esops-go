// Package onerror implements the --on-error {abort,skip,rollback}
// policy shared by multi-item destructive commands (index optimize /
// close / open / settings set, snapshot prune, etc).
//
// The data types operators see in JSON output live in pkg/types
// (OnErrorPolicy, OnErrorSummary, OnErrorItemResult, OnErrorItemStatus)
// because they're part of the public surface. This package only
// supplies the Parse / Run / RefuseRollback / SummaryError helpers.
//
// Each command resolves its items, then dispatches one work step per
// item through Run. Run aggregates the results into a Summary the
// caller renders alongside the command's existing output. Three rules
// are common to every consumer:
//
//   - "abort" stops on the first failure and returns the error. Default
//     behaviour, identical to what the pre-flag code did.
//   - "skip" warns on each failure, records the per-item result, and
//     continues. The summary's worst status sets the exit code at the
//     CLI layer.
//   - "rollback" performs a best-effort undo of items already applied
//     in this invocation when a failure occurs. Only valid for
//     reversible operations; the caller refuses rollback up-front for
//     irreversible ones with a usage error.
package onerror

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/esops-dev/esops-go/internal/audit"
	"github.com/esops-dev/esops-go/internal/logging"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// Parse normalises an operator-supplied --on-error value. Empty input
// returns OnErrorAbort so the default is uniform across commands.
func Parse(raw string) (types.OnErrorPolicy, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "", string(types.OnErrorAbort):
		return types.OnErrorAbort, nil
	case string(types.OnErrorSkip):
		return types.OnErrorSkip, nil
	case string(types.OnErrorRollback):
		return types.OnErrorRollback, nil
	default:
		return "", client.UsageError("--on-error must be one of abort, skip, rollback (got %q)", raw)
	}
}

// RefuseRollback returns a usage error for irreversible operations
// when the operator selected --on-error rollback. Centralised so the
// error wording matches across commands.
func RefuseRollback(op string) error {
	return client.UsageError("--on-error rollback is not supported for %s (operation is irreversible); use abort or skip", op)
}

// Step is the work for one item: a non-nil error marks the item failed.
// Implementations are called serially.
type Step[T any] func(ctx context.Context, item T) error

// Rollback undoes a previously-applied item during OnErrorRollback.
// Return a non-nil error to mark the undo failed — the summary records
// OnErrorStatusRollbackFailed and the worst-case exit code reflects it.
type Rollback[T any] func(ctx context.Context, item T) error

// Run iterates items, applying step under the configured policy. It is
// the single place per-item dispatch happens for multi-item destructive
// commands. Returns the per-item summary plus a non-nil error when the
// caller should propagate it (abort, or rollback's triggering failure).
//
// Aggregate semantics:
//
//   - abort: stop at the first failure; return the failing error and a
//     Summary with the items processed so far.
//   - skip: every item is attempted; per-failure warning + recorded
//     result; returns a nil error so the caller can render output. The
//     caller maps the Summary's worst status to the exit code via
//     SummaryError.
//   - rollback: on the first failure, every previously-succeeded item is
//     undone (in reverse order). Subsequent items are not attempted.
//     The triggering error is returned; rollback failures are recorded
//     individually in the Summary.
//
// name extracts the operator-facing identifier for each item — used in
// log lines and in the summary.
func Run[T any](ctx context.Context, items []T, name func(T) string, step Step[T], rollback Rollback[T], policy types.OnErrorPolicy) (types.OnErrorSummary, error) {
	if policy == types.OnErrorRollback && rollback == nil {
		// Defensive — callers should refuse this up-front with
		// RefuseRollback. Returning an error here (rather than panicking)
		// surfaces the misconfiguration through normal command exit paths.
		return types.OnErrorSummary{Policy: policy}, client.UsageError("rollback policy selected but no rollback function provided (this is a bug in esops; please file an issue)")
	}

	sum := types.OnErrorSummary{Policy: policy, Items: make([]types.OnErrorItemResult, 0, len(items))}
	// Push the final per-item tallies into the audit record on every
	// exit path — abort early-return, ctx.Done, rollback trigger, and
	// the normal completion at the bottom. audit.Summary is a no-op
	// when auditing is disabled, so the unconditional call is cheap.
	defer func() { audit.Summary(auditSummary(sum)) }()

	for i, item := range items {
		select {
		case <-ctx.Done():
			return sum, ctx.Err()
		default:
		}

		nm := name(item)
		err := step(ctx, item)
		if err == nil {
			sum.Items = append(sum.Items, types.OnErrorItemResult{Name: nm, Status: types.OnErrorStatusSucceeded})
			sum.Succeeded++
			continue
		}

		switch policy {
		case types.OnErrorAbort:
			sum.Items = append(sum.Items, types.OnErrorItemResult{Name: nm, Status: types.OnErrorStatusFailed, Error: err.Error()})
			sum.Failed++
			return sum, err

		case types.OnErrorSkip:
			sum.Items = append(sum.Items, types.OnErrorItemResult{Name: nm, Status: types.OnErrorStatusFailed, Error: err.Error()})
			sum.Failed++
			logging.Logger().Warn("on-error.skip", "item", nm, "error", err.Error())

		case types.OnErrorRollback:
			sum.Items = append(sum.Items, types.OnErrorItemResult{Name: nm, Status: types.OnErrorStatusFailed, Error: err.Error()})
			sum.Failed++
			// Walk back over the items already marked succeeded and
			// invoke rollback for each, in reverse order — the
			// last-applied is the first undone, which matches operator
			// intuition for non-commutative future step sequences.
			undoItems(ctx, items[:i], &sum, rollback)
			return sum, err
		}
	}

	return sum, nil
}

// auditSummary projects an OnErrorSummary down to the count-only shape
// the spec asks for in audit records — policy plus
// succeeded/failed/rolled-back/rollback-failed. Per-item names and
// error strings stay in the regular log; the audit record keeps the
// counts that compliance reviewers actually scan for.
func auditSummary(s types.OnErrorSummary) map[string]any {
	return map[string]any{
		"policy":          string(s.Policy),
		"succeeded":       s.Succeeded,
		"failed":          s.Failed,
		"rolled_back":     s.RolledBack,
		"rollback_failed": s.RollbackFailed,
	}
}

// undoItems walks the items the caller already marked succeeded and
// invokes rollback on each, in reverse. Rolled-back items have their
// recorded status flipped from succeeded → rolled-back so the summary
// accurately reflects the post-rollback state of the cluster. A
// rollback that itself fails is recorded as rollback-failed.
func undoItems[T any](ctx context.Context, items []T, sum *types.OnErrorSummary, rollback Rollback[T]) {
	if rollback == nil {
		return
	}
	for i := len(items) - 1; i >= 0; i-- {
		if sum.Items[i].Status != types.OnErrorStatusSucceeded {
			continue
		}
		err := rollback(ctx, items[i])
		if err == nil {
			sum.Items[i].Status = types.OnErrorStatusRolledBack
			sum.Succeeded--
			sum.RolledBack++
			continue
		}
		sum.Items[i].Status = types.OnErrorStatusRollbackFailed
		sum.Items[i].Error = err.Error()
		sum.Succeeded--
		sum.RollbackFailed++
		logging.Logger().Warn("on-error.rollback-failed",
			"item", sum.Items[i].Name,
			"error", err.Error(),
		)
	}
}

// ExitError is returned by the caller when the policy completed but
// the summary contains failures the operator should see reflected in
// the exit code. The CLI exit-code mapper recognises it as the
// "command output already rendered; exit non-zero" signal — distinct
// from a runtime error that should not have rendered output.
//
// Callers use SummaryError when the underlying business call succeeded
// (i.e. Run returned nil) but per-item failures occurred.
type ExitError struct {
	Summary types.OnErrorSummary
}

// Error implements error.
func (e *ExitError) Error() string {
	return fmt.Sprintf("on-error policy %s: %d failed, %d rolled-back, %d rollback-failed",
		e.Summary.Policy, e.Summary.Failed, e.Summary.RolledBack, e.Summary.RollbackFailed)
}

// SummaryError builds an ExitError if the summary records any
// non-success items, or returns nil. Use it after a successful business
// call to translate per-item failures into a non-zero exit.
func SummaryError(s types.OnErrorSummary) error {
	if s.Failed == 0 && s.RolledBack == 0 && s.RollbackFailed == 0 {
		return nil
	}
	return &ExitError{Summary: s}
}

// IsExitError reports whether err is an ExitError, used by the CLI
// exit-code mapper.
func IsExitError(err error) bool {
	var e *ExitError
	return errors.As(err, &e)
}
