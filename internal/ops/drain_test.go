package ops_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/ops"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// fakeDrainShards plays back a sequence of /_cat/shards responses;
// each Shards() call advances to the next stage so a test can drive a
// "drain in progress → drain complete" transition without rolling a
// goroutine.
type fakeDrainShards struct {
	stages [][]types.Shard
	calls  int
	err    error
}

func (f *fakeDrainShards) Shards(_ context.Context, _ types.ShardFilter) ([]types.Shard, error) {
	if f.err != nil {
		return nil, f.err
	}
	stage := f.stages[len(f.stages)-1]
	if f.calls < len(f.stages) {
		stage = f.stages[f.calls]
	}
	f.calls++
	return stage, nil
}

// fakeClusterSettings records the last PUT and serves canned reads.
type fakeClusterSettings struct {
	view        types.ClusterSettingsView
	getErr      error
	putErr      error
	put         types.ClusterAllocationExcludeUpdate
	putCalled   bool
	putAck      bool
	putResponse types.ClusterSettingsUpdateResult
}

func (f *fakeClusterSettings) GetClusterSettings(_ context.Context) (types.ClusterSettingsView, error) {
	return f.view, f.getErr
}

func (f *fakeClusterSettings) PutAllocationExclude(_ context.Context, req types.ClusterAllocationExcludeUpdate) (types.ClusterSettingsUpdateResult, error) {
	f.putCalled = true
	f.put = req
	if f.putErr != nil {
		return f.putResponse, f.putErr
	}
	if (f.putResponse == types.ClusterSettingsUpdateResult{}) && f.putAck {
		return types.ClusterSettingsUpdateResult{Acknowledged: true}, nil
	}
	return f.putResponse, nil
}

// fakeVotingExclusion records what add/clear were called with. Returns
// canned acks; default is acknowledged=true so most happy-path tests
// don't need to set it.
type fakeVotingExclusion struct {
	addCalledWith    []string
	clearCalled      bool
	listed           []string
	addErr           error
	clearErr         error
	listErr          error
	addAck, clearAck bool
}

func newFakeVoting() *fakeVotingExclusion {
	return &fakeVotingExclusion{addAck: true, clearAck: true}
}

func (f *fakeVotingExclusion) AddVotingExclusions(_ context.Context, names []string) (types.VotingExclusionsResult, error) {
	f.addCalledWith = append([]string(nil), names...)
	return types.VotingExclusionsResult{Acknowledged: f.addAck}, f.addErr
}

func (f *fakeVotingExclusion) ClearVotingExclusions(_ context.Context) (types.VotingExclusionsResult, error) {
	f.clearCalled = true
	return types.VotingExclusionsResult{Acknowledged: f.clearAck}, f.clearErr
}

func (f *fakeVotingExclusion) GetVotingExclusions(_ context.Context) ([]string, error) {
	return f.listed, f.listErr
}

func twoNodeFixture() *fakeNodeInspector {
	return &fakeNodeInspector{nodes: []types.Node{
		{Name: "es01", Roles: []string{"data"}, IsDataNode: true},
		{Name: "es02", Roles: []string{"data"}, IsDataNode: true},
	}}
}

func threeNodeFixture() *fakeNodeInspector {
	return &fakeNodeInspector{nodes: []types.Node{
		{Name: "es01", Roles: []string{"data", "master"}, IsDataNode: true},
		{Name: "es02", Roles: []string{"data"}, IsDataNode: true},
		{Name: "es03", Roles: []string{"data"}, IsDataNode: true},
	}}
}

func drainDeps(ni *fakeNodeInspector, si *fakeDrainShards, cs *fakeClusterSettings, vx *fakeVotingExclusion) ops.DrainDeps {
	return ops.DrainDeps{Nodes: ni, Shards: si, Settings: cs, Voting: vx}
}

