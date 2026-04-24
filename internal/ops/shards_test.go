package ops_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dj-wasabi/esops-go/internal/ops"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// fakeShardInspector records the filter it was called with and returns
// a canned response. It's exactly what the real adapter would return
// before business-layer filtering runs.
type fakeShardInspector struct {
	got    types.ShardFilter
	shards []types.Shard
	err    error
}

func (f *fakeShardInspector) Shards(_ context.Context, filter types.ShardFilter) ([]types.Shard, error) {
	f.got = filter
	return f.shards, f.err
}

func sampleShards() []types.Shard {
	return []types.Shard{
		{Index: "users", Shard: 0, Primary: true, State: "STARTED", Docs: 20, StoreBytes: 9255, Node: "es01", IP: "10.0.0.1"},
		{Index: "users", Shard: 0, Primary: false, State: "STARTED", Docs: 20, StoreBytes: 9255, Node: "es02", IP: "10.0.0.2"},
		{Index: "logs", Shard: 0, Primary: true, State: "STARTED", Docs: 200, StoreBytes: 34981, Node: "es01", IP: "10.0.0.1"},
		{Index: "logs", Shard: 1, Primary: true, State: "UNASSIGNED", Docs: 0, StoreBytes: 0},
	}
}

func TestShards_passesIndexFilterThrough(t *testing.T) {
	fake := &fakeShardInspector{shards: sampleShards()}

	_, err := ops.Shards(context.Background(), fake, types.ShardFilter{Index: "logs-*,users"})
	if err != nil {
		t.Fatalf("Shards: %v", err)
	}
	if fake.got.Index != "logs-*,users" {
		t.Errorf("adapter got index %q, want %q", fake.got.Index, "logs-*,users")
	}
}

func TestShards_propagatesError(t *testing.T) {
	sentinel := errors.New("boom")
	fake := &fakeShardInspector{err: sentinel}
	_, err := ops.Shards(context.Background(), fake, types.ShardFilter{})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestShards_stateFilterIsCaseInsensitive(t *testing.T) {
	fake := &fakeShardInspector{shards: sampleShards()}
	got, err := ops.Shards(context.Background(), fake, types.ShardFilter{State: "unassigned"})
	if err != nil {
		t.Fatalf("Shards: %v", err)
	}
	if len(got) != 1 || got[0].State != "UNASSIGNED" {
		t.Errorf("got %+v, want exactly the UNASSIGNED shard", got)
	}
}

func TestShards_nodeFilter(t *testing.T) {
	fake := &fakeShardInspector{shards: sampleShards()}
	got, err := ops.Shards(context.Background(), fake, types.ShardFilter{Node: "es02"})
	if err != nil {
		t.Fatalf("Shards: %v", err)
	}
	if len(got) != 1 || got[0].Node != "es02" {
		t.Errorf("got %+v, want exactly the es02 shard", got)
	}
}

func TestShards_primaryRoleFilter(t *testing.T) {
	fake := &fakeShardInspector{shards: sampleShards()}
	got, err := ops.Shards(context.Background(), fake, types.ShardFilter{Role: types.ShardRolePrimary})
	if err != nil {
		t.Fatalf("Shards: %v", err)
	}
	for _, s := range got {
		if !s.Primary {
			t.Errorf("got replica %+v, want primaries only", s)
		}
	}
	if len(got) != 3 {
		t.Errorf("got %d primaries, want 3", len(got))
	}
}

func TestShards_replicaRoleFilter(t *testing.T) {
	fake := &fakeShardInspector{shards: sampleShards()}
	got, err := ops.Shards(context.Background(), fake, types.ShardFilter{Role: types.ShardRoleReplica})
	if err != nil {
		t.Fatalf("Shards: %v", err)
	}
	if len(got) != 1 || got[0].Primary {
		t.Errorf("got %+v, want exactly the one replica", got)
	}
}

func TestShards_sortsByIndexThenShardThenRole(t *testing.T) {
	fake := &fakeShardInspector{shards: sampleShards()}
	got, err := ops.Shards(context.Background(), fake, types.ShardFilter{})
	if err != nil {
		t.Fatalf("Shards: %v", err)
	}

	// Expected stable order: logs/0/p, logs/1/p, users/0/p, users/0/r.
	want := []struct {
		index   string
		shard   int
		primary bool
	}{
		{"logs", 0, true},
		{"logs", 1, true},
		{"users", 0, true},
		{"users", 0, false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d shards, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Index != w.index || got[i].Shard != w.shard || got[i].Primary != w.primary {
			t.Errorf("row %d = %s/%d/prim=%v, want %s/%d/prim=%v",
				i, got[i].Index, got[i].Shard, got[i].Primary, w.index, w.shard, w.primary)
		}
	}
}
