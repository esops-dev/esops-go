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
(useful in CI; not recommended over slow links since the HTTP
connection stays open for the duration).

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
				Usage: "Block until the cluster reports the snapshot finished (?wait_for_completion=true)",
			},
			&cli.StringSliceFlag{
				Name:  "metadata",
				Usage: "Free-form labels attached to the snapshot, repeatable as KEY=VALUE (e.g. --metadata reason=pre-upgrade)",
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

	metadata, err := parseMetadataFlags(cmd.StringSlice("metadata"))
	if err != nil {
		return err
	}

	opts := esopssnapshot.CreateOptions{
		Repository:        cmd.String("repository"),
		Snapshot:          cmd.String("snapshot"),
		Indices:           cmd.String("indices"),
		WaitForCompletion: cmd.Bool("wait"),
		Metadata:          metadata,
		DryRun:            cmd.Bool("dry-run"),
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

	result, err := esopssnapshot.Create(ctx, s.Client.SnapshotCreate, opts)
	if err != nil {
		return err
	}

	s.Log("snapshot.create",
		"repository", result.Repository,
		"snapshot", result.Name,
		"dry_run", result.DryRun,
		"accepted", result.Accepted,
		"wait", result.WaitForCompletion,
	)
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

