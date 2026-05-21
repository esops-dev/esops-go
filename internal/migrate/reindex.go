package migrate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/esops-dev/esops-go/internal/logging"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// DefaultReindexPollInterval is the cluster-friendly default for
// /_tasks/<id> polling during a reindex. The cluster's master accepts
// roughly 1Hz before complaining; reindexes typically run for minutes
// or hours, so 5s strikes the same balance as snapshot waits — fresh
// enough that progress feels live, sparse enough that a 6h reindex
// doesn't issue 4000+ status calls.
const DefaultReindexPollInterval = 5 * time.Second

// MinReindexPollIntervalCLI is the lower bound the CLI layer enforces
// on --poll-interval. The cluster's master rate-limits /_tasks reads
// north of a couple of Hz; 1s leaves plenty of headroom and keeps an
// operator who passes "1ms" by mistake from brown-outing a master.
//
// Lives at the CLI boundary rather than the orchestrator so unit tests
// can drive the loop with sub-second intervals without crossing the
// validation the operator would.
const MinReindexPollIntervalCLI = 1 * time.Second

// MaxReindexPollFailures caps consecutive transient failures during
// /_tasks polling. A flapping master, a temporary 503, or a routing
// blip recovers in seconds; a dead cluster never recovers and the
// orchestrator should not loop until the operator's session ends.
// 50 ticks at 5s each ≈ 4 minutes — long enough to ride out a real
// incident, short enough that "the cluster is gone" surfaces while the
// operator is still at the keyboard.
const MaxReindexPollFailures = 50

// reindexCancelTimeout bounds the best-effort POST /_tasks/<id>/_cancel
// the orchestrator issues when its parent context is cancelled. The
// original ctx is already done at that point, so the cancel runs on a
// fresh background context with this timeout — long enough for one
// HTTP round-trip, short enough that a hung cluster doesn't keep the
// CLI from exiting after the operator hit Ctrl-C.
const reindexCancelTimeout = 10 * time.Second

