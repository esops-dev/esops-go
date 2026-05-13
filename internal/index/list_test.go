package index_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/index"
	"github.com/esops-dev/esops-go/pkg/types"
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

func (f *fakeIndexInspector) IndicesNamesStatus(_ context.Context, filter types.IndexFilter) ([]types.IndexNameStatus, error) {
	f.got = filter
	if f.err != nil {
		return nil, f.err
	}
	out := make([]types.IndexNameStatus, 0, len(f.indices))
	for _, ix := range f.indices {
		out = append(out, types.IndexNameStatus{Name: ix.Name, Status: ix.Status})
	}
	return out, nil
}

func (f *fakeIndexInspector) IndicesStream(_ context.Context, filter types.IndexFilter, yield func(types.Index) error) error {
	f.got = filter
	if f.err != nil {
		return f.err
	}
	for _, ix := range f.indices {
		if err := yield(ix); err != nil {
			return err
		}
	}
	return nil
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

func TestListStream_appliesHiddenAndHealthFilters(t *testing.T) {
	fake := &fakeIndexInspector{indices: sampleIndices()}
	var got []string
	err := index.ListStream(context.Background(), fake, types.IndexFilter{Health: "green"}, func(ix types.Index) error {
		got = append(got, ix.Name)
		return nil
	})
	if err != nil {
		t.Fatalf("ListStream: %v", err)
	}
	// Default view drops the dot-prefixed .kibana_1 even though it's
	// green; only "users" survives the green-health filter.
	if len(got) != 1 || got[0] != "users" {
		t.Errorf("got %v, want [users]", got)
	}
}

func TestListStream_yieldErrorStopsIteration(t *testing.T) {
	fake := &fakeIndexInspector{indices: sampleIndices()}
	stop := errors.New("stop")
	count := 0
	err := index.ListStream(context.Background(), fake, types.IndexFilter{Hidden: true}, func(types.Index) error {
		count++
		return stop
	})
	if !errors.Is(err, stop) {
		t.Errorf("err = %v, want stop sentinel", err)
	}
	if count != 1 {
		t.Errorf("yield invoked %d times, want 1 (sentinel should stop iteration)", count)
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

// indicesWithDataStreamBacking mixes a regular hidden index
// (`.kibana_1`) with two data-stream backing indices (`.ds-*`). Used
// to assert that --exclude-managed drops only the data-stream
// backers, not every dot-prefixed index.
func indicesWithDataStreamBacking() []types.Index {
	return []types.Index{
		{Name: "users", Health: "green", Status: "open"},
		{Name: ".kibana_1", Health: "green", Status: "open"},
		{Name: ".ds-logs-stream-2026.05.13-000001", Health: "green", Status: "open"},
		{Name: ".ds-logs-stream-2026.05.13-000002", Health: "green", Status: "open"},
	}
}

func TestList_excludeManagedDropsDataStreamBackingIndices(t *testing.T) {
	fake := &fakeIndexInspector{indices: indicesWithDataStreamBacking()}
	got, err := index.List(context.Background(), fake, types.IndexFilter{
		Hidden:         true,
		ExcludeManaged: true,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// `.kibana_1` is hidden but not a data-stream backing index, so it
	// stays in the view; the two `.ds-*` entries must be dropped.
	want := []string{".kibana_1", "users"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("got[%d] = %s, want %s", i, got[i].Name, name)
		}
	}
}

func TestListStream_excludeManagedDropsDataStreamBackingIndices(t *testing.T) {
	fake := &fakeIndexInspector{indices: indicesWithDataStreamBacking()}
	var got []string
	err := index.ListStream(context.Background(), fake, types.IndexFilter{
		Hidden:         true,
		ExcludeManaged: true,
	}, func(ix types.Index) error {
		got = append(got, ix.Name)
		return nil
	})
	if err != nil {
		t.Fatalf("ListStream: %v", err)
	}
	for _, name := range got {
		if strings.HasPrefix(name, ".ds-") {
			t.Errorf("data-stream backing index leaked through: %s", name)
		}
	}
}
