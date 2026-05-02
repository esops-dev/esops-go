package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/internal/output"
	esopssnapshot "github.com/esops-dev/esops-go/internal/snapshot"
	"github.com/esops-dev/esops-go/pkg/types"
)

func createCommand() *cli.Command {
	return &cli.Command{
		Name:  "create",
		Usage: "Take a snapshot in a registered repository",
		Description: `Take a snapshot via PUT /_snapshot/<repo>/<snapshot>.

The repository must already be registered (use 'snapshot list' to see
the registered set). --indices accepts the cluster's standard pattern
syntax (comma-separated names, '*' wildcards); empty captures every
open index — the cluster default.

By default the call returns once the cluster has accepted the request;
the snapshot continues asynchronously and 'snapshot list' surfaces its
final state. Use --wait to block until the cluster confirms completion
— a polling loop against /_snapshot/<repo>/<snap>/_status, *not* the
cluster's blocking wait_for_completion query param, so the HTTP
connection cycles per poll instead of hanging open for hours.

Each --wait run persists a small state file under $XDG_STATE_HOME/esops
so SIGINT (or a crashed shell) can be recovered with --resume. SIGINT
during --wait fires DELETE on the cluster-side snapshot, turning Ctrl-C
into a real abort instead of just abandoning the local poller.

Mutates cluster state. Use --dry-run first; against a prod-protected
context, --i-know-this-is-prod is required.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "repository",
				Required: true,
				Usage:    "Registered repository name (literal, no patterns)",
			},
			&cli.StringFlag{
				Name:     "snapshot",
				Required: true,
				Usage:    "Snapshot name to create (literal, no patterns; reusing an existing name fails)",
			},
			&cli.StringFlag{
				Name:  "indices",
				Usage: "Comma-separated index pattern(s) to include (e.g. 'logs-*,events-2026-*'). Empty = every open index.",
			},
			&cli.BoolFlag{
				Name:  "include-global-state",
				Usage: "Include cluster-level state (templates, persistent settings) in the snapshot",
			},
			&cli.BoolFlag{
				Name:  "partial",
				Usage: "Allow the snapshot to proceed even when some shards are unavailable (off by default — silent partial backups are a foot-gun)",
			},
			&cli.BoolFlag{
				Name:  "ignore-unavailable",
				Usage: "Skip indices that don't exist or are closed instead of failing",
			},
			&cli.BoolFlag{
				Name:  "wait",
				Usage: "Block until the cluster reports the snapshot finished (polls /_status; SIGINT triggers cluster-side DELETE)",
			},
			&cli.BoolFlag{
				Name:  "resume",
				Usage: "Pick up an interrupted --wait by polling the existing in-progress snapshot rather than re-submitting",
			},
			&cli.DurationFlag{
				Name:  "poll-interval",
				Usage: "How often --wait polls /_status (default 10s; cluster-friendly bound: 1s).",
				Value: esopssnapshot.DefaultPollInterval,
			},
			&cli.StringSliceFlag{
				Name:  "metadata",
				Usage: "Free-form labels attached to the snapshot, repeatable as KEY=VALUE (e.g. --metadata reason=pre-upgrade)",
			},
			&cli.StringFlag{
				Name:  "metadata-file",
				Usage: "YAML/JSON file whose top-level object merges into the snapshot's metadata block. Use when --metadata's flat string KVs aren't expressive enough.",
			},
			&cli.DurationFlag{
				Name:  "master-timeout",
				Usage: "Cluster-side master_timeout for this request (e.g. 1m). Default: cluster default (30s).",
			},
		},
		Action: runCreate,
	}
}

func runCreate(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	wait := cmd.Bool("wait")
	resume := cmd.Bool("resume")
	dryRun := cmd.Bool("dry-run")
	repository := cmd.String("repository")
	snapshotName := cmd.String("snapshot")

	metadata, err := parseMetadataFlags(cmd.StringSlice("metadata"))
	if err != nil {
		return err
	}
	if path := strings.TrimSpace(cmd.String("metadata-file")); path != "" {
		fileMeta, err := loadKVMapFile(path)
		if err != nil {
			return client.UsageError("--metadata-file: %v", err)
		}
		if metadata == nil {
			metadata = make(map[string]any, len(fileMeta))
		}
		for k, v := range fileMeta {
			if _, exists := metadata[k]; exists {
				return client.UsageError("metadata key %q is set both via --metadata and --metadata-file (refusing to silently pick one)", k)
			}
			metadata[k] = v
		}
	}

	opts := esopssnapshot.CreateOptions{
		Repository: repository,
		Snapshot:   snapshotName,
		Indices:    cmd.String("indices"),
		// Always async on the cluster side. We poll /_status ourselves;
		// blocking on wait_for_completion holds the HTTP connection
		// open and locks us out of the SIGINT-triggered DELETE.
		WaitForCompletion: false,
		Metadata:          metadata,
		MasterTimeout:     cmd.Duration("master-timeout"),
		DryRun:            dryRun,
	}
	if cmd.IsSet("include-global-state") {
		v := cmd.Bool("include-global-state")
		opts.IncludeGlobalState = &v
	}
	if cmd.IsSet("partial") {
		v := cmd.Bool("partial")
		opts.Partial = &v
	}
	if cmd.IsSet("ignore-unavailable") {
		v := cmd.Bool("ignore-unavailable")
		opts.IgnoreUnavailable = &v
	}

	contextName := cmd.String("context")
	if contextName == "" {
		contextName = "default"
	}

	var result types.SnapshotCreateResult
	if resume {
		// --resume: skip the cluster submit. The state file proves the
		// previous run handed off the request; we go straight to the
		// polling loop. If the state file is missing, fall through to a
		// fresh submit so --resume on first invocation isn't a hard
		// error.
		state, err := esopssnapshot.LoadCreateState(contextName, repository, snapshotName)
		switch {
		case err == nil && state.SubmittedAccepted:
			result = types.SnapshotCreateResult{
				Action:            "create",
				Repository:        repository,
				Name:              snapshotName,
				Accepted:          true,
				WaitForCompletion: wait,
			}
			s.Log("snapshot.create.resume", "repository", repository, "snapshot", snapshotName)
		case err != nil && !errors.Is(err, esopssnapshot.ErrNoState):
			return client.UsageError("--resume: %v", err)
		default:
			result, err = esopssnapshot.Create(ctx, s.Client.SnapshotCreate, opts)
			if err != nil {
				return err
			}
		}
	} else {
		result, err = esopssnapshot.Create(ctx, s.Client.SnapshotCreate, opts)
		if err != nil {
			return err
		}
	}

	if !dryRun && result.Accepted {
		// Persist resume state immediately on accept — even if --wait is
		// off, the operator can come back later with `--resume --wait`.
		if persistErr := esopssnapshot.SaveCreateState(esopssnapshot.CreateState{
			Action:            "create",
			Context:           contextName,
			Repository:        repository,
			Snapshot:          snapshotName,
			StartedAt:         time.Now(),
			SubmittedAccepted: true,
		}); persistErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not persist resume state: %v\n", persistErr)
		}
	}

	s.Log("snapshot.create",
		"repository", result.Repository,
		"snapshot", result.Name,
		"dry_run", result.DryRun,
		"accepted", result.Accepted,
		"wait", wait,
	)

	if !wait || dryRun || !result.Accepted {
		return output.SnapshotCreate(os.Stdout, result, s.Format)
	}

	// --wait path: print the initial accept record, then poll. SIGINT in
	// the parent context cancels the loop and triggers a cluster-side
	// DELETE on the in-progress snapshot.
	if err := output.SnapshotCreate(os.Stderr, result, s.Format); err != nil {
		return err
	}

	pollInterval := cmd.Duration("poll-interval")
	final, waitErr := esopssnapshot.WaitForCompletion(ctx, s.Client.Snapshot, s.Client.SnapshotPrune, esopssnapshot.WaitOptions{
		Repository:        repository,
		Snapshot:          snapshotName,
		PollInterval:      pollInterval,
		CancelOnInterrupt: true,
		ProgressFn: func(st types.SnapshotStatus) {
			fmt.Fprintf(os.Stderr,
				"snapshot %s/%s state=%s shards=%d/%d (init=%d started=%d failed=%d)\n",
				st.Repository, st.Snapshot, st.State,
				st.ShardsDone, st.ShardsTotal,
				st.ShardsInit, st.ShardsStarted, st.ShardsFailed,
			)
		},
	})

	// Whatever the outcome, the resume state is no longer useful — the
	// snapshot has either reached a terminal state or the operator
	// abandoned the wait.
	_ = esopssnapshot.ClearCreateState(contextName, repository, snapshotName)

	if waitErr != nil {
		// Cancellation surfaces as ctx.Err(); the wait machinery already
		// fired the DELETE.
		if errors.Is(waitErr, context.Canceled) || errors.Is(waitErr, context.DeadlineExceeded) {
			fmt.Fprintln(os.Stderr, "snapshot create: cancelled; cluster-side DELETE issued")
		}
		return waitErr
	}

	// Promote the polled status into the result so the operator's
	// captured JSON record carries the final state, not just the
	// initial accept.
	result.Snapshot.State = final.State
	result.Snapshot.StartTimeMillis = final.StartTimeMillis
	result.Snapshot.DurationMillis = final.DurationMillis
	result.Snapshot.ShardsTotal = final.ShardsTotal
	result.Snapshot.ShardsSuccessful = final.ShardsDone
	result.Snapshot.ShardsFailed = final.ShardsFailed
	return output.SnapshotCreate(os.Stdout, result, s.Format)
}

// parseMetadataFlags converts the --metadata KEY=VALUE repeatable flag
// into the map the cluster expects under the body's `metadata` block.
// Empty input returns nil so an unset flag falls cleanly through to
// "no metadata" rather than carrying an empty object on the wire.
func parseMetadataFlags(values []string) (map[string]any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(values))
	for _, raw := range values {
		k, v, ok := strings.Cut(raw, "=")
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if !ok || k == "" {
			return nil, client.UsageError("--metadata expects KEY=VALUE (got %q)", raw)
		}
		if _, exists := out[k]; exists {
			return nil, client.UsageError("--metadata key %q specified more than once", k)
		}
		out[k] = v
	}
	return out, nil
}

