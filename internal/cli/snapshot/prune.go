package snapshot

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/dj-wasabi/esops-go/internal/cli/cmdsetup"
	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/output"
	esopssnapshot "github.com/dj-wasabi/esops-go/internal/snapshot"
)

func pruneCommand() *cli.Command {
	return &cli.Command{
		Name:  "prune",
		Usage: "Apply a retention policy to snapshots in a repository",
		Description: `Apply --keep-last and/or --older-than retention to a repository.

Neither cluster ships a native prune endpoint; the retention plan is
computed client-side from 'snapshot list', and the resulting set is
deleted via DELETE /_snapshot/<repo>/<snap[,snap…]> in one bulk request.

--dry-run reports the exact set the live run would delete without
contacting the cluster's delete endpoint. The blast-radius guard refuses
a prune that would delete more than 50% of the candidate set in one run
unless --allow-large-prune is set — the verbose flag is the audit trail.

Mutates cluster state. Against a prod-protected context,
--i-know-this-is-prod is required.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "repository",
				Required: true,
				Usage:    "Registered repository name (literal, no patterns)",
			},
			&cli.StringFlag{
				Name:  "snapshot",
				Usage: "Comma-separated snapshot name pattern(s) to consider (e.g. 'nightly-*'). Empty = every snapshot in the repository.",
			},
			&cli.IntFlag{
				Name:  "keep-last",
				Usage: "Keep the N most-recent (by start_time) snapshots; combine with --older-than to layer rules",
			},
			&cli.StringFlag{
				Name:  "older-than",
				Usage: "Delete snapshots older than DURATION (e.g. 30d, 12w, 1y, or Go-syntax like 720h)",
			},
			&cli.StringFlag{
				Name:  "state",
				Usage: "Restrict candidates by state: success | failed | partial | in_progress | incompatible (default: every state)",
			},
			&cli.BoolFlag{
				Name:  "allow-large-prune",
				Usage: "Required when retention would delete more than 50% of the candidate set in one run",
			},
		},
		Action: runPrune,
	}
}

func runPrune(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	olderThanRaw := cmd.String("older-than")
	olderThan, err := esopssnapshot.ParseRetentionDuration(olderThanRaw)
	if err != nil {
		return client.UsageError("%s", err.Error())
	}

	opts := esopssnapshot.PruneOptions{
		Repository:      cmd.String("repository"),
		SnapshotPattern: cmd.String("snapshot"),
		KeepLast:        cmd.Int("keep-last"),
		OlderThan:       olderThan,
		OlderThanRaw:    olderThanRaw,
		StateFilter:     cmd.String("state"),
		AllowLargePrune: cmd.Bool("allow-large-prune"),
		DryRun:          cmd.Bool("dry-run"),
	}

	result, err := esopssnapshot.Prune(ctx, s.Client.Snapshot, s.Client.SnapshotPrune, opts)
	if err != nil {
		return err
	}

	s.Log("snapshot.prune",
		"repository", result.Repository,
		"considered", result.Considered,
		"kept", len(result.Kept),
		"deleted", len(result.Deleted),
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.SnapshotPrune(os.Stdout, result, s.Format)
}
