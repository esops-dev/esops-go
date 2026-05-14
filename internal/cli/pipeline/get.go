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

func getCommand() *cli.Command {
	return &cli.Command{
		Name:  "get",
		Usage: "Get a single ingest pipeline",
		Description: `Get one ingest pipeline by id. --name must be a literal pipeline
id (no patterns); for multi-name retrieval, use 'pipeline list --name
a,b'.

In table mode the default view is a one-line header plus the
processor chain as indented JSON. -o wide additionally appends the
_meta block when the cluster reports one. JSON/YAML emit the neutral
IngestPipeline shape directly.

Read-only. Backs onto GET /_ingest/pipeline/<name> on both
Elasticsearch and OpenSearch.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Pipeline id (literal)",
			},
		},
		Action: runGet,
		ShellComplete: completion.ForFlags(map[string]completion.Suggester{
			"name": completion.SuggestPipelines,
		}),
	}
}

func runGet(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	p, err := esopspipeline.Get(ctx, s.Client.IngestPipeline, cmd.String("name"))
	if err != nil {
		return err
	}

	s.Log("pipeline.get", "name", p.Name, "processors", len(p.Processors))
	return output.IngestPipeline(os.Stdout, p, s.Format)
}
