package ops_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/ops"
	"github.com/dj-wasabi/esops-go/internal/types"
)

func uncordonDeps(cs *fakeClusterSettings, vx *fakeVotingExclusion) ops.UncordonDeps {
	return ops.UncordonDeps{Settings: cs, Voting: vx}
}

func TestUncordonNode_removesFromTransient(t *testing.T) {
	cs := &fakeClusterSettings{
		view:   types.ClusterSettingsView{TransientExcludeName: []string{"es01", "es02"}},
		putAck: true,
	}
	got, err := ops.UncordonNode(context.Background(), uncordonDeps(cs, newFakeVoting()), types.NodeUncordonRequest{
		NodeNames: []string{"es01"},
	})
	if err != nil {
		t.Fatalf("UncordonNode: %v", err)
	}
	if !cs.putCalled {
		t.Fatalf("expected put to be called")
	}
	if !reflect.DeepEqual(cs.put.Names, []string{"es02"}) {
		t.Errorf("put.Names = %v, want [es02]", cs.put.Names)
	}
	if len(got.NotInList) != 0 || !got.Acknowledged {
		t.Errorf("got %+v, want NotInList=[] Acknowledged=true", got)
	}
}

func TestUncordonNode_multiNodeRemovesAll(t *testing.T) {
	cs := &fakeClusterSettings{
		view:   types.ClusterSettingsView{TransientExcludeName: []string{"es01", "es02", "es03"}},
		putAck: true,
	}
	got, err := ops.UncordonNode(context.Background(), uncordonDeps(cs, newFakeVoting()), types.NodeUncordonRequest{
		NodeNames: []string{"es01", "es03"},
	})
	if err != nil {
		t.Fatalf("UncordonNode: %v", err)
	}
	if !reflect.DeepEqual(cs.put.Names, []string{"es02"}) {
		t.Errorf("put.Names = %v, want [es02]", cs.put.Names)
	}
	if len(got.NotInList) != 0 {
		t.Errorf("NotInList = %v, want empty", got.NotInList)
	}
}

func TestUncordonNode_noOpWhenNoneInList(t *testing.T) {
	cs := &fakeClusterSettings{view: types.ClusterSettingsView{
		TransientExcludeName: []string{"es99"},
	}}
	got, err := ops.UncordonNode(context.Background(), uncordonDeps(cs, newFakeVoting()), types.NodeUncordonRequest{
		NodeNames: []string{"es01", "es02"},
	})
	if err != nil {
		t.Fatalf("UncordonNode: %v", err)
	}
	if cs.putCalled {
		t.Errorf("put should not be called when none of the nodes are in the exclude list")
	}
	if !reflect.DeepEqual(got.NotInList, []string{"es01", "es02"}) {
		t.Errorf("NotInList = %v, want [es01 es02]", got.NotInList)
	}
}

func TestUncordonNode_partialNotInListStillTriggersPut(t *testing.T) {
	cs := &fakeClusterSettings{
		view:   types.ClusterSettingsView{TransientExcludeName: []string{"es01"}},
		putAck: true,
	}
	got, err := ops.UncordonNode(context.Background(), uncordonDeps(cs, newFakeVoting()), types.NodeUncordonRequest{
		NodeNames: []string{"es01", "es02"},
	})
	if err != nil {
		t.Fatalf("UncordonNode: %v", err)
	}
	if !cs.putCalled {
		t.Fatalf("partial overlap must still PUT to remove the matched node")
	}
	if !reflect.DeepEqual(got.NotInList, []string{"es02"}) {
		t.Errorf("NotInList = %v, want [es02]", got.NotInList)
	}
}

func TestUncordonNode_dryRunDoesNotMutate(t *testing.T) {
	cs := &fakeClusterSettings{
		view:   types.ClusterSettingsView{TransientExcludeName: []string{"es01"}},
		putAck: true,
	}
	vx := newFakeVoting()
	got, err := ops.UncordonNode(context.Background(), uncordonDeps(cs, vx), types.NodeUncordonRequest{
		NodeNames:   []string{"es01"},
		DryRun:      true,
		ClearVoting: true,
	})
	if err != nil {
		t.Fatalf("UncordonNode: %v", err)
	}
	if cs.putCalled {
		t.Errorf("dry-run must not call PutAllocationExclude")
	}
	if vx.clearCalled {
		t.Errorf("dry-run must not call ClearVotingExclusions")
	}
	if len(got.After) != 0 {
		t.Errorf("dry-run After = %v, want empty (the would-be result)", got.After)
	}
}

