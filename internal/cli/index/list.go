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
	"github.com/esops-dev/esops-go/pkg/types"
)

func listCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List indices with size, docs and health",
		Flags: append([]cli.Flag{
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
			&cli.BoolFlag{
				Name:  "exclude-hidden",
				Usage: "Explicitly exclude hidden indices (the default; spell out the intent for scripts and audit grep)",
			},
			&cli.BoolFlag{
				Name:  "exclude-managed",
				Usage: "Hide data-stream backing indices (`.ds-*`); useful for operators triaging standalone indices",
			},
			&cli.StringFlag{
				Name:  "sort",
				Usage: "Sort by: name | size | docs",
				Value: "name",
			},
		}, watch.Flags()...),
		Action: runList,
		ShellComplete: completion.ForFlags(map[string]completion.Suggester{
			"index": completion.SuggestIndices,
		}),
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
	hidden, err := cmdsetup.ResolveHiddenFlags(cmd.Bool("hidden"), cmd.Bool("exclude-hidden"))
	if err != nil {
		return err
	}

	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	filter := types.IndexFilter{
		Index:          cmd.String("index"),
		Health:         cmd.String("health"),
		Status:         cmd.String("status"),
		Hidden:         hidden,
		ExcludeManaged: cmd.Bool("exclude-managed"),
		Sort:           sortKey,
	}

	return watch.Run(ctx, cmd, watch.Options{Label: "esops index list", ContextName: s.ContextName, Format: s.Format}, func(ctx context.Context) error {
		// NDJSON takes the streaming path: rows reach stdout as the
		// cluster sends them, so a 20k-index cluster doesn't have to
		// be buffered into memory before the first row prints. Other
		// formats (table, json, yaml, wide) need the full sorted set,
		// so they go through List which buffers, sorts, and renders.
		if s.Format == output.FormatNDJSON {
			count := 0
			err := output.IndicesNDJSONStream(os.Stdout, func(yield func(types.Index) error) error {
				return esopsindex.ListStream(ctx, s.Client.Indices, filter, func(ix types.Index) error {
					count++
					return yield(ix)
				})
			})
			if err != nil {
				return err
			}
			s.Log("index.list", "indices", count, "stream", true)
			return nil
		}

		indices, err := esopsindex.List(ctx, s.Client.Indices, filter)
		if err != nil {
			return err
		}
		s.Log("index.list", "indices", len(indices))
		return output.Indices(os.Stdout, indices, s.Format)
	})
}
