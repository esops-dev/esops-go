// Package segmentswire holds the wire shapes the Elasticsearch and
// OpenSearch adapters share for GET /<indices>/_segments. Both products
// expose the same three-level envelope
// (`indices.<name>.shards.<shardN>[].segments.<id>`) and the same
// per-segment fields, so a single wire type and a single translator
// cover both dialects.
package segmentswire

import (
	"sort"

	"github.com/esops-dev/esops-go/pkg/types"
)

// Envelope mirrors the top-level response: the cluster keys the
// `indices` map by index name, and each index value carries a
// `shards` map keyed by stringified shard number.
type Envelope struct {
	Indices map[string]struct {
		Shards map[string][]ShardEntry `json:"shards"`
	} `json:"indices"`
}

// ShardEntry is one element of the per-shard array; the cluster emits
// one record per copy (primary + replicas) under each shard key.
// Routing identifies the copy; Segments is keyed by segment id (e.g.
// `_a3`, `_5b`).
type ShardEntry struct {
	Routing struct {
		State   string `json:"state"`
		Primary bool   `json:"primary"`
		Node    string `json:"node"`
	} `json:"routing"`
	NumCommittedSegments int                     `json:"num_committed_segments"`
	NumSearchSegments    int                     `json:"num_search_segments"`
	Segments             map[string]SegmentEntry `json:"segments"`
}

// SegmentEntry is one segment record. NumDocs and DeletedDocs combine
// into the operator-facing deleted-doc ratio; SizeInBytes drives the
// "force-merge will rewrite this much data" estimate. Committed and
// Search are surfaced because a segment that is not yet committed can
// vanish on a restart, and a non-search segment is invisible to queries
// — both nuances belong to a deep-dive view rather than the default
// table.
type SegmentEntry struct {
	Generation  int    `json:"generation"`
	NumDocs     int64  `json:"num_docs"`
	DeletedDocs int64  `json:"deleted_docs"`
	SizeInBytes int64  `json:"size_in_bytes"`
	Committed   bool   `json:"committed"`
	Search      bool   `json:"search"`
	Version     string `json:"version"`
}

// ReportFromWire flattens the cluster's nested envelope into a sorted
// slice of IndexSegments. Aggregation rules:
//
//   - SegmentsTotal counts every segment record across all shard
//     copies (primary + replicas) — matches what /_segments natively
//     reports.
//   - SegmentsPrimary counts segments only from primary copies — the
//     "logical" segment count operators use when deciding whether to
//     force-merge to N segments.
//   - DocsTotal / DocsDeleted / Bytes sum across primaries only so
//     replicas don't double-count the same logical bytes (an
//     underreplicated index would otherwise look "smaller").
//   - Shards counts distinct shard ids the cluster echoed for the
//     index — informational; some indices expose only assigned shards
//     in /_segments, so this can be lower than the cluster's
//     primary-shard count when copies are unassigned.
//   - MaxSegmentsShard is the highest per-primary-shard segment count
//     we saw — the right number to hand a force-merge user since
//     `max_num_segments` is interpreted per shard.
//   - Committed / Searchable sum the cluster's per-shard
//     num_committed_segments / num_search_segments across primary
//     copies. Reported in the wide table only.
func ReportFromWire(env Envelope) types.SegmentsReport {
	out := types.SegmentsReport{
		Indices: make([]types.IndexSegments, 0, len(env.Indices)),
	}
	if len(env.Indices) == 0 {
		return out
	}

	names := make([]string, 0, len(env.Indices))
	for name := range env.Indices {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		row := types.IndexSegments{Index: name}
		shards := env.Indices[name].Shards
		row.Shards = len(shards)

		for _, copies := range shards {
			perShardPrimarySegs := 0
			// `cp` rather than `copy` — the latter shadows the
			// builtin and a future maintainer reaching for it inside
			// the loop would silently call the variable instead.
			for _, cp := range copies {
				row.SegmentsTotal += len(cp.Segments)
				if !cp.Routing.Primary {
					continue
				}
				row.SegmentsPrimary += len(cp.Segments)
				row.Committed += cp.NumCommittedSegments
				row.Searchable += cp.NumSearchSegments
				perShardPrimarySegs += len(cp.Segments)
				for _, seg := range cp.Segments {
					row.DocsTotal += seg.NumDocs
					row.DocsDeleted += seg.DeletedDocs
					row.Bytes += seg.SizeInBytes
				}
			}
			if perShardPrimarySegs > row.MaxSegmentsShard {
				row.MaxSegmentsShard = perShardPrimarySegs
			}
		}
		out.Indices = append(out.Indices, row)
	}
	return out
}
