package datastream_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/datastream"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeOptimizer captures the request the business layer hands to the
// IndexOptimizer. Returns canned shard counts so result assembly is
// observable end-to-end.
type fakeOptimizer struct {
	got types.IndexOptimizeRequest
	out types.IndexOptimizeResult
	err error
}

func (f *fakeOptimizer) ForceMerge(_ context.Context, req types.IndexOptimizeRequest) (types.IndexOptimizeResult, error) {
	f.got = req
	if f.err != nil {
		return types.IndexOptimizeResult{}, f.err
	}
	return f.out, nil
}

// fakeRecoveryInspector returns canned recovery rows and records the
// indices argument the business layer joined.
type fakeRecoveryInspector struct {
	got types.RecoveryRequest
	out types.RecoveryReport
	err error
}

func (f *fakeRecoveryInspector) Recovery(_ context.Context, req types.RecoveryRequest) (types.RecoveryReport, error) {
	f.got = req
	return f.out, f.err
}

// fakeSegmentsInspector returns canned per-index aggregates.
type fakeSegmentsInspector struct {
	got types.SegmentsRequest
	out types.SegmentsReport
	err error
}

func (f *fakeSegmentsInspector) Segments(_ context.Context, req types.SegmentsRequest) (types.SegmentsReport, error) {
	f.got = req
	return f.out, f.err
}

// streamWithBackings is a tiny constructor for a data stream with N
// backing indices, the last of which is the write index. Keeps the
// per-test fixture noise down.
func streamWithBackings(name string, backings ...string) types.DataStream {
	idx := make([]types.DataStreamBackingIndex, 0, len(backings))
	for _, n := range backings {
		idx = append(idx, types.DataStreamBackingIndex{Name: n})
	}
	d := types.DataStream{Name: name, Indices: idx, IndexCount: len(idx)}
	if len(backings) > 0 {
		d.WriteIndex = backings[len(backings)-1]
	}
	return d
}

// --- List: hidden / exclude-hidden semantics ----------------------------

