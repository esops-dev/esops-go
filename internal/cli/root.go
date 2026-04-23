// Package cli assembles the urfave/cli command tree and shared
// helpers. Command implementations live in subpackages; this file is
// the registration surface.
package cli

import (
	"context"
	"time"

	"github.com/urfave/cli/v3"

	configcmd "github.com/dj-wasabi/esops-go/internal/cli/config"
	"github.com/dj-wasabi/esops-go/internal/cli/ops"
	"github.com/dj-wasabi/esops-go/internal/version"
)

// Run builds and executes the root command. Errors bubble up to main,
// which maps them to stable exit codes.
func Run(ctx context.Context, args []string) error {
	app := &cli.Command{
		Name:    "esops",
		Usage:   "Operations CLI for self-managed Elasticsearch and OpenSearch clusters",
		Version: version.Version,
		Flags:   globalFlags(),
		Commands: []*cli.Command{
			ops.Command(),
			configcmd.Command(),
		},
	}
	return app.Run(ctx, args)
}

func globalFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "config",
			Aliases: []string{"c"},
			Usage:   "Path to YAML config file",
			Sources: cli.EnvVars("ESOPS_CONFIG"),
			Value:   "config/esops.example.yaml",
		},
		&cli.StringFlag{
			Name:    "context",
			Aliases: []string{"cluster"},
			Usage:   "Named cluster from config (overrides current-context)",
		},
		&cli.StringFlag{
			Name:  "url",
			Usage: "Override: single cluster URL (bypasses context lookup)",
		},
		&cli.DurationFlag{
			Name:  "timeout",
			Usage: "HTTP timeout",
			Value: 30 * time.Second,
		},
		&cli.BoolFlag{
			Name:  "insecure",
			Usage: "Skip TLS verification (last resort)",
		},
		&cli.StringFlag{
			Name:  "cacert",
			Usage: "Custom CA bundle path",
		},
		&cli.StringFlag{
			Name:    "output",
			Aliases: []string{"o"},
			Usage:   "Output format: table | json | yaml | wide",
			Value:   "table",
		},
		&cli.StringFlag{
			Name:  "log-level",
			Usage: "Log level: debug | info | warn | error",
			Value: "info",
		},
		&cli.StringFlag{
			Name:  "log-format",
			Usage: "Log format: text | json",
			Value: "text",
		},
	}
}
