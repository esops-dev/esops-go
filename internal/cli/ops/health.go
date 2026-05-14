package ops

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/watch"
	esopsops "github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/internal/output"
)

func healthCommand() *cli.Command {
	return &cli.Command{
		Name:   "health",
		Usage:  "Show cluster health",
		Flags:  watch.Flags(),
		Action: runHealth,
	}
}

func runHealth(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	return watch.Run(ctx, cmd, "esops ops health", s.Format, func(ctx context.Context) error {
		h, err := esopsops.Health(ctx, s.Client.Health)
		if err != nil {
			return err
		}
		s.Log("ops.health", "status", h.Status)
		return output.Health(os.Stdout, h, s.Client.Info, s.Format)
	})
}
