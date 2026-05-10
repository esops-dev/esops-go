package index

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	esopsindex "github.com/esops-dev/esops-go/internal/index"
	"github.com/esops-dev/esops-go/internal/output"
)

func recoveryCommand() *cli.Command {
	return &cli.Command{
		Name:  "recovery",
		Usage: "Show in-flight shard recoveries with progress",
		Description: `Read recoveries via GET /<indices>/_recovery — the per-shard
progress endpoint the cluster maintains for snapshot restores,
post-shrink relocations, and replica reassignment.

Read-only and safe to run against a prod-protected context at any time.

--active-only forwards ?active_only=true so the cluster only echoes
shards still in INIT / INDEX / TRANSLOG / FINALIZE; once everything is
DONE the result is empty. Without it the cluster also surfaces the
shards that finished recovering since startup, which can be hundreds
of rows on a healthy long-lived cluster.

Wide output adds the source snapshot tuple and raw byte/file totals;
the default keeps to stage, primary, percent recovered, and a
human-readable size so an operator watching a restore can tell at a
glance which shards are still copying.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "index",
				Usage: "Comma-separated index names or patterns (default: all indices)",
			},
			&cli.BoolFlag{
				Name:  "active-only",
				Usage: "Only show shards still recovering (drops DONE entries)",
				Value: true,
			},
		},
		Action: runRecovery,
	}
}

func runRecovery(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopsindex.RecoveryOptions{
		Pattern:    cmd.String("index"),
		ActiveOnly: cmd.Bool("active-only"),
	}
	report, err := esopsindex.Recovery(ctx, s.Client.Recovery, opts)
	if err != nil {
		return err
	}

	shardCount := 0
	for _, ix := range report.Indices {
		shardCount += len(ix.Shards)
	}
	s.Log("index.recovery",
		"indices", len(report.Indices),
		"shards", shardCount,
		"all_done", report.AllDone,
		"active_only", opts.ActiveOnly,
	)
	return output.Recovery(os.Stdout, report, s.Format)
}
