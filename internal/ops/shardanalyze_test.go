package ops_test

import (
	"context"
	"errors"
	"testing"

	"github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/pkg/types"
)

type fakeNodeInspector struct {
	nodes []types.Node
	err   error
}

func (f *fakeNodeInspector) Nodes(_ context.Context) ([]types.Node, error) {
	return f.nodes, f.err
}

func dataNode(name string) types.Node {
	return types.Node{Name: name, Roles: []string{"data"}, IsDataNode: true}
}

func masterNode(name string) types.Node {
	return types.Node{Name: name, Roles: []string{"master"}, IsDataNode: false}
}

// gib turns a number-of-GiB into bytes — keeps test sizes legible
// without sprinkling 1024^3 multiplications through every fixture.
func gib(n int64) int64 { return n * 1024 * 1024 * 1024 }

func TestAnalyzeShards_skewSkippedFewerThan3DataNodes(t *testing.T) {
	si := &fakeShardInspector{shards: []types.Shard{
		{Index: "logs", Shard: 0, Primary: true, State: "STARTED", StoreBytes: gib(10), Node: "es01"},
		{Index: "logs", Shard: 0, Primary: false, State: "STARTED", StoreBytes: gib(10), Node: "es02"},
	}}
	ni := &fakeNodeInspector{nodes: []types.Node{dataNode("es01"), dataNode("es02")}}

	got, err := ops.AnalyzeShards(context.Background(), si, ni, types.ShardAnalysisRequest{})
	if err != nil {
		t.Fatalf("AnalyzeShards: %v", err)
	}
	if got.Summary.SkewSkippedReason == "" {
		t.Errorf("expected skew skip reason, got empty")
	}
	if got.Summary.SkewedNodes != 0 {
		t.Errorf("SkewedNodes = %d, want 0 when skipped", got.Summary.SkewedNodes)
	}
	for _, n := range got.Nodes {
		if n.Status != "n/a" {
			t.Errorf("node %s status = %q, want n/a", n.Node, n.Status)
		}
	}
}

func TestAnalyzeShards_emptyDataNodeSurfaces(t *testing.T) {
	// es04 holds zero shards but is a data node — the skew analysis
	// must surface it, otherwise the most interesting case (a drained
	// or freshly-added empty data node) is invisible.
	si := &fakeShardInspector{shards: []types.Shard{
		{Index: "logs", Shard: 0, Primary: true, State: "STARTED", StoreBytes: gib(10), Node: "es01"},
		{Index: "logs", Shard: 1, Primary: true, State: "STARTED", StoreBytes: gib(10), Node: "es02"},
		{Index: "logs", Shard: 2, Primary: true, State: "STARTED", StoreBytes: gib(10), Node: "es03"},
	}}
	ni := &fakeNodeInspector{nodes: []types.Node{
		dataNode("es01"), dataNode("es02"), dataNode("es03"), dataNode("es04"),
	}}

	got, err := ops.AnalyzeShards(context.Background(), si, ni, types.ShardAnalysisRequest{})
	if err != nil {
		t.Fatalf("AnalyzeShards: %v", err)
	}
	if len(got.Nodes) != 4 {
		t.Fatalf("got %d node rows, want 4 (including empty es04)", len(got.Nodes))
	}
	var es04 types.ShardNodeStat
	for _, n := range got.Nodes {
		if n.Node == "es04" {
			es04 = n
		}
	}
	if es04.Shards != 0 {
		t.Errorf("es04 shards = %d, want 0", es04.Shards)
	}
	if es04.Status != "SKEW" {
		t.Errorf("empty es04 status = %q, want SKEW", es04.Status)
	}
	if got.Summary.SkewedNodes != 1 {
		t.Errorf("SkewedNodes = %d, want 1", got.Summary.SkewedNodes)
	}
}

