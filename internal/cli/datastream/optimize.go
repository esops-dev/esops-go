package datastream

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	esopsdatastream "github.com/esops-dev/esops-go/internal/datastream"
	"github.com/esops-dev/esops-go/internal/output"
)

func optimizeCommand() *cli.Command {
	return &cli.Command{
		Name:  "optimize",
		Usage: "Force-merge a data stream's backing indices (skipping the write index by default)",
		Description: `Run /{backing-index}/_forcemerge against every backing index of
the matched data streams to reduce segment count. The typical use is
sealing rotated backing indices after rollover: the cluster keeps the
write index hot for ongoing ingestion, while the older generations
benefit from compacting down to one segment.

The current write index is skipped by default — force-merging a
live write target is expensive (the merge competes with ingestion) and
on Elasticsearch the cluster refuses /_forcemerge on the active write
index of a data stream outright. Pass --include-write-index to opt in
when you know the stream is quiescent.

Mutates cluster state and is I/O-intensive: a force-merge can saturate
disk and slow down reads on the affected nodes. The resolved
backing-index list is capped at 50 by default — pass --max-indices N
for larger sweeps. Use --dry-run first; against a prod-protected
context, --i-know-this-is-prod is required.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Comma-separated data stream names or patterns (e.g. 'logs-*,metrics-app')",
			},
			&cli.IntFlag{
				Name:  "max-indices",
				Usage: "Override the default 50-backing-index blast-radius cap on the resolved sweep",
			},
			&cli.IntFlag{
				Name:  "max-num-segments",
				Usage: "Merge each shard down to N segments (1 is the typical sealed-archive value; 0 leaves the cluster default)",
			},
			&cli.BoolFlag{
				Name:  "only-expunge-deletes",
				Usage: "Only rewrite segments holding tombstoned docs (cheaper than a full merge)",
			},
			&cli.BoolFlag{
				Name:  "no-flush",
				Usage: "Skip the post-merge flush (cluster default is to flush)",
			},
			&cli.BoolFlag{
				Name:  "include-write-index",
				Usage: "Force-merge the data stream's current write index as well (default: skip it; ES refuses /_forcemerge on the write index)",
			},
			&cli.BoolFlag{
				Name:  "ignore-unavailable",
				Usage: "Treat missing backing indices as a no-op instead of a 404 error",
			},
		},
		Action:        runOptimize,
		ShellComplete: completion.ForFlags(dsNameSuggesters),
	}
}

func runOptimize(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopsdatastream.OptimizeOptions{
		Pattern:            cmd.String("name"),
		MaxIndices:         cmd.Int("max-indices"),
		DryRun:             cmd.Bool("dry-run"),
		MaxNumSegments:     cmd.Int("max-num-segments"),
		OnlyExpungeDeletes: cmd.Bool("only-expunge-deletes"),
		IgnoreUnavailable:  cmd.Bool("ignore-unavailable"),
		IncludeWriteIndex:  cmd.Bool("include-write-index"),
	}
	if cmd.Bool("no-flush") {
		flush := false
		opts.Flush = &flush
	}

	result, err := esopsdatastream.Optimize(ctx, s.Client.DataStream, s.Client.Indices, s.Client.IndexOptimize, opts)
	if err != nil {
		return err
	}

	s.Log("datastream.optimize",
		"dry_run", result.DryRun,
		"indices", len(result.Indices),
		"shards_total", result.ShardsTotal,
		"shards_failed", result.ShardsFailed,
	)
	return output.IndexOptimize(os.Stdout, result, s.Format)
}
