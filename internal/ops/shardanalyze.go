package ops

import (
	"context"
	"sort"
	"strings"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// Default thresholds. Operators override via CLI flags; the active
// values land in the response's Thresholds block so a saved capture
// remains interpretable on its own.
const (
	defaultOversizedBytes  int64 = 50 * 1024 * 1024 * 1024  // 50 GiB
	defaultCriticalBytes   int64 = 200 * 1024 * 1024 * 1024 // 200 GiB
	defaultUndersizedBytes int64 = 1 * 1024 * 1024 * 1024   // 1 GiB
	defaultSkewPercent           = 20
	defaultTopN                  = 10

	// minDataNodesForSkew gates the skew analysis: with fewer than 3
	// data nodes, the median is fragile and the result misleads more
	// than it helps. The summary records the skip reason so the reader
	// understands the section is empty by design, not by accident.
	minDataNodesForSkew = 3
)

// Section identifiers — the values --by accepts. Kept as constants so
// the CLI validator and the business layer agree on spelling.
const (
	sectionNodes      = "nodes"
	sectionIndices    = "indices"
	sectionTop        = "top"
	sectionUnassigned = "unassigned"
)

// Status strings — externalised because they appear in JSON output and
// scripts will pattern-match on them. Changing one is an API break.
const (
	statusOK         = "ok"
	statusOversized  = "OVERSIZED"
	statusCritical   = "CRITICAL"
	statusUndersized = "UNDERSIZED"
	statusSkew       = "SKEW"
	statusNA         = "n/a"

	noteSystemIndex = "system index"

	skewSkipFewNodes = "fewer than 3 data nodes"
)

// AnalyzeShards builds a multi-section health view from /_cat/shards
// plus /_cat/nodes: per-node distribution + skew, indices with
// shard-size issues, the top-N largest shards, and an unassigned
// summary. Read-only — does not mutate cluster state.
//
// The cluster fetch goes directly to the capability (not through
// ops.Shards) because analyze owns its own filtering pipeline:
// hidden inclusion and per-section gating are analysis concerns,
// not list-command concerns. Reusing ops.Shards would double-apply
// state/role/node filters that analyze does not want.
//
// /_cat/nodes failure is fatal: without it we can't tell data nodes
// from master-only nodes, and the heuristic alternative (every node
// with at least one shard) hides the empty-data-node case the
// analysis exists to catch. Better to surface the error than to
// silently produce a misleading report.
func AnalyzeShards(
	ctx context.Context,
	si client.ShardInspector,
	ni client.NodeInspector,
	req types.ShardAnalysisRequest,
) (types.ShardAnalysis, error) {
	thresholds := resolveThresholds(req)
	topN := req.TopN
	if topN < 0 {
		topN = 0
	} else if topN == 0 && !sectionExplicitlyDisabled(req.Sections, sectionTop) {
		topN = defaultTopN
	}

	shards, err := si.Shards(ctx, types.ShardFilter{})
	if err != nil {
		return types.ShardAnalysis{}, err
	}
	nodes, err := ni.Nodes(ctx)
	if err != nil {
		return types.ShardAnalysis{}, err
	}

	dataNodeNames := dataNodeNameSet(nodes)
	visible := filterVisibleShards(shards, req.IncludeHidden)

	want := wantedSections(req.Sections)

	analysis := types.ShardAnalysis{
		Thresholds: thresholds,
		Summary: types.ShardAnalysisSummary{
			DataNodes: len(dataNodeNames),
		},
	}

	if want[sectionNodes] {
		nodeStats, skewedCount, skewSkipReason := analyzeNodes(visible, nodes, dataNodeNames, thresholds.SkewPercent)
		analysis.Nodes = nodeStats
		analysis.Summary.SkewedNodes = skewedCount
		analysis.Summary.SkewSkippedReason = skewSkipReason
	}

	if want[sectionTop] && topN > 0 {
		analysis.TopShards = topShards(visible, topN, thresholds)
	}

	// Index-level stats and counters (oversized/critical/undersized) are
	// derived from the same per-index aggregation; compute once and
	// fan-out to summary even when the section itself is hidden — the
	// summary counts are headlines an operator wants regardless.
	indexStats := analyzeIndices(visible, thresholds)
	for _, ix := range indexStats {
		switch ix.Status {
		case statusCritical:
			// CRITICAL is a stricter OVERSIZED — count both buckets so
			// operators tracking only the OVERSIZED counter don't miss
			// CRITICAL incidents.
			analysis.Summary.CriticalShards++
			analysis.Summary.OversizedShards++
		case statusOversized:
			analysis.Summary.OversizedShards++
		case statusUndersized:
			analysis.Summary.UndersizedIndices++
		}
	}
	if want[sectionIndices] {
		analysis.IndicesAtRisk = filterAtRisk(indexStats)
	}

	if want[sectionUnassigned] {
		analysis.Unassigned = unassignedSummary(shards)
		analysis.Summary.UnassignedShards = analysis.Unassigned.Total
	} else {
		// Even when the section is hidden, the unassigned counter is a
		// headline that belongs in the summary — re-derive from raw
		// shards (cheap, single pass).
		analysis.Summary.UnassignedShards = countUnassigned(shards)
	}

	return analysis, nil
}

// resolveThresholds layers request overrides over package defaults.
// Zero on any field means "use the default" — the CLI flag layer
// passes zero when the operator didn't set the flag, so this is the
// single place where defaults materialise.
func resolveThresholds(req types.ShardAnalysisRequest) types.ShardAnalysisThresholds {
	t := types.ShardAnalysisThresholds{
		OversizedBytes:  req.MaxShardBytes,
		CriticalBytes:   req.CriticalShardBytes,
		UndersizedBytes: req.MinShardBytes,
		SkewPercent:     req.SkewPercent,
	}
	if t.OversizedBytes == 0 {
		t.OversizedBytes = defaultOversizedBytes
	}
	if t.CriticalBytes == 0 {
		t.CriticalBytes = defaultCriticalBytes
	}
	if t.UndersizedBytes == 0 {
		t.UndersizedBytes = defaultUndersizedBytes
	}
	if t.SkewPercent == 0 {
		t.SkewPercent = defaultSkewPercent
	}
	return t
}

// wantedSections expands the --by request into a section→bool map.
// Empty means "all sections" — the kubectl convention for "no filter
// is everything". Unknown values are silently ignored at this layer
// because the CLI validator is the source of truth for allowed values
// (see validateAnalyzeSections); the business layer is permissive on
// purpose so non-CLI callers don't accidentally trigger an error.
func wantedSections(sections []string) map[string]bool {
	if len(sections) == 0 {
		return map[string]bool{
			sectionNodes:      true,
			sectionIndices:    true,
			sectionTop:        true,
			sectionUnassigned: true,
		}
	}
	out := make(map[string]bool, 4)
	for _, s := range sections {
		out[s] = true
	}
	return out
}

// sectionExplicitlyDisabled reports whether the operator explicitly
// excluded a section (--by used, but the named section wasn't in the
// list). Distinct from "the operator passed nothing" because the
// default-everything semantics apply only to the latter.
func sectionExplicitlyDisabled(sections []string, name string) bool {
	if len(sections) == 0 {
		return false
	}
	for _, s := range sections {
		if s == name {
			return false
		}
	}
	return true
}

// dataNodeNameSet builds the canonical set of data-node names from
// /_cat/nodes. Used to filter the per-shard tally so master-only and
// coordinating-only nodes don't pollute skew analysis.
func dataNodeNameSet(nodes []types.Node) map[string]bool {
	out := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if n.IsDataNode {
			out[n.Name] = true
		}
	}
	return out
}

