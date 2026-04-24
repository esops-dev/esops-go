package ops_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dj-wasabi/esops-go/internal/ops"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// fakeAllocationExplainer records the request it was called with and
// returns a canned response. Same pattern as fakeShardInspector.
type fakeAllocationExplainer struct {
	got types.AllocationExplainRequest
	out types.AllocationExplanation
	err error
}

func (f *fakeAllocationExplainer) AllocationExplain(_ context.Context, req types.AllocationExplainRequest) (types.AllocationExplanation, error) {
	f.got = req
	return f.out, f.err
}

func TestAllocationExplain_passesRequestThrough(t *testing.T) {
	fake := &fakeAllocationExplainer{out: types.AllocationExplanation{Index: "logs", Shard: 2}}
	in := types.AllocationExplainRequest{
		Index:               "logs",
		Shard:               2,
		Primary:             true,
		IncludeDiskInfo:     true,
		IncludeYesDecisions: true,
	}
	got, err := ops.AllocationExplain(context.Background(), fake, in)
	if err != nil {
		t.Fatalf("AllocationExplain: %v", err)
	}
	if fake.got != in {
		t.Errorf("adapter got %+v, want %+v", fake.got, in)
	}
	if got.Index != "logs" || got.Shard != 2 {
		t.Errorf("got %+v, want Index=logs Shard=2", got)
	}
}

func TestAllocationExplain_propagatesError(t *testing.T) {
	sentinel := errors.New("boom")
	fake := &fakeAllocationExplainer{err: sentinel}
	_, err := ops.AllocationExplain(context.Background(), fake, types.AllocationExplainRequest{})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestAllocationExplain_rejectsShardWithoutIndex(t *testing.T) {
	fake := &fakeAllocationExplainer{}
	_, err := ops.AllocationExplain(context.Background(), fake, types.AllocationExplainRequest{Shard: 3})
	if err == nil || !strings.Contains(err.Error(), "--shard requires --index") {
		t.Errorf("err = %v, want shard-requires-index error", err)
	}
	// Must fail-fast before the adapter is called.
	if fake.got.Index != "" || fake.got.Shard != 0 {
		t.Errorf("adapter must not be invoked when validation fails; got %+v", fake.got)
	}
}

func TestAllocationExplain_rejectsPrimaryWithoutIndex(t *testing.T) {
	fake := &fakeAllocationExplainer{}
	_, err := ops.AllocationExplain(context.Background(), fake, types.AllocationExplainRequest{Primary: true})
	if err == nil || !strings.Contains(err.Error(), "--primary requires --index") {
		t.Errorf("err = %v, want primary-requires-index error", err)
	}
}

func TestAllocationExplain_allowsEmptyRequest(t *testing.T) {
	// Zero-value request is the "let the cluster pick an unassigned shard"
	// case — must not be rejected by the business layer.
	fake := &fakeAllocationExplainer{out: types.AllocationExplanation{CurrentState: "unassigned"}}
	got, err := ops.AllocationExplain(context.Background(), fake, types.AllocationExplainRequest{})
	if err != nil {
		t.Fatalf("AllocationExplain: %v", err)
	}
	if got.CurrentState != "unassigned" {
		t.Errorf("got %+v, want state=unassigned", got)
	}
}
