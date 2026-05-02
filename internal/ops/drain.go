package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// defaultDrainWaitInterval is the poll cadence between shard-count
// re-reads while waiting for a drain to complete. 5s is the same value
// the cluster's own master uses between allocator passes, so polling
// faster than that is wasted load.
const defaultDrainWaitInterval = 5 * time.Second

// DrainDeps bundles the capabilities the drain path needs. The shape
// matches the rebalance/cutover idiom: one struct, one constructor,
// one place for the CLI to assemble them. Wait is plumbed through the
// same struct so test fakes can drive shard-count progression without
// reaching for a global clock.
type DrainDeps struct {
	Nodes    client.NodeInspector
	Shards   client.ShardInspector
	Settings client.ClusterSettingsManager
	Voting   client.VotingExclusionManager
}

// DrainNode adds one or more nodes to
// cluster.routing.allocation.exclude._name — the cluster reads this and
// refuses to assign new shards to the named nodes, then relocates the
// existing ones away. The setting is the canonical "kubectl drain"
// equivalent for ES/OS; the alternative (decommission via
// _node/<name>/_voting_config_exclusions) only removes voting rights,
// not data, and is exposed separately via AlsoExcludeVoting.
//
// The function is idempotent at the per-node level: nodes already in
// the exclude list are recorded in result.AlreadyExcluded and not
// re-added. When *every* requested node is already excluded the PUT
// is skipped entirely (the cluster-state version is not bumped).
//
// When AlsoExcludeVoting is set, after the allocation PUT acknowledges
// we POST /_cluster/voting_config_exclusions for the same node set.
// A failure of the secondary call returns a wrapped error but the
// allocation result is preserved so the operator sees the partial
// success.
//
// When Wait is set, the function polls /_cat/shards filtered to the
// node set and returns once the total count hits zero — or when
// WaitTimeout (or the surrounding context) fires, whichever comes
// first. Wait is best-effort: the result always carries the latest
// shard count plus a per-node breakdown so the caller can decide
// whether to retry, force-allocate, or escalate.
//
// Concurrency note: the GET-then-PUT against /_cluster/settings is
// not atomic — two operators draining different nodes in parallel
// can clobber each other's exclude list. The cluster has no CAS
// primitive for settings, so this is a known limitation; in
// practice, drains are rare enough that an operational lock at the
// runbook level is sufficient.
func DrainNode(ctx context.Context, deps DrainDeps, req types.NodeDrainRequest) (types.NodeDrainResult, error) {
	names, err := normaliseNodeNames(req.NodeNames)
	if err != nil {
		return types.NodeDrainResult{}, err
	}

	if err := assertNodesExist(ctx, deps.Nodes, names); err != nil {
		return types.NodeDrainResult{}, err
	}

	view, err := deps.Settings.GetClusterSettings(ctx)
	if err != nil {
		return types.NodeDrainResult{}, err
	}
	before := scopeExcludes(view, req.Persistent)

	merged, alreadyExcluded := mergeExcludes(before, names)

	result := types.NodeDrainResult{
		NodeNames:       names,
		DryRun:          req.DryRun,
		Persistent:      req.Persistent,
		AlreadyExcluded: alreadyExcluded,
		Before:          copySorted(before),
		After:           copySorted(merged),
	}

	allAlready := len(alreadyExcluded) == len(names)

	if !req.DryRun && !allAlready {
		put, err := deps.Settings.PutAllocationExclude(ctx, types.ClusterAllocationExcludeUpdate{
			Persistent: req.Persistent,
			Names:      merged,
		})
		if err != nil {
			return result, err
		}
		result.Acknowledged = put.Acknowledged
		if !put.Acknowledged {
			return result, fmt.Errorf("%w: cluster did not acknowledge the put_settings", client.ErrDryRunBlocker)
		}
	} else if allAlready && !req.DryRun {
		// No-op write: the desired state is already in place, so the
		// cluster has effectively acknowledged what we asked for.
		// Skipping the PUT avoids a cluster-state version bump every
		// time an already-drained node is re-drained.
		result.Acknowledged = true
	}

	if req.AlsoExcludeVoting && !req.DryRun {
		votingResult, votingErr := deps.Voting.AddVotingExclusions(ctx, names)
		result.VotingExclusionAcked = votingResult.Acknowledged
		if votingErr != nil {
			// Allocation succeeded but voting failed: surface the
			// partial success on the result and bubble the error so
			// the caller exits non-zero. The operator sees both the
			// before/after exclude list (allocation worked) and the
			// voting failure message in stderr.
			return result, fmt.Errorf("voting_config_exclusions add failed (allocation exclude was applied): %w", votingErr)
		}
		result.VotingExclusionsAdded = append([]string(nil), names...)
	}

	if req.Wait && !req.DryRun {
		remaining, perNode, err := waitForDrain(ctx, deps.Shards, names, req)
		result.Waited = true
		result.ShardsRemaining = remaining
		result.PerNodeShardsRemaining = perNode
		if err != nil {
			return result, err
		}
	} else if !req.DryRun {
		// No wait requested: still surface the current count so the
		// caller can show "the drain is in progress, X shards left to
		// move" without a second command invocation.
		count, perNode, err := shardCountOnNodes(ctx, deps.Shards, names)
		if err == nil {
			result.ShardsRemaining = count
			result.PerNodeShardsRemaining = perNode
		}
	}

	return result, nil
}

