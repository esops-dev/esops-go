package snapshot

import (
	"context"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/dj-wasabi/esops-go/internal/cli/cmdsetup"
	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/output"
	esopssnapshot "github.com/dj-wasabi/esops-go/internal/snapshot"
)

func restoreCommand() *cli.Command {
	return &cli.Command{
		Name:  "restore",
		Usage: "Restore a snapshot into the cluster",
		Description: `Restore a snapshot via POST /_snapshot/<repo>/<snapshot>/_restore.

The cluster refuses to restore over an existing open index — surface that
500 verbatim. The standard zero-downtime workflow is to restore alongside
with --rename-pattern / --rename-replacement (e.g. '(.+)' → 'restored-$1')
and swap aliases afterwards.

By default the call returns once the cluster has accepted the request;
the restore continues asynchronously and 'snapshot list' / cluster
recovery surface its progress. Use --wait to block until completion.

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
			&cli.BoolFlag{
				Name:  "wait",
				Usage: "Block until the cluster reports the restore finished (?wait_for_completion=true)",
			},
		},
		Action: runRestore,
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

	indexSettings, err := parseIndexSettingFlags(cmd.StringSlice("index-setting"))
	if err != nil {
		return err
	}

	opts := esopssnapshot.RestoreOptions{
		Repository:          cmd.String("repository"),
		Snapshot:            cmd.String("snapshot"),
		Indices:             cmd.String("indices"),
		RenamePattern:       cmd.String("rename-pattern"),
		RenameReplacement:   cmd.String("rename-replacement"),
		IndexSettings:       indexSettings,
		IgnoreIndexSettings: cmd.StringSlice("ignore-index-setting"),
		WaitForCompletion:   cmd.Bool("wait"),
		DryRun:              cmd.Bool("dry-run"),
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

	result, err := esopssnapshot.Restore(ctx, s.Client.SnapshotRestore, opts)
	if err != nil {
		return err
	}

	s.Log("snapshot.restore",
		"repository", result.Repository,
		"snapshot", result.Name,
		"dry_run", result.DryRun,
		"accepted", result.Accepted,
		"wait", result.WaitForCompletion,
		"rename", result.RenamePattern != "",
	)
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
