package snapshot

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/onerror"
	"github.com/esops-dev/esops-go/internal/output"
	esopssnapshot "github.com/esops-dev/esops-go/internal/snapshot"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
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
--i-know-this-is-prod is required.

--on-error abort (default) issues a single bulk DELETE; skip
dispatches one DELETE per resolved snapshot and continues past
failures. Rollback is refused — snapshot deletion is irreversible.`,
		Flags: append([]cli.Flag{
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
				Usage: "Required when retention would delete more than 50% of the eligible set in one run",
			},
			&cli.BoolFlag{
				Name:  "continue-on-missing",
				Usage: "When the bulk DELETE 404s on one or more names (concurrent operator), retry remaining names individually and report the missing ones in the audit trail",
			},
		}, cmdsetup.OnErrorFlags()...),
		Action:        runPrune,
		ShellComplete: completion.ForFlags(snapshotFlagSuggesters),
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

	policy, err := cmdsetup.OnErrorPolicy(cmd)
	if err != nil {
		return err
	}
	if policy == types.OnErrorRollback {
		return onerror.RefuseRollback("snapshot prune")
	}

	olderThanRaw := cmd.String("older-than")
	olderThan, err := esopssnapshot.ParseRetentionDuration(olderThanRaw)
	if err != nil {
		return client.UsageError("%s", err.Error())
	}

	opts := esopssnapshot.PruneOptions{
		Repository:        cmd.String("repository"),
		SnapshotPattern:   cmd.String("snapshot"),
		KeepLast:          cmd.Int("keep-last"),
		OlderThan:         olderThan,
		StateFilter:       cmd.String("state"),
		AllowLargePrune:   cmd.Bool("allow-large-prune"),
		ContinueOnMissing: cmd.Bool("continue-on-missing"),
		DryRun:            cmd.Bool("dry-run"),
		OnError:           policy,
	}

	result, err := esopssnapshot.Prune(ctx, s.Client.Snapshot, s.Client.SnapshotPrune, opts)
	if err != nil {
		return err
	}
	// Echo the operator's --older-than spelling back into the result so a
	// captured JSON record reads "30d" rather than the canonical
	// "720h0m0s" form Go's time.Duration would print. Display-only — the
	// retention math used the parsed duration, so the round-trip is
	// information-preserving for the operator's audit log.
	result.OlderThan = olderThanRaw

	s.Log("snapshot.prune",
		"repository", result.Repository,
		"considered", result.Considered,
		"kept", len(result.Kept),
		"deleted", len(result.Deleted),
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	if err := output.SnapshotPrune(os.Stdout, result, s.Format); err != nil {
		return err
	}
	output.OnErrorSummaryFooter(os.Stdout, result.OnError, s.Format, cmdsetup.Verbose(cmd))
	if result.OnError != nil {
		return onerror.SummaryError(*result.OnError)
	}
	return nil
}