// filterVisibleShards drops hidden indices unless the operator opted
// in via --include-hidden. The cat/shards endpoint reports hidden
// indices on ES (default expand_wildcards includes "hidden"); on OS
// the typed client doesn't expose expand_wildcards, so hidden indices
// don't reach this stage to begin with — the flag is therefore a
// no-op on OS in practice.
func filterVisibleShards(shards []types.Shard, includeHidden bool) []types.Shard {
	if includeHidden {
		return shards
	}
	out := shards[:0:0] // new slice — don't mutate the input.
	for _, s := range shards {
		if isHiddenIndex(s.Index) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// isHiddenIndex matches the dot-prefixed system-index naming that both
// dialects use ("." for ES system indices, "." for OS plugin indices).
// Keeping this client-side keeps the rule one place — the cluster's
// own hidden flag isn't on /_cat/shards output, only on settings.
func isHiddenIndex(name string) bool {
	return strings.HasPrefix(name, ".")
}

// analyzeNodes builds the per-node distribution + skew analysis. The
// returned slice is sorted by node name; medians are computed only
// across data nodes. Returns the slice, the skewed-node count, and a
// non-empty skip reason when skew analysis was deliberately not done.
func analyzeNodes(
	shards []types.Shard,
	allNodes []types.Node,
	dataNodes map[string]bool,
	skewPercent int,
) ([]types.ShardNodeStat, int, string) {
	// Aggregate per node; pre-seed every known data node so empty data
	// nodes show up with zero shards (the most interesting skew case).
	type agg struct {
		shards     int
		storeBytes int64
	}
	tally := make(map[string]*agg, len(allNodes))
	for _, n := range allNodes {
		if n.IsDataNode {
			tally[n.Name] = &agg{}
		}
	}
	for _, s := range shards {
		if s.Node == "" || !dataNodes[s.Node] {
			continue
		}
		a, ok := tally[s.Node]
		if !ok {
			// Defensive: shard is on a node /_cat/nodes didn't report
			// (race during a node join). Surface the count anyway.
			a = &agg{}
			tally[s.Node] = a
		}
		a.shards++
		a.storeBytes += s.StoreBytes
	}

	names := make([]string, 0, len(tally))
	for name := range tally {
		names = append(names, name)
	}
	sort.Strings(names)

	stats := make([]types.ShardNodeStat, 0, len(names))
	for _, name := range names {
		a := tally[name]
		stats = append(stats, types.ShardNodeStat{
			Node:       name,
			Shards:     a.shards,
			StoreBytes: a.storeBytes,
		})
	}

	if len(stats) < minDataNodesForSkew {
		for i := range stats {
			stats[i].Status = statusNA
		}
		return stats, 0, skewSkipFewNodes
	}

	medShards := medianInts(extractInts(stats, func(s types.ShardNodeStat) int { return s.Shards }))
	medStore := medianInt64s(extractInt64s(stats, func(s types.ShardNodeStat) int64 { return s.StoreBytes }))

	skewed := 0
	for i := range stats {
		shardsDev := percentDevInt(stats[i].Shards, medShards)
		storeDev := percentDevInt64(stats[i].StoreBytes, medStore)
		stats[i].ShardsDeviationPercent = shardsDev
		stats[i].StoreDeviationPercent = storeDev
		if absInt(shardsDev) > skewPercent || absInt(storeDev) > skewPercent {
			stats[i].Status = statusSkew
			skewed++
		} else {
			stats[i].Status = statusOK
		}
	}
	return stats, skewed, ""
}

// analyzeIndices buckets every index by its primaries' size profile.
// Replicas are intentionally ignored — they mirror primaries in size
// (same Lucene segments by construction) so summing them double-counts
// what's already in the per-node store totals. Indices with no
// primaries (rare: a deleted-and-recreating state) are skipped.
//
// Returned slice is sorted by index name. Caller filters down to the
// "issues only" view via filterAtRisk; we keep the full list internally
// so summary counters can be computed before the section is gated off.
func analyzeIndices(shards []types.Shard, t types.ShardAnalysisThresholds) []types.ShardIndexStat {
	type agg struct {
		primaries int
		totalBy   int64
		maxBy     int64
		docs      int64
		hidden    bool
	}
	by := make(map[string]*agg)

	for _, s := range shards {
		if !s.Primary {
			continue
		}
		a, ok := by[s.Index]
		if !ok {
			a = &agg{hidden: isHiddenIndex(s.Index)}
			by[s.Index] = a
		}
		a.primaries++
		a.totalBy += s.StoreBytes
		if s.StoreBytes > a.maxBy {
			a.maxBy = s.StoreBytes
		}
		a.docs += s.Docs
	}

	names := make([]string, 0, len(by))
	for n := range by {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]types.ShardIndexStat, 0, len(names))
	for _, n := range names {
		a := by[n]
		if a.primaries == 0 {
			continue
		}
		avg := a.totalBy / int64(a.primaries)
		ix := types.ShardIndexStat{
			Index:           n,
			Primaries:       a.primaries,
			AvgPrimaryBytes: avg,
			MaxPrimaryBytes: a.maxBy,
			DocsCount:       a.docs,
			Hidden:          a.hidden,
		}
		ix.Status, ix.Note = bucketIndex(a.maxBy, avg, a.docs, a.hidden, t)
		out = append(out, ix)
	}
	return out
}

// bucketIndex classifies an index by its primaries' size profile.
// Order of checks is deliberate: CRITICAL before OVERSIZED (a critical
// shard is also oversized — the stricter label wins); UNDERSIZED only
// when there are docs, because an empty index isn't "too small", it's
// just empty. Hidden indices that fall into UNDERSIZED carry a
// "system index" note so a reader doesn't chase a non-issue.
func bucketIndex(maxBy, avg, docs int64, hidden bool, t types.ShardAnalysisThresholds) (status, note string) {
	switch {
	case maxBy >= t.CriticalBytes:
		return statusCritical, ""
	case maxBy >= t.OversizedBytes:
		return statusOversized, ""
	case avg < t.UndersizedBytes && docs > 0:
		if hidden {
			return statusUndersized, noteSystemIndex
		}
		return statusUndersized, ""
	}
	return statusOK, ""
}

// filterAtRisk keeps only the index rows that aren't ok — the
// "issues" focus the section is for.
func filterAtRisk(stats []types.ShardIndexStat) []types.ShardIndexStat {
	out := make([]types.ShardIndexStat, 0, len(stats))
	for _, s := range stats {
		if s.Status == statusOK {
			continue
		}
		out = append(out, s)
	}
	return out
}

// topShards returns the N largest shards by StoreBytes, descending.
// Ties break on (index, shard, primary) so the order is stable across
// runs — a flapping order would make repeated invocations look more
// chaotic than the cluster actually is.
func topShards(shards []types.Shard, n int, t types.ShardAnalysisThresholds) []types.ShardHotEntry {
	if n <= 0 || len(shards) == 0 {
		return nil
	}
	sorted := make([]types.Shard, len(shards))
	copy(sorted, shards)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].StoreBytes != sorted[j].StoreBytes {
			return sorted[i].StoreBytes > sorted[j].StoreBytes
		}
		if sorted[i].Index != sorted[j].Index {
			return sorted[i].Index < sorted[j].Index
		}
		if sorted[i].Shard != sorted[j].Shard {
			return sorted[i].Shard < sorted[j].Shard
		}
		return sorted[i].Primary && !sorted[j].Primary
	})
	if n > len(sorted) {
		n = len(sorted)
	}
	out := make([]types.ShardHotEntry, 0, n)
	for i := 0; i < n; i++ {
		s := sorted[i]
		out = append(out, types.ShardHotEntry{
			Index:      s.Index,
			Shard:      s.Shard,
			Primary:    s.Primary,
			Node:       s.Node,
			StoreBytes: s.StoreBytes,
			Status:     bucketShard(s.StoreBytes, t),
		})
	}
	return out
}

