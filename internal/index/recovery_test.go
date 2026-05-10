package index_test

import (
	"context"
	"errors"
	"testing"

	"github.com/esops-dev/esops-go/internal/index"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeRecoveryInspector records the request the business layer issues
// and returns a canned response. Mirrors fakeIndexInspector in shape.
type fakeRecoveryInspector struct {
	got    types.RecoveryRequest
	report types.RecoveryReport
	err    error
}

func (f *fakeRecoveryInspector) Recovery(_ context.Context, req types.RecoveryRequest) (types.RecoveryReport, error) {
	f.got = req
	return f.report, f.err
}

func TestRecovery_forwardsPatternAndActiveOnly(t *testing.T) {
	fake := &fakeRecoveryInspector{}
	_, err := index.Recovery(context.Background(), fake, index.RecoveryOptions{
		Pattern:    "logs-*",
		ActiveOnly: true,
	})
	if err != nil {
		t.Fatalf("Recovery: %v", err)
	}
	if fake.got.Indices != "logs-*" {
		t.Errorf("adapter got Indices = %q, want logs-*", fake.got.Indices)
	}
	if !fake.got.ActiveOnly {
		t.Errorf("adapter got ActiveOnly = false, want true")
	}
}

func TestRecovery_propagatesError(t *testing.T) {
	sentinel := errors.New("boom")
	fake := &fakeRecoveryInspector{err: sentinel}
	_, err := index.Recovery(context.Background(), fake, index.RecoveryOptions{})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

func TestRecovery_sortsIndicesByName(t *testing.T) {
	fake := &fakeRecoveryInspector{report: types.RecoveryReport{
		Indices: []types.IndexRecovery{
			{Index: "users", Shards: []types.ShardRecovery{{ID: 0, Stage: "DONE"}}},
			{Index: "logs", Shards: []types.ShardRecovery{{ID: 0, Stage: "INDEX"}}},
			{Index: "archive", Shards: []types.ShardRecovery{{ID: 0, Stage: "INIT"}}},
		},
	}}
	got, err := index.Recovery(context.Background(), fake, index.RecoveryOptions{})
	if err != nil {
		t.Fatalf("Recovery: %v", err)
	}
	want := []string{"archive", "logs", "users"}
	if len(got.Indices) != len(want) {
		t.Fatalf("got %d indices, want %d", len(got.Indices), len(want))
	}
	for i, n := range want {
		if got.Indices[i].Index != n {
			t.Errorf("row %d = %s, want %s", i, got.Indices[i].Index, n)
		}
	}
}

func TestRecovery_emptyPatternForwardsEmpty(t *testing.T) {
	fake := &fakeRecoveryInspector{}
	_, err := index.Recovery(context.Background(), fake, index.RecoveryOptions{
		Pattern: "  ",
	})
	if err != nil {
		t.Fatalf("Recovery: %v", err)
	}
	// Whitespace-only pattern collapses to empty so the adapter falls
	// through to its "_all" default rather than escaping a space into
	// the URL path.
	if fake.got.Indices != "" {
		t.Errorf("adapter got Indices = %q, want empty (whitespace stripped)", fake.got.Indices)
	}
}
