package ops

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/dj-wasabi/esops-go/internal/cluster"
	"github.com/dj-wasabi/esops-go/internal/config"
	esopsops "github.com/dj-wasabi/esops-go/internal/ops"
	"github.com/dj-wasabi/esops-go/internal/output"
	"github.com/dj-wasabi/esops-go/internal/types"
)

func shardsCommand() *cli.Command {
	return &cli.Command{
		Name:  "shards",
		Usage: "Show shard allocation across the cluster",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "index",
				Usage: "Limit to index pattern(s), comma-separated (e.g. 'logs-*,users')",
			},
			&cli.StringFlag{
				Name:  "state",
				Usage: "Filter by shard state (e.g. STARTED, UNASSIGNED, RELOCATING)",
			},
			&cli.StringFlag{
				Name:  "node",
				Usage: "Filter by node name (exact match)",
			},
			&cli.BoolFlag{
				Name:  "primary",
				Usage: "Show primaries only",
			},
			&cli.BoolFlag{
				Name:  "replica",
				Usage: "Show replicas only",
			},
			&cli.BoolFlag{
				Name:  "unassigned",
				Usage: "Shortcut for --state UNASSIGNED",
			},
		},
		Action: runShards,
	}
}

func runShards(ctx context.Context, cmd *cli.Command) error {
	if cmd.Bool("primary") && cmd.Bool("replica") {
		return fmt.Errorf("--primary and --replica are mutually exclusive")
	}
	if cmd.Bool("unassigned") && cmd.String("state") != "" {
		return fmt.Errorf("--unassigned and --state are mutually exclusive")
	}

	cfg, _, err := config.LoadDefault(cmd.String("config"))
	if err != nil {
		return err
	}
	name, cc, err := cfg.ResolveContext(cmd.String("context"))
	if err != nil {
		return err
	}
	applyOverrides(&cc, cmd)

	format, err := output.Parse(cmd.String("output"))
	if err != nil {
		return err
	}

	c, err := cluster.New(ctx, cc)
	if err != nil {
		return err
	}

	filter := types.ShardFilter{
		Index: cmd.String("index"),
		State: cmd.String("state"),
		Node:  cmd.String("node"),
	}
	if cmd.Bool("unassigned") {
		filter.State = "UNASSIGNED"
	}
	switch {
	case cmd.Bool("primary"):
		filter.Role = types.ShardRolePrimary
	case cmd.Bool("replica"):
		filter.Role = types.ShardRoleReplica
	}

	shards, err := esopsops.Shards(ctx, c, filter)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "context=%s dialect=%s version=%s shards=%d\n", name, c.Info.Dialect, c.Info.Version, len(shards))
	return output.Shards(os.Stdout, shards, format)
}
