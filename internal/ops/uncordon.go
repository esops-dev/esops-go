package ops

import (
	"context"
	"fmt"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// UncordonDeps is uncordon's slim version of DrainDeps — no shard
// inspector because uncordon does not poll progress, it just removes
// the exclusion and returns. The node may have already been
// decommissioned, so we deliberately skip the "node still in cluster"
// check that the drain path enforces: a stale exclusion left over
// after a node was destroyed is exactly the case `ops uncordon` exists
// to clean up.
type UncordonDeps struct {
	Settings client.ClusterSettingsManager
	Voting   client.VotingExclusionManager
}

// UncordonNode removes one or more nodes from
// cluster.routing.allocation.exclude._name. Idempotent at the per-node
// level: nodes not in the exclude list are recorded in
// result.NotInList and the cluster is not touched on their account.
// When *every* requested node is already absent the PUT is skipped.
//
// The transient/persistent scope must match the scope the drain used;
// the function does not silently sweep the other scope so "drain
// transient, uncordon persistent" produces a clear "not in list"
// outcome instead of a surprising no-op.
//
// When ClearVoting is set, after the allocation PUT a DELETE
// /_cluster/voting_config_exclusions is fired. The cluster API only
// supports clear-all (no per-node delete), so this is a cluster-wide
// sweep — call it during the standard "bring nodes back online" flow
// when the voting list is known stale. ClearVoting can also be used
// without NodeNames (an empty allocation-uncordon paired with a
// voting clear) for the post-maintenance cleanup case.
func UncordonNode(ctx context.Context, deps UncordonDeps, req types.NodeUncordonRequest) (types.NodeUncordonResult, error) {
	if len(req.NodeNames) == 0 && !req.ClearVoting {
		return types.NodeUncordonResult{}, fmt.Errorf("--node or --clear-voting is required")
	}

	var (
		names []string
		err   error
	)
	if len(req.NodeNames) > 0 {
		names, err = normaliseNodeNames(req.NodeNames)
		if err != nil {
			return types.NodeUncordonResult{}, err
		}
	}

	result := types.NodeUncordonResult{
		NodeNames:  names,
		DryRun:     req.DryRun,
		Persistent: req.Persistent,
	}

	if len(names) > 0 {
		view, err := deps.Settings.GetClusterSettings(ctx)
		if err != nil {
			return result, err
		}
		before := scopeExcludes(view, req.Persistent)
		updated, notInList := removeExcludes(before, names)

		result.Before = copySorted(before)
		result.After = copySorted(updated)
		result.NotInList = notInList

		// allAbsent: every requested node was already not in the list.
		// In that case there's nothing for the cluster to do; report
		// success and skip the PUT to avoid a cluster-state version
		// bump.
		allAbsent := len(notInList) == len(names)

		switch {
		case req.DryRun, allAbsent:
			result.Acknowledged = !req.DryRun
		default:
			put, err := deps.Settings.PutAllocationExclude(ctx, types.ClusterAllocationExcludeUpdate{
				Persistent: req.Persistent,
				Names:      updated,
			})
			if err != nil {
				return result, err
			}
			result.Acknowledged = put.Acknowledged
			if !put.Acknowledged {
				return result, fmt.Errorf("%w: cluster did not acknowledge the put_settings", client.ErrDryRunBlocker)
			}
		}
	} else {
		// No --node, only --clear-voting: the allocation half is a
		// no-op so the result acks "yes, I did the thing the operator
		// asked for" once the voting clear succeeds below.
		result.Acknowledged = !req.DryRun
	}

	if req.ClearVoting && !req.DryRun {
		votingResult, votingErr := deps.Voting.ClearVotingExclusions(ctx)
		result.VotingExclusionAcked = votingResult.Acknowledged
		if votingErr != nil {
			return result, fmt.Errorf("voting_config_exclusions clear failed: %w", votingErr)
		}
		result.VotingExclusionsCleared = true
	}

	return result, nil
}