// Reindex submits a deferred /_reindex per source-resolved index on
// the target cluster, then polls /_tasks/<id> until each completes.
// The reindex itself runs cluster-side; this function is the
// orchestrator.
//
// Multi-index requests are processed sequentially: one task at a time.
// Concurrency knobs belong on the cluster (slices/requests-per-second
// per task) rather than client-side fan-out — a single esops invocation
// running ten reindexes in parallel is exactly the foot-gun the
// max_in_flight context cap exists to prevent.
//
// On context cancellation the in-flight task is cancelled cluster-side
// via /_tasks/<id>/_cancel; the function returns ctx.Err() so SIGINT
// propagates as exit 130. The cancel runs on a fresh background context
// (the parent is already done) bounded by reindexCancelTimeout.
//
// State is persisted to $XDG_STATE_HOME/esops/migrate/ after every
// task ID is captured so a SIGINT or crashed shell can be recovered
// with `esops migrate reindex --resume`.
func Reindex(ctx context.Context, src, tgt Endpoint, req types.MigrationReindexRequest) (types.MigrationReindexResult, error) {
	if err := validateReindexEndpoints(src, tgt); err != nil {
		return types.MigrationReindexResult{}, err
	}
	if err := validateReindexRequest(req); err != nil {
		return types.MigrationReindexResult{}, err
	}
	pollInterval := req.PollInterval
	if pollInterval <= 0 {
		pollInterval = DefaultReindexPollInterval
	}

	if req.Resume {
		return resumeReindex(ctx, src, tgt, req, pollInterval)
	}

	indices, err := src.Indices.Indices(ctx, types.IndexFilter{
		Index:  req.Indices,
		Status: "open",
	})
	if err != nil {
		return types.MigrationReindexResult{}, fmt.Errorf("listing source indices: %w", err)
	}

	result := types.MigrationReindexResult{
		Action: "migrate-reindex",
		Source: src.summary(),
		Target: tgt.summary(),
		DryRun: req.DryRun,
	}

	if len(indices) == 0 {
		result.Status = types.MigrationStatusOK
		return result, nil
	}
	if req.TargetIndexLiteral != "" && len(indices) > 1 {
		return result, client.UsageError("--to-index requires a single source index; --indices=%q resolved to %d", req.Indices, len(indices))
	}

	// Sort by name so the per-source order is deterministic — operators
	// re-running a partial reindex see the same sequence in the output
	// across runs.
	sort.SliceStable(indices, func(i, j int) bool { return indices[i].Name < indices[j].Name })

	state := ReindexState{
		Action:        "migrate-reindex",
		SourceContext: src.Context,
		TargetContext: tgt.Context,
		TargetSuffix:  req.TargetSuffix,
		TargetLiteral: req.TargetIndexLiteral,
		StartedAt:     time.Now().UTC(),
	}

	for _, ix := range indices {
		entry := types.MigrationReindexEntry{
			SourceIndex: ix.Name,
			TargetIndex: resolveTargetIndex(req, ix.Name, len(indices) == 1),
			DryRun:      req.DryRun,
		}

		if req.DryRun {
			entry.Status = types.MigrationStatusOK
			entry.Detail = fmt.Sprintf("would submit /_reindex %s → %s on target", ix.Name, entry.TargetIndex)
			result.Indices = append(result.Indices, entry)
			continue
		}

		submit, err := tgt.Reindex.SubmitReindex(ctx, types.ReindexSubmitRequest{
			SourceIndex:         ix.Name,
			SourceRemoteHost:    src.URL,
			SourceUsername:      src.Auth.Username,
			SourcePassword:      src.Auth.Password,
			SourceHeaders:       src.Auth.remoteHeaders(),
			TargetIndex:         entry.TargetIndex,
			Conflicts:           req.Conflicts,
			SizeLimit:           req.SizeLimit,
			RequestsPerSecond:   req.RequestsPerSecond,
			Slices:              req.Slices,
			WaitForActiveShards: req.WaitForActiveShards,
		})
		if err != nil {
			// Auth failures and forbidden bubble straight up so the
			// CLI maps to the right exit code; everything else is a
			// per-index blocker the report records and continues
			// past — half-done reindexes are real and the operator
			// needs to see which finished.
			if errors.Is(err, client.ErrAuth) || errors.Is(err, client.ErrForbidden) {
				return result, err
			}
			entry.Status = types.MigrationStatusBlock
			entry.Detail = fmt.Sprintf("submit failed: %v", err)
			result.Indices = append(result.Indices, entry)
			continue
		}
		entry.TaskID = submit.TaskID

		// Persist as soon as the task ID is known so a SIGINT between
		// submit and poll is recoverable. The save is best-effort —
		// log a warning but keep going if disk is unavailable.
		state.Entries = appendOrReplaceStateEntry(state.Entries, ReindexStateEntry{
			SourceIndex: ix.Name,
			TargetIndex: entry.TargetIndex,
			TaskID:      submit.TaskID,
		})
		if err := SaveReindexState(state); err != nil {
			logging.Logger().Warn("migrate.reindex.state_save_failed", "err", err)
		}

		final := pollAndFinaliseEntry(ctx, tgt, entry, submit.TaskID, pollInterval, req.Conflicts)
		if errors.Is(final.pollErr, context.Canceled) || errors.Is(final.pollErr, context.DeadlineExceeded) {
			// SIGINT mid-task: best-effort cancel cluster-side, then
			// propagate ctx.Err() so the CLI exits 130. State is
			// already persisted; --resume picks up where we left off.
			result.Indices = append(result.Indices, final.MigrationReindexEntry)
			cancelRemoteTask(tgt, submit.TaskID)
			return result, final.pollErr
		}
		if errors.Is(final.pollErr, client.ErrAuth) || errors.Is(final.pollErr, client.ErrForbidden) {
			result.Indices = append(result.Indices, final.MigrationReindexEntry)
			return result, final.pollErr
		}

		// Mark this entry's state row complete (whether OK or blocker —
		// "the task is done, don't try to resume it" is the contract).
		// Carry the final Status + Detail too so a `--resume` after a
		// partial run reproduces the original outcome instead of
		// upgrading every Completed row to OK.
		state.Entries = appendOrReplaceStateEntry(state.Entries, ReindexStateEntry{
			SourceIndex: ix.Name,
			TargetIndex: final.TargetIndex,
			TaskID:      submit.TaskID,
			Completed:   true,
			Status:      final.Status,
			Detail:      final.Detail,
		})
		if err := SaveReindexState(state); err != nil {
			logging.Logger().Warn("migrate.reindex.state_save_failed", "err", err)
		}

		result.Indices = append(result.Indices, final.MigrationReindexEntry)
	}

	finaliseAggregate(&result)

	// Successful, non-dry-run runs clean up state so a follow-up resume
	// against the same key surfaces "nothing to resume" rather than a
	// stale file.
	if !req.DryRun && result.Status != types.MigrationStatusBlock {
		_ = ClearReindexState(src.Context, tgt.Context, req.TargetSuffix, req.TargetIndexLiteral)
	}
	return result, nil
}

