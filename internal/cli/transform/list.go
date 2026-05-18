package transform

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/output"
	esopstransform "github.com/esops-dev/esops-go/internal/transform"
)

func listCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List transforms",
		Description: `List the cluster's transforms. The default table shows id,
continuous flag, source pattern, dest index and (truncated)
description. -o wide adds created-at, version and the full
description.

Backs onto GET /_transform (Elasticsearch) and
GET /_plugins/_transform (OpenSearch). Read-only and safe to run
against a prod-protected context at any time.

--name takes one or more transform ids to narrow the response.`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{
				Name:  "name",
				Usage: "Limit to specific transform id(s); repeatable",
			},
		},
		Action:        runList,
		ShellComplete: completion.ForFlags(nameSuggesters),
	}
}

func runList(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	rows, err := esopstransform.List(ctx, s.Client.Transform, esopstransform.ListOptions{
		Names: cmd.StringSlice("name"),
	})
	if err != nil {
		return err
	}

	s.Log("transform.list", "rows", len(rows))
	return output.Transforms(os.Stdout, rows, s.Format)
}
