package datastream

import (
	"context"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// SegmentSort identifies the column the per-backing-index segments
// view should be ordered by. Values and behaviour mirror
// internal/index.SegmentSort so the two surfaces (`index segments`
// and `datastream segments`) accept the same --sort vocabulary.
// Duplicated rather than imported across sibling internal packages —
// the enum is four constants and a parser, not worth cross-coupling.
//
// Drift note: when adding a new sort key here, mirror it in
// internal/index.SegmentSort (and ParseSegmentSort) so the two
// surfaces stay symmetrical. There's no compiler check for this; a
// renderer test in either package will catch a silently-divergent
// numeric value.
type SegmentSort int

const (
	// SegmentSortName orders by backing-index name, ascending. Default.
	SegmentSortName SegmentSort = iota
	// SegmentSortCount orders by primary segment count, descending —
	// the right column for force-merge planning.
	SegmentSortCount
	// SegmentSortDeleted orders by deleted-doc ratio, descending —
	// surfaces backings where --only-expunge-deletes would reclaim
	// the most space.
	SegmentSortDeleted
	// SegmentSortBytes orders by primary on-disk size, descending.
	SegmentSortBytes
)

// ParseSegmentSort turns a --sort string into the typed value. Accepts
// the empty string as an alias for "name" so callers don't need to
// special-case an unset flag. Vocabulary matches the index variant.
func ParseSegmentSort(s string) (SegmentSort, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "name":
		return SegmentSortName, nil
	case "count", "segments":
		return SegmentSortCount, nil
	case "deleted":
		return SegmentSortDeleted, nil
	case "bytes", "size":
		return SegmentSortBytes, nil
	default:
		return 0, client.UsageError("unknown --sort value %q (supported: name, count, deleted, bytes)", s)
	}
}

// SegmentsOptions narrows a `datastream segments` call. Pattern is the
// operator-supplied data-stream name (or comma-joined list, possibly
// with patterns). MinSegments filters out backing indices with fewer
// than N primary segments; Sort selects the rendered order.
type SegmentsOptions struct {
	Pattern     string
	MinSegments int
	Sort        SegmentSort
}

// Segments is the business-layer entry point for `datastream
// segments`. It resolves the stream pattern to the underlying backing
// indices via DataStreamInspector and forwards the comma-joined list
// to SegmentsInspector — one cluster round-trip regardless of how
// many backing indices the pattern resolves to.
//
// Closed backing indices are pre-filtered via IndicesNamesStatus
// because /_segments returns 400 on a closed index. Without the
// pre-flight a single cold-storage backing in the resolved list
// would fail the whole call.
//
// MinSegments is applied client-side so a backing index that already
// has fewer than N segments is dropped from the rendered output (the
// canonical "indices worth force-merging" filter).
//
// indexes may be nil; the status pre-flight is skipped in that case
// (useful for tests). A real CLI invocation always threads the live
// IndexInspector through.
func Segments(ctx context.Context, inspector client.DataStreamInspector, indexes client.IndexInspector, seg client.SegmentsInspector, opts SegmentsOptions) (types.SegmentsReport, error) {
	pattern := strings.TrimSpace(opts.Pattern)
	if pattern == "" {
		return types.SegmentsReport{}, client.UsageError("--name is required (a comma-separated list or pattern)")
	}
	if opts.MinSegments < 0 {
		return types.SegmentsReport{}, client.UsageError("--min-segments must be >= 0 (got %d)", opts.MinSegments)
	}

	rows, err := inspector.DataStreams(ctx, types.DataStreamFilter{
		Names: client.SplitCSV(pattern),
	})
	if err != nil {
		return types.SegmentsReport{}, err
	}

	var backings []string
	for _, r := range rows {
		for _, b := range r.Indices {
			backings = append(backings, b.Name)
		}
	}
	if len(backings) == 0 {
		return types.SegmentsReport{}, nil
	}
	sort.Strings(backings)

	target, err := openBackings(ctx, indexes, backings)
	if err != nil {
		return types.SegmentsReport{}, err
	}
	if len(target) == 0 {
		return types.SegmentsReport{}, nil
	}

	report, err := seg.Segments(ctx, types.SegmentsRequest{
		Indices: strings.Join(target, ","),
	})
	if err != nil {
		return types.SegmentsReport{}, err
	}

	out := report.Indices[:0]
	for _, ix := range report.Indices {
		if opts.MinSegments > 0 && ix.SegmentsPrimary < opts.MinSegments {
			continue
		}
		out = append(out, ix)
	}

	switch opts.Sort {
	case SegmentSortCount:
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].SegmentsPrimary > out[j].SegmentsPrimary
		})
	case SegmentSortDeleted:
		sort.SliceStable(out, func(i, j int) bool {
			return deletedRatio(out[i]) > deletedRatio(out[j])
		})
	case SegmentSortBytes:
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].Bytes > out[j].Bytes
		})
	default:
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].Index < out[j].Index
		})
	}
	return types.SegmentsReport{Indices: out}, nil
}

// openBackings drops every name the cluster reports with status=close
// so the caller never sends a closed backing index to /_segments
// (returns 400) or any other read endpoint that refuses closed
// targets. Hidden:true on the filter because data-stream backings
// are dot-prefixed and /_cat/indices would drop them by default.
//
// indexes may be nil; the function returns the input verbatim in
// that case so tests don't have to thread a fake IndexInspector
// through every case.
func openBackings(ctx context.Context, indexes client.IndexInspector, backings []string) ([]string, error) {
	if indexes == nil || len(backings) == 0 {
		return backings, nil
	}
	rows, err := indexes.IndicesNamesStatus(ctx, types.IndexFilter{
		Index:  strings.Join(backings, ","),
		Hidden: true,
	})
	if err != nil {
		return nil, err
	}
	statusByName := make(map[string]string, len(rows))
	for _, r := range rows {
		statusByName[r.Name] = r.Status
	}
	out := make([]string, 0, len(backings))
	for _, name := range backings {
		if strings.EqualFold(statusByName[name], "close") {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}

// deletedRatio is a local mirror of the index-side helper — the same
// fraction (deleted_docs / (deleted_docs + num_docs)). Duplicated
// rather than imported because internal/index keeps it private to
// the segments file; one numeric expression isn't worth a wider
// export.
func deletedRatio(ix types.IndexSegments) float64 {
	total := ix.DocsTotal + ix.DocsDeleted
	if total == 0 {
		return 0
	}
	return float64(ix.DocsDeleted) / float64(total)
}
