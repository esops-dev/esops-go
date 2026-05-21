package index_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/index"
	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeStateChanger captures the request the business layer issues to
// the adapter and returns a canned response. Same shape as
// fakeIndexInspector in list_test.go, kept private to its test file
// because the two fakes carry different surfaces.
type fakeStateChanger struct {
	openCalls  []types.IndexStateChangeRequest
	closeCalls []types.IndexStateChangeRequest
	resp       types.IndexStateChange
	err        error
}

func (f *fakeStateChanger) OpenIndex(_ context.Context, req types.IndexStateChangeRequest) (types.IndexStateChange, error) {
	f.openCalls = append(f.openCalls, req)
	if f.err != nil {
		return types.IndexStateChange{}, f.err
	}
	r := f.resp
	if r.Action == "" {
		r.Action = types.IndexStateOpen
	}
	return r, nil
}

func (f *fakeStateChanger) CloseIndex(_ context.Context, req types.IndexStateChangeRequest) (types.IndexStateChange, error) {
	f.closeCalls = append(f.closeCalls, req)
	if f.err != nil {
		return types.IndexStateChange{}, f.err
	}
	r := f.resp
	if r.Action == "" {
		r.Action = types.IndexStateClose
	}
	return r, nil
}

func ackResponse() types.IndexStateChange {
	return types.IndexStateChange{Acknowledged: true, ShardsAcknowledged: true}
}

