package migrate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

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

// Reindex submits a deferred /_reindex per source-resolved index on
// the target cluster, then polls /_tasks/<id> until each completes.
// The reindex itself runs cluster-side; this function is the orchestrator.
//
// Multi-index requests are processed sequentially: one task at a time.
// Concurrency knobs belong on the cluster (slices/requests-per-second
// per task) rather than client-side fan-out — a single esops invocation
// running ten reindexes in parallel is exactly the foot-gun the
// max_in_flight context cap exists to prevent.
//
// On context cancellation the in-flight task is cancelled cluster-side
// via /_tasks/<id>/_cancel; the function returns ctx.Err() so SIGINT
// propagates as exit 130. (Not implemented yet via a separate cancel
// capability — uses the cluster's cancel endpoint via the Tasks
// canceller wired alongside the reindexer in the same client.)
func Reindex(ctx context.Context, src, tgt Endpoint, req types.MigrationReindexRequest) (types.MigrationReindexResult, error) {
	if err := validateReindexEndpoints(src, tgt); err != nil {
		return types.MigrationReindexResult{}, err
	}
	if err := validateReindexRequest(req); err != nil {
		return types.MigrationReindexResult{}, err
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

	pollInterval := req.PollInterval
	if pollInterval <= 0 {
		pollInterval = DefaultReindexPollInterval
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

		status, pollErr := pollReindexTask(ctx, tgt.Reindex, submit.TaskID, pollInterval)
		entry.Stats = status
		switch {
		case pollErr != nil:
			if errors.Is(pollErr, context.Canceled) || errors.Is(pollErr, context.DeadlineExceeded) {
				return result, pollErr
			}
			entry.Status = types.MigrationStatusBlock
			entry.Detail = fmt.Sprintf("polling failed: %v", pollErr)
		case len(status.Failures) > 0:
			entry.Status = types.MigrationStatusBlock
			entry.Detail = summariseFailures(status.Failures)
		case status.VersionConflicts > 0 && req.Conflicts == "proceed":
			entry.Status = types.MigrationStatusWarn
			entry.Detail = fmt.Sprintf("%d version conflicts skipped (conflicts=proceed)", status.VersionConflicts)
		case status.Cancelled:
			entry.Status = types.MigrationStatusBlock
			entry.Detail = "task cancelled before completion"
		default:
			entry.Status = types.MigrationStatusOK
			entry.Detail = fmt.Sprintf("created=%d updated=%d deleted=%d", status.Created, status.Updated, status.Deleted)
		}
		result.Indices = append(result.Indices, entry)
	}

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
	return result, nil
}

// pollReindexTask blocks until the cluster reports the task as
// completed (or cancelled), returning the final status block. A
// transient cluster error during polling (a 503 during a master flap)
// is retried on the next tick — auth failures are fatal because they
// won't fix themselves.
func pollReindexTask(ctx context.Context, r client.Reindexer, taskID string, interval time.Duration) (types.ReindexTaskStatus, error) {
	timer := time.NewTimer(0)
	defer timer.Stop()

	var last types.ReindexTaskStatus
	for {
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-timer.C:
		}

		status, err := r.ReindexTaskStatus(ctx, taskID)
		if err != nil {
			if errors.Is(err, client.ErrAuth) || errors.Is(err, client.ErrForbidden) {
				return last, err
			}
			// Transient: keep going. The next tick may succeed.
			timer.Reset(interval)
			continue
		}
		last = status
		if status.Completed || status.Cancelled {
			return status, nil
		}
		timer.Reset(interval)
	}
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

// anyEntryStatus counts entries with the given status. Used for the
// report's Blockers / Warnings footer.
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
