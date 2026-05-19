package datastream

import (
	"context"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// RecoveryOptions narrows a `datastream recovery` call. Pattern is the
// operator-supplied data-stream name (or comma-joined list, possibly
// with patterns). ActiveOnly forwards as ?active_only=true so the
// cluster only echoes shards still in progress — the common case when
// an operator is watching a restore or a post-rollover relocation.
type RecoveryOptions struct {
	Pattern    string
	ActiveOnly bool
}

// Recovery is the business-layer entry point for `datastream
// recovery`. It resolves the stream pattern to the underlying backing
// indices via DataStreamInspector and forwards the comma-joined list
// to RecoveryInspector — keeps the wire shape (one /_recovery call,
// per-shard rows) the same as `index recovery` so the rendered table
// is interpretable side-by-side. The cluster keys the response by
// backing-index name; the renderer already groups rows visually, so
// no extra business-side grouping is needed for the data stream to
// be readable in the output.
//
// Returns a noop report (empty Indices, AllDone=true) when the
// pattern resolves to zero streams or zero backing indices — useful
// so `watch`/polling loops on a stream that hasn't rolled yet don't
// fire a wasted call.
func Recovery(ctx context.Context, inspector client.DataStreamInspector, rec client.RecoveryInspector, opts RecoveryOptions) (types.RecoveryReport, error) {
	pattern := strings.TrimSpace(opts.Pattern)
	if pattern == "" {
		return types.RecoveryReport{}, client.UsageError("--name is required (a comma-separated list or pattern)")
	}

	rows, err := inspector.DataStreams(ctx, types.DataStreamFilter{
		Names: client.SplitCSV(pattern),
	})
	if err != nil {
		return types.RecoveryReport{}, err
	}

	var backings []string
	for _, r := range rows {
		for _, b := range r.Indices {
			backings = append(backings, b.Name)
		}
	}
	if len(backings) == 0 {
		return types.RecoveryReport{AllDone: true}, nil
	}
	sort.Strings(backings)

	report, err := rec.Recovery(ctx, types.RecoveryRequest{
		Indices:    strings.Join(backings, ","),
		ActiveOnly: opts.ActiveOnly,
	})
	if err != nil {
		return types.RecoveryReport{}, err
	}
	sort.SliceStable(report.Indices, func(i, j int) bool {
		return report.Indices[i].Index < report.Indices[j].Index
	})
	return report, nil
}