func TestUncordonNode_persistentScopeMustMatch(t *testing.T) {
	cs := &fakeClusterSettings{view: types.ClusterSettingsView{
		TransientExcludeName: []string{"es01"},
	}}
	got, err := ops.UncordonNode(context.Background(), uncordonDeps(cs, newFakeVoting()), types.NodeUncordonRequest{
		NodeNames:  []string{"es01"},
		Persistent: true,
	})
	if err != nil {
		t.Fatalf("UncordonNode: %v", err)
	}
	if cs.putCalled {
		t.Errorf("scope mismatch should not trigger a put")
	}
	if !reflect.DeepEqual(got.NotInList, []string{"es01"}) {
		t.Errorf("NotInList = %v, want [es01] on scope mismatch", got.NotInList)
	}
}

func TestUncordonNode_unacknowledgedReturnsBlocker(t *testing.T) {
	cs := &fakeClusterSettings{view: types.ClusterSettingsView{
		TransientExcludeName: []string{"es01"},
	}} // putAck=false → cluster did not ack
	_, err := ops.UncordonNode(context.Background(), uncordonDeps(cs, newFakeVoting()), types.NodeUncordonRequest{
		NodeNames: []string{"es01"},
	})
	if !errors.Is(err, client.ErrDryRunBlocker) {
		t.Errorf("err = %v, want ErrDryRunBlocker", err)
	}
}

func TestUncordonNode_propagatesGetSettingsError(t *testing.T) {
	sentinel := errors.New("boom")
	cs := &fakeClusterSettings{getErr: sentinel}
	_, err := ops.UncordonNode(context.Background(), uncordonDeps(cs, newFakeVoting()), types.NodeUncordonRequest{
		NodeNames: []string{"es01"},
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestUncordonNode_rejectsEmptyRequest(t *testing.T) {
	cs := &fakeClusterSettings{}
	_, err := ops.UncordonNode(context.Background(), uncordonDeps(cs, newFakeVoting()), types.NodeUncordonRequest{})
	if err == nil {
		t.Fatalf("empty request should fail validation (need --node or --clear-voting)")
	}
}

func TestUncordonNode_clearVotingOnlyDoesNotCallPut(t *testing.T) {
	cs := &fakeClusterSettings{}
	vx := newFakeVoting()
	got, err := ops.UncordonNode(context.Background(), uncordonDeps(cs, vx), types.NodeUncordonRequest{
		ClearVoting: true,
	})
	if err != nil {
		t.Fatalf("UncordonNode: %v", err)
	}
	if cs.putCalled {
		t.Errorf("clear-voting alone must not touch /_cluster/settings")
	}
	if !vx.clearCalled {
		t.Fatalf("clear-voting must call ClearVotingExclusions")
	}
	if !got.VotingExclusionsCleared || !got.VotingExclusionAcked {
		t.Errorf("got %+v, want VotingExclusionsCleared=true and Acked=true", got)
	}
}

func TestUncordonNode_clearVotingPairedWithUncordon(t *testing.T) {
	cs := &fakeClusterSettings{
		view:   types.ClusterSettingsView{TransientExcludeName: []string{"es01"}},
		putAck: true,
	}
	vx := newFakeVoting()
	got, err := ops.UncordonNode(context.Background(), uncordonDeps(cs, vx), types.NodeUncordonRequest{
		NodeNames:   []string{"es01"},
		ClearVoting: true,
	})
	if err != nil {
		t.Fatalf("UncordonNode: %v", err)
	}
	if !cs.putCalled {
		t.Errorf("expected put to remove es01 from allocation excludes")
	}
	if !vx.clearCalled {
		t.Errorf("expected ClearVotingExclusions to fire")
	}
	if !got.VotingExclusionsCleared {
		t.Errorf("VotingExclusionsCleared = false, want true")
	}
}

func TestUncordonNode_votingClearFailureSurfacesError(t *testing.T) {
	cs := &fakeClusterSettings{
		view:   types.ClusterSettingsView{TransientExcludeName: []string{"es01"}},
		putAck: true,
	}
	votingErr := errors.New("master not available")
	vx := &fakeVotingExclusion{clearErr: votingErr}

	got, err := ops.UncordonNode(context.Background(), uncordonDeps(cs, vx), types.NodeUncordonRequest{
		NodeNames:   []string{"es01"},
		ClearVoting: true,
	})
	if !errors.Is(err, votingErr) {
		t.Errorf("err = %v, want it to wrap %v", err, votingErr)
	}
	if !got.Acknowledged {
		t.Errorf("allocation Acknowledged should still be true on voting failure")
	}
	if got.VotingExclusionsCleared {
		t.Errorf("VotingExclusionsCleared should not be set when the call failed")
	}
}
