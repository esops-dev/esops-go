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

func startCommand() *cli.Command {
	return &cli.Command{
		Name:  "start",
		Usage: "Start a stopped transform",
		Description: `Start a stopped transform. Both products are idempotent — starting
an already-running transform returns acknowledged=true with no
side-effect.

Mutates cluster state, but does not destroy existing destination
data — start is strictly additive (resumes materialisation). Per
the protection-tier rules, only delete/stop/overwriting-apply
require --i-know-this-is-prod against a 'protection: prod' context;
start proceeds without the flag.

--dry-run reports the planned start without contacting the cluster.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Transform id (literal)",
			},
		},
		Action:        runStart,
		ShellComplete: completion.ForFlags(nameSuggesters),
	}
}

func runStart(ctx context.Context, cmd *cli.Command) error {
	// Destructive=false: start mutates state but does not destroy
	// destination data. The story's §8 protection list (delete, stop,
	// overwriting apply) does not include start, so we don't gate it
	// behind --i-know-this-is-prod.
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	result, err := esopstransform.Start(ctx, s.Client.TransformManage, esopstransform.StartOptions{
		Name:   cmd.String("name"),
		DryRun: cmd.Bool("dry-run"),
	})
	if err != nil {
		return err
	}

	s.Log("transform.start",
		"name", result.Name,
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.TransformStart(os.Stdout, result, s.Format)
}
