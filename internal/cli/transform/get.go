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

func getCommand() *cli.Command {
	return &cli.Command{
		Name:  "get",
		Usage: "Get a single transform definition",
		Description: `Get one transform by id. --name must be a literal transform id (no
patterns).

In table mode the default view is a one-line header followed by an
indented JSON dump of the cluster's raw definition body. JSON/YAML
emit the neutral Transform shape directly.

Read-only. Backs onto GET /_transform/<id> (Elasticsearch) and
GET /_plugins/_transform/<id> (OpenSearch).`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Transform id (literal)",
			},
		},
		Action:        runGet,
		ShellComplete: completion.ForFlags(nameSuggesters),
	}
}

func runGet(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	t, err := esopstransform.Get(ctx, s.Client.Transform, cmd.String("name"))
	if err != nil {
		return err
	}

	s.Log("transform.get", "name", t.Name, "continuous", t.Continuous)
	return output.Transform(os.Stdout, t, s.Format)
}
