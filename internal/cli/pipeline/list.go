package pipeline

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/output"
	esopspipeline "github.com/esops-dev/esops-go/internal/pipeline"
)

func listCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List ingest pipelines",
		Description: `List the cluster's ingest pipelines. The default table shows
name, processor count and (truncated) description; -o wide adds the
version, on_failure count and the processor-type chain so an operator
can spot e.g. an unexpected 'set' or 'pipeline' processor without
dropping into --output json.

Backs onto GET /_ingest/pipeline on both Elasticsearch and OpenSearch
(same path, same envelope). Read-only and safe to run against a
prod-protected context at any time.

--name takes one or more pipeline ids to narrow the response. Both
products honour a comma-joined id list at the path segment, so multi-
name fans out in a single round-trip.`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{
				Name:  "name",
				Usage: "Limit to specific pipeline name(s); repeatable",
			},
		},
		Action: runList,
		ShellComplete: completion.ForFlags(map[string]completion.Suggester{
			"name": completion.SuggestPipelines,
		}),
	}
}

func runList(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	rows, err := esopspipeline.List(ctx, s.Client.IngestPipeline, esopspipeline.ListOptions{
		Names: cmd.StringSlice("name"),
	})
	if err != nil {
		return err
	}

	s.Log("pipeline.list", "rows", len(rows))
	return output.IngestPipelines(os.Stdout, rows, s.Format)
}