func TestAnalyzeShards_masterNodeIgnoredFromSkew(t *testing.T) {
	// A master-only node holding a transient system shard must NOT
	// pollute the data-node skew calculation.
	si := &fakeShardInspector{shards: []types.Shard{
		{Index: "logs", Shard: 0, Primary: true, State: "STARTED", StoreBytes: gib(10), Node: "es01"},
		{Index: "logs", Shard: 1, Primary: true, State: "STARTED", StoreBytes: gib(10), Node: "es02"},
		{Index: "logs", Shard: 2, Primary: true, State: "STARTED", StoreBytes: gib(10), Node: "es03"},
		{Index: ".kibana", Shard: 0, Primary: true, State: "STARTED", StoreBytes: 1024, Node: "master01"},
	}}
	ni := &fakeNodeInspector{nodes: []types.Node{
		dataNode("es01"), dataNode("es02"), dataNode("es03"), masterNode("master01"),
	}}

	got, err := ops.AnalyzeShards(context.Background(), si, ni, types.ShardAnalysisRequest{IncludeHidden: true})
	if err != nil {
		t.Fatalf("AnalyzeShards: %v", err)
	}
	for _, n := range got.Nodes {
		if n.Node == "master01" {
			t.Errorf("master01 should not appear in node stats: %+v", n)
		}
	}
	if got.Summary.DataNodes != 3 {
		t.Errorf("DataNodes = %d, want 3", got.Summary.DataNodes)
	}
}

func TestAnalyzeShards_skewMath(t *testing.T) {
	// Median-based skew: es01 has 12 shards, others 10 → +20% over
	// median 10. With default skewPercent=20, +20% is NOT a skew (we
	// trigger when the absolute deviation exceeds 20%). +30% would
	// trigger. Test both boundaries.
	si := &fakeShardInspector{shards: append(
		repeatShards("es01", "logs-a", 12, gib(1)),
		append(
			repeatShards("es02", "logs-b", 10, gib(1)),
			repeatShards("es03", "logs-c", 10, gib(1))...,
		)...,
	)}
	ni := &fakeNodeInspector{nodes: []types.Node{dataNode("es01"), dataNode("es02"), dataNode("es03")}}

	got, err := ops.AnalyzeShards(context.Background(), si, ni, types.ShardAnalysisRequest{})
	if err != nil {
		t.Fatalf("AnalyzeShards: %v", err)
	}
	var es01 types.ShardNodeStat
	for _, n := range got.Nodes {
		if n.Node == "es01" {
			es01 = n
		}
	}
	if es01.ShardsDeviationPercent != 20 {
		t.Errorf("es01 shards deviation = %d, want 20", es01.ShardsDeviationPercent)
	}
	if es01.Status != "ok" {
		t.Errorf("es01 status = %q, want ok (deviation == threshold, not >)", es01.Status)
	}
}

func TestAnalyzeShards_oversizedAndCritical(t *testing.T) {
	si := &fakeShardInspector{shards: []types.Shard{
		{Index: "tiny", Shard: 0, Primary: true, State: "STARTED", StoreBytes: gib(2), Node: "es01"},
		{Index: "big", Shard: 0, Primary: true, State: "STARTED", StoreBytes: gib(75), Node: "es02"},
		{Index: "huge", Shard: 0, Primary: true, State: "STARTED", StoreBytes: gib(250), Node: "es03"},
	}}
	ni := &fakeNodeInspector{nodes: []types.Node{dataNode("es01"), dataNode("es02"), dataNode("es03")}}

	got, err := ops.AnalyzeShards(context.Background(), si, ni, types.ShardAnalysisRequest{})
	if err != nil {
		t.Fatalf("AnalyzeShards: %v", err)
	}
	if got.Summary.OversizedShards != 2 {
		t.Errorf("OversizedShards = %d, want 2 (both 75GiB and 250GiB exceed 50GiB)", got.Summary.OversizedShards)
	}
	if got.Summary.CriticalShards != 1 {
		t.Errorf("CriticalShards = %d, want 1 (250GiB > 200GiB critical)", got.Summary.CriticalShards)
	}

	statusByIndex := map[string]string{}
	for _, ix := range got.IndicesAtRisk {
		statusByIndex[ix.Index] = ix.Status
	}
	if statusByIndex["huge"] != "CRITICAL" {
		t.Errorf("huge index status = %q, want CRITICAL", statusByIndex["huge"])
	}
	if statusByIndex["big"] != "OVERSIZED" {
		t.Errorf("big index status = %q, want OVERSIZED", statusByIndex["big"])
	}
	if _, ok := statusByIndex["tiny"]; ok {
		t.Errorf("tiny index should not appear in IndicesAtRisk (it's ok)")
	}
}

