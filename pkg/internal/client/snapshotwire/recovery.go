package snapshotwire

import (
	"sort"

	"github.com/esops-dev/esops-go/pkg/types"
)

// RecoveryEnvelope mirrors the GET /<indices>/_recovery response: the
// cluster keys the response by index name, and each value carries a
// `shards` array. Both Elasticsearch and OpenSearch share this shape.
type RecoveryEnvelope map[string]struct {
	Shards []RecoveryShard `json:"shards"`
}

// RecoveryShard is one entry inside the per-index `shards` array. The
// cluster's response nests size and file counts under
// `index.size.{...}` and `index.files.{...}`; we surface both the
// percent-string the cluster prints (operator-friendly) and the raw
// totals (so a CLI can compute its own derived progress without
// re-parsing the percent string).
type RecoveryShard struct {
	ID      int    `json:"id"`
	Type    string `json:"type"`
	Stage   string `json:"stage"`
	Primary bool   `json:"primary"`
	Source  struct {
		Repository string `json:"repository"`
		Snapshot   string `json:"snapshot"`
		Index      string `json:"index"`
	} `json:"source"`
	Index struct {
		Size struct {
			TotalInBytes     int64  `json:"total_in_bytes"`
			RecoveredInBytes int64  `json:"recovered_in_bytes"`
			Percent          string `json:"percent"`
		} `json:"size"`
		Files struct {
			Total     int    `json:"total"`
			Recovered int    `json:"recovered"`
			Percent   string `json:"percent"`
		} `json:"files"`
	} `json:"index"`
}

// RecoveryReportFromWire flattens the cluster's name-keyed envelope into
// a sorted slice of IndexRecovery records. AllDone is true only when
// every shard across every index reports stage `DONE`; an empty
// envelope (no active recoveries) also reports AllDone=true so a
// caller polling on `?active_only=true` can stop the moment the active
// list goes empty.
func RecoveryReportFromWire(env RecoveryEnvelope) types.RecoveryReport {
	out := types.RecoveryReport{
		Indices: make([]types.IndexRecovery, 0, len(env)),
		AllDone: true,
	}
	if len(env) == 0 {
		return out
	}

	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		entry := env[name]
		shards := make([]types.ShardRecovery, 0, len(entry.Shards))
		for _, s := range entry.Shards {
			if s.Stage != "DONE" {
				out.AllDone = false
			}
			source := ""
			if s.Source.Snapshot != "" {
				source = s.Source.Repository + "/" + s.Source.Snapshot + "/" + s.Source.Index
			}
			shards = append(shards, types.ShardRecovery{
				ID:             s.ID,
				Type:           s.Type,
				Stage:          s.Stage,
				Primary:        s.Primary,
				Source:         source,
				BytesPercent:   s.Index.Size.Percent,
				FilesPercent:   s.Index.Files.Percent,
				BytesTotal:     s.Index.Size.TotalInBytes,
				BytesRecovered: s.Index.Size.RecoveredInBytes,
				FilesTotal:     s.Index.Files.Total,
				FilesRecovered: s.Index.Files.Recovered,
			})
		}
		out.Indices = append(out.Indices, types.IndexRecovery{
			Index:  name,
			Shards: shards,
		})
	}
	return out
}
