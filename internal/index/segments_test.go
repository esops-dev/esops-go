package index_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/index"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeSegmentsInspector records the request and returns a canned
// SegmentsReport. Same shape as the other index-fakes so test files
// stay consistent.
type fakeSegmentsInspector struct {
	got    types.SegmentsRequest
	report types.SegmentsReport
	err    error
}

func (f *fakeSegmentsInspector) Segments(_ context.Context, req types.SegmentsRequest) (types.SegmentsReport, error) {
	f.got = req
	return f.report, f.err
}

func sampleSegments() types.SegmentsReport {
	return types.SegmentsReport{
		Indices: []types.IndexSegments{
			{Index: "users", Shards: 1, SegmentsPrimary: 2, SegmentsTotal: 3, MaxSegmentsShard: 2, DocsTotal: 100, DocsDeleted: 0, Bytes: 4096},
			{Index: "logs", Shards: 3, SegmentsPrimary: 30, SegmentsTotal: 60, MaxSegmentsShard: 12, DocsTotal: 5000, DocsDeleted: 1000, Bytes: 10 * 1024 * 1024},
			{Index: ".kibana_1", Shards: 1, SegmentsPrimary: 1, SegmentsTotal: 1, MaxSegmentsShard: 1, DocsTotal: 7, DocsDeleted: 3, Bytes: 256},
			{Index: "archive", Shards: 1, SegmentsPrimary: 1, SegmentsTotal: 1, MaxSegmentsShard: 1, DocsTotal: 50, DocsDeleted: 25, Bytes: 1024},
		},
	}
}

func TestSegments_forwardsPattern(t *testing.T) {
	fake := &fakeSegmentsInspector{}
	_, err := index.Segments(context.Background(), fake, index.SegmentsOptions{
		Pattern: "logs-*",
	})
	if err != nil {
		t.Fatalf("Segments: %v", err)
	}
	if fake.got.Indices != "logs-*" {
		t.Errorf("adapter got Indices = %q, want logs-*", fake.got.Indices)
	}
}

