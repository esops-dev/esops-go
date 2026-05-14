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

func getCommand() *cli.Command {
	return &cli.Command{
		Name:  "get",
		Usage: "Get a single data stream",
		Description: `Get one data stream by literal name. --name must be a literal
data stream name (no patterns); for multi-name retrieval, use
'datastream list --name a,b'.

In table mode the default view is a one-line header plus the backing
indices in generation order; -o wide adds the per-index uuid and
managed-by columns when the cluster reports them. JSON/YAML emit the
neutral DataStream shape directly.

Read-only. Backs onto GET /_data_stream/<name> on both Elasticsearch
and OpenSearch.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Data stream name (literal)",
			},
		},
		Action:        runGet,
		ShellComplete: completion.ForFlags(dsNameSuggesters),
	}
}

func runGet(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	d, err := esopsdatastream.Get(ctx, s.Client.DataStream, s.Client.Indices, cmd.String("name"))
	if err != nil {
		return err
	}

	s.Log("datastream.get", "name", d.Name, "indices", d.IndexCount)
	return output.DataStream(os.Stdout, d, s.Format)
}
