package ops

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/dj-wasabi/esops-go/internal/cli/cmdsetup"
	esopsops "github.com/dj-wasabi/esops-go/internal/ops"
	"github.com/dj-wasabi/esops-go/internal/output"
	"github.com/dj-wasabi/esops-go/internal/types"
)

func uncordonCommand() *cli.Command {
	return &cli.Command{
		Name:  "uncordon",
		Usage: "Remove allocation exclusion for one or more drained nodes",
		Description: `Remove nodes from cluster.routing.allocation.exclude._name. The
inverse of ops drain. Idempotent at the per-node level: nodes not in
the exclusion list are recorded as such and exit cleanly without
touching cluster state.

The --persistent flag must match the scope the drain used. A node
drained transiently and uncordoned persistently produces a clear "not
in list" outcome rather than a silent no-op.

--clear-voting additionally fires DELETE
/_cluster/voting_config_exclusions, wiping the voting exclusions list.
The cluster API only supports clear-all (no per-node delete), so this
is a cluster-wide sweep — call it during the standard "bring nodes
back online" flow when the voting list is known stale.

Mutates cluster state. Use --dry-run first; against a prod-protected
context, --i-know-this-is-prod is required.`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{
				Name:  "node",
				Usage: "Node name(s) to uncordon. Repeatable, or comma-separated",
			},
			&cli.BoolFlag{
				Name:  "persistent",
				Usage: "Operate on the persistent scope (default: transient — must match the drain scope)",
			},
			&cli.BoolFlag{
				Name:  "clear-voting",
				Usage: "Also clear /_cluster/voting_config_exclusions (cluster-wide; the API only supports clear-all)",
			},
		},
		Action: runUncordon,
	}
}

func runUncordon(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	req := types.NodeUncordonRequest{
		NodeNames:   cmd.StringSlice("node"),
		Persistent:  cmd.Bool("persistent"),
		DryRun:      cmd.Bool("dry-run"),
		ClearVoting: cmd.Bool("clear-voting"),
	}

	deps := esopsops.UncordonDeps{
		Settings: s.Client.ClusterSettings,
		Voting:   s.Client.VotingExclusion,
	}
	result, opErr := esopsops.UncordonNode(ctx, deps, req)
	// Render whenever the result has any indication that we reached
	// the cluster — non-empty NodeNames OR a voting clear was
	// requested. Bare validation errors (missing both --node and
	// --clear-voting) leave the result zero-valued; main prints those
	// alone for a tight UX.
	if len(result.NodeNames) > 0 || result.VotingExclusionsCleared || result.VotingExclusionAcked {
		s.Log("ops.uncordon",
			"nodes", result.NodeNames,
			"persistent", result.Persistent,
			"dry_run", result.DryRun,
			"not_in_list", result.NotInList,
			"acknowledged", result.Acknowledged,
			"voting_cleared", result.VotingExclusionsCleared,
		)
		if renderErr := output.NodeUncordon(os.Stdout, result, s.Format); renderErr != nil {
			return renderErr
		}
	}
	return opErr
}
