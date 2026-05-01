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

// WaitOptions configure the snapshot create completion poller. Repository
// and Snapshot identify the target. PollInterval is how often the
// cluster is asked for status; the cluster cap is roughly 1Hz before
// the master node starts complaining, and most snapshots take minutes
// or hours, so the default 10s suits both ends.
//
// CancelOnInterrupt, when true, fires DELETE /_snapshot/<repo>/<snap>
// on the cluster when the parent context is cancelled — turns a local
// SIGINT into an actual cluster-side abort instead of just abandoning
// the local poller. The cluster honours DELETE on an in-progress
// snapshot by tearing it down at the next safe point.
//
// ProgressFn is called once per poll with the latest status; nil means
// "no progress reporting". The CLI hands a function that prints to
// stderr so the operator sees the snapshot moving without affecting
// the stdout contract.
type WaitOptions struct {
	Repository        string
	Snapshot          string
	PollInterval      time.Duration
	CancelOnInterrupt bool
	ProgressFn        func(types.SnapshotStatus)
}

// DefaultPollInterval is the cluster-friendly default for a long-running
// `snapshot create --wait`. A snapshot that takes hours rarely needs
// finer granularity than 10s; busy clusters with many concurrent ops
// notice tighter polls.
const DefaultPollInterval = 10 * time.Second

// terminalSnapshotStates is the set of cluster state values from
// /_status that mean "stop polling". SUCCESS and FAILED speak for
// themselves; PARTIAL/INCOMPATIBLE are also terminal — the cluster has
// settled. ABORTED lands here when the operator (or another caller)
// hit DELETE on an in-progress snapshot.
var terminalSnapshotStates = map[string]struct{}{
	"SUCCESS":      {},
	"FAILED":       {},
	"PARTIAL":      {},
	"INCOMPATIBLE": {},
	"ABORTED":      {},
}

// WaitForCompletion polls /_snapshot/<repo>/<snap>/_status until the
// snapshot reaches a terminal state or the context is cancelled. On
// cancellation with CancelOnInterrupt=true, the cluster-side snapshot
// is aborted via DELETE; the function returns ctx.Err() so the caller's
// SIGINT propagates as exit 130.
//
// Returns the final status. A "missing from cluster" reading mid-wait
// is suspect — the cluster may have garbage-collected an aborted entry
// before our poll caught up — surfaced as an error so the operator sees
// it rather than the function silently returning success.
func WaitForCompletion(ctx context.Context, ins client.SnapshotInspector, pruner client.SnapshotPruner, opts WaitOptions) (types.SnapshotStatus, error) {
	repo := strings.TrimSpace(opts.Repository)
	snap := strings.TrimSpace(opts.Snapshot)
	if repo == "" || snap == "" {
		return types.SnapshotStatus{}, client.UsageError("WaitForCompletion: repository and snapshot are required")
	}
	interval := opts.PollInterval
	if interval <= 0 {
		interval = DefaultPollInterval
	}

	// First poll happens immediately so the operator sees movement
	// quickly on a snapshot that's already done by the time --wait
	// starts (a small one-shard backup, for example).
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return handleWaitCancellation(pruner, opts, ctx.Err())
		case <-timer.C:
		}

		status, err := ins.SnapshotStatus(ctx, types.SnapshotStatusRequest{
			Repository: repo,
			Snapshot:   snap,
		})
		if err != nil {
			// Transient cluster errors (a 503 during a master flap)
			// shouldn't kill an 8-hour wait. Retry on the next tick.
			// Auth failures are fatal because they won't fix themselves.
			if errors.Is(err, client.ErrAuth) || errors.Is(err, client.ErrForbidden) {
				return types.SnapshotStatus{}, err
			}
			if opts.ProgressFn != nil {
				opts.ProgressFn(types.SnapshotStatus{
					Repository: repo, Snapshot: snap,
					State: fmt.Sprintf("POLL_ERROR: %v", err),
				})
			}
			timer.Reset(interval)
			continue
		}

		if opts.ProgressFn != nil {
			opts.ProgressFn(status)
		}

		if status.MissingFromCluster {
			// Cluster has no record. If the resume state says we did
			// hand off the request, this is the cluster having
			// garbage-collected the entry — surface as an error.
			return status, fmt.Errorf("snapshot %s/%s missing from cluster mid-wait (likely aborted or expired)", repo, snap)
		}

		if _, terminal := terminalSnapshotStates[strings.ToUpper(status.State)]; terminal {
			return status, nil
		}

		timer.Reset(interval)
	}
}

// handleWaitCancellation fires the cluster-side abort on context
// cancellation when the operator opted in. The DELETE is best-effort:
// a failure to abort doesn't change the fact that the local wait was
// interrupted, so we surface the original ctx.Err() and let the abort
// failure log through the caller's structured logger if it cares.
func handleWaitCancellation(pruner client.SnapshotPruner, opts WaitOptions, ctxErr error) (types.SnapshotStatus, error) {
	if !opts.CancelOnInterrupt || pruner == nil {
		return types.SnapshotStatus{}, ctxErr
	}
	// Use a fresh context with a short deadline — the parent context is
	// already cancelled, but we still want to send the DELETE.
	cancelCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = pruner.DeleteSnapshots(cancelCtx, types.SnapshotDeleteRequest{
		Repository: opts.Repository,
		Snapshots:  []string{opts.Snapshot},
	})
	return types.SnapshotStatus{
		Repository: opts.Repository,
		Snapshot:   opts.Snapshot,
		State:      "ABORTED_BY_CANCEL",
	}, ctxErr
}
