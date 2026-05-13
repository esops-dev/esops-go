package datastream

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	esopsdatastream "github.com/esops-dev/esops-go/internal/datastream"
	"github.com/esops-dev/esops-go/internal/output"
)

func listCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List data streams with generation, backing-index count, and lifecycle binding",
		Description: `List the cluster's data streams. The default table shows name,
status, generation, backing-index count, oldest and newest backing
index, and the bound lifecycle (ILM policy on Elasticsearch, ISM
template on OpenSearch). -o wide adds the bound index template and
the hidden/system flags.

Backs onto GET /_data_stream[/<names>] on both products (same path,
same envelope). Read-only and safe to run against a prod-protected
context at any time.

--name takes one or more data stream names or patterns; both products
honour patterns at the path segment. The bare endpoint already
includes hidden streams on both dialects, so there is no separate
flag for them — pass --name '.*' if you want to scan them specifically.`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{
				Name:  "name",
				Usage: "Limit to specific data stream name(s) or pattern(s); repeatable",
			},
		},
		Action: runList,
	}
}

func runList(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	rows, err := esopsdatastream.List(ctx, s.Client.DataStream, s.Client.Indices, esopsdatastream.ListOptions{
		Names: cmd.StringSlice("name"),
	})
	if err != nil {
		return err
	}

	s.Log("datastream.list", "streams", len(rows))
	return output.DataStreams(os.Stdout, rows, s.Format)
}
