package datastream

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/cli/watch"
	esopsdatastream "github.com/esops-dev/esops-go/internal/datastream"
	"github.com/esops-dev/esops-go/internal/output"
)

func recoveryCommand() *cli.Command {
	return &cli.Command{
		Name:  "recovery",
		Usage: "Show in-flight shard recoveries across a data stream's backing indices",
		Description: `Read recoveries for every backing index of the matched data
streams via GET /<backing-indices>/_recovery — the per-shard progress
endpoint the cluster maintains for snapshot restores, post-shrink
relocations, and replica reassignment.

Resolves --name to the underlying backing indices and forwards a
single /_recovery call with the comma-joined list, so the rendered
table mirrors 'index recovery' row-for-row.

Read-only and safe to run against a prod-protected context at any
time.

--active-only forwards ?active_only=true so the cluster only echoes
shards still in INIT / INDEX / TRANSLOG / FINALIZE; once everything
is DONE the result is empty. Without it the cluster also surfaces
the shards that finished recovering since startup.`,
		Flags: append([]cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Comma-separated data stream names or patterns (e.g. 'logs-*,metrics-app')",
			},
			&cli.BoolFlag{
				Name:  "active-only",
				Usage: "Only show shards still recovering (drops DONE entries)",
				Value: true,
			},
		}, watch.Flags()...),
		Action:        runRecovery,
		ShellComplete: completion.ForFlags(dsNameSuggesters),
	}
}

func runRecovery(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopsdatastream.RecoveryOptions{
		Pattern:    cmd.String("name"),
		ActiveOnly: cmd.Bool("active-only"),
	}
	return watch.Run(ctx, cmd, "esops datastream recovery", s.Format, func(ctx context.Context) error {
		report, err := esopsdatastream.Recovery(ctx, s.Client.DataStream, s.Client.Recovery, opts)
		if err != nil {
			return err
		}
		shardCount := 0
		for _, ix := range report.Indices {
			shardCount += len(ix.Shards)
		}
		s.Log("datastream.recovery",
			"indices", len(report.Indices),
			"shards", shardCount,
			"all_done", report.AllDone,
			"active_only", opts.ActiveOnly,
		)
		return output.Recovery(os.Stdout, report, s.Format)
	})
}
