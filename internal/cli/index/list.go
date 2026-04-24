package index

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/dj-wasabi/esops-go/internal/cli/cmdsetup"
	esopsindex "github.com/dj-wasabi/esops-go/internal/index"
	"github.com/dj-wasabi/esops-go/internal/logging"
	"github.com/dj-wasabi/esops-go/internal/output"
	"github.com/dj-wasabi/esops-go/internal/types"
)

func listCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List indices with size, docs and health",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "index",
				Usage: "Limit to index pattern(s), comma-separated (e.g. 'logs-*,users')",
			},
			&cli.StringFlag{
				Name:  "health",
				Usage: "Filter by health: green | yellow | red",
			},
			&cli.StringFlag{
				Name:  "status",
				Usage: "Filter by status: open | close",
			},
			&cli.BoolFlag{
				Name:  "hidden",
				Usage: "Include hidden indices (dot-prefixed system indices)",
			},
			&cli.StringFlag{
				Name:  "sort",
				Usage: "Sort by: name | size | docs",
				Value: "name",
			},
		},
		Action: runList,
	}
}

func runList(ctx context.Context, cmd *cli.Command) error {
	sortKey, err := esopsindex.ParseSort(cmd.String("sort"))
	if err != nil {
		return err
	}
	if h := cmd.String("health"); h != "" {
		if err := esopsindex.ValidateHealth(h); err != nil {
			return err
		}
	}
	if s := cmd.String("status"); s != "" {
		if err := esopsindex.ValidateStatus(s); err != nil {
			return err
		}
	}

	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	filter := types.IndexFilter{
		Index:  cmd.String("index"),
		Health: cmd.String("health"),
		Status: cmd.String("status"),
		Hidden: cmd.Bool("hidden"),
		Sort:   sortKey,
	}

	indices, err := esopsindex.List(ctx, s.Client.Indices, filter)
	if err != nil {
		return err
	}

	logging.Logger().Info("index.list",
		"context", s.ContextName,
		"dialect", string(s.Client.Info.Dialect),
		"version", s.Client.Info.Version,
		"indices", len(indices),
	)
	return output.Indices(os.Stdout, indices, s.Format)
}
