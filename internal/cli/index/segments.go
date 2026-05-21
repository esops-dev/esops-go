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
)

func segmentsCommand() *cli.Command {
	return &cli.Command{
		Name:  "segments",
		Usage: "Per-index segment counts and deleted-doc ratio",
		Description: `Read per-shard segments via GET /<indices>/_segments and aggregate
to the index level: primary segment count, deleted-doc ratio, on-disk
size, and the highest per-shard segment count (the number that matters
for force-merge planning, since --max-num-segments is interpreted per
shard).

Read-only and safe to run against a prod-protected context at any time.

--min-segments N drops indices with fewer than N primary segments —
the operator-friendly way to skip already-merged indices when triaging
fragmentation. --sort defaults to name; "count" surfaces the most
fragmented indices first, "deleted" surfaces the indices where
--only-expunge-deletes would reclaim the most space.`,
		Flags: append([]cli.Flag{
			&cli.StringFlag{
				Name:  "index",
				Usage: "Comma-separated index names or patterns (default: all indices)",
			},
			&cli.IntFlag{
				Name:  "min-segments",
				Usage: "Hide indices with fewer than N primary segments",
			},
			&cli.BoolFlag{
				Name:  "hidden",
				Usage: "Include hidden indices (dot-prefixed system indices)",
			},
			&cli.BoolFlag{
				Name:  "exclude-hidden",
				Usage: "Explicitly exclude hidden indices (the default; spell out the intent for scripts and audit grep)",
			},
			&cli.StringFlag{
				Name:  "sort",
				Usage: "Sort by: name | count | deleted | bytes",
				Value: "name",
			},
		}, watch.Flags()...),
		Action: runSegments,
		ShellComplete: completion.ForFlags(map[string]completion.Suggester{
			"index": completion.SuggestIndices,
		}),
	}
}

func runSegments(ctx context.Context, cmd *cli.Command) error {
	sortKey, err := esopsindex.ParseSegmentSort(cmd.String("sort"))
	if err != nil {
		return err
	}
	hidden, err := cmdsetup.ResolveHiddenFlags(cmd.Bool("hidden"), cmd.Bool("exclude-hidden"))
	if err != nil {
		return err
	}

	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopsindex.SegmentsOptions{
		Pattern:     cmd.String("index"),
		MinSegments: cmd.Int("min-segments"),
		Hidden:      hidden,
		Sort:        sortKey,
	}
	return watch.Run(ctx, cmd, watch.Options{Label: "esops index segments", ContextName: s.ContextName, Format: s.Format}, func(ctx context.Context) error {
		report, err := esopsindex.Segments(ctx, s.Client.Segments, opts)
		if err != nil {
			return err
		}
		s.Log("index.segments", "indices", len(report.Indices))
		return output.Segments(os.Stdout, report, s.Format)
	})
}
