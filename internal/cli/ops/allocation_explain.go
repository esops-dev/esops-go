package ops

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/logging"
	esopsops "github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/types"
)

func allocationExplainCommand() *cli.Command {
	return &cli.Command{
		Name:  "allocation-explain",
		Usage: "Explain why a shard is (un)assigned — wraps /_cluster/allocation/explain",
		Description: `Ask the cluster to explain the allocation of a specific shard, or — with
no flags — to pick the first unassigned shard it can find. This is the
standard first stop when a cluster goes yellow/red and you want the
per-decider breakdown ("disk watermark", "same_shard", "awaiting_info", …).`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "index",
				Usage: "Index name to explain (omit to let the cluster pick any unassigned shard)",
			},
			&cli.IntFlag{
				Name:  "shard",
				Usage: "Shard number (requires --index)",
			},
			&cli.BoolFlag{
				Name:  "primary",
				Usage: "Explain the primary copy (default: replica). Requires --index.",
			},
			&cli.BoolFlag{
				Name:  "include-disk-info",
				Usage: "Include per-node disk info (free space, watermarks) in the response",
			},
			&cli.BoolFlag{
				Name:  "include-yes-decisions",
				Usage: "Include YES decider rulings too (noisier; useful for 'why did it pick X')",
			},
		},
		Action:        runAllocationExplain,
		ShellComplete: completion.ForFlags(indexFlagSuggesters),
	}
}

func runAllocationExplain(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	req := types.AllocationExplainRequest{
		Index:               cmd.String("index"),
		Shard:               cmd.Int("shard"),
		Primary:             cmd.Bool("primary"),
		IncludeDiskInfo:     cmd.Bool("include-disk-info"),
		IncludeYesDecisions: cmd.Bool("include-yes-decisions"),
	}

	ex, err := esopsops.AllocationExplain(ctx, s.Client.AllocationExplain, req)
	if err != nil {
		return err
	}

	logging.Logger().Info("ops.allocation-explain",
		"context", s.ContextName,
		"dialect", string(s.Client.Info.Dialect),
		"version", s.Client.Info.Version,
		"index", ex.Index,
		"shard", ex.Shard,
		"state", ex.CurrentState,
	)
	return output.AllocationExplain(os.Stdout, ex, s.Format)
}
