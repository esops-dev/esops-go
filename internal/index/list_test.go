package index_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dj-wasabi/esops-go/internal/index"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// fakeIndexInspector records the filter it was called with and returns a
// canned response. It's exactly what the adapter would return before
// the business-layer filter runs.
type fakeIndexInspector struct {
	got     types.IndexFilter
	indices []types.Index
	err     error
}

func (f *fakeIndexInspector) Indices(_ context.Context, filter types.IndexFilter) ([]types.Index, error) {
	f.got = filter
	return f.indices, f.err
}

func sampleIndices() []types.Index {
	return []types.Index{
		// A user-visible index, green and open.
		{Name: "users", Health: "green", Status: "open", Primaries: 1, Replicas: 1, DocsCount: 42, StoreBytes: 4096},
		// Another visible index, larger, yellow health (replica missing).
		{Name: "logs", Health: "yellow", Status: "open", Primaries: 3, Replicas: 1, DocsCount: 5000, StoreBytes: 10 * 1024 * 1024},
		// A hidden system index (Kibana-style dot-prefix). Default view hides it.
		{Name: ".kibana_1", Health: "green", Status: "open", Primaries: 1, Replicas: 0, DocsCount: 7, StoreBytes: 256},
		// A closed index — reported by cat/indices but with limited fields.
		{Name: "archive-2024", Health: "", Status: "close", Primaries: 1, Replicas: 1},
	}
}

func TestList_passesIndexFilterThrough(t *testing.T) {
	fake := &fakeIndexInspector{indices: sampleIndices()}

	_, err := index.List(context.Background(), fake, types.IndexFilter{Index: "logs-*,users"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if fake.got.Index != "logs-*,users" {
		t.Errorf("adapter got index %q, want %q", fake.got.Index, "logs-*,users")
	}
}

func TestList_propagatesError(t *testing.T) {
	sentinel := errors.New("boom")
	fake := &fakeIndexInspector{err: sentinel}
	_, err := index.List(context.Background(), fake, types.IndexFilter{})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestList_hidesDotPrefixedByDefault(t *testing.T) {
	fake := &fakeIndexInspector{indices: sampleIndices()}
	got, err := index.List(context.Background(), fake, types.IndexFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, ix := range got {
		if ix.Name == ".kibana_1" {
			t.Errorf("hidden index leaked into default view: %+v", got)
		}
	}
}

func TestList_includesHiddenWhenRequested(t *testing.T) {
	fake := &fakeIndexInspector{indices: sampleIndices()}
	got, err := index.List(context.Background(), fake, types.IndexFilter{Hidden: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found bool
	for _, ix := range got {
		if ix.Name == ".kibana_1" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected hidden index in result, got %+v", got)
	}
}

func TestList_healthFilterIsCaseInsensitive(t *testing.T) {
	fake := &fakeIndexInspector{indices: sampleIndices()}
	got, err := index.List(context.Background(), fake, types.IndexFilter{Health: "YELLOW"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "logs" {
		t.Errorf("got %+v, want exactly logs (the only yellow index)", got)
	}
}

func TestList_statusFilterOpenExcludesClosed(t *testing.T) {
	fake := &fakeIndexInspector{indices: sampleIndices()}
	got, err := index.List(context.Background(), fake, types.IndexFilter{Status: "open"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, ix := range got {
		if ix.Status != "open" {
			t.Errorf("got %s with status %q, want only open", ix.Name, ix.Status)
		}
	}
}

func TestList_statusFilterClose(t *testing.T) {
	fake := &fakeIndexInspector{indices: sampleIndices()}
	got, err := index.List(context.Background(), fake, types.IndexFilter{Status: "close"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "archive-2024" {
		t.Errorf("got %+v, want exactly archive-2024", got)
	}
}

func TestList_sortByName(t *testing.T) {
	fake := &fakeIndexInspector{indices: sampleIndices()}
	got, err := index.List(context.Background(), fake, types.IndexFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Default sort is alphabetical (hidden is filtered out, so 3 rows).
	want := []string{"archive-2024", "logs", "users"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i, n := range want {
		if got[i].Name != n {
			t.Errorf("row %d = %s, want %s", i, got[i].Name, n)
		}
	}
}

func TestList_sortBySizeDescending(t *testing.T) {
	fake := &fakeIndexInspector{indices: sampleIndices()}
	got, err := index.List(context.Background(), fake, types.IndexFilter{Sort: types.IndexSortSize})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Largest first. archive-2024 has StoreBytes=0 so it ties with any
	// zero-byte index but ranks below the non-zero ones.
	if got[0].Name != "logs" {
		t.Errorf("got %s first, want logs (largest)", got[0].Name)
	}
	if got[1].Name != "users" {
		t.Errorf("got %s second, want users", got[1].Name)
	}
}

func TestList_sortByDocsDescending(t *testing.T) {
	fake := &fakeIndexInspector{indices: sampleIndices()}
	got, err := index.List(context.Background(), fake, types.IndexFilter{Sort: types.IndexSortDocs})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got[0].Name != "logs" {
		t.Errorf("got %s first, want logs (most docs)", got[0].Name)
	}
}

func TestList_combinesHealthAndHidden(t *testing.T) {
	fake := &fakeIndexInspector{indices: sampleIndices()}
	got, err := index.List(context.Background(), fake, types.IndexFilter{Health: "green", Hidden: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// green + hidden: users and .kibana_1 (archive-2024 is Status=close with empty Health).
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(got), got)
	}
}