// resumeReindex re-loads a prior reindex's state and polls every
// not-yet-completed task ID to finality. Submissions are not repeated:
// resume's contract is "finish what was started", not "start over with
// the same flags". An entry without a TaskID indicates the cluster
// never accepted the original submit; resume reports it as a blocker
// so the operator re-runs the original command for that index.
func resumeReindex(ctx context.Context, src, tgt Endpoint, req types.MigrationReindexRequest, pollInterval time.Duration) (types.MigrationReindexResult, error) {
	state, err := LoadReindexState(src.Context, tgt.Context, req.TargetSuffix, req.TargetIndexLiteral)
	if err != nil {
		if errors.Is(err, ErrNoReindexState) {
			return types.MigrationReindexResult{}, client.UsageError("--resume found no state for source=%q target=%q (suffix=%q literal=%q)", src.Context, tgt.Context, req.TargetSuffix, req.TargetIndexLiteral)
		}
		return types.MigrationReindexResult{}, err
	}

	result := types.MigrationReindexResult{
		Action: "migrate-reindex",
		Source: src.summary(),
		Target: tgt.summary(),
	}

	for _, e := range state.Entries {
		entry := types.MigrationReindexEntry{
			SourceIndex: e.SourceIndex,
			TargetIndex: e.TargetIndex,
			TaskID:      e.TaskID,
		}
		if e.Completed {
			// Replay the original verdict so a partial run that
			// produced a per-row block doesn't silently upgrade to OK
			// on resume. Older state files (schema 1, before this
			// field) carry an empty Status; fall back to OK for
			// backwards compatibility — the same observable behaviour
			// the prior implementation produced.
			if e.Status == "" {
				entry.Status = types.MigrationStatusOK
				entry.Detail = "previously completed"
			} else {
				entry.Status = e.Status
				entry.Detail = e.Detail
				if entry.Detail == "" {
					entry.Detail = "previously completed (status preserved from original run)"
				}
			}
			result.Indices = append(result.Indices, entry)
			continue
		}
		if e.TaskID == "" {
			entry.Status = types.MigrationStatusBlock
			entry.Detail = "no task_id captured during the original run; re-run without --resume to retry"
			result.Indices = append(result.Indices, entry)
			continue
		}

		final := pollAndFinaliseEntry(ctx, tgt, entry, e.TaskID, pollInterval, req.Conflicts)
		if errors.Is(final.pollErr, context.Canceled) || errors.Is(final.pollErr, context.DeadlineExceeded) {
			result.Indices = append(result.Indices, final.MigrationReindexEntry)
			cancelRemoteTask(tgt, e.TaskID)
			return result, final.pollErr
		}
		if errors.Is(final.pollErr, client.ErrAuth) || errors.Is(final.pollErr, client.ErrForbidden) {
			result.Indices = append(result.Indices, final.MigrationReindexEntry)
			return result, final.pollErr
		}

		state.Entries = appendOrReplaceStateEntry(state.Entries, ReindexStateEntry{
			SourceIndex: e.SourceIndex,
			TargetIndex: e.TargetIndex,
			TaskID:      e.TaskID,
			Completed:   true,
			Status:      final.Status,
			Detail:      final.Detail,
		})
		if err := SaveReindexState(state); err != nil {
			logging.Logger().Warn("migrate.reindex.state_save_failed", "err", err)
		}
		result.Indices = append(result.Indices, final.MigrationReindexEntry)
	}

	finaliseAggregate(&result)
	if result.Status != types.MigrationStatusBlock {
		_ = ClearReindexState(src.Context, tgt.Context, req.TargetSuffix, req.TargetIndexLiteral)
	}
	return result, nil
}

