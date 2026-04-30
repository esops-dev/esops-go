package index_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dj-wasabi/esops-go/internal/index"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// fakeAliasInspector returns a canned response and records the filter
// it was handed.
type fakeAliasInspector struct {
	got     types.AliasFilter
	aliases []types.Alias
	err     error
}

func (f *fakeAliasInspector) Aliases(_ context.Context, filter types.AliasFilter) ([]types.Alias, error) {
	f.got = filter
	return f.aliases, f.err
}

func (f *fakeAliasInspector) AliasesStream(_ context.Context, filter types.AliasFilter, yield func(types.Alias) error) error {
	f.got = filter
	if f.err != nil {
		return f.err
	}
	for _, a := range f.aliases {
		if err := yield(a); err != nil {
			return err
		}
	}
	return nil
}

// fakeAliasUpdater records every UpdateAliases request and returns a
// canned response. Mirrors the other fake-* shapes in this package.
type fakeAliasUpdater struct {
	calls []types.AliasUpdateRequest
	resp  types.AliasUpdateResult
	err   error
}

func (f *fakeAliasUpdater) UpdateAliases(_ context.Context, req types.AliasUpdateRequest) (types.AliasUpdateResult, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return types.AliasUpdateResult{}, f.err
	}
	r := f.resp
	if r.Action == "" {
		r.Action = "alias-update"
	}
	if r.Actions == nil {
		r.Actions = req.Actions
	}
	return r, nil
}

func okAliasUpdateResp() types.AliasUpdateResult {
	return types.AliasUpdateResult{
		Action:       "alias-update",
		Acknowledged: true,
	}
}

func sampleAliases() []types.Alias {
	return []types.Alias{
		// Out-of-order on purpose — ListAliases sorts client-side.
		{Alias: "logs", Index: "logs-2024-12", IsWriteIndex: true},
		{Alias: "events", Index: "events-2024-12"},
		{Alias: "logs", Index: "logs-2024-11"},
	}
}

func TestListAliases_passesFilterThrough(t *testing.T) {
	ai := &fakeAliasInspector{aliases: sampleAliases()}
	_, err := index.ListAliases(context.Background(), ai, index.AliasListOptions{
		Alias: "logs,events",
		Index: "logs-2024-*",
	})
	if err != nil {
		t.Fatalf("ListAliases: %v", err)
	}
	if ai.got.Alias != "logs,events" {
		t.Errorf("adapter Alias filter = %q, want 'logs,events'", ai.got.Alias)
	}
	if ai.got.Index != "logs-2024-*" {
		t.Errorf("adapter Index filter = %q, want 'logs-2024-*'", ai.got.Index)
	}
}

func TestListAliases_propagatesError(t *testing.T) {
	sentinel := errors.New("boom")
	ai := &fakeAliasInspector{err: sentinel}
	_, err := index.ListAliases(context.Background(), ai, index.AliasListOptions{})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

func TestListAliases_sortsByAliasThenIndex(t *testing.T) {
	ai := &fakeAliasInspector{aliases: sampleAliases()}
	got, err := index.ListAliases(context.Background(), ai, index.AliasListOptions{})
	if err != nil {
		t.Fatalf("ListAliases: %v", err)
	}
	want := []struct{ alias, index string }{
		{"events", "events-2024-12"},
		{"logs", "logs-2024-11"},
		{"logs", "logs-2024-12"},
	}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Alias != w.alias || got[i].Index != w.index {
			t.Errorf("row[%d] = (%q,%q), want (%q,%q)", i, got[i].Alias, got[i].Index, w.alias, w.index)
		}
	}
}

func TestAddAlias_rejectsEmptyAlias(t *testing.T) {
	au := &fakeAliasUpdater{resp: okAliasUpdateResp()}
	_, err := index.AddAlias(context.Background(), au, index.AliasUpdateOptions{
		Index: "logs-2024-12",
	})
	if err == nil || !strings.Contains(err.Error(), "alias") {
		t.Errorf("expected error mentioning --alias, got: %v", err)
	}
	if len(au.calls) != 0 {
		t.Errorf("cluster contacted on validation failure: %+v", au.calls)
	}
}

func TestAddAlias_rejectsEmptyIndex(t *testing.T) {
	au := &fakeAliasUpdater{resp: okAliasUpdateResp()}
	_, err := index.AddAlias(context.Background(), au, index.AliasUpdateOptions{
		Alias: "logs",
	})
	if err == nil || !strings.Contains(err.Error(), "index") {
		t.Errorf("expected error mentioning --index, got: %v", err)
	}
	if len(au.calls) != 0 {
		t.Errorf("cluster contacted on validation failure: %+v", au.calls)
	}
}

func TestAddAlias_rejectsPatterns(t *testing.T) {
	cases := []struct {
		name         string
		alias, index string
	}{
		{"alias wildcard", "logs-*", "logs-2024-12"},
		{"index wildcard", "logs", "logs-2024-*"},
		{"_all index", "logs", "_all"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			au := &fakeAliasUpdater{resp: okAliasUpdateResp()}
			_, err := index.AddAlias(context.Background(), au, index.AliasUpdateOptions{
				Alias: c.alias, Index: c.index,
			})
			if err == nil {
				t.Errorf("expected pattern rejection for alias=%q index=%q", c.alias, c.index)
			}
			if len(au.calls) != 0 {
				t.Errorf("cluster contacted on pattern reject: %+v", au.calls)
			}
		})
	}
}