func TestList_excludesHiddenByDefault(t *testing.T) {
	streams := []types.DataStream{
		{Name: "logs-stream"},
		{Name: ".fleet-events", Hidden: true},
		{Name: "metrics-stream"},
	}
	ds := &fakeDataStreamInspector{listOut: streams}
	got, err := datastream.List(context.Background(), ds, nil, datastream.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	names := make([]string, 0, len(got))
	for _, s := range got {
		names = append(names, s.Name)
	}
	want := []string{"logs-stream", "metrics-stream"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("default-hidden list = %v, want %v", names, want)
	}
}

func TestList_hiddenOptInIncludesAll(t *testing.T) {
	streams := []types.DataStream{
		{Name: "logs-stream"},
		{Name: ".fleet-events", Hidden: true},
	}
	ds := &fakeDataStreamInspector{listOut: streams}
	got, err := datastream.List(context.Background(), ds, nil, datastream.ListOptions{Hidden: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("hidden=true len = %d, want 2 (kept hidden row)", len(got))
	}
}

// --- Stats --------------------------------------------------------------

func TestStats_sortsAndForwardsNames_hiddenIncluded(t *testing.T) {
	ds := &fakeDataStreamInspector{
		statsOut: types.DataStreamStatsReport{
			DataStreamCount:     2,
			BackingIndicesCount: 5,
			TotalStoreBytes:     1024 * 1024,
			DataStreams: []types.DataStreamStats{
				{DataStream: "metrics-stream", BackingIndicesCount: 2, StoreBytes: 512},
				{DataStream: "logs-stream", BackingIndicesCount: 3, StoreBytes: 512 * 1024},
			},
		},
	}
	rep, err := datastream.Stats(context.Background(), ds, datastream.StatsOptions{
		Names:  []string{"logs-*", "metrics-*", " "},
		Hidden: true,
	})
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	// Hidden=true short-circuits the pre-resolve, so DataStreams must
	// not have been called — the call stays a single round-trip.
	if ds.listFilter.Names != nil {
		t.Errorf("Hidden=true must skip the /_data_stream pre-resolve; got listFilter=%+v", ds.listFilter)
	}
	// Names should drop the empty entry but keep the two patterns.
	if got := ds.statsReq.Names; len(got) != 2 || got[0] != "logs-*" || got[1] != "metrics-*" {
		t.Errorf("forwarded Names = %v, want [logs-* metrics-*]", got)
	}
	// Streams sorted alphabetically: logs-stream before metrics-stream.
	if rep.DataStreams[0].DataStream != "logs-stream" || rep.DataStreams[1].DataStream != "metrics-stream" {
		t.Errorf("sort order = %+v, want logs-stream then metrics-stream", rep.DataStreams)
	}
}

func TestStats_excludeHiddenDefaultFiltersThenForwardsVisibleNames(t *testing.T) {
	ds := &fakeDataStreamInspector{
		listOut: []types.DataStream{
			{Name: "logs-stream"},
			{Name: ".fleet-events", Hidden: true},
			{Name: "metrics-stream"},
		},
		statsOut: types.DataStreamStatsReport{
			DataStreamCount:     2,
			BackingIndicesCount: 4,
			TotalStoreBytes:     2048,
			DataStreams: []types.DataStreamStats{
				{DataStream: "logs-stream", BackingIndicesCount: 2, StoreBytes: 1024},
				{DataStream: "metrics-stream", BackingIndicesCount: 2, StoreBytes: 1024},
			},
		},
	}
	rep, err := datastream.Stats(context.Background(), ds, datastream.StatsOptions{})
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	// /_stats must be narrowed to the two visible streams — never
	// called with an empty name list (which would re-include hidden).
	got := ds.statsReq.Names
	if len(got) != 2 || got[0] != "logs-stream" || got[1] != "metrics-stream" {
		t.Errorf("filtered Names = %v, want [logs-stream metrics-stream]", got)
	}
	if rep.DataStreamCount != 2 {
		t.Errorf("DataStreamCount = %d, want 2", rep.DataStreamCount)
	}
}

func TestStats_excludeHiddenAllHiddenReturnsEmpty(t *testing.T) {
	ds := &fakeDataStreamInspector{
		listOut: []types.DataStream{
			{Name: ".fleet-events", Hidden: true},
		},
		statsErr: errors.New("DataStreamStats must not be called when no visible streams remain"),
	}
	rep, err := datastream.Stats(context.Background(), ds, datastream.StatsOptions{})
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if rep.DataStreamCount != 0 || len(rep.DataStreams) != 0 {
		t.Errorf("expected empty report when every match is hidden, got %+v", rep)
	}
}

func TestStats_surfacesCapabilityError(t *testing.T) {
	wantErr := errors.New("cluster on fire")
	ds := &fakeDataStreamInspector{statsErr: wantErr}
	_, err := datastream.Stats(context.Background(), ds, datastream.StatsOptions{Hidden: true})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}

func TestStats_excludeHiddenSurfacesPreResolveError(t *testing.T) {
	wantErr := errors.New("/_data_stream down")
	ds := &fakeDataStreamInspector{listErr: wantErr}
	_, err := datastream.Stats(context.Background(), ds, datastream.StatsOptions{})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}

// --- Optimize -----------------------------------------------------------

func TestOptimize_skipsWriteIndexByDefault(t *testing.T) {
	ds := &fakeDataStreamInspector{listOut: []types.DataStream{
		streamWithBackings("logs-stream", ".ds-logs-000001", ".ds-logs-000002", ".ds-logs-000003"),
	}}
	opt := &fakeOptimizer{out: types.IndexOptimizeResult{ShardsSuccess: 6}}
	res, err := datastream.Optimize(context.Background(), ds, nil, opt, datastream.OptimizeOptions{
		Pattern: "logs-stream",
	})
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	wantTargets := []string{".ds-logs-000001", ".ds-logs-000002"}
	if got := opt.got.Indices; strings.Join(got, ",") != strings.Join(wantTargets, ",") {
		t.Errorf("forwarded Indices = %v, want %v", got, wantTargets)
	}
	if res.ShardsSuccess != 6 {
		t.Errorf("ShardsSuccess = %d, want 6", res.ShardsSuccess)
	}
	// The write index should appear in Failures as a documented skip
	// so the rendered output explains why an operator's pattern didn't
	// merge every backing.
	joined := strings.Join(res.Failures, " ")
	if !strings.Contains(joined, ".ds-logs-000003") {
		t.Errorf("Failures should mention skipped write index, got %v", res.Failures)
	}
}

func TestOptimize_includeWriteIndexMergesEverything(t *testing.T) {
	ds := &fakeDataStreamInspector{listOut: []types.DataStream{
		streamWithBackings("logs-stream", ".ds-logs-000001", ".ds-logs-000002"),
	}}
	opt := &fakeOptimizer{}
	_, err := datastream.Optimize(context.Background(), ds, nil, opt, datastream.OptimizeOptions{
		Pattern:           "logs-stream",
		IncludeWriteIndex: true,
	})
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if len(opt.got.Indices) != 2 {
		t.Errorf("IncludeWriteIndex=true should pass every backing; got %v", opt.got.Indices)
	}
}

func TestOptimize_dryRunDoesNotCallCapability(t *testing.T) {
	ds := &fakeDataStreamInspector{listOut: []types.DataStream{
		streamWithBackings("logs-stream", ".ds-logs-000001", ".ds-logs-000002"),
	}}
	opt := &fakeOptimizer{err: errors.New("must not be called")}
	res, err := datastream.Optimize(context.Background(), ds, nil, opt, datastream.OptimizeOptions{
		Pattern: "logs-stream",
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if !res.DryRun {
		t.Errorf("DryRun flag not echoed on result")
	}
	if len(res.Indices) != 1 || res.Indices[0] != ".ds-logs-000001" {
		t.Errorf("resolved Indices = %v, want one non-write backing", res.Indices)
	}
}

func TestOptimize_emptyMatchReturnsNoop(t *testing.T) {
	ds := &fakeDataStreamInspector{listOut: nil}
	opt := &fakeOptimizer{err: errors.New("must not be called")}
	res, err := datastream.Optimize(context.Background(), ds, nil, opt, datastream.OptimizeOptions{
		Pattern: "doesnt-exist",
	})
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if len(res.Indices) != 0 || res.ShardsTotal != 0 {
		t.Errorf("expected noop on empty match, got %+v", res)
	}
}

func TestOptimize_refusesBareWildcard(t *testing.T) {
	ds := &fakeDataStreamInspector{}
	opt := &fakeOptimizer{}
	_, err := datastream.Optimize(context.Background(), ds, nil, opt, datastream.OptimizeOptions{Pattern: "*"})
	if !errors.Is(err, client.ErrBlastRadius) {
		t.Errorf("err = %v, want ErrBlastRadius for '*'", err)
	}
}

// fakeStatusInspector returns canned (name, status) pairs and records
// the filter the business layer passed. Only IndicesNamesStatus is
// exercised — Indices and IndicesStream stay no-ops because the
// partition path doesn't reach them.
type fakeStatusInspector struct {
	got  types.IndexFilter
	rows []types.IndexNameStatus
	err  error
}

func (f *fakeStatusInspector) Indices(_ context.Context, _ types.IndexFilter) ([]types.Index, error) {
	return nil, nil
}
func (f *fakeStatusInspector) IndicesNamesStatus(_ context.Context, filter types.IndexFilter) ([]types.IndexNameStatus, error) {
	f.got = filter
	return f.rows, f.err
}
func (f *fakeStatusInspector) IndicesStream(_ context.Context, _ types.IndexFilter, _ func(types.Index) error) error {
	return nil
}

func TestOptimize_skipsClosedBackings(t *testing.T) {
	ds := &fakeDataStreamInspector{listOut: []types.DataStream{
		streamWithBackings("logs-stream", ".ds-logs-000001", ".ds-logs-000002", ".ds-logs-000003"),
	}}
	// 000001 closed → must be skipped; 000003 is the write index → also
	// skipped by default. Only 000002 should reach the cluster call.
	idx := &fakeStatusInspector{rows: []types.IndexNameStatus{
		{Name: ".ds-logs-000001", Status: "close"},
		{Name: ".ds-logs-000002", Status: "open"},
		{Name: ".ds-logs-000003", Status: "open"},
	}}
	opt := &fakeOptimizer{}
	res, err := datastream.Optimize(context.Background(), ds, idx, opt, datastream.OptimizeOptions{
		Pattern: "logs-stream",
	})
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if len(opt.got.Indices) != 1 || opt.got.Indices[0] != ".ds-logs-000002" {
		t.Errorf("forwarded Indices = %v, want [.ds-logs-000002]", opt.got.Indices)
	}
	failures := strings.Join(res.Failures, " ")
	if !strings.Contains(failures, ".ds-logs-000001: skipped (closed") {
		t.Errorf("Failures should explain closed-backing skip; got %v", res.Failures)
	}
	if !strings.Contains(failures, ".ds-logs-000003: skipped (write index") {
		t.Errorf("Failures should explain write-index skip; got %v", res.Failures)
	}
	// Status lookup must include Hidden:true — backings are dot-prefixed.
	if !idx.got.Hidden {
		t.Errorf("partition must call IndicesNamesStatus with Hidden=true to surface .ds-* rows")
	}
}

func TestOptimize_statusLookupErrorBubbles(t *testing.T) {
	ds := &fakeDataStreamInspector{listOut: []types.DataStream{
		streamWithBackings("logs-stream", ".ds-logs-000001", ".ds-logs-000002"),
	}}
	idx := &fakeStatusInspector{err: errors.New("/_cat/indices down")}
	opt := &fakeOptimizer{err: errors.New("ForceMerge must not be called")}
	_, err := datastream.Optimize(context.Background(), ds, idx, opt, datastream.OptimizeOptions{
		Pattern: "logs-stream",
	})
	if err == nil || !strings.Contains(err.Error(), "/_cat/indices down") {
		t.Errorf("err = %v, want status-lookup failure to surface", err)
	}
}

func TestOptimize_blastRadiusCap(t *testing.T) {
	// Two streams with three backings each; the write index is excluded,
	// leaving four target backings. A limit of three triggers the cap.
	ds := &fakeDataStreamInspector{listOut: []types.DataStream{
		streamWithBackings("a", "a-1", "a-2", "a-3"),
		streamWithBackings("b", "b-1", "b-2", "b-3"),
	}}
	opt := &fakeOptimizer{}
	_, err := datastream.Optimize(context.Background(), ds, nil, opt, datastream.OptimizeOptions{
		Pattern:    "a,b",
		MaxIndices: 3,
	})
	if !errors.Is(err, client.ErrBlastRadius) {
		t.Errorf("err = %v, want ErrBlastRadius", err)
	}
}

// --- Recovery -----------------------------------------------------------

func TestRecovery_resolvesBackingsAndForwardsCommaJoined(t *testing.T) {
	ds := &fakeDataStreamInspector{listOut: []types.DataStream{
		streamWithBackings("logs-stream", ".ds-logs-000001", ".ds-logs-000002"),
	}}
	rec := &fakeRecoveryInspector{out: types.RecoveryReport{
		Indices: []types.IndexRecovery{
			{Index: ".ds-logs-000001", Shards: []types.ShardRecovery{{ID: 0, Stage: "DONE", Primary: true}}},
		},
	}}
	rep, err := datastream.Recovery(context.Background(), ds, rec, datastream.RecoveryOptions{
		Pattern:    "logs-stream",
		ActiveOnly: true,
	})
	if err != nil {
		t.Fatalf("Recovery: %v", err)
	}
	if rec.got.Indices != ".ds-logs-000001,.ds-logs-000002" {
		t.Errorf("forwarded Indices = %q, want comma-joined sorted backings", rec.got.Indices)
	}
	if !rec.got.ActiveOnly {
		t.Errorf("ActiveOnly not forwarded")
	}
	if len(rep.Indices) != 1 {
		t.Errorf("unexpected report shape: %+v", rep)
	}
}

func TestRecovery_emptyPatternRejected(t *testing.T) {
	ds := &fakeDataStreamInspector{}
	rec := &fakeRecoveryInspector{}
	_, err := datastream.Recovery(context.Background(), ds, rec, datastream.RecoveryOptions{})
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("err = %v, want --name is required", err)
	}
}

func TestRecovery_noMatchReturnsAllDone(t *testing.T) {
	ds := &fakeDataStreamInspector{listOut: nil}
	rec := &fakeRecoveryInspector{err: errors.New("must not be called")}
	rep, err := datastream.Recovery(context.Background(), ds, rec, datastream.RecoveryOptions{Pattern: "nope"})
	if err != nil {
		t.Fatalf("Recovery: %v", err)
	}
	if !rep.AllDone {
		t.Errorf("expected AllDone=true on empty resolve, got %+v", rep)
	}
}

// --- Segments -----------------------------------------------------------

func TestSegments_resolvesBackingsAndFiltersMin(t *testing.T) {
	ds := &fakeDataStreamInspector{listOut: []types.DataStream{
		streamWithBackings("logs-stream", ".ds-logs-000001", ".ds-logs-000002"),
	}}
	seg := &fakeSegmentsInspector{out: types.SegmentsReport{
		Indices: []types.IndexSegments{
			{Index: ".ds-logs-000001", SegmentsPrimary: 12},
			{Index: ".ds-logs-000002", SegmentsPrimary: 2},
		},
	}}
	rep, err := datastream.Segments(context.Background(), ds, nil, seg, datastream.SegmentsOptions{
		Pattern:     "logs-stream",
		MinSegments: 10,
	})
	if err != nil {
		t.Fatalf("Segments: %v", err)
	}
	if seg.got.Indices != ".ds-logs-000001,.ds-logs-000002" {
		t.Errorf("forwarded Indices = %q", seg.got.Indices)
	}
	if len(rep.Indices) != 1 || rep.Indices[0].Index != ".ds-logs-000001" {
		t.Errorf("MinSegments filter dropped the wrong rows: %+v", rep)
	}
}

func TestSegments_sortByCount(t *testing.T) {
	ds := &fakeDataStreamInspector{listOut: []types.DataStream{
		streamWithBackings("logs-stream", "a", "b"),
	}}
	seg := &fakeSegmentsInspector{out: types.SegmentsReport{
		Indices: []types.IndexSegments{
			{Index: "a", SegmentsPrimary: 2},
			{Index: "b", SegmentsPrimary: 9},
		},
	}}
	rep, err := datastream.Segments(context.Background(), ds, nil, seg, datastream.SegmentsOptions{
		Pattern: "logs-stream",
		Sort:    datastream.SegmentSortCount,
	})
	if err != nil {
		t.Fatalf("Segments: %v", err)
	}
	if rep.Indices[0].Index != "b" {
		t.Errorf("sort-by-count placed wrong row first: %+v", rep.Indices)
	}
}

func TestSegments_skipsClosedBackings(t *testing.T) {
	ds := &fakeDataStreamInspector{listOut: []types.DataStream{
		streamWithBackings("logs-stream", ".ds-logs-000001", ".ds-logs-000002"),
	}}
	idx := &fakeStatusInspector{rows: []types.IndexNameStatus{
		{Name: ".ds-logs-000001", Status: "close"},
		{Name: ".ds-logs-000002", Status: "open"},
	}}
	seg := &fakeSegmentsInspector{out: types.SegmentsReport{
		Indices: []types.IndexSegments{
			{Index: ".ds-logs-000002", SegmentsPrimary: 5},
		},
	}}
	rep, err := datastream.Segments(context.Background(), ds, idx, seg, datastream.SegmentsOptions{
		Pattern: "logs-stream",
	})
	if err != nil {
		t.Fatalf("Segments: %v", err)
	}
	if seg.got.Indices != ".ds-logs-000002" {
		t.Errorf("forwarded Indices = %q, want only the open backing", seg.got.Indices)
	}
	if len(rep.Indices) != 1 || rep.Indices[0].Index != ".ds-logs-000002" {
		t.Errorf("report = %+v, want only the open backing", rep)
	}
}

func TestSegments_allClosedReturnsEmpty(t *testing.T) {
	ds := &fakeDataStreamInspector{listOut: []types.DataStream{
		streamWithBackings("logs-stream", ".ds-logs-000001"),
	}}
	idx := &fakeStatusInspector{rows: []types.IndexNameStatus{
		{Name: ".ds-logs-000001", Status: "close"},
	}}
	seg := &fakeSegmentsInspector{err: errors.New("Segments must not be called when no backings are open")}
	rep, err := datastream.Segments(context.Background(), ds, idx, seg, datastream.SegmentsOptions{
		Pattern: "logs-stream",
	})
	if err != nil {
		t.Fatalf("Segments: %v", err)
	}
	if len(rep.Indices) != 0 {
		t.Errorf("expected empty report when every backing is closed, got %+v", rep)
	}
}

func TestSegments_emptyPatternRejected(t *testing.T) {
	ds := &fakeDataStreamInspector{}
	seg := &fakeSegmentsInspector{}
	_, err := datastream.Segments(context.Background(), ds, nil, seg, datastream.SegmentsOptions{})
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("err = %v, want --name is required", err)
	}
}

func TestParseSegmentSort_recognisedValues(t *testing.T) {
	cases := map[string]datastream.SegmentSort{
		"":         datastream.SegmentSortName,
		"name":     datastream.SegmentSortName,
		"count":    datastream.SegmentSortCount,
		"segments": datastream.SegmentSortCount,
		"deleted":  datastream.SegmentSortDeleted,
		"bytes":    datastream.SegmentSortBytes,
		"size":     datastream.SegmentSortBytes,
	}
	for in, want := range cases {
		got, err := datastream.ParseSegmentSort(in)
		if err != nil {
			t.Errorf("ParseSegmentSort(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseSegmentSort(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseSegmentSort_rejectsUnknown(t *testing.T) {
	_, err := datastream.ParseSegmentSort("madness")
	if err == nil {
		t.Errorf("expected error for unknown sort key")
	}
}
