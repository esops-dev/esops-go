package datastream

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/cli/watch"
	esopsdatastream "github.com/esops-dev/esops-go/internal/datastream"
	"github.com/esops-dev/esops-go/internal/output"
)

func segmentsCommand() *cli.Command {
	return &cli.Command{
		Name:  "segments",
		Usage: "Per-backing-index segment counts and deleted-doc ratio for matched data streams",
		Description: `Read per-shard segments for every backing index of the matched
data streams via GET /<backing-indices>/_segments and aggregate to
the index level: primary segment count, deleted-doc ratio, on-disk
size, and the highest per-shard segment count (the number that
matters for force-merge planning, since --max-num-segments is
interpreted per shard).

Resolves --name to the underlying backing indices and forwards a
single /_segments call with the comma-joined list, so the rendered
table mirrors 'index segments' row-for-row.

Read-only and safe to run against a prod-protected context at any
time.

--min-segments N drops backing indices with fewer than N primary
segments — the operator-friendly way to skip already-merged
generations when triaging fragmentation. --sort defaults to name;
"count" surfaces the most fragmented backings first, "deleted"
surfaces the backings where --only-expunge-deletes would reclaim the
most space.`,
		Flags: append([]cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Comma-separated data stream names or patterns (e.g. 'logs-*,metrics-app')",
			},
			&cli.IntFlag{
				Name:  "min-segments",
				Usage: "Hide backing indices with fewer than N primary segments",
			},
			&cli.StringFlag{
				Name:  "sort",
				Usage: "Sort by: name | count | deleted | bytes",
				Value: "name",
			},
		}, watch.Flags()...),
		Action:        runSegments,
		ShellComplete: completion.ForFlags(dsNameSuggesters),
	}
}

func runSegments(ctx context.Context, cmd *cli.Command) error {
	sortKey, err := esopsdatastream.ParseSegmentSort(cmd.String("sort"))
	if err != nil {
		return err
	}

	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopsdatastream.SegmentsOptions{
		Pattern:     cmd.String("name"),
		MinSegments: cmd.Int("min-segments"),
		Sort:        sortKey,
	}
	return watch.Run(ctx, cmd, "esops datastream segments", s.Format, func(ctx context.Context) error {
		report, err := esopsdatastream.Segments(ctx, s.Client.DataStream, s.Client.Indices, s.Client.Segments, opts)
		if err != nil {
			return err
		}
		s.Log("datastream.segments", "indices", len(report.Indices))
		return output.Segments(os.Stdout, report, s.Format)
	})
}
