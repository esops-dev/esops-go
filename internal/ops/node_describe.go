package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// NodeDescribeOptions is the business-layer input to NodeDescribe. Name
// is the operator-supplied positional argument — a single literal node
// name. Patterns are refused up-front; describe is intrinsically a
// single-resource verb.
type NodeDescribeOptions struct {
	Name string
}

// NodeDescribeCapabilities is the read-only capability bundle the
// aggregator consumes. Pulling each interface by name (rather than
// taking *client.Client) keeps the contract narrow and lets tests
// substitute one capability at a time.
//
// Stats/Bootstrap/Settings/Shards/ClusterSettings/VotingExclusion are
// nil-tolerant — tests can omit any of them and the aggregator
// records the gap as a section warning rather than panicking.
type NodeDescribeCapabilities struct {
	Nodes           client.NodeInspector
	Stats           client.NodeStatsInspector
	Bootstrap       client.NodeBootstrapInspector
	Settings        client.NodeSettingsInspector
	Shards          client.ShardInspector
	ClusterSettings client.ClusterSettingsInspector
	VotingExclusion client.VotingExclusionManager
	Dialect         string
}

// NodeDescribe is the business-layer entry point for
// `esops ops node describe`. It validates the operator's input,
// finds the matching node in /_cat/nodes, then fans out across the
// read-only inspectors in parallel and assembles a neutral
// NodeDescription.
//
// Refusals:
//   - empty / patternish Name (ErrUsage)
//   - the resolved name does not match any node in the cluster
//     (ErrNotFound)
//
// Non-fatal collector failures land in SectionWarnings rather than
// aborting the whole call.
func NodeDescribe(ctx context.Context, caps NodeDescribeCapabilities, opts NodeDescribeOptions) (types.NodeDescription, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return types.NodeDescription{}, client.UsageError("NAME is required (a single literal node name)")
	}
	if client.IsPatternish(name) {
		return types.NodeDescription{}, client.UsageError("NAME must be a literal node name, not a pattern (got %q); use `esops ops nodes` for fan-out", name)
	}

	nodes, err := caps.Nodes.Nodes(ctx)
	if err != nil {
		return types.NodeDescription{}, err
	}
	target, ok := pickNode(nodes, name)
	if !ok {
		return types.NodeDescription{}, fmt.Errorf("%w: node %q", client.ErrNotFound, name)
	}

	desc := types.NodeDescription{
		Name:            target.Name,
		Dialect:         caps.Dialect,
		Info:            target,
		SectionWarnings: map[string]string{},
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	warn := func(section string, err error) {
		mu.Lock()
		desc.SectionWarnings[section] = err.Error()
		mu.Unlock()
	}
	run := func(section string, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil {
				warn(section, err)
			}
		}()
	}

	if caps.Stats != nil {
		run("stats", func() error {
			rows, err := caps.Stats.NodeStats(ctx)
			if err != nil {
				return err
			}
			for _, r := range rows {
				if r.Name == target.Name {
					s := r
					desc.Stats = &s
					return nil
				}
			}
			return nil
		})
	}

	if caps.Bootstrap != nil {
		run("bootstrap", func() error {
			rows, err := caps.Bootstrap.NodeBootstrap(ctx)
			if err != nil {
				return err
			}
			for _, r := range rows {
				if r.Name == target.Name {
					b := r
					desc.Bootstrap = &b
					return nil
				}
			}
			return nil
		})
	}

	if caps.Settings != nil {
		run("settings", func() error {
			rows, err := caps.Settings.NodeSettings(ctx)
			if err != nil {
				return err
			}
			for _, r := range rows {
				if r.Name == target.Name {
					s := r
					desc.Settings = &s
					return nil
				}
			}
			return nil
		})
	}

	if caps.Shards != nil {
		run("shards", func() error {
			rows, err := caps.Shards.Shards(ctx, types.ShardFilter{Node: target.Name})
			if err != nil {
				return err
			}
			desc.Shards = rows
			desc.ShardSummary = summariseNodeShards(rows)
			return nil
		})
	}

	if caps.ClusterSettings != nil {
		run("drain", func() error {
			view, err := caps.ClusterSettings.GetClusterSettings(ctx)
			if err != nil {
				return err
			}
			scopes := []string{}
			if containsString(view.PersistentExcludeName, target.Name) {
				scopes = append(scopes, "persistent")
			}
			if containsString(view.TransientExcludeName, target.Name) {
				scopes = append(scopes, "transient")
			}
			if len(scopes) > 0 {
				// Sort so the rendered order is stable regardless of
				// which scope was populated first by the cluster
				// settings response. Cheap future-proofing if a third
				// scope is ever added (e.g. session-level overrides).
				sort.Strings(scopes)
				desc.Drain.AllocationExcluded = true
				desc.Drain.Scopes = scopes
			}
			return nil
		})
	}

	if caps.VotingExclusion != nil {
		run("voting", func() error {
			excluded, err := caps.VotingExclusion.GetVotingExclusions(ctx)
			if err != nil {
				return err
			}
			if containsString(excluded, target.Name) {
				desc.Drain.VotingExcluded = true
			}
			return nil
		})
	}

	wg.Wait()

	if len(desc.SectionWarnings) == 0 {
		desc.SectionWarnings = nil
	}
	return desc, nil
}

// pickNode finds the exact-name row in a /_cat/nodes response. The
// cluster's name column is the operator-typed node.name setting, so
// an exact-match lookup is the right answer; aliasing nodes by IP or
// ID is intentionally out of scope here.
func pickNode(rows []types.Node, name string) (types.Node, bool) {
	for _, r := range rows {
		if r.Name == name {
			return r, true
		}
	}
	return types.Node{}, false
}

// summariseNodeShards aggregates the per-shard slice into the
// primary/replica/store-bytes triple the describe header surfaces.
// Counts shard copies, not distinct shard IDs — a replica of shard 0
// is its own row in /_cat/shards and counts here.
func summariseNodeShards(rows []types.Shard) types.NodeShardSummary {
	var s types.NodeShardSummary
	for _, r := range rows {
		if r.Primary {
			s.Primaries++
		} else {
			s.Replicas++
		}
		s.StoreBytes += r.StoreBytes
	}
	return s
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