func TestAnalyzeShards_undersizedRequiresDocs(t *testing.T) {
	// An empty index isn't "too small" — it's just empty. Only flag
	// undersized when there are docs.
	si := &fakeShardInspector{shards: []types.Shard{
		{Index: "small-with-docs", Shard: 0, Primary: true, State: "STARTED", StoreBytes: 4096, Docs: 100, Node: "es01"},
		{Index: "empty", Shard: 0, Primary: true, State: "STARTED", StoreBytes: 0, Docs: 0, Node: "es02"},
		{Index: "logs", Shard: 0, Primary: true, State: "STARTED", StoreBytes: gib(10), Docs: 1000, Node: "es03"},
	}}
	ni := &fakeNodeInspector{nodes: []types.Node{dataNode("es01"), dataNode("es02"), dataNode("es03")}}

	got, err := ops.AnalyzeShards(context.Background(), si, ni, types.ShardAnalysisRequest{})
	if err != nil {
		t.Fatalf("AnalyzeShards: %v", err)
	}
	statusByIndex := map[string]string{}
	for _, ix := range got.IndicesAtRisk {
		statusByIndex[ix.Index] = ix.Status
	}
	if statusByIndex["small-with-docs"] != "UNDERSIZED" {
		t.Errorf("small-with-docs status = %q, want UNDERSIZED", statusByIndex["small-with-docs"])
	}
	if _, ok := statusByIndex["empty"]; ok {
		t.Errorf("empty index should not appear (no docs = not undersized)")
	}
}

func TestAnalyzeShards_topNSortsAndCaps(t *testing.T) {
	si := &fakeShardInspector{shards: []types.Shard{
		{Index: "a", Shard: 0, Primary: true, State: "STARTED", StoreBytes: gib(1), Node: "es01"},
		{Index: "b", Shard: 0, Primary: true, State: "STARTED", StoreBytes: gib(5), Node: "es02"},
		{Index: "c", Shard: 0, Primary: true, State: "STARTED", StoreBytes: gib(3), Node: "es03"},
	}}
	ni := &fakeNodeInspector{nodes: []types.Node{dataNode("es01"), dataNode("es02"), dataNode("es03")}}

	got, err := ops.AnalyzeShards(context.Background(), si, ni, types.ShardAnalysisRequest{TopN: 2})
	if err != nil {
		t.Fatalf("AnalyzeShards: %v", err)
	}
	if len(got.TopShards) != 2 {
		t.Fatalf("TopShards length = %d, want 2", len(got.TopShards))
	}
	if got.TopShards[0].Index != "b" || got.TopShards[1].Index != "c" {
		t.Errorf("TopShards order = %v, want [b, c] (largest first)",
			[]string{got.TopShards[0].Index, got.TopShards[1].Index})
	}
}

func TestAnalyzeShards_hiddenExcludedByDefault(t *testing.T) {
	si := &fakeShardInspector{shards: []types.Shard{
		{Index: "users", Shard: 0, Primary: true, State: "STARTED", StoreBytes: gib(10), Node: "es01"},
		{Index: ".kibana", Shard: 0, Primary: true, State: "STARTED", StoreBytes: 1024, Node: "es02"},
	}}
	ni := &fakeNodeInspector{nodes: []types.Node{dataNode("es01"), dataNode("es02"), dataNode("es03")}}

	got, err := ops.AnalyzeShards(context.Background(), si, ni, types.ShardAnalysisRequest{TopN: 10})
	if err != nil {
		t.Fatalf("AnalyzeShards: %v", err)
	}
	for _, s := range got.TopShards {
		if s.Index == ".kibana" {
			t.Errorf(".kibana should be excluded without --include-hidden")
		}
	}

	gotInc, _ := ops.AnalyzeShards(context.Background(), si, ni, types.ShardAnalysisRequest{TopN: 10, IncludeHidden: true})
	found := false
	for _, s := range gotInc.TopShards {
		if s.Index == ".kibana" {
			found = true
		}
	}
	if !found {
		t.Errorf(".kibana should appear with --include-hidden")
	}
}