// finalisedEntry pairs a per-index outcome with the polling error that
// produced it. Kept private — callers fold into MigrationReindexResult.
type finalisedEntry struct {
	types.MigrationReindexEntry
	pollErr error
}

// pollAndFinaliseEntry blocks on /_tasks/<id> until terminal, then
// renders the per-index status / detail row. The polling error is
// returned alongside the entry so callers can branch on auth /
// cancellation without re-classifying the detail string.
func pollAndFinaliseEntry(ctx context.Context, tgt Endpoint, entry types.MigrationReindexEntry, taskID string, pollInterval time.Duration, conflicts string) finalisedEntry {
	status, pollErr := pollReindexTask(ctx, tgt.Reindex, taskID, pollInterval)
	entry.Stats = status
	switch {
	case pollErr != nil:
		if errors.Is(pollErr, context.Canceled) || errors.Is(pollErr, context.DeadlineExceeded) {
			entry.Status = types.MigrationStatusBlock
			entry.Detail = "polling cancelled; cluster-side task cancel requested"
			return finalisedEntry{MigrationReindexEntry: entry, pollErr: pollErr}
		}
		if errors.Is(pollErr, client.ErrNotFound) {
			entry.Status = types.MigrationStatusBlock
			entry.Detail = fmt.Sprintf("task %s no longer exists on the cluster — cannot recover", taskID)
			return finalisedEntry{MigrationReindexEntry: entry, pollErr: pollErr}
		}
		entry.Status = types.MigrationStatusBlock
		entry.Detail = fmt.Sprintf("polling failed: %v", pollErr)
		return finalisedEntry{MigrationReindexEntry: entry, pollErr: pollErr}
	case len(status.Failures) > 0:
		entry.Status = types.MigrationStatusBlock
		entry.Detail = summariseFailures(status.Failures)
	case status.VersionConflicts > 0 && conflicts == "proceed":
		entry.Status = types.MigrationStatusWarn
		entry.Detail = fmt.Sprintf("%d version conflicts skipped (conflicts=proceed)", status.VersionConflicts)
	case status.Cancelled:
		entry.Status = types.MigrationStatusBlock
		entry.Detail = "task cancelled before completion"
	default:
		entry.Status = types.MigrationStatusOK
		entry.Detail = fmt.Sprintf("created=%d updated=%d deleted=%d", status.Created, status.Updated, status.Deleted)
	}
	return finalisedEntry{MigrationReindexEntry: entry}
}

