package index

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/cli/watch"
	esopsindex "github.com/esops-dev/esops-go/internal/index"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/client"
)

func describeCommand() *cli.Command {
	return &cli.Command{
		Name:      "describe",
		Usage:     "Show a detailed summary of one index",
		ArgsUsage: "NAME",
		Description: `Aggregate read-only views of a single index — settings, mappings,
aliases, shards, recovery, segments, lifecycle (ILM/ISM) state,
matched index template, and any in-flight tasks touching the index
— into one sectioned summary.

Read-only and safe against a prod-protected context.

NAME is the only positional argument and must be a single literal
index or alias name; patterns are rejected. When NAME is an alias
that points at exactly one index (or the write index of a multi-
target alias), the alias resolves to that index. Multi-target
aliases without a write index are refused — name the index directly
or use 'esops index list' to fan out.

Default mappings view is a per-type field summary. Pass
--full-mappings for the cluster's raw properties tree (which on a
multi-thousand-field log mapping can be megabytes).

The per-shard listing is shown automatically when the index has 12
or fewer shards. --show-shards forces the listing regardless,
--no-shards hides it, and --output wide implies --show-shards as
the "give me everything" mode operators reach for during incidents.

--watch re-renders the summary on a fixed interval (kubectl-style)
so an operator can sit on the output during a reindex, restore, or
forcemerge and watch doc counts, segments, and recovery progress
converge.`,
		Flags: append([]cli.Flag{
			&cli.BoolFlag{
				Name:  "show-shards",
				Usage: "Force the per-shard listing even on indices with more than 12 shards",
			},
			&cli.BoolFlag{
				Name:  "no-shards",
				Usage: "Hide the per-shard listing entirely (overrides --show-shards / wide)",
			},
			&cli.BoolFlag{
				Name:  "full-mappings",
				Usage: "Print the full mappings properties tree instead of the per-type summary",
			},
			&cli.BoolFlag{
				Name:  "include-defaults",
				Usage: "Also print inherited setting defaults alongside the configured values",
			},
		}, watch.Flags()...),
		Action: runDescribe,
		// Tab-complete the positional NAME against the union of index
		// and alias names; the cluster names share a namespace, so
		// describe accepts both at the same position.
		ShellComplete: completion.ForPositional(1, completion.SuggestIndices, completion.SuggestAliases),
	}
}

func runDescribe(ctx context.Context, cmd *cli.Command) error {
	args := cmd.Args()
	if args.Len() != 1 {
		return client.UsageError("describe takes exactly one positional argument (NAME); got %d", args.Len())
	}
	name := args.First()

	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	caps := esopsindex.DescribeCapabilities{
		Info:      s.Client.Indices,
		Aliases:   s.Client.AliasInspect,
		Settings:  s.Client.IndexSettings,
		Mappings:  s.Client.Mappings,
		Shards:    s.Client.Shards,
		Recovery:  s.Client.Recovery,
		Segments:  s.Client.Segments,
		ILM:       s.Client.ILM,
		ISM:       s.Client.ISM,
		Tasks:     s.Client.Tasks,
		Templates: s.Client.IndexTemplateGet,
		Dialect:   string(s.Client.Info.Dialect),
	}

	showShards, err := resolveShardVisibility(cmd)
	if err != nil {
		return err
	}

	renderOpts := output.IndexDescribeOptions{
		ShowShards:      showShards,
		FullMappings:    cmd.Bool("full-mappings"),
		IncludeDefaults: cmd.Bool("include-defaults"),
	}
	aggOpts := esopsindex.DescribeOptions{
		Name:            name,
		IncludeDefaults: cmd.Bool("include-defaults"),
	}

	return watch.Run(ctx, cmd, watch.Options{Label: "esops index describe", ContextName: s.ContextName, Format: s.Format}, func(ctx context.Context) error {
		desc, err := esopsindex.Describe(ctx, caps, aggOpts)
		if err != nil {
			return err
		}
		s.Log("index.describe",
			"index", desc.Info.Name,
			"resolved_from", desc.ResolvedFrom,
			"health", desc.Info.Health,
			"status", desc.Info.Status,
			"warnings", len(desc.SectionWarnings),
		)
		return output.IndexDescribe(os.Stdout, desc, renderOpts, s.Format)
	})
}

// resolveShardVisibility folds the two booleans (--show-shards and
// --no-shards) into the renderer's tri-state. Setting both is a
// usage error rather than a silent precedence — the operator's
// intent matters too much to guess.
func resolveShardVisibility(cmd *cli.Command) (*bool, error) {
	show := cmd.Bool("show-shards")
	hide := cmd.Bool("no-shards")
	if show && hide {
		return nil, client.UsageError("--show-shards and --no-shards are mutually exclusive")
	}
	switch {
	case show:
		v := true
		return &v, nil
	case hide:
		v := false
		return &v, nil
	default:
		return nil, nil
	}
}
