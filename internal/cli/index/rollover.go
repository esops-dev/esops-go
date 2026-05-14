package index

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	esopsindex "github.com/esops-dev/esops-go/internal/index"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/types"
)

func rolloverCommand() *cli.Command {
	return &cli.Command{
		Name:  "rollover",
		Usage: "Roll an alias's write index over by age, size, or doc count",
		Description: `Roll an alias forward to a new write index when one or more
conditions match. POST /<alias>/_rollover with a {conditions:{...}}
body — the cluster evaluates the conditions against the alias's
current write index and either creates the next generation or
reports which condition(s) didn't match.

Either at least one condition (--max-age / --max-docs / --max-size /
--max-primary-size) must be set, or --force must be passed to
acknowledge an unconditional rollover. Use --dry-run to evaluate
conditions without creating the new index. Against a prod-protected
context, --i-know-this-is-prod is required.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "alias",
				Required: true,
				Usage:    "Alias whose current write index will be rolled over (e.g. 'esops-logs')",
			},
			&cli.StringFlag{
				Name:  "new-index",
				Usage: "Override the cluster's default name for the new write index (rarely needed)",
			},
			&cli.StringFlag{
				Name:  "max-age",
				Usage: "Roll over when the current write index is older than DURATION (e.g. '7d', '1h')",
			},
			&cli.Int64Flag{
				Name:  "max-docs",
				Usage: "Roll over when the current write index reaches N documents",
			},
			&cli.StringFlag{
				Name:  "max-size",
				Usage: "Roll over when the current write index reaches SIZE (e.g. '50gb')",
			},
			&cli.StringFlag{
				Name:  "max-primary-size",
				Usage: "Roll over when any primary shard reaches SIZE (e.g. '25gb')",
			},
			&cli.BoolFlag{
				Name:  "force",
				Usage: "Acknowledge an unconditional rollover when no --max-* condition is set",
			},
			&cli.StringFlag{
				Name:  "wait-for-active-shards",
				Usage: "Forward to the cluster as ?wait_for_active_shards=<value> on the new index (numeric or 'all')",
			},
		},
		Action: runRollover,
		ShellComplete: completion.ForFlags(map[string]completion.Suggester{
			"alias": completion.SuggestAliases,
		}),
	}
}

func runRollover(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopsindex.RolloverOptions{
		Alias:    cmd.String("alias"),
		NewIndex: cmd.String("new-index"),
		Conditions: types.IndexRolloverConditions{
			MaxAge:         cmd.String("max-age"),
			MaxDocs:        cmd.Int64("max-docs"),
			MaxSize:        cmd.String("max-size"),
			MaxPrimarySize: cmd.String("max-primary-size"),
		},
		DryRun:              cmd.Bool("dry-run"),
		Force:               cmd.Bool("force"),
		WaitForActiveShards: cmd.String("wait-for-active-shards"),
	}

	result, err := esopsindex.Rollover(ctx, s.Client.IndexRollover, opts)
	if err != nil {
		return err
	}

	s.Log("index.rollover",
		"alias", result.Alias,
		"old_index", result.OldIndex,
		"new_index", result.NewIndex,
		"rolled_over", result.RolledOver,
		"dry_run", result.DryRun,
	)
	return output.IndexRollover(os.Stdout, result, s.Format)
}
