package ops

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/cli/watch"
	esopsops "github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/client"
)

// nodeCommand is the singular-noun parent for `esops ops node …`.
// Reserves a stable namespace for per-node operations (describe today,
// future hot-threads / settings views tomorrow). The plural-noun
// `ops nodes` (list) stays alongside it — matches the kubectl idiom
// where `get nodes` and `describe node NAME` live side by side.
func nodeCommand() *cli.Command {
	return &cli.Command{
		Name:  "node",
		Usage: "Inspect one cluster node by name",
		Commands: []*cli.Command{
			nodeDescribeCommand(),
		},
	}
}

func nodeDescribeCommand() *cli.Command {
	return &cli.Command{
		Name:      "describe",
		Usage:     "Show a detailed summary of one cluster node",
		ArgsUsage: "NAME",
		Description: `Aggregate read-only views of a single node — /_cat/nodes row,
JVM/OS introspection, bootstrap limits, static settings, current
shard layout, and drain/voting-exclusion state — into one sectioned
summary.

Read-only and safe against a prod-protected context.

NAME is required and must be the literal node name (the cluster's
node.name setting), not a pattern. The per-shard listing collapses
to a one-line summary when the node holds more than 25 shards;
--show-shards (or --output wide) forces the full listing.

--watch repaints the summary on a fixed interval so an operator can
sit on the output during a node drain and watch the shard count
fall to zero.`,
		Flags: append([]cli.Flag{
			&cli.BoolFlag{
				Name:  "show-shards",
				Usage: "Force the per-shard listing even on busy data nodes",
			},
			&cli.BoolFlag{
				Name:  "no-shards",
				Usage: "Hide the per-shard listing entirely (overrides --show-shards / wide)",
			},
		}, watch.Flags()...),
		Action:        runNodeDescribe,
		ShellComplete: completion.ForPositional(1, completion.SuggestNodes),
	}
}

func runNodeDescribe(ctx context.Context, cmd *cli.Command) error {
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

	caps := esopsops.NodeDescribeCapabilities{
		Nodes:           s.Client.Nodes,
		Stats:           s.Client.NodeStats,
		Bootstrap:       s.Client.NodeBootstrap,
		Settings:        s.Client.NodeSettings,
		Shards:          s.Client.Shards,
		ClusterSettings: s.Client.ClusterSettingsRead,
		VotingExclusion: s.Client.VotingExclusion,
		Dialect:         string(s.Client.Info.Dialect),
	}

	showShards, err := resolveNodeShardVisibility(cmd)
	if err != nil {
		return err
	}

	return watch.Run(ctx, cmd, watch.Options{Label: "esops ops node describe", ContextName: s.ContextName, Format: s.Format}, func(ctx context.Context) error {
		desc, err := esopsops.NodeDescribe(ctx, caps, esopsops.NodeDescribeOptions{Name: name})
		if err != nil {
			return err
		}
		s.Log("ops.node.describe",
			"node", desc.Info.Name,
			"shards", desc.ShardSummary.Primaries+desc.ShardSummary.Replicas,
			"drained", desc.Drain.AllocationExcluded,
			"voting_excluded", desc.Drain.VotingExcluded,
			"warnings", len(desc.SectionWarnings),
		)
		return output.NodeDescribe(os.Stdout, desc, output.NodeDescribeOptions{
			ShowShards: showShards,
		}, s.Format)
	})
}

func resolveNodeShardVisibility(cmd *cli.Command) (*bool, error) {
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
