package index_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/index"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeRollover captures the request the business layer issues and
// returns a canned response. No pattern resolution path here — rollover
// targets a single alias the operator names, so it's a thinner adapter
// than open/close/optimize.
type fakeRollover struct {
	calls []types.IndexRolloverRequest
	resp  types.IndexRolloverResult
	err   error
}

func (f *fakeRollover) Rollover(_ context.Context, req types.IndexRolloverRequest) (types.IndexRolloverResult, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return types.IndexRolloverResult{}, f.err
	}
	r := f.resp
	if r.Alias == "" {
		r.Alias = req.Alias
	}
	return r, nil
}

func okRolloverResp() types.IndexRolloverResult {
	return types.IndexRolloverResult{
		Acknowledged: true, ShardsAcknowledged: true,
		RolledOver: true,
		OldIndex:   "esops-logs-000001",
		NewIndex:   "esops-logs-000002",
	}
}

func TestRollover_rejectsEmptyAlias(t *testing.T) {
	ro := &fakeRollover{resp: okRolloverResp()}
	_, err := index.Rollover(context.Background(), ro, index.RolloverOptions{
		Conditions: types.IndexRolloverConditions{MaxDocs: 1000},
	})
	if err == nil {
		t.Fatal("expected error for empty alias")
	}
	if len(ro.calls) != 0 {
		t.Errorf("cluster contacted on validation failure: %+v", ro.calls)
	}
}

func TestRollover_rejectsUnconditionalWithoutForce(t *testing.T) {
	ro := &fakeRollover{resp: okRolloverResp()}
	_, err := index.Rollover(context.Background(), ro, index.RolloverOptions{
		Alias: "esops-logs",
	})
	if err == nil || !strings.Contains(err.Error(), "force") {
		t.Errorf("expected error mentioning --force, got: %v", err)
	}
	if len(ro.calls) != 0 {
		t.Errorf("cluster contacted on validation failure: %+v", ro.calls)
	}
}

func TestRollover_unconditionalAcceptsForce(t *testing.T) {
	ro := &fakeRollover{resp: okRolloverResp()}
	_, err := index.Rollover(context.Background(), ro, index.RolloverOptions{
		Alias: "esops-logs", Force: true,
	})
	if err != nil {
		t.Fatalf("Rollover: %v", err)
	}
	if len(ro.calls) != 1 {
		t.Fatalf("expected 1 rollover call, got %d", len(ro.calls))
	}
}

func TestRollover_rejectsNegativeMaxDocs(t *testing.T) {
	ro := &fakeRollover{resp: okRolloverResp()}
	_, err := index.Rollover(context.Background(), ro, index.RolloverOptions{
		Alias:      "esops-logs",
		Conditions: types.IndexRolloverConditions{MaxDocs: -1},
	})
	if err == nil || !strings.Contains(err.Error(), "max-docs") {
		t.Errorf("expected error mentioning max-docs, got: %v", err)
	}
}

func TestRollover_passesConditionsThrough(t *testing.T) {
	ro := &fakeRollover{resp: okRolloverResp()}
	_, err := index.Rollover(context.Background(), ro, index.RolloverOptions{
		Alias: "esops-logs",
		Conditions: types.IndexRolloverConditions{
			MaxAge:         "7d",
			MaxDocs:        1_000_000,
			MaxSize:        "50gb",
			MaxPrimarySize: "25gb",
		},
		WaitForActiveShards: "all",
	})
	if err != nil {
		t.Fatalf("Rollover: %v", err)
	}
	if len(ro.calls) != 1 {
		t.Fatalf("expected 1 rollover call, got %d", len(ro.calls))
	}
	got := ro.calls[0]
	if got.Alias != "esops-logs" {
		t.Errorf("Alias = %q", got.Alias)
	}
	if got.Conditions.MaxAge != "7d" || got.Conditions.MaxDocs != 1_000_000 {
		t.Errorf("conditions not forwarded: %+v", got.Conditions)
	}
	if got.Conditions.MaxSize != "50gb" || got.Conditions.MaxPrimarySize != "25gb" {
		t.Errorf("size conditions not forwarded: %+v", got.Conditions)
	}
	if got.WaitForActiveShards != "all" {
		t.Errorf("WaitForActiveShards = %q, want 'all'", got.WaitForActiveShards)
	}
}

func TestRollover_dryRunForwardedToCluster(t *testing.T) {
	ro := &fakeRollover{resp: types.IndexRolloverResult{
		Acknowledged: true, RolledOver: false, DryRun: true,
		OldIndex:      "esops-logs-000001",
		ConditionsMet: map[string]bool{"max_age": false},
	}}
	got, err := index.Rollover(context.Background(), ro, index.RolloverOptions{
		Alias:      "esops-logs",
		Conditions: types.IndexRolloverConditions{MaxAge: "30d"},
		DryRun:     true,
	})
	if err != nil {
		t.Fatalf("Rollover: %v", err)
	}
	if !got.DryRun {
		t.Errorf("DryRun = false, want true")
	}
	if got.RolledOver {
		t.Errorf("RolledOver = true on no-match dry-run")
	}
	if len(ro.calls) != 1 || !ro.calls[0].DryRun {
		t.Errorf("DryRun not forwarded to cluster: %+v", ro.calls)
	}
}

func TestRollover_propagatesAdapterError(t *testing.T) {
	sentinel := errors.New("boom")
	ro := &fakeRollover{err: sentinel}
	_, err := index.Rollover(context.Background(), ro, index.RolloverOptions{
		Alias:      "esops-logs",
		Conditions: types.IndexRolloverConditions{MaxDocs: 100},
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

func TestRolloverConditions_IsEmpty(t *testing.T) {
	cases := []struct {
		c    types.IndexRolloverConditions
		want bool
	}{
		{types.IndexRolloverConditions{}, true},
		{types.IndexRolloverConditions{MaxAge: "1d"}, false},
		{types.IndexRolloverConditions{MaxDocs: 100}, false},
		{types.IndexRolloverConditions{MaxSize: "10gb"}, false},
		{types.IndexRolloverConditions{MaxPrimarySize: "5gb"}, false},
	}
	for i, c := range cases {
		if got := c.c.IsEmpty(); got != c.want {
			t.Errorf("case %d: IsEmpty() = %v, want %v", i, got, c.want)
		}
	}
}
