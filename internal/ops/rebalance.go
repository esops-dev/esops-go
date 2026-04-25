package ops

import (
	"context"
	"fmt"
	"strings"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// Rebalance is the business-layer entry point for `ops rebalance`. It
// validates the request shape — the cluster does its own validation,
// but failing fast on shape errors gives operators a usable message
// before a 400 round-trips through the master — and forwards to the
// adapter.
//
// Validation rules:
//   - Action is required.
//   - "retry_failed" carries no command of its own; req.Commands must
//     be empty and req.RetryFailed must be true. The CLI builds this
//     shape; the rule guards against future callers.
//   - Every other action requires Index and a non-zero Shard hint
//     (shard 0 is valid; we accept any int including zero — the
//     cluster validates that the shard exists).
//   - allocate_empty/stale_primary require AcceptDataLoss=true. The
//     cluster also enforces this, but surfacing it client-side keeps
//     the safety opt-in obvious in shell history.
//
// Rebalance returns the cluster's response normalised into types.RerouteResult.
//
// When the cluster's response sets acknowledged=false (a dry-run that
// reported a blocker, or a live request the master rejected), Rebalance
// returns the result alongside ErrDryRunBlocker so callers can render
// the explanations block AND bubble exit code 7 up to main. Without
// this, automation around `ops rebalance --dry-run` would treat a
// refused move as a successful no-op.
func Rebalance(ctx context.Context, c client.RerouteExecutor, req types.RerouteRequest) (types.RerouteResult, error) {
	if err := validateRerouteRequest(req); err != nil {
		return types.RerouteResult{}, err
	}
	result, err := c.Reroute(ctx, req)
	if err != nil {
		return result, err
	}
	if !result.Acknowledged {
		return result, fmt.Errorf("%w: cluster did not acknowledge the reroute", client.ErrDryRunBlocker)
	}
	return result, nil
}

func validateRerouteRequest(req types.RerouteRequest) error {
	if req.RetryFailed {
		if len(req.Commands) > 0 {
			return fmt.Errorf("--retry-failed cannot be combined with another action")
		}
		return nil
	}
	if len(req.Commands) == 0 {
		return fmt.Errorf("at least one reroute command is required (use --action and --index/--shard, or --retry-failed)")
	}
	for i, cmd := range req.Commands {
		if err := validateRerouteCommand(cmd); err != nil {
			return fmt.Errorf("command %d: %w", i, err)
		}
	}
	return nil
}

func validateRerouteCommand(cmd types.RerouteCommand) error {
	if cmd.Action == "" {
		return fmt.Errorf("action is required")
	}
	switch cmd.Action {
	case types.RerouteRetryFailed:
		return fmt.Errorf("retry_failed is request-level, not a command (use --retry-failed)")
	case types.RerouteMove:
		if cmd.Index == "" {
			return fmt.Errorf("--action move requires --index")
		}
		if cmd.FromNode == "" {
			return fmt.Errorf("--action move requires --from-node")
		}
		if cmd.ToNode == "" {
			return fmt.Errorf("--action move requires --to-node")
		}
		if strings.EqualFold(cmd.FromNode, cmd.ToNode) {
			return fmt.Errorf("--from-node and --to-node must differ")
		}
	case types.RerouteCancel:
		if cmd.Index == "" {
			return fmt.Errorf("--action cancel requires --index")
		}
		if cmd.FromNode == "" {
			return fmt.Errorf("--action cancel requires --from-node (the node currently holding the shard)")
		}
	case types.RerouteAllocateReplica:
		if cmd.Index == "" {
			return fmt.Errorf("--action allocate-replica requires --index")
		}
		if cmd.ToNode == "" {
			return fmt.Errorf("--action allocate-replica requires --to-node")
		}
	case types.RerouteAllocateEmptyPrimary, types.RerouteAllocateStalePrimary:
		if cmd.Index == "" {
			return fmt.Errorf("--action %s requires --index", cmd.Action)
		}
		if cmd.ToNode == "" {
			return fmt.Errorf("--action %s requires --to-node", cmd.Action)
		}
		if !cmd.AcceptDataLoss {
			return fmt.Errorf("--action %s requires --accept-data-loss (this command can lose data)", cmd.Action)
		}
	default:
		return fmt.Errorf("unsupported action %q", cmd.Action)
	}
	return nil
}

// ParseRerouteAction maps a CLI-friendly action string to a typed
// RerouteAction. Hyphenated aliases ("allocate-replica") match the
// flag-style spelling; underscored aliases match the cluster's wire
// names so a copy-paste from the docs also works.
func ParseRerouteAction(s string) (types.RerouteAction, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "move":
		return types.RerouteMove, nil
	case "cancel":
		return types.RerouteCancel, nil
	case "allocate-replica", "allocate_replica":
		return types.RerouteAllocateReplica, nil
	case "allocate-empty-primary", "allocate_empty_primary":
		return types.RerouteAllocateEmptyPrimary, nil
	case "allocate-stale-primary", "allocate_stale_primary":
		return types.RerouteAllocateStalePrimary, nil
	case "retry-failed", "retry_failed":
		return types.RerouteRetryFailed, nil
	case "":
		return "", fmt.Errorf("--action is required (one of: move, cancel, allocate-replica, allocate-empty-primary, allocate-stale-primary, retry-failed)")
	default:
		return "", fmt.Errorf("unknown action %q (one of: move, cancel, allocate-replica, allocate-empty-primary, allocate-stale-primary, retry-failed)", s)
	}
}
