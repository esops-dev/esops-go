package datastream

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	esopsdatastream "github.com/esops-dev/esops-go/internal/datastream"
	"github.com/esops-dev/esops-go/internal/output"
)

func statsCommand() *cli.Command {
	return &cli.Command{
		Name:  "stats",
		Usage: "Aggregate size, backing-index count, and max-timestamp per data stream",
		Description: `Read aggregate data-stream stats via
GET /_data_stream[/<names>]/_stats — the fast path for "how big are
my streams" that returns size and maximum-timestamp numbers directly
without joining /_cat/indices.

Backs onto the same endpoint on both Elasticsearch and OpenSearch.
Read-only and safe to run against a prod-protected context at any
time.

--name takes one or more data stream names or patterns; both products
honour patterns at the path segment. Default (no --name) returns
stats for every configured stream.

Hidden data streams (Fleet, APM, and other integration-managed
streams) are excluded from the default view to match 'datastream
list'. Pass --hidden to include them, or --exclude-hidden to spell
out the default for scripted invocations. The /_stats endpoint does
not expose the per-stream hidden flag in its response, so default
--exclude-hidden mode costs one extra round-trip to /_data_stream
for the filter.

Wide output adds the maximum-timestamp column (RFC3339 UTC) so an
operator scanning a fleet spots a stalled stream — one whose newest
backing index stopped receiving writes — without paging into per-id
queries.`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{
				Name:  "name",
				Usage: "Limit to specific data stream name(s) or pattern(s); repeatable",
			},
			&cli.BoolFlag{
				Name:  "hidden",
				Usage: "Include hidden data streams (Fleet/APM/integration-managed)",
			},
			&cli.BoolFlag{
				Name:  "exclude-hidden",
				Usage: "Explicitly exclude hidden data streams (the default; spell out the intent for scripts and audit grep)",
			},
		},
		Action:        runStats,
		ShellComplete: completion.ForFlags(dsNameSuggesters),
	}
}

func runStats(ctx context.Context, cmd *cli.Command) error {
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

	rep, err := esopsdatastream.Stats(ctx, s.Client.DataStream, esopsdatastream.StatsOptions{
		Names:  cmd.StringSlice("name"),
		Hidden: hidden,
	})
	if err != nil {
		return err
	}

	s.Log("datastream.stats",
		"streams", rep.DataStreamCount,
		"backing_indices", rep.BackingIndicesCount,
		"total_store_bytes", rep.TotalStoreBytes,
	)
	return output.DataStreamStats(os.Stdout, rep, s.Format)
}
