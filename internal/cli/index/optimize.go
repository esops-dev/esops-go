package index

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	esopsindex "github.com/esops-dev/esops-go/internal/index"
	"github.com/esops-dev/esops-go/internal/onerror"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/types"
)

func optimizeCommand() *cli.Command {
	flags := []cli.Flag{
		&cli.StringFlag{
			Name:     "index",
			Required: true,
			Usage:    "Comma-separated index names or patterns (e.g. 'logs-2024-*,esops-archive')",
		},
		&cli.IntFlag{
			Name:  "max-indices",
			Usage: "Override the default 20-index blast-radius cap on the resolved pattern",
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
			Name:  "ignore-unavailable",
			Usage: "Treat missing indices as a no-op instead of a 404 error",
		},
	}
	flags = append(flags, cmdsetup.OnErrorFlags()...)
	return &cli.Command{
		Name:  "optimize",
		Usage: "Force-merge indices to reduce segment count",
		Description: `Run /{index}/_forcemerge against one or more open indices to reduce
their segment count. The typical use is sealing a rotated index after
it stops receiving writes — merging to one segment recovers heap and
speeds up small-shard searches.

Mutates cluster state and is I/O-intensive: a force-merge can saturate
disk and slow down reads on the affected nodes. The resolved pattern
is capped at 20 indices by default — pass --max-indices N for larger
sweeps. Use --dry-run first; against a prod-protected context,
--i-know-this-is-prod is required. Closed indices in the pattern are
skipped (force-merge against a closed index is a 400 on both
dialects).

--on-error abort (default) stops on the first per-shard rejection;
--on-error skip dispatches one /_forcemerge per resolved index and
continues past failures (reported in the ON-ERROR summary). Rollback
is refused — force-merge is irreversible.`,
		Flags:  flags,
		Action: runOptimize,
		ShellComplete: completion.ForFlags(map[string]completion.Suggester{
			"index": completion.SuggestIndices,
		}),
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

	policy, err := cmdsetup.OnErrorPolicy(cmd)
	if err != nil {
		return err
	}
	if policy == types.OnErrorRollback {
		return onerror.RefuseRollback("index optimize")
	}

	opts := esopsindex.OptimizeOptions{
		Pattern:            cmd.String("index"),
		MaxIndices:         cmd.Int("max-indices"),
		DryRun:             cmd.Bool("dry-run"),
		MaxNumSegments:     cmd.Int("max-num-segments"),
		OnlyExpungeDeletes: cmd.Bool("only-expunge-deletes"),
		IgnoreUnavailable:  cmd.Bool("ignore-unavailable"),
		OnError:            policy,
	}
	if cmd.Bool("no-flush") {
		flush := false
		opts.Flush = &flush
	}

	result, err := esopsindex.Optimize(ctx, s.Client.Indices, s.Client.IndexOptimize, opts)
	if err != nil {
		return err
	}

	s.Log("index.optimize",
		"dry_run", result.DryRun,
		"indices", len(result.Indices),
		"shards_total", result.ShardsTotal,
		"shards_failed", result.ShardsFailed,
	)
	if err := output.IndexOptimize(os.Stdout, result, s.Format); err != nil {
		return err
	}
	output.OnErrorSummaryFooter(os.Stdout, result.OnError, s.Format, cmdsetup.Verbose(cmd))
	if result.OnError != nil {
		return onerror.SummaryError(*result.OnError)
	}
	return nil
}
