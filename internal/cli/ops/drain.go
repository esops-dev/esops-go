package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	esopsops "github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

func drainCommand() *cli.Command {
	return &cli.Command{
		Name:  "drain",
		Usage: "Exclude one or more nodes from shard allocation; wait for shards to relocate",
		Description: `Add nodes to cluster.routing.allocation.exclude._name. The cluster
refuses to assign new shards to the named nodes and relocates the
existing ones away. The drain itself is the cluster setting; this
command is the safe path to flipping it (validates the nodes exist,
shows before/after, optionally waits for shard counts to hit zero).

Multi-node drains write a single PUT — adding three nodes is one
request, not three. The wait loop counts shards across the entire set
and reports a per-node breakdown.

--also-exclude-voting additionally adds the named nodes to the
cluster's voting_config_exclusions list — required for safely retiring
master-eligible nodes. Non-master nodes are accepted as a no-op.

--list switches to read-only mode: print the current
allocation-exclude lists (transient + persistent) and the voting
exclusions, then exit. Mutually exclusive with --node.

Mutates cluster state. Use --dry-run first; against a prod-protected
context, --i-know-this-is-prod is required. Default scope is
transient (clears on master restart — safer for a temporary drain).
--persistent flips it to persistent, which survives restart and stays
in place until ops uncordon removes it.`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{
				Name:  "node",
				Usage: "Node name(s) to drain. Repeatable, or comma-separated. Required unless --list",
			},
			&cli.BoolFlag{
				Name:  "persistent",
				Usage: "Apply via persistent scope (survives master restart) instead of transient",
			},
			&cli.BoolFlag{
				Name:  "wait",
				Usage: "Block until the named nodes hold zero shards (or --wait-timeout fires)",
			},
			&cli.DurationFlag{
				Name:  "wait-timeout",
				Usage: "Maximum time to wait when --wait is set (e.g. 30m). 0 = no timeout",
				Value: 30 * time.Minute,
			},
			&cli.DurationFlag{
				Name:  "wait-interval",
				Usage: "Poll interval while waiting (default 5s — matches cluster allocator pace)",
				Value: 5 * time.Second,
			},
			&cli.BoolFlag{
				Name:  "also-exclude-voting",
				Usage: "Also add the nodes to /_cluster/voting_config_exclusions (safe master retirement)",
			},
			&cli.BoolFlag{
				Name:  "list",
				Usage: "Read-only: print current drained nodes and voting exclusions, then exit",
			},
		},
		Action:        runDrain,
		ShellComplete: completion.ForFlags(nodeFlagSuggesters),
	}
}

func runDrain(ctx context.Context, cmd *cli.Command) error {
	if cmd.Bool("list") {
		return runDrainList(ctx, cmd)
	}
	if len(cmd.StringSlice("node")) == 0 {
		return fmt.Errorf("--node is required (or pass --list to view current state)")
	}

	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	req := types.NodeDrainRequest{
		NodeNames:         cmd.StringSlice("node"),
		Persistent:        cmd.Bool("persistent"),
		DryRun:            cmd.Bool("dry-run"),
		Wait:              cmd.Bool("wait"),
		WaitTimeout:       cmd.Duration("wait-timeout"),
		WaitInterval:      cmd.Duration("wait-interval"),
		AlsoExcludeVoting: cmd.Bool("also-exclude-voting"),
	}

	deps := esopsops.DrainDeps{
		Nodes:    s.Client.Nodes,
		Shards:   s.Client.Shards,
		Settings: s.Client.ClusterSettings,
		Voting:   s.Client.VotingExclusion,
	}
	result, opErr := esopsops.DrainNode(ctx, deps, req)

	// Render the outcome when DrainNode reached the cluster (the
	// result carries a non-empty NodeNames slice) — the operator still
	// wants to see the before/after exclusion lists and the
	// shard-count snapshot before any non-zero exit fires. Validation
	// errors leave the result zero-valued; in that case skip the
	// empty render and let main print the message.
	rendered := len(result.NodeNames) > 0
	if rendered || errors.Is(opErr, client.ErrDryRunBlocker) {
		s.Log("ops.drain",
			"nodes", result.NodeNames,
			"persistent", result.Persistent,
			"dry_run", result.DryRun,
			"already_excluded", result.AlreadyExcluded,
			"acknowledged", result.Acknowledged,
			"voting_excluded", result.VotingExclusionsAdded,
			"waited", result.Waited,
			"shards_remaining", result.ShardsRemaining,
		)
		if renderErr := output.NodeDrain(os.Stdout, result, s.Format); renderErr != nil {
			return renderErr
		}
	}
	return opErr
}

// runDrainList is the read-only branch of `ops drain`. Marked
// non-destructive so it skips the protection-tier check — listing the
// drained nodes is a safe operation on any cluster regardless of tier.
func runDrainList(ctx context.Context, cmd *cli.Command) error {
	if len(cmd.StringSlice("node")) > 0 {
		return fmt.Errorf("--list and --node are mutually exclusive")
	}
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}
	result, err := esopsops.ListDrained(ctx, s.Client.ClusterSettings, s.Client.VotingExclusion)
	if err != nil {
		return err
	}
	s.Log("ops.drain.list",
		"persistent", len(result.PersistentExcluded),
		"transient", len(result.TransientExcluded),
		"voting", len(result.VotingExcluded),
	)
	return output.NodeDrainList(os.Stdout, result, s.Format)
}
