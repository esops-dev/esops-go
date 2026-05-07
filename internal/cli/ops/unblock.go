package ops

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	esopsops "github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/internal/output"
)

func unblockCommand() *cli.Command {
	return &cli.Command{
		Name:  "unblock",
		Usage: "Clear an index block (e.g. read_only_allow_delete after disk pressure)",
		Description: `Clear a cluster-side index block by writing a JSON null to the
matching index.blocks.<name> setting. The default block is
read_only_allow_delete — the disk-watermark block the cluster auto-
applies under disk pressure and does not auto-clear when the disk
returns under threshold.

Other accepted block names: read_only, write, metadata, read.

Mutates cluster state. The cluster has no native dry-run for
/_settings, so --dry-run is a client-side "validate + report" path.
Against a prod-protected context, --i-know-this-is-prod is required.

The match-everything shortcuts ('*' and '_all') are refused outright;
name a pattern that matches the indices you intend to unblock.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "index",
				Required: true,
				Usage:    "Comma-separated index names or patterns (e.g. 'logs-2024-*'); '*' / '_all' refused",
			},
			&cli.StringFlag{
				Name:  "block",
				Value: "read_only_allow_delete",
				Usage: "Cluster index block to clear (read_only_allow_delete, read_only, write, metadata, read)",
			},
			&cli.BoolFlag{
				Name:  "ignore-unavailable",
				Usage: "Treat missing indices as a no-op instead of a 404 error",
			},
		},
		Action: runUnblock,
	}
}

func runUnblock(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopsops.UnblockOptions{
		Index:             cmd.String("index"),
		Block:             cmd.String("block"),
		IgnoreUnavailable: cmd.Bool("ignore-unavailable"),
		DryRun:            cmd.Bool("dry-run"),
	}
	result, err := esopsops.UnblockIndices(ctx, s.Client.IndexSettingsSet, opts)
	if err != nil {
		return err
	}
	s.Log("ops.unblock",
		"index", opts.Index,
		"block", result.Block,
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.IndexUnblock(os.Stdout, result, s.Format)
}
