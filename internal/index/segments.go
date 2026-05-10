package index

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// SegmentSort identifies the column the per-index segments view should
// be ordered by. Mirrors IndexSort in shape so command flag parsing can
// share the same pattern.
type SegmentSort int

const (
	// SegmentSortName orders by index name, ascending. Default; the
	// stable choice operators expect when grepping a long output.
	SegmentSortName SegmentSort = iota
	// SegmentSortCount orders by primary segment count, descending.
	// The right column to sort on when planning a force-merge — the
	// indices with the most fragmentation surface first.
	SegmentSortCount
	// SegmentSortDeleted orders by deleted-doc ratio (deleted /
	// total), descending. Surfaces indices where an
	// `--only-expunge-deletes` merge would reclaim the most space.
	SegmentSortDeleted
	// SegmentSortBytes orders by primary on-disk size, descending.
	// Useful when capacity planning a merge whose I/O budget is the
	// constraint.
	SegmentSortBytes
)

// ParseSegmentSort turns a --sort string into the typed value. Accepts
// the empty string as an alias for "name" so callers don't need to
// special-case an unset flag.
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

// SegmentsOptions are the knobs the CLI layer hands to Segments.
// Pattern is forwarded to the cluster as the path indices selector
// (empty means "every index"). MinSegments filters out indices with
// fewer than N primary segments — the operator-friendly way to skip
// already-merged indices when triaging fragmentation. Sort selects
// the rendered order.
type SegmentsOptions struct {
	Pattern      string
	MinSegments  int
	Sort         SegmentSort
	Hidden       bool
}

// Segments is the business-layer entry point for `esops index
// segments`. It forwards the pattern to /_segments, drops dot-prefixed
// system indices unless --hidden is set (defence-in-depth: the cluster
// occasionally surfaces .geoip_databases or .security-* in a default
// view, which is a real surprise in a triage table), applies
// MinSegments, and sorts the result.
func Segments(ctx context.Context, c client.SegmentsInspector, opts SegmentsOptions) (types.SegmentsReport, error) {
	if opts.MinSegments < 0 {
		return types.SegmentsReport{}, client.UsageError("--min-segments must be >= 0 (got %d)", opts.MinSegments)
	}
	pattern := strings.TrimSpace(opts.Pattern)
	report, err := c.Segments(ctx, types.SegmentsRequest{Indices: pattern})
	if err != nil {
		return types.SegmentsReport{}, err
	}

	out := report.Indices[:0]
	for _, ix := range report.Indices {
		if !opts.Hidden && strings.HasPrefix(ix.Index, ".") {
			continue
		}
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

// deletedRatio returns deleted_docs / (deleted_docs + num_docs) — the
// fraction of physical docs that are tombstoned. Returns 0 for an
// empty index so the sort doesn't push zero-doc indices to the top
// alongside genuinely deletion-heavy ones.
func deletedRatio(ix types.IndexSegments) float64 {
	total := ix.DocsTotal + ix.DocsDeleted
	if total == 0 {
		return 0
	}
	return float64(ix.DocsDeleted) / float64(total)
}

// FormatDeletedRatio renders the deleted-doc ratio as a percentage
// string (one decimal, trailing %). Exposed so the table renderer
// shares the same formatting rules as the JSON consumer can reproduce
// from the raw counts. A zero-doc index renders as "-" rather than
// "0.0%" since the ratio is undefined.
func FormatDeletedRatio(ix types.IndexSegments) string {
	total := ix.DocsTotal + ix.DocsDeleted
	if total == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", deletedRatio(ix)*100)
}