// ListDrained returns a snapshot of currently-drained nodes (both
// scopes of the allocation exclude list) plus the cluster's
// voting-config exclusions. Used by `ops drain --list` so an operator
// can answer "what's drained right now?" without dropping into raw
// _cluster/settings.
//
// When the voting capability is not wired (test fakes), the
// voting-exclusions slice is left empty rather than erroring — the
// operator still gets the allocation view, which is the load-bearing
// half.
func ListDrained(ctx context.Context, settings client.ClusterSettingsManager, voting client.VotingExclusionManager) (types.NodeDrainListResult, error) {
	view, err := settings.GetClusterSettings(ctx)
	if err != nil {
		return types.NodeDrainListResult{}, err
	}
	out := types.NodeDrainListResult{
		PersistentExcluded: copySorted(view.PersistentExcludeName),
		TransientExcluded:  copySorted(view.TransientExcludeName),
	}
	if voting != nil {
		v, err := voting.GetVotingExclusions(ctx)
		if err != nil {
			return out, err
		}
		out.VotingExcluded = copySorted(v)
	}
	return out, nil
}

// normaliseNodeNames trims whitespace, drops empties, deduplicates,
// and rejects invalid shapes. Returns the cleaned slice in input order
// (after dedup) so the result's NodeNames matches what the operator
// typed without re-shuffling.
func normaliseNodeNames(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("--node is required (one or more node names)")
	}
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, raw := range names {
		// urfave/cli's StringSliceFlag accepts both `--node a,b` and
		// `--node a --node b`. Splitting on commas here means callers
		// don't have to normalise; the tests pass either shape.
		for _, part := range strings.Split(raw, ",") {
			n := strings.TrimSpace(part)
			if n == "" {
				continue
			}
			if strings.ContainsAny(n, " \t\n") {
				return nil, fmt.Errorf("--node %q contains whitespace; node names must be single tokens", n)
			}
			if _, dup := seen[n]; dup {
				continue
			}
			seen[n] = struct{}{}
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--node is required (one or more node names)")
	}
	return out, nil
}