func TestSegments_propagatesError(t *testing.T) {
	sentinel := errors.New("boom")
	fake := &fakeSegmentsInspector{err: sentinel}
	_, err := index.Segments(context.Background(), fake, index.SegmentsOptions{})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

func TestSegments_rejectsNegativeMinSegments(t *testing.T) {
	fake := &fakeSegmentsInspector{report: sampleSegments()}
	_, err := index.Segments(context.Background(), fake, index.SegmentsOptions{MinSegments: -1})
	if err == nil || !strings.Contains(err.Error(), "min-segments") {
		t.Errorf("expected error mentioning min-segments, got: %v", err)
	}
}

func TestSegments_hidesDotPrefixedByDefault(t *testing.T) {
	fake := &fakeSegmentsInspector{report: sampleSegments()}
	got, err := index.Segments(context.Background(), fake, index.SegmentsOptions{})
	if err != nil {
		t.Fatalf("Segments: %v", err)
	}
	for _, ix := range got.Indices {
		if ix.Index == ".kibana_1" {
			t.Errorf("hidden index leaked: %+v", got.Indices)
		}
	}
}

func TestSegments_includesHiddenWhenRequested(t *testing.T) {
	fake := &fakeSegmentsInspector{report: sampleSegments()}
	got, err := index.Segments(context.Background(), fake, index.SegmentsOptions{Hidden: true})
	if err != nil {
		t.Fatalf("Segments: %v", err)
	}
	var found bool
	for _, ix := range got.Indices {
		if ix.Index == ".kibana_1" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected hidden index in result, got %+v", got.Indices)
	}
}

func TestSegments_minSegmentsFiltersFragmented(t *testing.T) {
	fake := &fakeSegmentsInspector{report: sampleSegments()}
	got, err := index.Segments(context.Background(), fake, index.SegmentsOptions{MinSegments: 5})
	if err != nil {
		t.Fatalf("Segments: %v", err)
	}
	if len(got.Indices) != 1 || got.Indices[0].Index != "logs" {
		t.Errorf("got %+v, want only [logs] (>= 5 primary segments)", got.Indices)
	}
}

func TestSegments_sortByCount(t *testing.T) {
	fake := &fakeSegmentsInspector{report: sampleSegments()}
	got, err := index.Segments(context.Background(), fake, index.SegmentsOptions{Sort: index.SegmentSortCount})
	if err != nil {
		t.Fatalf("Segments: %v", err)
	}
	if got.Indices[0].Index != "logs" {
		t.Errorf("first row = %s, want logs (most fragmented)", got.Indices[0].Index)
	}
}

func TestSegments_sortByDeletedRatio(t *testing.T) {
	fake := &fakeSegmentsInspector{report: sampleSegments()}
	got, err := index.Segments(context.Background(), fake, index.SegmentsOptions{Sort: index.SegmentSortDeleted})
	if err != nil {
		t.Fatalf("Segments: %v", err)
	}
	// archive: 25/(50+25)=33%, logs: 1000/(5000+1000)=16.6%, users: 0%.
	// .kibana_1 is excluded as hidden by default.
	if got.Indices[0].Index != "archive" {
		t.Errorf("first row = %s, want archive (highest deleted ratio)", got.Indices[0].Index)
	}
}

func TestSegments_sortByBytes(t *testing.T) {
	fake := &fakeSegmentsInspector{report: sampleSegments()}
	got, err := index.Segments(context.Background(), fake, index.SegmentsOptions{Sort: index.SegmentSortBytes})
	if err != nil {
		t.Fatalf("Segments: %v", err)
	}
	if got.Indices[0].Index != "logs" {
		t.Errorf("first row = %s, want logs (largest)", got.Indices[0].Index)
	}
}

func TestSegments_sortByNameDefault(t *testing.T) {
	fake := &fakeSegmentsInspector{report: sampleSegments()}
	got, err := index.Segments(context.Background(), fake, index.SegmentsOptions{})
	if err != nil {
		t.Fatalf("Segments: %v", err)
	}
	want := []string{"archive", "logs", "users"}
	if len(got.Indices) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got.Indices), len(want), got.Indices)
	}
	for i, n := range want {
		if got.Indices[i].Index != n {
			t.Errorf("row %d = %s, want %s", i, got.Indices[i].Index, n)
		}
	}
}

func TestParseSegmentSort(t *testing.T) {
	cases := []struct {
		in   string
		want index.SegmentSort
		err  bool
	}{
		{"", index.SegmentSortName, false},
		{"name", index.SegmentSortName, false},
		{"NAME", index.SegmentSortName, false},
		{"count", index.SegmentSortCount, false},
		{"segments", index.SegmentSortCount, false},
		{"deleted", index.SegmentSortDeleted, false},
		{"bytes", index.SegmentSortBytes, false},
		{"size", index.SegmentSortBytes, false},
		{"unknown", 0, true},
	}
	for _, tc := range cases {
		got, err := index.ParseSegmentSort(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("ParseSegmentSort(%q) err = nil, want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSegmentSort(%q) err = %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseSegmentSort(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestFormatDeletedRatio(t *testing.T) {
	cases := []struct {
		ix   types.IndexSegments
		want string
	}{
		{types.IndexSegments{}, "-"},
		{types.IndexSegments{DocsTotal: 100, DocsDeleted: 0}, "0.0%"},
		{types.IndexSegments{DocsTotal: 50, DocsDeleted: 50}, "50.0%"},
		{types.IndexSegments{DocsTotal: 0, DocsDeleted: 100}, "100.0%"},
	}
	for _, tc := range cases {
		got := index.FormatDeletedRatio(tc.ix)
		if got != tc.want {
			t.Errorf("FormatDeletedRatio(%+v) = %q, want %q", tc.ix, got, tc.want)
		}
	}
}