func TestDrainNode_addsToTransientByDefault(t *testing.T) {
	ni := twoNodeFixture()
	si := &fakeDrainShards{stages: [][]types.Shard{{
		{Index: "logs", Shard: 0, Node: "es01"},
		{Index: "logs", Shard: 0, Node: "es02"},
	}}}
	cs := &fakeClusterSettings{putAck: true}
	vx := newFakeVoting()

	got, err := ops.DrainNode(context.Background(), drainDeps(ni, si, cs, vx), types.NodeDrainRequest{
		NodeNames: []string{"es01"},
	})
	if err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	if !cs.putCalled {
		t.Fatalf("expected put to be called")
	}
	if cs.put.Persistent {
		t.Errorf("default scope should be transient, got persistent")
	}
	if !reflect.DeepEqual(cs.put.Names, []string{"es01"}) {
		t.Errorf("put.Names = %v, want [es01]", cs.put.Names)
	}
	if !got.Acknowledged {
		t.Errorf("Acknowledged = false, want true")
	}
	if len(got.AlreadyExcluded) != 0 {
		t.Errorf("AlreadyExcluded = %v, want empty on first drain", got.AlreadyExcluded)
	}
	if got.ShardsRemaining != 1 {
		t.Errorf("ShardsRemaining = %d, want 1", got.ShardsRemaining)
	}
	if vx.addCalledWith != nil {
		t.Errorf("voting add should not be called without --also-exclude-voting")
	}
}

func TestDrainNode_idempotentWhenAllNodesAlreadyExcluded(t *testing.T) {
	ni := twoNodeFixture()
	si := &fakeDrainShards{stages: [][]types.Shard{nil}}
	cs := &fakeClusterSettings{view: types.ClusterSettingsView{
		TransientExcludeName: []string{"es01"},
	}}

	got, err := ops.DrainNode(context.Background(), drainDeps(ni, si, cs, newFakeVoting()), types.NodeDrainRequest{
		NodeNames: []string{"es01"},
	})
	if err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	if cs.putCalled {
		t.Errorf("put should not be called when every requested node is already excluded")
	}
	if !reflect.DeepEqual(got.AlreadyExcluded, []string{"es01"}) {
		t.Errorf("AlreadyExcluded = %v, want [es01]", got.AlreadyExcluded)
	}
	if !got.Acknowledged {
		t.Errorf("Acknowledged = false, want true (no-op = success)")
	}
}

func TestDrainNode_partialAlreadyExcludedTriggersPut(t *testing.T) {
	// Two nodes requested, only one already excluded → the other
	// still needs the cluster touched. Idempotency is per-node, not
	// all-or-nothing.
	ni := threeNodeFixture()
	si := &fakeDrainShards{stages: [][]types.Shard{nil}}
	cs := &fakeClusterSettings{
		view:   types.ClusterSettingsView{TransientExcludeName: []string{"es01"}},
		putAck: true,
	}

	got, err := ops.DrainNode(context.Background(), drainDeps(ni, si, cs, newFakeVoting()), types.NodeDrainRequest{
		NodeNames: []string{"es01", "es02"},
	})
	if err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	if !cs.putCalled {
		t.Fatalf("put should be called when at least one node is new to the list")
	}
	if !reflect.DeepEqual(cs.put.Names, []string{"es01", "es02"}) {
		t.Errorf("put.Names = %v, want [es01 es02]", cs.put.Names)
	}
	if !reflect.DeepEqual(got.AlreadyExcluded, []string{"es01"}) {
		t.Errorf("AlreadyExcluded = %v, want [es01]", got.AlreadyExcluded)
	}
}

func TestDrainNode_persistentScope(t *testing.T) {
	ni := twoNodeFixture()
	si := &fakeDrainShards{stages: [][]types.Shard{nil}}
	cs := &fakeClusterSettings{putAck: true}

	_, err := ops.DrainNode(context.Background(), drainDeps(ni, si, cs, newFakeVoting()), types.NodeDrainRequest{
		NodeNames:  []string{"es01"},
		Persistent: true,
	})
	if err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	if !cs.put.Persistent {
		t.Errorf("--persistent did not propagate to PutAllocationExclude")
	}
}