// assertNodesExist protects against typoed --node values. The cluster
// accepts any string for exclude._name, so a typo would look like
// "drain succeeded" and the real node would keep receiving shards. We
// validate the entire requested set up-front and report all unknown
// names in one error so the operator fixes them in one pass instead of
// playing whack-a-mole.
func assertNodesExist(ctx context.Context, ni client.NodeInspector, names []string) error {
	nodes, err := ni.Nodes(ctx)
	if err != nil {
		return err
	}
	known := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		known[n.Name] = struct{}{}
	}
	var missing []string
	for _, n := range names {
		if _, ok := known[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if len(missing) == 1 {
		return fmt.Errorf("node %q not found in the cluster (run 'esops ops nodes' to list)", missing[0])
	}
	return fmt.Errorf("nodes not found in the cluster: %s (run 'esops ops nodes' to list)", strings.Join(missing, ", "))
}

// scopeExcludes returns the persistent or transient slice from the
// settings view. Picked by a boolean to keep call sites at the
// drain/uncordon layer single-line.
func scopeExcludes(v types.ClusterSettingsView, persistent bool) []string {
	if persistent {
		return v.PersistentExcludeName
	}
	return v.TransientExcludeName
}

// mergeExcludes returns the existing list with every name in `add`
// appended (de-duplicating against the existing list and the input),
// plus the subset of `add` that was already present. Insertion order
// is preserved: existing entries first, then the new additions in the
// caller's order, so a captured PUT body matches what the operator
// typed.
func mergeExcludes(existing []string, add []string) (merged []string, alreadyExcluded []string) {
	seen := make(map[string]struct{}, len(existing)+len(add))
	merged = append(merged, existing...)
	for _, e := range existing {
		seen[e] = struct{}{}
	}
	for _, n := range add {
		if _, ok := seen[n]; ok {
			alreadyExcluded = append(alreadyExcluded, n)
			continue
		}
		seen[n] = struct{}{}
		merged = append(merged, n)
	}
	return merged, alreadyExcluded
}

// removeExcludes returns the existing list with every name in
// `remove` filtered out, plus the subset of `remove` that was not
// actually in the list.
func removeExcludes(existing []string, remove []string) (updated []string, notInList []string) {
	target := make(map[string]struct{}, len(remove))
	for _, n := range remove {
		target[n] = struct{}{}
	}
	hit := make(map[string]bool, len(remove))
	for _, e := range existing {
		if _, drop := target[e]; drop {
			hit[e] = true
			continue
		}
		updated = append(updated, e)
	}
	for _, n := range remove {
		if !hit[n] {
			notInList = append(notInList, n)
		}
	}
	return updated, notInList
}

// copySorted returns a deduplicated, lexicographically-sorted copy of
// the input. Used for the Before/After slices on the result so JSON
// captures of the same transition compare equal regardless of how the
// cluster ordered the underlying setting (the cluster preserves
// insertion order, which a captured listing should not).
func copySorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// waitForDrain polls the shard count on the named nodes until the
// total hits zero, the timeout fires, or the context is cancelled.
// Returns the most recent total and per-node breakdown, plus any
// error from the cluster — a context cancellation surfaces as
// ctx.Err() so the caller can distinguish "drain is still in
// progress" (>0 remaining) from "I gave up".
func waitForDrain(ctx context.Context, si client.ShardInspector, names []string, req types.NodeDrainRequest) (int, map[string]int, error) {
	interval := req.WaitInterval
	if interval <= 0 {
		interval = defaultDrainWaitInterval
	}

	deadline := time.Time{}
	if req.WaitTimeout > 0 {
		deadline = time.Now().Add(req.WaitTimeout)
	}

	timer := time.NewTimer(0)
	defer timer.Stop()

	var (
		lastTotal   int
		lastPerNode map[string]int
	)
	for {
		select {
		case <-ctx.Done():
			return lastTotal, lastPerNode, ctx.Err()
		case <-timer.C:
		}

		total, perNode, err := shardCountOnNodes(ctx, si, names)
		if err != nil {
			return lastTotal, lastPerNode, err
		}
		lastTotal = total
		lastPerNode = perNode
		if total == 0 {
			return 0, perNode, nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return lastTotal, lastPerNode, fmt.Errorf("drain timeout: %d shards still on %s after %s", lastTotal, formatNodeList(names), req.WaitTimeout)
		}
		timer.Reset(interval)
	}
}

// shardCountOnNodes returns the total shard count across the named
// nodes plus a per-node breakdown. Includes RELOCATING because a
// relocation in flight is exactly "shards still on this node" — the
// drain is incomplete until they land elsewhere.
//
// The per-node map always contains every requested name (zero count
// for nodes that hold nothing) so the rendered table can show "es01:
// 0, es02: 12" without the caller having to fill in absent keys.
func shardCountOnNodes(ctx context.Context, si client.ShardInspector, names []string) (int, map[string]int, error) {
	shards, err := si.Shards(ctx, types.ShardFilter{})
	if err != nil {
		return 0, nil, err
	}
	target := make(map[string]struct{}, len(names))
	perNode := make(map[string]int, len(names))
	for _, n := range names {
		target[n] = struct{}{}
		perNode[n] = 0
	}
	total := 0
	for _, s := range shards {
		if _, ok := target[s.Node]; !ok {
			continue
		}
		perNode[s.Node]++
		total++
	}
	return total, perNode, nil
}

// formatNodeList is the timeout-message helper that quotes a single
// name for readability and joins many with commas. Avoids the awkward
// "1 shards still on [es01]" formatting that strings.Join would give.
func formatNodeList(names []string) string {
	if len(names) == 1 {
		return fmt.Sprintf("node %q", names[0])
	}
	return fmt.Sprintf("nodes %s", strings.Join(names, ", "))
}