// bucketShard classifies a single shard against the size thresholds.
// CRITICAL is a stricter OVERSIZED, so the order of checks matters.
func bucketShard(bytes int64, t types.ShardAnalysisThresholds) string {
	switch {
	case bytes >= t.CriticalBytes:
		return statusCritical
	case bytes >= t.OversizedBytes:
		return statusOversized
	}
	return statusOK
}

// unassignedSummary collapses the unassigned shards down to count and
// distinct indices. The full per-shard view stays available via
// `ops shards --unassigned`; this section's job is to get the operator
// to the right next command, not to enumerate.
func unassignedSummary(shards []types.Shard) types.ShardUnassignedSummary {
	var total int
	seen := make(map[string]bool)
	for _, s := range shards {
		if !strings.EqualFold(s.State, "UNASSIGNED") {
			continue
		}
		total++
		seen[s.Index] = true
	}
	indices := make([]string, 0, len(seen))
	for n := range seen {
		indices = append(indices, n)
	}
	sort.Strings(indices)
	return types.ShardUnassignedSummary{Total: total, Indices: indices}
}

// countUnassigned is the cheap variant when the section is gated off
// but the headline counter still belongs in the summary.
func countUnassigned(shards []types.Shard) int {
	var n int
	for _, s := range shards {
		if strings.EqualFold(s.State, "UNASSIGNED") {
			n++
		}
	}
	return n
}

