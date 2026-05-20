package ops_test

import (
	"context"
	"errors"
	"testing"

	"github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

type ndNodeInspector struct {
	nodes []types.Node
	err   error
}

func (f *ndNodeInspector) Nodes(_ context.Context) ([]types.Node, error) {
	return f.nodes, f.err
}

type ndNodeStatsInspector struct {
	rows []types.NodeStats
	err  error
}

func (f *ndNodeStatsInspector) NodeStats(_ context.Context) ([]types.NodeStats, error) {
	return f.rows, f.err
}

type ndShardInspector struct {
	shards []types.Shard
	gotF   types.ShardFilter
	err    error
}

func (f *ndShardInspector) Shards(_ context.Context, filter types.ShardFilter) ([]types.Shard, error) {
	f.gotF = filter
	return f.shards, f.err
}

type ndClusterSettingsInspector struct {
	view types.ClusterSettingsView
	err  error
}

func (f *ndClusterSettingsInspector) GetClusterSettings(_ context.Context) (types.ClusterSettingsView, error) {
	return f.view, f.err
}

type ndVotingExclusion struct {
	names []string
	err   error
}

func (f *ndVotingExclusion) AddVotingExclusions(_ context.Context, _ []string) (types.VotingExclusionsResult, error) {
	return types.VotingExclusionsResult{}, client.ErrUnsupported
}
func (f *ndVotingExclusion) ClearVotingExclusions(_ context.Context) (types.VotingExclusionsResult, error) {
	return types.VotingExclusionsResult{}, client.ErrUnsupported
}
func (f *ndVotingExclusion) GetVotingExclusions(_ context.Context) ([]string, error) {
	return f.names, f.err
}

func TestNodeDescribe_rejectsEmpty(t *testing.T) {
	_, err := ops.NodeDescribe(context.Background(), ops.NodeDescribeCapabilities{
		Nodes: &ndNodeInspector{},
	}, ops.NodeDescribeOptions{Name: " "})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("want ErrUsage, got %v", err)
	}
}

func TestNodeDescribe_rejectsPattern(t *testing.T) {
	_, err := ops.NodeDescribe(context.Background(), ops.NodeDescribeCapabilities{
		Nodes: &ndNodeInspector{},
	}, ops.NodeDescribeOptions{Name: "data-*"})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("want ErrUsage, got %v", err)
	}
}

func TestNodeDescribe_notFound(t *testing.T) {
	caps := ops.NodeDescribeCapabilities{
		Nodes: &ndNodeInspector{nodes: []types.Node{{Name: "data-1"}}},
	}
	_, err := ops.NodeDescribe(context.Background(), caps, ops.NodeDescribeOptions{Name: "missing"})
	if err == nil || !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestNodeDescribe_aggregatesAllCapabilities(t *testing.T) {
	caps := ops.NodeDescribeCapabilities{
		Nodes:           &ndNodeInspector{nodes: []types.Node{{Name: "data-1", Roles: []string{"data"}}}},
		Stats:           &ndNodeStatsInspector{rows: []types.NodeStats{{Name: "data-1", JVM: types.NodeJVMStats{Heap: types.NodeJVMHeap{InitBytes: 1 << 30, MaxBytes: 1 << 30}}}}},
		Shards:          &ndShardInspector{shards: []types.Shard{{Index: "logs", Shard: 0, Primary: true, State: "STARTED", StoreBytes: 100}}},
		ClusterSettings: &ndClusterSettingsInspector{view: types.ClusterSettingsView{PersistentExcludeName: []string{"data-1"}}},
		VotingExclusion: &ndVotingExclusion{names: []string{"data-1"}},
		Dialect:         "elasticsearch",
	}
	desc, err := ops.NodeDescribe(context.Background(), caps, ops.NodeDescribeOptions{Name: "data-1"})
	if err != nil {
		t.Fatalf("NodeDescribe: %v", err)
	}
	if desc.Stats == nil || desc.Stats.JVM.Heap.MaxBytes == 0 {
		t.Errorf("expected stats populated, got %+v", desc.Stats)
	}
	if desc.ShardSummary.Primaries != 1 || desc.ShardSummary.StoreBytes != 100 {
		t.Errorf("ShardSummary = %+v", desc.ShardSummary)
	}
	if !desc.Drain.AllocationExcluded || len(desc.Drain.Scopes) != 1 || desc.Drain.Scopes[0] != "persistent" {
		t.Errorf("Drain.AllocationExcluded path wrong: %+v", desc.Drain)
	}
	if !desc.Drain.VotingExcluded {
		t.Errorf("VotingExcluded should be true")
	}
}

func TestNodeDescribe_drainTransientOnly(t *testing.T) {
	caps := ops.NodeDescribeCapabilities{
		Nodes:           &ndNodeInspector{nodes: []types.Node{{Name: "data-1"}}},
		ClusterSettings: &ndClusterSettingsInspector{view: types.ClusterSettingsView{TransientExcludeName: []string{"data-1"}}},
	}
	desc, err := ops.NodeDescribe(context.Background(), caps, ops.NodeDescribeOptions{Name: "data-1"})
	if err != nil {
		t.Fatalf("NodeDescribe: %v", err)
	}
	if !desc.Drain.AllocationExcluded || len(desc.Drain.Scopes) != 1 || desc.Drain.Scopes[0] != "transient" {
		t.Errorf("Drain scopes wrong: %+v", desc.Drain)
	}
}

func TestNodeDescribe_capturesNonFatalErrorsAsWarnings(t *testing.T) {
	caps := ops.NodeDescribeCapabilities{
		Nodes:           &ndNodeInspector{nodes: []types.Node{{Name: "data-1"}}},
		Stats:           &ndNodeStatsInspector{err: errors.New("boom")},
		ClusterSettings: &ndClusterSettingsInspector{},
	}
	desc, err := ops.NodeDescribe(context.Background(), caps, ops.NodeDescribeOptions{Name: "data-1"})
	if err != nil {
		t.Fatalf("NodeDescribe: %v", err)
	}
	if desc.SectionWarnings["stats"] != "boom" {
		t.Errorf("SectionWarnings[stats] = %q, want boom", desc.SectionWarnings["stats"])
	}
}