func TestDrainNode_dryRunDoesNotMutate(t *testing.T) {
	ni := twoNodeFixture()
	si := &fakeDrainShards{stages: [][]types.Shard{nil}}
	cs := &fakeClusterSettings{}
	vx := newFakeVoting()

	got, err := ops.DrainNode(context.Background(), drainDeps(ni, si, cs, vx), types.NodeDrainRequest{
		NodeNames:         []string{"es01"},
		DryRun:            true,
		AlsoExcludeVoting: true,
	})
	if err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	if cs.putCalled {
		t.Errorf("dry-run must not call PutAllocationExclude")
	}
	if vx.addCalledWith != nil {
		t.Errorf("dry-run must not call AddVotingExclusions")
	}
	if !reflect.DeepEqual(got.After, []string{"es01"}) {
		t.Errorf("dry-run After = %v, want [es01] (the would-be result)", got.After)
	}
}

func TestDrainNode_validatesNodesExist(t *testing.T) {
	ni := twoNodeFixture()
	si := &fakeDrainShards{stages: [][]types.Shard{nil}}
	cs := &fakeClusterSettings{}

	_, err := ops.DrainNode(context.Background(), drainDeps(ni, si, cs, newFakeVoting()), types.NodeDrainRequest{
		NodeNames: []string{"es99"},
	})
	if err == nil {
		t.Fatalf("expected error for unknown node")
	}
	if cs.putCalled {
		t.Errorf("typed-wrong node should not reach PutAllocationExclude")
	}
}

func TestDrainNode_reportsAllUnknownNodes(t *testing.T) {
	// When two of three names are bogus, the error should name both
	// in one go. Operators fix typos in one pass instead of running
	// drain three times.
	ni := twoNodeFixture()
	si := &fakeDrainShards{stages: [][]types.Shard{nil}}
	cs := &fakeClusterSettings{}

	_, err := ops.DrainNode(context.Background(), drainDeps(ni, si, cs, newFakeVoting()), types.NodeDrainRequest{
		NodeNames: []string{"es01", "es77", "es99"},
	})
	if err == nil {
		t.Fatalf("expected error for unknown nodes")
	}
	if !strings.Contains(err.Error(), "es77") || !strings.Contains(err.Error(), "es99") {
		t.Errorf("err should name both unknown nodes, got: %v", err)
	}
}

func TestDrainNode_rejectsEmptyNames(t *testing.T) {
	cs := &fakeClusterSettings{}
	_, err := ops.DrainNode(context.Background(), drainDeps(twoNodeFixture(), &fakeDrainShards{stages: [][]types.Shard{nil}}, cs, newFakeVoting()),
		types.NodeDrainRequest{NodeNames: nil})
	if err == nil {
		t.Fatalf("empty --node should fail validation")
	}
	if cs.putCalled {
		t.Errorf("validation failure must not reach PutAllocationExclude")
	}
}

func TestDrainNode_normalizesCommaAndWhitespaceSeparators(t *testing.T) {
	// urfave/cli's StringSliceFlag accepts both `--node a,b` and
	// `--node a --node b`. The business layer also tolerates extra
	// whitespace inside an entry that the shell quoted.
	ni := threeNodeFixture()
	si := &fakeDrainShards{stages: [][]types.Shard{nil}}
	cs := &fakeClusterSettings{putAck: true}

	got, err := ops.DrainNode(context.Background(), drainDeps(ni, si, cs, newFakeVoting()), types.NodeDrainRequest{
		NodeNames: []string{" es01 ,es02", "es02 ", "es03"},
	})
	if err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	want := []string{"es01", "es02", "es03"}
	if !reflect.DeepEqual(got.NodeNames, want) {
		t.Errorf("NodeNames = %v, want %v (deduplicated, trimmed)", got.NodeNames, want)
	}
}

func TestDrainNode_rejectsWhitespaceWithinName(t *testing.T) {
	cs := &fakeClusterSettings{}
	_, err := ops.DrainNode(context.Background(), drainDeps(twoNodeFixture(), &fakeDrainShards{stages: [][]types.Shard{nil}}, cs, newFakeVoting()),
		types.NodeDrainRequest{NodeNames: []string{"es 01"}})
	if err == nil {
		t.Fatalf("whitespace inside name should fail validation")
	}
}

