package cli

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/dj-wasabi/esops-go/internal/config"
	"github.com/dj-wasabi/esops-go/internal/version"
)

func Run(ctx context.Context, args []string) error {
	app := &cli.Command{
		Name:    "esops",
		Usage:   "Operations CLI for self-managed Elasticsearch and OpenSearch clusters",
		Version: version.Version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Aliases: []string{"c"},
				Usage:   "Path to YAML config file",
				Sources: cli.EnvVars("ESOPS_CONFIG"),
				Value:   "config/esops.example.yaml",
			},
		},
		Commands: []*cli.Command{
			{
				Name:  "hello",
				Usage: "Print the greeting loaded from the config file",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					cfg, err := config.Load(cmd.String("config"))
					if err != nil {
						return err
					}
					fmt.Println(cfg.Greeting)
					return nil
				},
			},
		},
	}
	return app.Run(ctx, args)
}
