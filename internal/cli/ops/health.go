package ops

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/dj-wasabi/esops-go/internal/cluster"
	"github.com/dj-wasabi/esops-go/internal/config"
	esopsops "github.com/dj-wasabi/esops-go/internal/ops"
	"github.com/dj-wasabi/esops-go/internal/output"
)

func healthCommand() *cli.Command {
	return &cli.Command{
		Name:   "health",
		Usage:  "Show cluster health",
		Action: runHealth,
	}
}

func runHealth(ctx context.Context, cmd *cli.Command) error {
	cfg, err := config.Load(cmd.String("config"))
	if err != nil {
		return err
	}
	name, cc, err := cfg.ResolveContext(cmd.String("context"))
	if err != nil {
		return err
	}
	applyOverrides(&cc, cmd)

	format, err := output.Parse(cmd.String("output"))
	if err != nil {
		return err
	}

	c, err := cluster.New(ctx, cc)
	if err != nil {
		return err
	}

	h, err := esopsops.Health(ctx, c)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "context=%s dialect=%s version=%s\n", name, c.Info.Dialect, c.Info.Version)
	return output.Health(os.Stdout, h, c.Info, format)
}

// applyOverrides layers command-line flags on top of the resolved
// context. URL/timeout/TLS flags override the config; everything else
// is left to the context.
func applyOverrides(cc *config.Context, cmd *cli.Command) {
	if u := cmd.String("url"); u != "" {
		cc.URL = u
		cc.URLs = nil
	}
	if t := cmd.Duration("timeout"); cmd.IsSet("timeout") || cc.Timeout == 0 {
		cc.Timeout = t
	}
	if cmd.Bool("insecure") {
		cc.TLS.Insecure = true
	}
	if ca := cmd.String("cacert"); ca != "" {
		cc.TLS.CACert = ca
	}
}