func TestParseStateAction_knownValues(t *testing.T) {
	cases := []struct {
		in   string
		want types.IndexStateAction
	}{
		{"open", types.IndexStateOpen},
		{"OPEN", types.IndexStateOpen},
		{"close", types.IndexStateClose},
		{" Close ", types.IndexStateClose},
	}
	for _, c := range cases {
		got, err := index.ParseStateAction(c.in)
		if err != nil {
			t.Errorf("ParseStateAction(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseStateAction(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseStateAction_rejectsUnknown(t *testing.T) {
	_, err := index.ParseStateAction("freeze")
	if err == nil || !strings.Contains(err.Error(), "freeze") {
		t.Errorf("expected error naming the rejected verb, got: %v", err)
	}
}

func TestChangeState_rejectsEmptyPattern(t *testing.T) {
	insp := &fakeIndexInspector{}
	chg := &fakeStateChanger{resp: ackResponse()}
	_, err := index.ChangeState(context.Background(), insp, chg, index.StateChangeOptions{
		Action: types.IndexStateClose,
	})
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
	if len(chg.closeCalls) != 0 {
		t.Errorf("cluster was contacted on validation failure: %+v", chg.closeCalls)
	}
}

func TestChangeState_rejectsMatchAllPattern(t *testing.T) {
	for _, pattern := range []string{"*", "_all"} {
		insp := &fakeIndexInspector{}
		chg := &fakeStateChanger{resp: ackResponse()}
		_, err := index.ChangeState(context.Background(), insp, chg, index.StateChangeOptions{
			Action:  types.IndexStateClose,
			Pattern: pattern,
		})
		if !errors.Is(err, esopsclient.ErrBlastRadius) {
			t.Errorf("pattern %q: err = %v, want ErrBlastRadius", pattern, err)
		}
	}
}

func TestChangeState_close_partitionsOpenVsClosed(t *testing.T) {
	insp := &fakeIndexInspector{indices: []types.Index{
		{Name: "logs-2024", Status: "open"},
		{Name: "logs-2023", Status: "close"},
		{Name: "logs-2022", Status: "open"},
	}}
	chg := &fakeStateChanger{resp: ackResponse()}
	got, err := index.ChangeState(context.Background(), insp, chg, index.StateChangeOptions{
		Action:  types.IndexStateClose,
		Pattern: "logs-*",
	})
	if err != nil {
		t.Fatalf("ChangeState: %v", err)
	}
	wantTarget := []string{"logs-2022", "logs-2024"}
	wantSkip := []string{"logs-2023"}
	if !equalSlices(got.Indices, wantTarget) {
		t.Errorf("Indices = %v, want %v", got.Indices, wantTarget)
	}
	if !equalSlices(got.Skipped, wantSkip) {
		t.Errorf("Skipped = %v, want %v", got.Skipped, wantSkip)
	}
	if len(chg.closeCalls) != 1 {
		t.Fatalf("expected 1 close call, got %d", len(chg.closeCalls))
	}
	// Cluster receives the resolved name list, not the raw pattern.
	wantIndices := []string{"logs-2022", "logs-2024"}
	if !equalSlices(chg.closeCalls[0].Indices, wantIndices) {
		t.Errorf("close request Indices = %v, want %v", chg.closeCalls[0].Indices, wantIndices)
	}
}

func TestChangeState_open_targetsClosedIndices(t *testing.T) {
	insp := &fakeIndexInspector{indices: []types.Index{
		{Name: "logs-2024", Status: "open"},
		{Name: "logs-2023", Status: "close"},
		{Name: "logs-2022", Status: "close"},
	}}
	chg := &fakeStateChanger{resp: ackResponse()}
	got, err := index.ChangeState(context.Background(), insp, chg, index.StateChangeOptions{
		Action:  types.IndexStateOpen,
		Pattern: "logs-*",
	})
	if err != nil {
		t.Fatalf("ChangeState: %v", err)
	}
	wantTarget := []string{"logs-2022", "logs-2023"}
	wantSkip := []string{"logs-2024"}
	if !equalSlices(got.Indices, wantTarget) {
		t.Errorf("Indices = %v, want %v", got.Indices, wantTarget)
	}
	if !equalSlices(got.Skipped, wantSkip) {
		t.Errorf("Skipped = %v, want %v", got.Skipped, wantSkip)
	}
	if len(chg.openCalls) != 1 {
		t.Fatalf("expected 1 open call, got %d", len(chg.openCalls))
	}
}

func TestChangeState_blastRadius_refusesAt21WithDefault(t *testing.T) {
	// 21 open indices, all matching the pattern. Default cap is 20,
	// so this must refuse before issuing the request.
	rows := make([]types.Index, 21)
	for i := range rows {
		rows[i] = types.Index{Name: indexName(i), Status: "open"}
	}
	insp := &fakeIndexInspector{indices: rows}
	chg := &fakeStateChanger{resp: ackResponse()}
	_, err := index.ChangeState(context.Background(), insp, chg, index.StateChangeOptions{
		Action:  types.IndexStateClose,
		Pattern: "rotated-*",
	})
	if !errors.Is(err, esopsclient.ErrBlastRadius) {
		t.Errorf("err = %v, want ErrBlastRadius", err)
	}
	if len(chg.closeCalls) != 0 {
		t.Errorf("cluster contacted past blast-radius limit: %+v", chg.closeCalls)
	}
}

func TestChangeState_blastRadius_overrideLetsItThrough(t *testing.T) {
	rows := make([]types.Index, 25)
	for i := range rows {
		rows[i] = types.Index{Name: indexName(i), Status: "open"}
	}
	insp := &fakeIndexInspector{indices: rows}
	chg := &fakeStateChanger{resp: ackResponse()}
	_, err := index.ChangeState(context.Background(), insp, chg, index.StateChangeOptions{
		Action:     types.IndexStateClose,
		Pattern:    "rotated-*",
		MaxIndices: 50,
	})
	if err != nil {
		t.Fatalf("ChangeState: %v", err)
	}
	if len(chg.closeCalls) != 1 {
		t.Errorf("expected 1 close call with override, got %d", len(chg.closeCalls))
	}
}

func TestChangeState_blastRadius_skippedDoesNotCount(t *testing.T) {
	// 30 closed indices match the pattern. For action=close, *zero*
	// would change — and the limit only governs the would-change set.
	rows := make([]types.Index, 30)
	for i := range rows {
		rows[i] = types.Index{Name: indexName(i), Status: "close"}
	}
	insp := &fakeIndexInspector{indices: rows}
	chg := &fakeStateChanger{resp: ackResponse()}
	got, err := index.ChangeState(context.Background(), insp, chg, index.StateChangeOptions{
		Action:  types.IndexStateClose,
		Pattern: "rotated-*",
	})
	if err != nil {
		t.Fatalf("ChangeState: %v", err)
	}
	if len(got.Indices) != 0 {
		t.Errorf("expected 0 changed, got %d", len(got.Indices))
	}
	if len(got.Skipped) != 30 {
		t.Errorf("expected 30 skipped, got %d", len(got.Skipped))
	}
	if len(chg.closeCalls) != 0 {
		t.Errorf("cluster contacted on no-op: %+v", chg.closeCalls)
	}
	if !got.Acknowledged {
		t.Errorf("no-op should report Acknowledged=true (got %+v)", got)
	}
}

func TestChangeState_dryRun_doesNotCallCluster(t *testing.T) {
	insp := &fakeIndexInspector{indices: []types.Index{
		{Name: "logs-2024", Status: "open"},
		{Name: "logs-2023", Status: "open"},
	}}
	chg := &fakeStateChanger{resp: ackResponse()}
	got, err := index.ChangeState(context.Background(), insp, chg, index.StateChangeOptions{
		Action:  types.IndexStateClose,
		Pattern: "logs-*",
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("ChangeState: %v", err)
	}
	if !got.DryRun {
		t.Errorf("DryRun = false on result, want true")
	}
	if !got.Acknowledged {
		t.Errorf("dry-run should ack synthetically (got %+v)", got)
	}
	if len(chg.closeCalls) != 0 {
		t.Errorf("dry-run contacted cluster: %+v", chg.closeCalls)
	}
	wantTarget := []string{"logs-2023", "logs-2024"}
	if !equalSlices(got.Indices, wantTarget) {
		t.Errorf("Indices = %v, want %v", got.Indices, wantTarget)
	}
}

func TestChangeState_emptyMatchIsNoOp(t *testing.T) {
	insp := &fakeIndexInspector{indices: nil}
	chg := &fakeStateChanger{resp: ackResponse()}
	got, err := index.ChangeState(context.Background(), insp, chg, index.StateChangeOptions{
		Action:  types.IndexStateClose,
		Pattern: "missing-*",
	})
	if err != nil {
		t.Fatalf("ChangeState: %v", err)
	}
	if len(got.Indices) != 0 || len(got.Skipped) != 0 {
		t.Errorf("expected empty result, got %+v", got)
	}
	if !got.Acknowledged {
		t.Errorf("empty match should ack synthetically (got %+v)", got)
	}
	if len(chg.closeCalls) != 0 {
		t.Errorf("empty match contacted cluster: %+v", chg.closeCalls)
	}
}

func TestChangeState_propagatesAdapterError(t *testing.T) {
	sentinel := errors.New("boom")
	insp := &fakeIndexInspector{indices: []types.Index{
		{Name: "logs", Status: "open"},
	}}
	chg := &fakeStateChanger{err: sentinel}
	_, err := index.ChangeState(context.Background(), insp, chg, index.StateChangeOptions{
		Action:  types.IndexStateClose,
		Pattern: "logs",
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

func TestChangeState_passesIgnoreUnavailableThrough(t *testing.T) {
	insp := &fakeIndexInspector{indices: []types.Index{
		{Name: "logs", Status: "open"},
	}}
	chg := &fakeStateChanger{resp: ackResponse()}
	_, err := index.ChangeState(context.Background(), insp, chg, index.StateChangeOptions{
		Action:              types.IndexStateClose,
		Pattern:             "logs",
		IgnoreUnavailable:   true,
		WaitForActiveShards: "all",
	})
	if err != nil {
		t.Fatalf("ChangeState: %v", err)
	}
	if len(chg.closeCalls) != 1 {
		t.Fatalf("expected 1 close call, got %d", len(chg.closeCalls))
	}
	got := chg.closeCalls[0]
	if !got.IgnoreUnavailable {
		t.Errorf("IgnoreUnavailable = false on cluster request, want true")
	}
	if got.WaitForActiveShards != "all" {
		t.Errorf("WaitForActiveShards = %q on cluster request, want %q", got.WaitForActiveShards, "all")
	}
}

func indexName(i int) string {
	return "rotated-" + intToBase10(i)
}

// intToBase10 avoids pulling strconv just for this helper. The test
// suite is small enough that the loop is faster than the import churn.
func intToBase10(i int) string {
	if i == 0 {
		return "0"
	}
	var out []byte
	for i > 0 {
		out = append([]byte{byte('0' + i%10)}, out...)
		i /= 10
	}
	return string(out)
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