func TestDrainNode_unacknowledgedReturnsBlocker(t *testing.T) {
	ni := twoNodeFixture()
	si := &fakeDrainShards{stages: [][]types.Shard{nil}}
	cs := &fakeClusterSettings{} // putAck=false → cluster did not ack

	_, err := ops.DrainNode(context.Background(), drainDeps(ni, si, cs, newFakeVoting()), types.NodeDrainRequest{
		NodeNames: []string{"es01"},
	})
	if !errors.Is(err, client.ErrDryRunBlocker) {
		t.Errorf("err = %v, want ErrDryRunBlocker", err)
	}
}

func TestDrainNode_alsoExcludeVotingFiresAfterAllocationAck(t *testing.T) {
	ni := twoNodeFixture()
	si := &fakeDrainShards{stages: [][]types.Shard{nil}}
	cs := &fakeClusterSettings{putAck: true}
	vx := newFakeVoting()

	got, err := ops.DrainNode(context.Background(), drainDeps(ni, si, cs, vx), types.NodeDrainRequest{
		NodeNames:         []string{"es01", "es02"},
		AlsoExcludeVoting: true,
	})
	if err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	if !reflect.DeepEqual(vx.addCalledWith, []string{"es01", "es02"}) {
		t.Errorf("voting add called with %v, want [es01 es02]", vx.addCalledWith)
	}
	if !got.VotingExclusionAcked {
		t.Errorf("VotingExclusionAcked = false, want true")
	}
	if !reflect.DeepEqual(got.VotingExclusionsAdded, []string{"es01", "es02"}) {
		t.Errorf("VotingExclusionsAdded = %v, want [es01 es02]", got.VotingExclusionsAdded)
	}
}

func TestDrainNode_votingFailureSurfacesPartialSuccess(t *testing.T) {
	// Allocation PUT succeeds, voting POST fails — operator must see
	// both: the allocation result so they know what landed, and a
	// non-zero exit so they know cleanup is incomplete.
	ni := twoNodeFixture()
	si := &fakeDrainShards{stages: [][]types.Shard{nil}}
	cs := &fakeClusterSettings{putAck: true}
	votingErr := errors.New("master not available")
	vx := &fakeVotingExclusion{addErr: votingErr}

	got, err := ops.DrainNode(context.Background(), drainDeps(ni, si, cs, vx), types.NodeDrainRequest{
		NodeNames:         []string{"es01"},
		AlsoExcludeVoting: true,
	})
	if err == nil {
		t.Fatalf("expected error when voting POST fails")
	}
	if !errors.Is(err, votingErr) {
		t.Errorf("err = %v, want it to wrap %v", err, votingErr)
	}
	if !got.Acknowledged {
		t.Errorf("allocation Acknowledged = false; expected to round-trip even on voting failure")
	}
	if got.VotingExclusionAcked {
		t.Errorf("VotingExclusionAcked = true; expected false on voting failure")
	}
	if len(got.VotingExclusionsAdded) != 0 {
		t.Errorf("VotingExclusionsAdded should be empty when the POST failed: %v", got.VotingExclusionsAdded)
	}
}

func TestDrainNode_waitReturnsWhenShardCountHitsZero(t *testing.T) {
	ni := threeNodeFixture()
	// Two stages: first poll sees one shard each on es01/es02,
	// second poll sees them relocated to es03 — drain is complete.
	si := &fakeDrainShards{stages: [][]types.Shard{
		{
			{Index: "logs", Shard: 0, Node: "es01"},
			{Index: "logs", Shard: 0, Node: "es02"},
			{Index: "logs", Shard: 0, Node: "es03"},
		},
		{
			{Index: "logs", Shard: 0, Node: "es03"},
			{Index: "logs", Shard: 0, Node: "es03"},
			{Index: "logs", Shard: 0, Node: "es03"},
		},
	}}
	cs := &fakeClusterSettings{putAck: true}

	got, err := ops.DrainNode(context.Background(), drainDeps(ni, si, cs, newFakeVoting()), types.NodeDrainRequest{
		NodeNames:    []string{"es01", "es02"},
		Wait:         true,
		WaitInterval: time.Millisecond,
		WaitTimeout:  time.Second,
	})
	if err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	if !got.Waited {
		t.Errorf("Waited = false, want true")
	}
	if got.ShardsRemaining != 0 {
		t.Errorf("ShardsRemaining = %d, want 0", got.ShardsRemaining)
	}
	for _, name := range []string{"es01", "es02"} {
		if got.PerNodeShardsRemaining[name] != 0 {
			t.Errorf("PerNodeShardsRemaining[%s] = %d, want 0", name, got.PerNodeShardsRemaining[name])
		}
	}
}

