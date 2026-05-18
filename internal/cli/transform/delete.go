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

func deleteCommand() *cli.Command {
	return &cli.Command{
		Name:  "delete",
		Usage: "Delete one or more transforms",
		Description: `Delete a transform by id or comma-separated pattern. The cluster
refuses to delete a running transform; pass --force-stop to stop it
first as part of the same call.

Resolved matches are capped at 10 transforms by default — pass
--max-transforms N for larger sweeps. Use --dry-run first; against a
prod-protected context, --i-know-this-is-prod is required.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Transform id(s) or pattern(s) (comma-separated)",
			},
			&cli.IntFlag{
				Name:  "max-transforms",
				Usage: "Override the default 10-transform blast-radius cap on the resolved pattern",
			},
			&cli.BoolFlag{
				Name:  "force-stop",
				Usage: "Stop a running transform before deleting it",
			},
		},
		Action:        runDelete,
		ShellComplete: completion.ForFlags(nameSuggesters),
	}
}

func runDelete(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	rows, err := esopstransform.Delete(ctx, s.Client.Transform, s.Client.TransformManage, esopstransform.DeleteOptions{
		Pattern:       cmd.String("name"),
		MaxTransforms: cmd.Int("max-transforms"),
		ForceStop:     cmd.Bool("force-stop"),
		DryRun:        cmd.Bool("dry-run"),
	})
	if err != nil {
		return err
	}

	s.Log("transform.delete",
		"deleted", len(rows),
		"force_stop", cmd.Bool("force-stop"),
		"dry_run", cmd.Bool("dry-run"),
	)
	return output.TransformDeletes(os.Stdout, rows, s.Format)
}