func TestAddAlias_rejectsBadFilterJSON(t *testing.T) {
	au := &fakeAliasUpdater{resp: okAliasUpdateResp()}
	_, err := index.AddAlias(context.Background(), au, index.AliasUpdateOptions{
		Alias: "logs", Index: "logs-2024-12",
		Filter: "not-json",
	})
	if err == nil || !strings.Contains(err.Error(), "filter") {
		t.Errorf("expected error mentioning --filter, got: %v", err)
	}
}

func TestAddAlias_rejectsFilterArrayShape(t *testing.T) {
	au := &fakeAliasUpdater{resp: okAliasUpdateResp()}
	_, err := index.AddAlias(context.Background(), au, index.AliasUpdateOptions{
		Alias: "logs", Index: "logs-2024-12",
		Filter: `["not","an","object"]`,
	})
	if err == nil || !strings.Contains(err.Error(), "object") {
		t.Errorf("expected error mentioning JSON object, got: %v", err)
	}
}

func TestAddAlias_dryRun_doesNotCallCluster(t *testing.T) {
	au := &fakeAliasUpdater{resp: okAliasUpdateResp()}
	got, err := index.AddAlias(context.Background(), au, index.AliasUpdateOptions{
		Alias: "logs", Index: "logs-2024-12",
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("AddAlias: %v", err)
	}
	if !got.DryRun {
		t.Errorf("DryRun = false on result, want true")
	}
	if !got.Acknowledged {
		t.Errorf("dry-run should ack synthetically (got %+v)", got)
	}
	if len(got.Actions) != 1 {
		t.Errorf("expected 1 action echoed, got %d", len(got.Actions))
	}
	if len(au.calls) != 0 {
		t.Errorf("dry-run contacted cluster: %+v", au.calls)
	}
}

func TestAddAlias_passesActionThrough(t *testing.T) {
	au := &fakeAliasUpdater{resp: okAliasUpdateResp()}
	writeIndex := true
	_, err := index.AddAlias(context.Background(), au, index.AliasUpdateOptions{
		Alias:        "logs",
		Index:        "logs-2024-12",
		Filter:       `{"term":{"tenant":"x"}}`,
		Routing:      "tenant-x",
		IsWriteIndex: &writeIndex,
	})
	if err != nil {
		t.Fatalf("AddAlias: %v", err)
	}
	if len(au.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(au.calls))
	}
	if len(au.calls[0].Actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(au.calls[0].Actions))
	}
	got := au.calls[0].Actions[0]
	if got.Action != types.AliasActionAdd {
		t.Errorf("Action = %q, want add", got.Action)
	}
	if got.Alias != "logs" || got.Index != "logs-2024-12" {
		t.Errorf("Alias/Index = %q/%q", got.Alias, got.Index)
	}
	if got.Filter != `{"term":{"tenant":"x"}}` {
		t.Errorf("Filter = %q", got.Filter)
	}
	if got.Routing != "tenant-x" {
		t.Errorf("Routing = %q", got.Routing)
	}
	if got.IsWriteIndex == nil || *got.IsWriteIndex != true {
		t.Errorf("IsWriteIndex = %v, want pointer to true", got.IsWriteIndex)
	}
}

func TestRemoveAlias_passesActionThrough(t *testing.T) {
	au := &fakeAliasUpdater{resp: okAliasUpdateResp()}
	_, err := index.RemoveAlias(context.Background(), au, index.AliasUpdateOptions{
		Alias: "logs", Index: "logs-2024-12",
	})
	if err != nil {
		t.Fatalf("RemoveAlias: %v", err)
	}
	if len(au.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(au.calls))
	}
	got := au.calls[0].Actions[0]
	if got.Action != types.AliasActionRemove {
		t.Errorf("Action = %q, want remove", got.Action)
	}
	if got.Alias != "logs" || got.Index != "logs-2024-12" {
		t.Errorf("Alias/Index = %q/%q", got.Alias, got.Index)
	}
}

func TestRemoveAlias_dryRun_doesNotCallCluster(t *testing.T) {
	au := &fakeAliasUpdater{resp: okAliasUpdateResp()}
	got, err := index.RemoveAlias(context.Background(), au, index.AliasUpdateOptions{
		Alias: "logs", Index: "logs-2024-12",
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("RemoveAlias: %v", err)
	}
	if !got.DryRun || !got.Acknowledged {
		t.Errorf("expected synthetic dry-run ack, got %+v", got)
	}
	if len(au.calls) != 0 {
		t.Errorf("dry-run contacted cluster: %+v", au.calls)
	}
}

func TestRemoveAlias_propagatesAdapterError(t *testing.T) {
	sentinel := errors.New("boom")
	au := &fakeAliasUpdater{err: sentinel}
	_, err := index.RemoveAlias(context.Background(), au, index.AliasUpdateOptions{
		Alias: "logs", Index: "logs-2024-12",
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

func TestRemoveAlias_rejectsEmptyAlias(t *testing.T) {
	au := &fakeAliasUpdater{resp: okAliasUpdateResp()}
	_, err := index.RemoveAlias(context.Background(), au, index.AliasUpdateOptions{
		Index: "logs-2024-12",
	})
	if err == nil {
		t.Fatal("expected error for empty alias")
	}
	if len(au.calls) != 0 {
		t.Errorf("cluster contacted on validation failure: %+v", au.calls)
	}
}