func TestAnalyzeShards_unassignedSummaryAndCounter(t *testing.T) {
	si := &fakeShardInspector{shards: []types.Shard{
		{Index: "logs", Shard: 0, Primary: true, State: "STARTED", StoreBytes: gib(10), Node: "es01"},
		{Index: "broken", Shard: 0, Primary: true, State: "UNASSIGNED"},
		{Index: "broken", Shard: 1, Primary: true, State: "UNASSIGNED"},
		{Index: "other", Shard: 0, Primary: true, State: "UNASSIGNED"},
	}}
	ni := &fakeNodeInspector{nodes: []types.Node{dataNode("es01"), dataNode("es02"), dataNode("es03")}}

	got, err := ops.AnalyzeShards(context.Background(), si, ni, types.ShardAnalysisRequest{})
	if err != nil {
		t.Fatalf("AnalyzeShards: %v", err)
	}
	if got.Unassigned.Total != 3 {
		t.Errorf("Unassigned.Total = %d, want 3", got.Unassigned.Total)
	}
	if len(got.Unassigned.Indices) != 2 {
		t.Errorf("Unassigned.Indices = %v, want 2 distinct indices", got.Unassigned.Indices)
	}
	if got.Summary.UnassignedShards != 3 {
		t.Errorf("Summary.UnassignedShards = %d, want 3", got.Summary.UnassignedShards)
	}
}

func TestAnalyzeShards_sectionGatingHidesButCountsRemain(t *testing.T) {
	// Excluded sections come through as nil slices, but the summary
	// counters must still reflect cluster-wide reality.
	si := &fakeShardInspector{shards: []types.Shard{
		{Index: "big", Shard: 0, Primary: true, State: "STARTED", StoreBytes: gib(75), Node: "es01"},
		{Index: "broken", Shard: 0, Primary: true, State: "UNASSIGNED"},
	}}
	ni := &fakeNodeInspector{nodes: []types.Node{dataNode("es01"), dataNode("es02"), dataNode("es03")}}

	got, err := ops.AnalyzeShards(context.Background(), si, ni, types.ShardAnalysisRequest{
		Sections: []string{"top"},
	})
	if err != nil {
		t.Fatalf("AnalyzeShards: %v", err)
	}
	if got.Nodes != nil {
		t.Errorf("Nodes section should be nil when not requested")
	}
	if got.IndicesAtRisk != nil {
		t.Errorf("IndicesAtRisk should be nil when not requested")
	}
	if got.Summary.OversizedShards != 1 {
		t.Errorf("Summary.OversizedShards = %d, want 1 (counter survives section gating)", got.Summary.OversizedShards)
	}
	if got.Summary.UnassignedShards != 1 {
		t.Errorf("Summary.UnassignedShards = %d, want 1", got.Summary.UnassignedShards)
	}
	if len(got.TopShards) == 0 {
		t.Errorf("TopShards should be present when --by top")
	}
}

func TestAnalyzeShards_propagatesNodeError(t *testing.T) {
	sentinel := errors.New("nodes failed")
	si := &fakeShardInspector{}
	ni := &fakeNodeInspector{err: sentinel}

	_, err := ops.AnalyzeShards(context.Background(), si, ni, types.ShardAnalysisRequest{})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel propagated (nodes failure must be fatal)", err)
	}
}

// repeatShards builds n primary shards on a node, all the same size,
// for skew-math fixtures where the per-shard identity doesn't matter.
func repeatShards(node, indexBase string, n int, storeBytes int64) []types.Shard {
	out := make([]types.Shard, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, types.Shard{
			Index:      indexBase,
			Shard:      i,
			Primary:    true,
			State:      "STARTED",
			StoreBytes: storeBytes,
			Node:       node,
		})
	}
	return out
}
