package ops

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/watch"
	esopsops "github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/internal/output"
)

func nodesCommand() *cli.Command {
	return &cli.Command{
		Name:  "nodes",
		Usage: "Show cluster nodes with role, version, heap, and disk usage",
		Description: `Read-only listing over /_cat/nodes. Default columns cover the
question an on-call asks first: who's in the cluster, what version,
how much heap and disk are they sitting on. -o wide adds the master
flag, IP, CPU, load average, and uptime.

The same /_cat/nodes pull powers shard-skew analysis (ops shards
analyze) — a node holding zero shards is invisible to /_cat/shards
but still listed here, which is exactly the row that matters when a
freshly-added or drained node is the cause of allocation drift.`,
		Flags:  watch.Flags(),
		Action: runNodes,
	}
}

func runNodes(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	return watch.Run(ctx, cmd, "esops ops nodes", s.Format, func(ctx context.Context) error {
		nodes, err := esopsops.Nodes(ctx, s.Client.Nodes)
		if err != nil {
			return err
		}
		s.Log("ops.nodes", "nodes", len(nodes))
		return output.Nodes(os.Stdout, nodes, s.Format)
	})
}