func TestDrainNode_waitTimesOutWithPerNodeBreakdown(t *testing.T) {
	ni := threeNodeFixture()
	// Both drained nodes still hold a shard each.
	si := &fakeDrainShards{stages: [][]types.Shard{{
		{Index: "logs", Shard: 0, Node: "es01"},
		{Index: "logs", Shard: 1, Node: "es02"},
		{Index: "logs", Shard: 2, Node: "es02"},
	}}}
	cs := &fakeClusterSettings{putAck: true}

	got, err := ops.DrainNode(context.Background(), drainDeps(ni, si, cs, newFakeVoting()), types.NodeDrainRequest{
		NodeNames:    []string{"es01", "es02"},
		Wait:         true,
		WaitInterval: time.Millisecond,
		WaitTimeout:  10 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "drain timeout") {
		t.Errorf("err = %v, want drain-timeout error", err)
	}
	if got.ShardsRemaining != 3 {
		t.Errorf("ShardsRemaining = %d, want 3", got.ShardsRemaining)
	}
	if got.PerNodeShardsRemaining["es01"] != 1 || got.PerNodeShardsRemaining["es02"] != 2 {
		t.Errorf("per-node breakdown wrong: %+v", got.PerNodeShardsRemaining)
	}
}

func TestDrainNode_propagatesGetSettingsError(t *testing.T) {
	sentinel := errors.New("boom")
	cs := &fakeClusterSettings{getErr: sentinel}
	_, err := ops.DrainNode(context.Background(), drainDeps(twoNodeFixture(), &fakeDrainShards{stages: [][]types.Shard{nil}}, cs, newFakeVoting()),
		types.NodeDrainRequest{NodeNames: []string{"es01"}})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestListDrained_returnsBothListsSorted(t *testing.T) {
	cs := &fakeClusterSettings{view: types.ClusterSettingsView{
		PersistentExcludeName: []string{"es02", "es01"},
		TransientExcludeName:  []string{"es04"},
	}}
	vx := &fakeVotingExclusion{listed: []string{"es05", "es03"}}

	got, err := ops.ListDrained(context.Background(), cs, vx)
	if err != nil {
		t.Fatalf("ListDrained: %v", err)
	}
	if !reflect.DeepEqual(got.PersistentExcluded, []string{"es01", "es02"}) {
		t.Errorf("PersistentExcluded = %v, want sorted [es01 es02]", got.PersistentExcluded)
	}
	if !reflect.DeepEqual(got.TransientExcluded, []string{"es04"}) {
		t.Errorf("TransientExcluded = %v", got.TransientExcluded)
	}
	if !reflect.DeepEqual(got.VotingExcluded, []string{"es03", "es05"}) {
		t.Errorf("VotingExcluded = %v, want sorted [es03 es05]", got.VotingExcluded)
	}
}

func TestListDrained_nilVotingCapabilityIsTolerated(t *testing.T) {
	// Test fakes may pass nil for voting; the read-only listing
	// should still surface the allocation half rather than panicking
	// or erroring.
	cs := &fakeClusterSettings{view: types.ClusterSettingsView{
		TransientExcludeName: []string{"es01"},
	}}
	got, err := ops.ListDrained(context.Background(), cs, nil)
	if err != nil {
		t.Fatalf("ListDrained: %v", err)
	}
	if !reflect.DeepEqual(got.TransientExcluded, []string{"es01"}) {
		t.Errorf("TransientExcluded = %v", got.TransientExcluded)
	}
	if got.VotingExcluded != nil {
		t.Errorf("VotingExcluded should be nil when voting cap is not wired")
	}
}
