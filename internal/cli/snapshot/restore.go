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
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/output"
	esopssnapshot "github.com/esops-dev/esops-go/internal/snapshot"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

func restoreCommand() *cli.Command {
	return &cli.Command{
		Name:  "restore",
		Usage: "Restore a snapshot into the cluster",
		Description: `Restore a snapshot via POST /_snapshot/<repo>/<snapshot>/_restore.

The cluster refuses to restore over an existing open index. --dry-run
runs a collision pre-flight (resolves the snapshot's index list,
applies any rename pair, queries /_cat/indices) and refuses with exit
code 7 when collisions exist. The standard zero-downtime workflow is
to restore alongside with --rename-pattern / --rename-replacement
(e.g. '(.+)' → 'restored-$1') and swap aliases afterwards.

By default the call returns once the cluster has accepted the request.
--wait polls /_recovery on the post-rename target indices every
--poll-interval (default 10s) and prints per-index/per-shard progress
to stderr until every shard reaches stage DONE. Each --wait run
persists state under $XDG_STATE_HOME/esops/snapshot/ — re-run with
--resume to pick up an interrupted wait without re-issuing the
restore.

SIGINT during --wait stops the local poller but does NOT cancel the
cluster-side restore (the cluster has no clean way to abort a
recovery mid-flight). The cluster keeps recovering; clean up partial
indices afterwards with 'esops index list' / 'esops index close'
if needed.

Mutates cluster state. Defaults to --dry-run-friendly: the cluster is
not contacted on --dry-run; against a prod-protected context,
--i-know-this-is-prod is required.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "repository",
				Required: true,
				Usage:    "Registered repository name (literal, no patterns)",
			},
			&cli.StringFlag{
				Name:     "snapshot",
				Required: true,
				Usage:    "Snapshot name to restore (literal, no patterns)",
			},
			&cli.StringFlag{
				Name:  "indices",
				Usage: "Comma-separated index pattern(s) to restore (e.g. 'logs-*'). Empty restores every index in the snapshot.",
			},
			&cli.BoolFlag{
				Name:  "include-global-state",
				Usage: "Restore cluster-level state (templates, persistent settings) from the snapshot",
			},
			&cli.BoolFlag{
				Name:  "partial",
				Usage: "Allow the restore to proceed even when some shards are missing from the snapshot",
			},
			&cli.BoolFlag{
				Name:  "ignore-unavailable",
				Usage: "Skip indices that aren't in the snapshot instead of failing",
			},
			&cli.StringFlag{
				Name:  "rename-pattern",
				Usage: "Regex matched against snapshot index names; pair with --rename-replacement (e.g. '(.+)' to capture the source name)",
			},
			&cli.StringFlag{
				Name:  "rename-replacement",
				Usage: "Replacement for --rename-pattern using $1, $2 etc. (e.g. 'restored-$1')",
			},
			&cli.StringSliceFlag{
				Name:  "index-setting",
				Usage: "Override an index setting on the restored index, repeatable as KEY=VALUE (e.g. --index-setting index.number_of_replicas=0)",
			},
			&cli.StringSliceFlag{
				Name:  "ignore-index-setting",
				Usage: "Drop a setting from the snapshot's stored metadata so the cluster falls back to its default; repeatable",
			},
			&cli.StringFlag{
				Name:  "index-settings-file",
				Usage: "YAML/JSON file whose keys merge into the body's index_settings block. Use when --index-setting's flat string KVs aren't expressive enough.",
			},
			&cli.BoolFlag{
				Name:  "wait",
				Usage: "Poll /_recovery until every target shard reaches DONE. SIGINT stops the local poller; the cluster keeps going.",
			},
			&cli.BoolFlag{
				Name:  "resume",
				Usage: "Pick up an interrupted --wait by polling the existing in-progress restore.",
			},
			&cli.DurationFlag{
				Name:  "poll-interval",
				Usage: "How often --wait polls /_recovery (default 10s; cluster-friendly minimum: 1s).",
				Value: esopssnapshot.DefaultPollInterval,
			},
			&cli.DurationFlag{
				Name:  "master-timeout",
				Usage: "Cluster-side master_timeout for this request (e.g. 1m). Default: cluster default (30s).",
			},
		},
		Action:        runRestore,
		ShellComplete: completion.ForFlags(snapshotFlagSuggesters),
	}
}

func runRestore(ctx context.Context, cmd *cli.Command) error {
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

	indexSettings, err := parseIndexSettingFlags(cmd.StringSlice("index-setting"))
	if err != nil {
		return err
	}
	if path := strings.TrimSpace(cmd.String("index-settings-file")); path != "" {
		fileSettings, err := loadKVMapFile(path)
		if err != nil {
			return client.UsageError("--index-settings-file: %v", err)
		}
		if indexSettings == nil {
			indexSettings = make(map[string]any, len(fileSettings))
		}
		for k, v := range fileSettings {
			if _, exists := indexSettings[k]; exists {
				return client.UsageError("index_settings key %q is set both via --index-setting and --index-settings-file (refusing to silently pick one)", k)
			}
			indexSettings[k] = v
		}
	}

	opts := esopssnapshot.RestoreOptions{
		Repository:          repository,
		Snapshot:            snapshotName,
		Indices:             cmd.String("indices"),
		RenamePattern:       cmd.String("rename-pattern"),
		RenameReplacement:   cmd.String("rename-replacement"),
		IndexSettings:       indexSettings,
		IgnoreIndexSettings: cmd.StringSlice("ignore-index-setting"),
		// Always async on the cluster side. We poll /_recovery
		// ourselves; blocking on wait_for_completion holds the HTTP
		// connection open and gives the operator no progress signal.
		WaitForCompletion: false,
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

	var (
		result        types.SnapshotRestoreResult
		targetIndices []string
	)
	if resume {
		state, loadErr := esopssnapshot.LoadRestoreState(contextName, repository, snapshotName)
		switch {
		case loadErr == nil && state.SubmittedAccepted:
			result = types.SnapshotRestoreResult{
				Action:            "restore",
				Repository:        repository,
				Name:              snapshotName,
				Accepted:          true,
				WaitForCompletion: wait,
				RenamePattern:     opts.RenamePattern,
				RenameReplacement: opts.RenameReplacement,
				Snapshot: types.SnapshotRestoreInfo{
					Snapshot: snapshotName,
					Indices:  state.TargetIndices,
				},
			}
			targetIndices = state.TargetIndices
			s.Log("snapshot.restore.resume",
				"repository", repository, "snapshot", snapshotName,
				"target_indices", len(targetIndices),
			)
		case loadErr != nil && !errors.Is(loadErr, esopssnapshot.ErrNoState):
			return client.UsageError("--resume: %v", loadErr)
		default:
			result, err = esopssnapshot.RestoreWithPreflight(ctx, s.Client.SnapshotRestore, s.Client.Snapshot, s.Client.Indices, opts)
			if err != nil {
				return err
			}
		}
	} else {
		result, err = esopssnapshot.RestoreWithPreflight(ctx, s.Client.SnapshotRestore, s.Client.Snapshot, s.Client.Indices, opts)
		if err != nil {
			return err
		}
	}

	// Resolve the post-rename target indices once we know the cluster
	// has accepted the request. Reuses the snapshot's stored index
	// list rather than guessing from `result.Snapshot.Indices` because
	// the async accept path doesn't echo it back.
	if !dryRun && result.Accepted && len(targetIndices) == 0 {
		targetIndices, err = esopssnapshot.ResolveRestoreTargets(ctx, s.Client.Snapshot, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not resolve target indices for --wait/--resume (%v); the restore is in flight server-side\n", err)
		}
	}

	if !dryRun && result.Accepted && len(targetIndices) > 0 {
		// Persist resume state so a SIGINT during --wait can be
		// recovered with `--resume --wait`. Even when --wait is off,
		// the operator may come back later with `--resume --wait`.
		if persistErr := esopssnapshot.SaveRestoreState(esopssnapshot.RestoreState{
			Action:            "restore",
			Context:           contextName,
			Repository:        repository,
			Snapshot:          snapshotName,
			StartedAt:         time.Now(),
			SubmittedAccepted: true,
			TargetIndices:     targetIndices,
		}); persistErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not persist resume state: %v\n", persistErr)
		}
	}
	if len(targetIndices) > 0 && len(result.Snapshot.Indices) == 0 {
		// Echo the resolved target list into the result so a captured
		// JSON record describes what the cluster is restoring.
		result.Snapshot.Indices = targetIndices
	}
	result.WaitForCompletion = wait

	s.Log("snapshot.restore",
		"repository", result.Repository,
		"snapshot", result.Name,
		"dry_run", result.DryRun,
		"accepted", result.Accepted,
		"wait", wait,
		"rename", result.RenamePattern != "",
		"target_indices", len(targetIndices),
	)

	if !wait || dryRun || !result.Accepted || len(targetIndices) == 0 {
		return output.SnapshotRestore(os.Stdout, result, s.Format)
	}

	// --wait path. Print the initial accept record to stderr (so stdout
	// is reserved for the final record) and start polling /_recovery.
	if err := output.SnapshotRestore(os.Stderr, result, s.Format); err != nil {
		return err
	}

	pollInterval := cmd.Duration("poll-interval")
	_, waitErr := esopssnapshot.WaitForRestore(ctx, s.Client.Recovery, esopssnapshot.RestoreWaitOptions{
		Repository:    repository,
		Snapshot:      snapshotName,
		TargetIndices: targetIndices,
		PollInterval:  pollInterval,
		ProgressFn: func(r types.RecoveryReport) {
			for _, idx := range r.Indices {
				done, total := 0, len(idx.Shards)
				for _, sh := range idx.Shards {
					if strings.ToUpper(sh.Stage) == "DONE" {
						done++
					}
				}
				fmt.Fprintf(os.Stderr,
					"restore %s/%s -> %s shards=%d/%d done\n",
					repository, snapshotName, idx.Index, done, total,
				)
			}
		},
	})

	// Resume state is no longer useful — either the restore reached a
	// terminal state or the operator abandoned the wait. The cluster-
	// side restore continues regardless; the next operator can poll
	// `index list` / `_recovery` directly without resume state.
	_ = esopssnapshot.ClearRestoreState(contextName, repository, snapshotName)

	if waitErr != nil {
		if errors.Is(waitErr, context.Canceled) || errors.Is(waitErr, context.DeadlineExceeded) {
			fmt.Fprintln(os.Stderr, "snapshot restore: --wait cancelled; the restore CONTINUES server-side")
			fmt.Fprintln(os.Stderr, "                 use 'esops index list' to inspect partially-restored indices")
		}
		return waitErr
	}

	fmt.Fprintln(os.Stderr, "snapshot restore: all target shards reached DONE")
	return output.SnapshotRestore(os.Stdout, result, s.Format)
}

// parseIndexSettingFlags converts the --index-setting KEY=VALUE repeatable
// flag into the map the cluster expects under the body's `index_settings`
// block. Mirrors parseMetadataFlags in shape; kept separate so each flag's
// error messages name the right flag.
func parseIndexSettingFlags(values []string) (map[string]any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(values))
	for _, raw := range values {
		k, v, ok := strings.Cut(raw, "=")
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if !ok || k == "" {
			return nil, client.UsageError("--index-setting expects KEY=VALUE (got %q)", raw)
		}
		if _, exists := out[k]; exists {
			return nil, client.UsageError("--index-setting key %q specified more than once", k)
		}
		out[k] = v
	}
	return out, nil
}