// pollReindexTask blocks until the cluster reports the task as
// completed (or cancelled), returning the final status block. A
// transient cluster error during polling (a 503 during a master flap)
// is retried with exponential backoff up to MaxReindexPollFailures
// consecutive failures — auth failures, ErrNotFound, and context
// cancellation are fatal because they won't fix themselves.
//
// Backoff doubles each consecutive failure, capped at the cluster-side
// 1Hz floor (interval × 8) so a flapping master sees us back off but
// the polling rate doesn't drift to "every 10 minutes" once the master
// recovers.
func pollReindexTask(ctx context.Context, r client.Reindexer, taskID string, interval time.Duration) (types.ReindexTaskStatus, error) {
	timer := time.NewTimer(0)
	defer timer.Stop()

	const maxBackoff = 8
	consecutiveFailures := 0
	backoffShift := 0

	var last types.ReindexTaskStatus
	for {
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-timer.C:
		}

		status, err := r.ReindexTaskStatus(ctx, taskID)
		if err != nil {
			if errors.Is(err, client.ErrAuth) || errors.Is(err, client.ErrForbidden) || errors.Is(err, client.ErrNotFound) {
				return last, err
			}
			consecutiveFailures++
			if consecutiveFailures >= MaxReindexPollFailures {
				return last, fmt.Errorf("reindex task %s: %d consecutive transient errors (last: %w)", taskID, consecutiveFailures, err)
			}
			// Exponential backoff: interval * 2^backoffShift, capped.
			backoffShift++
			if backoffShift > maxBackoff {
				backoffShift = maxBackoff
			}
			timer.Reset(interval << backoffShift)
			continue
		}
		// Successful read resets backoff so the next blip starts from
		// the configured interval again.
		consecutiveFailures = 0
		backoffShift = 0
		last = status
		if status.Completed || status.Cancelled {
			return status, nil
		}
		timer.Reset(interval)
	}
}

// cancelRemoteTask issues a best-effort POST /_tasks/<id>/_cancel on a
// fresh background context. The parent context is already cancelled at
// the call site (that's why we're cancelling), so we can't reuse it.
// All errors are logged-and-swallowed — the operator already knows
// they pressed Ctrl-C; failing the cancel doesn't change what they need
// to do (the cluster will eventually time the task out on its own).
func cancelRemoteTask(tgt Endpoint, taskID string) {
	if tgt.TasksCancel == nil {
		logging.Logger().Warn("migrate.reindex.cancel_skipped",
			"reason", "TasksCancel capability not wired",
			"task_id", taskID,
		)
		return
	}
	cctx, cancel := context.WithTimeout(context.Background(), reindexCancelTimeout)
	defer cancel()
	_, err := tgt.TasksCancel.CancelTasks(cctx, types.TasksCancelRequest{
		TaskIDs:           []string{taskID},
		WaitForCompletion: false,
	})
	if err != nil {
		logging.Logger().Warn("migrate.reindex.cancel_failed",
			"task_id", taskID,
			"err", err,
		)
		return
	}
	logging.Logger().Info("migrate.reindex.cancel_requested", "task_id", taskID)
}

// validateReindexEndpoints surfaces a clear "wiring bug" message when
// the CLI built an endpoint without the capability the call needs —
// better than a nil-pointer panic in adapter code half a function
// deep.
func validateReindexEndpoints(src, tgt Endpoint) error {
	if src.Indices == nil {
		return errors.New("source endpoint missing IndexInspector — wiring bug")
	}
	if tgt.Reindex == nil {
		return errors.New("target endpoint missing Reindexer — wiring bug")
	}
	if src.URL == "" {
		return errors.New("source endpoint URL is empty — cannot build remote reindex source")
	}
	return nil
}

