package ops

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/dj-wasabi/esops-go/internal/cli/cmdsetup"
	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/logging"
	esopsops "github.com/dj-wasabi/esops-go/internal/ops"
	"github.com/dj-wasabi/esops-go/internal/output"
	"github.com/dj-wasabi/esops-go/internal/types"
)

func rebalanceCommand() *cli.Command {
	return &cli.Command{
		Name:  "rebalance",
		Usage: "Move, cancel, or retry shard allocations via /_cluster/reroute",
		Description: `Submit one shard-routing command to the cluster: move a started shard
between nodes, cancel an in-flight relocation, allocate a specific
replica copy on a chosen node, force-allocate an empty/stale primary
(data-loss territory — explicit opt-in required), or retry shards
whose allocation hit max_retries.

Mutates cluster state. Use --dry-run first; against a prod-protected
context, --i-know-this-is-prod is required.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "action",
				Required: true,
				Usage:    "One of: move, cancel, allocate-replica, allocate-empty-primary, allocate-stale-primary, retry-failed",
			},
			&cli.StringFlag{
				Name:  "index",
				Usage: "Index name (required for all actions except retry-failed)",
			},
			&cli.IntFlag{
				Name:  "shard",
				Usage: "Shard number (required for all actions except retry-failed; defaults to 0)",
			},
			&cli.StringFlag{
				Name:  "from-node",
				Usage: "Source node name (move: where the shard is now; cancel: which copy to cancel)",
			},
			&cli.StringFlag{
				Name:  "to-node",
				Usage: "Destination node name (move / allocate-* actions)",
			},
			&cli.BoolFlag{
				Name:  "allow-primary",
				Usage: "Cancel a primary's recovery (cancel action only; off by default)",
			},
			&cli.BoolFlag{
				Name:  "accept-data-loss",
				Usage: "Required for allocate-empty-primary / allocate-stale-primary — these can lose data",
			},
			&cli.BoolFlag{
				Name:  "explain",
				Usage: "Ask the cluster to explain its per-decider ruling for the submitted command(s)",
			},
		},
		Action: runRebalance,
	}
}

func runRebalance(ctx context.Context, cmd *cli.Command) error {
	action, err := esopsops.ParseRerouteAction(cmd.String("action"))
	if err != nil {
		return err
	}

	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	req := types.RerouteRequest{
		DryRun:  cmd.Bool("dry-run"),
		Explain: cmd.Bool("explain"),
	}

	if action == types.RerouteRetryFailed {
		if err := rejectInapplicableFlags(cmd); err != nil {
			return err
		}
		req.RetryFailed = true
	} else {
		req.Commands = []types.RerouteCommand{{
			Action:         action,
			Index:          cmd.String("index"),
			Shard:          cmd.Int("shard"),
			FromNode:       cmd.String("from-node"),
			ToNode:         cmd.String("to-node"),
			AllowPrimary:   cmd.Bool("allow-primary"),
			AcceptDataLoss: cmd.Bool("accept-data-loss"),
		}}
	}

	result, opErr := esopsops.Rebalance(ctx, s.Client.Reroute, req)
	// A dry-run blocker (acknowledged=false) carries a useful result —
	// the explanations[] block names the deciders. Render it before
	// returning the sentinel so the operator sees the "why" alongside
	// the non-zero exit. Validation/transport errors leave the result
	// zero-valued; skip rendering in that case.
	ackBlocked := errors.Is(opErr, client.ErrDryRunBlocker)
	if opErr != nil && !ackBlocked {
		return opErr
	}

	logging.Logger().Info("ops.rebalance",
		"context", s.ContextName,
		"dialect", string(s.Client.Info.Dialect),
		"version", s.Client.Info.Version,
		"action", string(action),
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
		"commands", len(result.Commands),
	)
	if renderErr := output.Rebalance(os.Stdout, result, s.Format); renderErr != nil {
		return renderErr
	}
	return opErr
}

// rejectInapplicableFlags fails fast when --action retry-failed is paired
// with shard-targeting flags. The cluster ignores them on retry_failed,
// but silent-ignore is a footgun — operators tend to assume the flags
// did something. Surfacing the conflict matches the rest of esops's
// "loud-on-conflict" idiom (cf. shards.go's --primary/--replica check).
func rejectInapplicableFlags(cmd *cli.Command) error {
	for _, name := range []string{"index", "from-node", "to-node"} {
		if cmd.IsSet(name) {
			return fmt.Errorf("--%s is not applicable to --action retry-failed", name)
		}
	}
	if cmd.IsSet("shard") {
		return fmt.Errorf("--shard is not applicable to --action retry-failed")
	}
	if cmd.Bool("allow-primary") || cmd.Bool("accept-data-loss") {
		return fmt.Errorf("--allow-primary / --accept-data-loss are not applicable to --action retry-failed")
	}
	return nil
}
