package datastream

import (
	"context"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// StatsOptions narrows a `datastream stats` call. Names is a repeatable
// filter forwarded to the cluster at the path segment; both ES and OS
// honour patterns there. Empty Names returns aggregate stats for
// every configured stream.
//
// Hidden mirrors the `datastream list` toggle. The /_data_stream/_stats
// endpoint itself does not expose the per-stream Hidden flag, so the
// business layer pre-resolves via /_data_stream when Hidden is false
// (the default), filters out hidden streams, and only then calls
// /_stats with the explicit name list — at the cost of one extra
// round-trip when the operator did not opt into hidden streams. With
// Hidden=true the pre-resolve is skipped and /_stats is called
// directly.
type StatsOptions struct {
	Names  []string
	Hidden bool
}

// Stats is the business-layer entry point for `datastream stats`.
// Forwards the request to GET /_data_stream[/<names>]/_stats via the
// capability and sorts the per-stream rows by name for deterministic
// rendered output. The endpoint returns aggregates directly; no
// per-backing /_cat/indices enrichment is needed.
//
// When opts.Hidden is false, the request fans out to /_data_stream
// first so the response can be narrowed to non-hidden streams — the
// /_stats endpoint has no way to discriminate hidden streams in the
// response and would otherwise produce a row set that disagrees with
// `datastream list` for the same operator-visible cluster.
func Stats(ctx context.Context, c client.DataStreamInspector, opts StatsOptions) (types.DataStreamStatsReport, error) {
	names := make([]string, 0, len(opts.Names))
	for _, n := range opts.Names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		names = append(names, n)
	}

	if !opts.Hidden {
		// Resolve the pattern first so hidden streams can be dropped
		// before the /_stats call; the endpoint's response does not
		// carry the per-stream Hidden flag and would otherwise count
		// hidden streams in the aggregate.
		rows, err := c.DataStreams(ctx, types.DataStreamFilter{Names: names})
		if err != nil {
			return types.DataStreamStatsReport{}, err
		}
		visible := make([]string, 0, len(rows))
		for _, r := range rows {
			if r.Hidden {
				continue
			}
			visible = append(visible, r.Name)
		}
		if len(visible) == 0 {
			// No visible streams — return a clean zero rather than
			// falling through to /_stats with an empty name list,
			// which would yield the cluster-wide aggregate (including
			// hidden) and contradict the --exclude-hidden contract.
			return types.DataStreamStatsReport{}, nil
		}
		names = visible
	}

	rep, err := c.DataStreamStats(ctx, types.DataStreamStatsRequest{Names: names})
	if err != nil {
		return types.DataStreamStatsReport{}, err
	}
	sort.SliceStable(rep.DataStreams, func(i, j int) bool {
		return rep.DataStreams[i].DataStream < rep.DataStreams[j].DataStream
	})
	return rep, nil
}
