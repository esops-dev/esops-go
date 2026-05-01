package snapshot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// RestoreWaitOptions configure the snapshot restore completion poller.
// Repository and Snapshot identify the source; TargetIndices is the
// post-rename list of indices the restore is materialising into the
// cluster — what the poller actually queries via `/_recovery`. The
// caller resolves the list (typically via a SnapshotInspector) so the
// wait machinery doesn't need to know about rename math.
//
// PollInterval is how often /_recovery is asked; the cluster cap on
// useful polling frequency is around 1Hz, and most restores take
// minutes-to-hours, so the default 10s suits both ends.
//
// ProgressFn is called once per poll with the latest report; nil means
// "no progress reporting". The CLI hands a function that prints to
// stderr so the operator sees per-shard movement without affecting the
// stdout contract.
//
// **No `CancelOnInterrupt` knob.** Restore can't be cleanly cancelled
// at the cluster — there is no `_cancel` endpoint and deleting the
// recovering index loses the partial recovery. On context
// cancellation the poller stops and the cluster keeps going; the
// caller should print a clear "restore continues server-side" message
// so the operator knows the indices will eventually arrive.
type RestoreWaitOptions struct {
	Repository    string
	Snapshot      string
	TargetIndices []string
	PollInterval  time.Duration
	ProgressFn    func(types.RecoveryReport)
}

// WaitForRestore polls /_recovery until every shard of every target
// index reaches stage `DONE`, the context is cancelled, or a fatal
// error fires. Returns the final report on success, or the partial
// last-known report plus the error otherwise.
//
// Unlike WaitForCompletion (snapshot create), this function does not
// touch the cluster on cancellation: there is no good cluster-side
// mechanism to abort a restore mid-flight. Callers should print a
// "restore continues server-side; use index list / index close to
// clean up" message so the operator isn't left wondering whether the
// SIGINT achieved anything. (It did — locally, by abandoning the
// poller.)
func WaitForRestore(ctx context.Context, rec client.RecoveryInspector, opts RestoreWaitOptions) (types.RecoveryReport, error) {
	repo := strings.TrimSpace(opts.Repository)
	snap := strings.TrimSpace(opts.Snapshot)
	if repo == "" || snap == "" {
		return types.RecoveryReport{}, client.UsageError("WaitForRestore: repository and snapshot are required")
	}
	if len(opts.TargetIndices) == 0 {
		return types.RecoveryReport{}, client.UsageError("WaitForRestore: target_indices is required (resolve via the snapshot's index list and rename pair)")
	}
	interval := opts.PollInterval
	if interval <= 0 {
		interval = DefaultPollInterval
	}

	indices := strings.Join(opts.TargetIndices, ",")
	timer := time.NewTimer(0)
	defer timer.Stop()

	var last types.RecoveryReport
	for {
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-timer.C:
		}

		report, err := rec.Recovery(ctx, types.RecoveryRequest{
			Indices:    indices,
			ActiveOnly: false,
		})
		if err != nil {
			// Auth failures are fatal; transient errors retry on the
			// next tick so a master flap doesn't kill an hour-long
			// restore wait.
			if errors.Is(err, client.ErrAuth) || errors.Is(err, client.ErrForbidden) {
				return last, err
			}
			if opts.ProgressFn != nil {
				opts.ProgressFn(types.RecoveryReport{
					Indices: []types.IndexRecovery{{
						Index:  fmt.Sprintf("POLL_ERROR: %v", err),
						Shards: nil,
					}},
				})
			}
			timer.Reset(interval)
			continue
		}

		last = report
		if opts.ProgressFn != nil {
			opts.ProgressFn(report)
		}

		// Restoration is done when /_recovery reports AllDone over the
		// target index set. With ?active_only=false we see DONE entries
		// for indices that have already finished, so the AllDone check
		// is meaningful even after the cluster has cleared them from
		// the active list.
		if restoreCoversAllTargets(report, opts.TargetIndices) {
			return report, nil
		}

		timer.Reset(interval)
	}
}

// restoreCoversAllTargets reports whether the report's indices cover
// every target name and every shard in those indices is at stage DONE.
// A target index that's missing from the report is treated as "not
// done yet" — the cluster surfaces an index in /_recovery from the
// moment shard allocation begins, so a missing entry mid-restore
// almost certainly means the cluster hasn't started that index yet.
func restoreCoversAllTargets(report types.RecoveryReport, targets []string) bool {
	seen := make(map[string]bool, len(targets))
	for _, t := range targets {
		seen[t] = false
	}
	for _, idx := range report.Indices {
		if _, ok := seen[idx.Index]; !ok {
			continue
		}
		allShardsDone := len(idx.Shards) > 0
		for _, s := range idx.Shards {
			if strings.ToUpper(s.Stage) != "DONE" {
				allShardsDone = false
				break
			}
		}
		seen[idx.Index] = allShardsDone
	}
	for _, done := range seen {
		if !done {
			return false
		}
	}
	return true
}

// ResolveRestoreTargets reads the snapshot's stored index list,
// applies the operator's --indices filter and rename pair, and
// returns the post-rename list — the same resolution
// `RestoreWithPreflight` runs for the dry-run collision check, lifted
// out so the wait machinery can call it independently.
//
// Used by `restore --wait` to know which indices to poll, and by
// `restore --resume` to recover the target list when the previous
// run's state file is missing.
func ResolveRestoreTargets(ctx context.Context, ins client.SnapshotInspector, opts RestoreOptions) ([]string, error) {
	rows, err := ins.Snapshots(ctx, types.SnapshotFilter{
		Repository: opts.Repository,
		Snapshot:   opts.Snapshot,
	})
	if err != nil {
		return nil, err
	}
	var sourceIndices []string
	for _, r := range rows {
		if r.Name == opts.Snapshot {
			sourceIndices = append(sourceIndices, r.Indices...)
			break
		}
	}
	if len(sourceIndices) == 0 {
		return nil, errors.New("snapshot not found or has no indices recorded")
	}
	if filt := strings.TrimSpace(opts.Indices); filt != "" {
		want := splitIndicesPreview(filt)
		wantSet := make(map[string]struct{}, len(want))
		for _, w := range want {
			wantSet[w] = struct{}{}
		}
		filtered := sourceIndices[:0]
		for _, n := range sourceIndices {
			if _, ok := wantSet[n]; ok {
				filtered = append(filtered, n)
			}
		}
		if len(filtered) > 0 {
			sourceIndices = filtered
		}
	}
	return applyRestoreRename(sourceIndices, opts.RenamePattern, opts.RenameReplacement)
}