// validateReindexRequest enforces the rules `migrate reindex` carries
// over from the CLI surface. The CLI layer can re-state these (better
// error messages with flag names), but enforcing them here too keeps
// the package callable from outside the CLI without re-implementing
// the same checks.
func validateReindexRequest(req types.MigrationReindexRequest) error {
	if req.TargetSuffix != "" && req.TargetIndexLiteral != "" {
		return client.UsageError("--to-suffix and --to-index are mutually exclusive")
	}
	if c := strings.ToLower(strings.TrimSpace(req.Conflicts)); c != "" && c != "abort" && c != "proceed" {
		return client.UsageError("conflicts: expected 'abort' or 'proceed' (got %q)", req.Conflicts)
	}
	if req.RequestsPerSecond < -1 {
		return client.UsageError("requests-per-second: -1 = unbounded; expected >= -1, got %v", req.RequestsPerSecond)
	}
	if req.Slices != "" && req.Slices != "auto" {
		// Positive integer is the only other accepted form. A bad
		// value here would surface as a 400 from the cluster; we
		// intercept so the operator sees the flag name in the error.
		if !isPositiveIntString(req.Slices) {
			return client.UsageError("slices: expected 'auto' or a positive integer (got %q)", req.Slices)
		}
	}
	return nil
}

// isPositiveIntString reports whether s is a base-10 positive integer
// string. Used to validate --slices client-side; we don't need the
// parsed value (the cluster does that), only the shape.
func isPositiveIntString(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	// Reject leading zero — the cluster honours "01" as 1, but the
	// shape is suspect enough that we'd rather flag it than honour
	// it silently.
	return s[0] != '0'
}

// resolveTargetIndex picks the destination index name from the request
// shape: a literal --to-index wins over a --to-suffix, both win over
// "use the source name as-is" (the in-place upgrade case where the
// operator has provisioned a fresh index with the right mappings under
// a new name and the CLI wires the rest).
func resolveTargetIndex(req types.MigrationReindexRequest, sourceName string, single bool) string {
	if req.TargetIndexLiteral != "" && single {
		return req.TargetIndexLiteral
	}
	if req.TargetSuffix != "" {
		return sourceName + req.TargetSuffix
	}
	return sourceName
}

// appendOrReplaceStateEntry merges a new entry by SourceIndex so a
// re-save after polling completion replaces the prior submission row
// instead of appending a duplicate. Order is preserved so a captured
// state file reads top-to-bottom in submission order.
func appendOrReplaceStateEntry(entries []ReindexStateEntry, e ReindexStateEntry) []ReindexStateEntry {
	for i, prev := range entries {
		if prev.SourceIndex == e.SourceIndex {
			entries[i] = e
			return entries
		}
	}
	return append(entries, e)
}

// finaliseAggregate sets the result's Status / Blockers / Warnings from
// its per-entry rows. Kept as a helper so the live and resume paths
// produce identical aggregates.
func finaliseAggregate(result *types.MigrationReindexResult) {
	switch {
	case anyEntryStatus(result.Indices, types.MigrationStatusBlock) > 0:
		result.Status = types.MigrationStatusBlock
	case anyEntryStatus(result.Indices, types.MigrationStatusWarn) > 0:
		result.Status = types.MigrationStatusWarn
	default:
		result.Status = types.MigrationStatusOK
	}
	result.Blockers = anyEntryStatus(result.Indices, types.MigrationStatusBlock)
	result.Warnings = anyEntryStatus(result.Indices, types.MigrationStatusWarn)
}

// anyEntryStatus counts entries with the given status. Used for the
// result's Blockers / Warnings footer.
func anyEntryStatus(entries []types.MigrationReindexEntry, status string) int {
	n := 0
	for _, e := range entries {
		if e.Status == status {
			n++
		}
	}
	return n
}

// summariseFailures clips a long failure list to a short one-line
// detail for the table cell. The full list lives in entry.Stats.Failures
// — a saved JSON record keeps everything; the table view stays
// readable in a 100-column terminal.
func summariseFailures(failures []string) string {
	if len(failures) == 0 {
		return ""
	}
	first := failures[0]
	if len(first) > 120 {
		first = first[:119] + "…"
	}
	if len(failures) == 1 {
		return first
	}
	return fmt.Sprintf("%s (+%d more)", first, len(failures)-1)
}
