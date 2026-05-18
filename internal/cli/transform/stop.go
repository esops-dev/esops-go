package transform

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/logging"
	"github.com/esops-dev/esops-go/internal/output"
	esopstransform "github.com/esops-dev/esops-go/internal/transform"
	"github.com/esops-dev/esops-go/pkg/types"
)

func stopCommand() *cli.Command {
	return &cli.Command{
		Name:  "stop",
		Usage: "Stop a running transform",
		Description: `Stop a running transform.

CONTINUOUS TRANSFORMS: stopping a continuous transform halts the
ongoing materialisation — the destination index stops receiving
new data until you start it again. Pass --wait-for-checkpoint to
let the in-flight checkpoint complete first so the destination
isn't left mid-batch.

NOTE: --wait-for-checkpoint is honoured natively by Elasticsearch
(?wait_for_checkpoint=true). OpenSearch's _stop endpoint does not
yet expose the knob, so the flag is recorded for symmetry but the
cluster halts at the next safe page boundary regardless. The CLI
logs a warning when an operator passes the flag against an OS
context so the missing guarantee is visible, not hidden.

Mutates cluster state. Against a prod-protected context,
--i-know-this-is-prod is required. --dry-run reports the planned
stop without contacting the cluster.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Transform id (literal)",
			},
			&cli.BoolFlag{
				Name:  "wait-for-checkpoint",
				Usage: "Let the in-flight checkpoint complete before halting (Elasticsearch only; OS halts at the next page boundary)",
			},
		},
		Action:        runStop,
		ShellComplete: completion.ForFlags(nameSuggesters),
	}
}

func runStop(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	waitForCheckpoint := cmd.Bool("wait-for-checkpoint")
	if waitForCheckpoint && s.Client.Info.Dialect == types.DialectOpenSearch {
		logging.Logger().Warn("transform.stop.wait_for_checkpoint_unsupported",
			"dialect", string(s.Client.Info.Dialect),
			"hint", "OpenSearch's _stop endpoint does not expose the knob; the transform will halt at the next page boundary",
		)
	}

	result, err := esopstransform.Stop(ctx, s.Client.TransformManage, esopstransform.StopOptions{
		Name:              cmd.String("name"),
		WaitForCheckpoint: waitForCheckpoint,
		DryRun:            cmd.Bool("dry-run"),
	})
	if err != nil {
		return err
	}

	s.Log("transform.stop",
		"name", result.Name,
		"wait_for_checkpoint", result.WaitForCheckpoint,
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.TransformStop(os.Stdout, result, s.Format)
}