// medianInts returns the median of a sorted-on-the-fly int slice.
// For even-length inputs we round down (floor) rather than averaging,
// because the analyses are integer-counts where "the midpoint between
// two real nodes" has no operational meaning.
func medianInts(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	cp := make([]int, len(xs))
	copy(cp, xs)
	sort.Ints(cp)
	return cp[len(cp)/2]
}

// medianInt64s mirrors medianInts for byte counts. Same floor-on-even
// rationale; for byte counts in the GiB range the rounding error is
// nine orders of magnitude below noise.
func medianInt64s(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	cp := make([]int64, len(xs))
	copy(cp, xs)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp[len(cp)/2]
}

// percentDevInt returns the signed deviation of v from the median, as
// a whole percent. v=120, median=100 → +20. v=80, median=100 → -20.
// median=0 produces 0 (no signal: every node has zero, so no skew).
// Round-to-nearest via integer arithmetic; +5/-5 for half-up handles
// the boundary case operators care about ("is it ≥20% or not?").
func percentDevInt(v, median int) int {
	if median == 0 {
		return 0
	}
	diff := v - median
	if diff >= 0 {
		return (diff*100 + median/2) / median
	}
	return -((-diff*100 + median/2) / median)
}

// percentDevInt64 is the int64 byte-count variant.
func percentDevInt64(v, median int64) int {
	if median == 0 {
		return 0
	}
	diff := v - median
	if diff >= 0 {
		return int((diff*100 + median/2) / median)
	}
	return -int((-diff*100 + median/2) / median)
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// extractInts / extractInt64s pull a single field out of a slice into
// a flat []int / []int64 so the median helpers can stay generic
// without a real generics dance.
func extractInts(stats []types.ShardNodeStat, get func(types.ShardNodeStat) int) []int {
	out := make([]int, len(stats))
	for i, s := range stats {
		out[i] = get(s)
	}
	return out
}

func extractInt64s(stats []types.ShardNodeStat, get func(types.ShardNodeStat) int64) []int64 {
	out := make([]int64, len(stats))
	for i, s := range stats {
		out[i] = get(s)
	}
	return out
}
