package output_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/types"
)

func sampleHealth() types.ClusterHealth {
	return types.ClusterHealth{
		ClusterName:                 "esops-dev-es",
		Status:                      "green",
		NumberOfNodes:               2,
		NumberOfDataNodes:           2,
		ActivePrimaryShards:         3,
		ActiveShards:                3,
		UnassignedShards:            0,
		NumberOfPendingTasks:        0,
		ActiveShardsPercentAsNumber: 100.0,
	}
}

func sampleInfo() types.ClusterInfo {
	return types.ClusterInfo{
		Dialect:     types.DialectElasticsearch,
		ClusterName: "esops-dev-es",
		Version:     "9.3.3",
	}
}

func TestParse_knownFormats(t *testing.T) {
	cases := map[string]output.Format{
		"":       output.FormatTable,
		"table":  output.FormatTable,
		"TABLE":  output.FormatTable,
		"json":   output.FormatJSON,
		"yaml":   output.FormatYAML,
		"yml":    output.FormatYAML,
		"wide":   output.FormatWide,
		"ndjson": output.FormatNDJSON,
	}
	for in, want := range cases {
		got, err := output.Parse(in)
		if err != nil {
			t.Errorf("Parse(%q) err = %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("Parse(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParse_rejectsUnknown(t *testing.T) {
	_, err := output.Parse("csv")
	if err == nil || !strings.Contains(err.Error(), "csv") {
		t.Errorf("err = %v, want error naming csv", err)
	}
}

func TestParse_ndjsonAccepted(t *testing.T) {
	// Parsing now accepts ndjson; per-command renderers decide whether
	// they support it. `Health` does not, and must return an error.
	if got, err := output.Parse("ndjson"); err != nil || got != output.FormatNDJSON {
		t.Errorf("Parse(ndjson) = %v, %v; want FormatNDJSON, nil", got, err)
	}
	if err := output.Health(&bytes.Buffer{}, sampleHealth(), sampleInfo(), output.FormatNDJSON); err == nil {
		t.Error("Health(ndjson) should return 'not supported'")
	}
}

func TestHealth_jsonIsValidAndStable(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Health(&buf, sampleHealth(), sampleInfo(), output.FormatJSON); err != nil {
		t.Fatalf("Health: %v", err)
	}
	// Must be valid JSON and have the contract-relevant top-level fields.
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not valid JSON: %v\noutput: %s", err, buf.String())
	}
	for _, field := range []string{"cluster_name", "status", "number_of_nodes", "active_shards"} {
		if _, ok := m[field]; !ok {
			t.Errorf("missing field %q in JSON output:\n%s", field, buf.String())
		}
	}
	if m["status"] != "green" {
		t.Errorf("status = %v, want green", m["status"])
	}
}

func TestHealth_yamlIsValidAndStable(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Health(&buf, sampleHealth(), sampleInfo(), output.FormatYAML); err != nil {
		t.Fatalf("Health: %v", err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not valid YAML: %v\noutput: %s", err, buf.String())
	}
	if m["cluster_name"] != "esops-dev-es" {
		t.Errorf("cluster_name = %v, want esops-dev-es", m["cluster_name"])
	}
	if m["status"] != "green" {
		t.Errorf("status = %v, want green", m["status"])
	}
}

func TestHealth_tableHasExpectedHeaders(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Health(&buf, sampleHealth(), sampleInfo(), output.FormatTable); err != nil {
		t.Fatalf("Health: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"NAME", "STATUS", "NODES", "SHARDS", "UNASSIGNED", "PENDING"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing header %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "green") {
		t.Errorf("table missing status value:\n%s", out)
	}
}

func TestHealth_wideIncludesDialectAndVersion(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Health(&buf, sampleHealth(), sampleInfo(), output.FormatWide); err != nil {
		t.Fatalf("Health: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"DIALECT", "VERSION", "elasticsearch", "9.3.3"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide output missing %q:\n%s", want, out)
		}
	}
}

func TestHealth_rejectsUnknownFormat(t *testing.T) {
	err := output.Health(&bytes.Buffer{}, sampleHealth(), sampleInfo(), output.Format("bogus"))
	if err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func sampleShards() []types.Shard {
	return []types.Shard{
		{Index: "logs", Shard: 0, Primary: true, State: "STARTED", Docs: 200, StoreBytes: 34981, Node: "es01", IP: "10.0.0.1"},
		{Index: "logs", Shard: 1, Primary: true, State: "UNASSIGNED",
			UnassignedReason: "NODE_LEFT", UnassignedAt: "2026-04-24T09:18:19.440Z", UnassignedFor: "12.3s",
			UnassignedDetails: "node_left[abc123]"},
		{Index: "users", Shard: 0, Primary: true, State: "STARTED", Docs: 20, StoreBytes: 9255, Node: "es02", IP: "10.0.0.2"},
	}
}

func allStartedShards() []types.Shard {
	return []types.Shard{
		{Index: "logs", Shard: 0, Primary: true, State: "STARTED", Docs: 200, StoreBytes: 34981, Node: "es01", IP: "10.0.0.1"},
		{Index: "users", Shard: 0, Primary: true, State: "STARTED", Docs: 20, StoreBytes: 9255, Node: "es02", IP: "10.0.0.2"},
	}
}

func TestShards_jsonIsStable(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Shards(&buf, sampleShards(), output.FormatJSON); err != nil {
		t.Fatalf("Shards: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("not valid JSON: %v\noutput: %s", err, buf.String())
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	for _, field := range []string{"index", "shard", "primary", "state", "docs", "store_bytes"} {
		if _, ok := rows[0][field]; !ok {
			t.Errorf("missing field %q in JSON row: %+v", field, rows[0])
		}
	}
	// omitempty: unassigned shard has no node/ip fields.
	if _, ok := rows[1]["node"]; ok {
		t.Errorf("unassigned shard should not have a node field: %+v", rows[1])
	}
	// omitempty: STARTED shards never carry an unassigned_reason.
	for _, field := range []string{"unassigned_reason", "unassigned_at", "unassigned_for", "unassigned_details"} {
		if _, ok := rows[0][field]; ok {
			t.Errorf("STARTED shard must omit %q (saw %+v)", field, rows[0])
		}
	}
	// Unassigned shard carries the full reason quartet.
	wantReason := map[string]string{
		"unassigned_reason":  "NODE_LEFT",
		"unassigned_at":      "2026-04-24T09:18:19.440Z",
		"unassigned_for":     "12.3s",
		"unassigned_details": "node_left[abc123]",
	}
	for k, want := range wantReason {
		if got, _ := rows[1][k].(string); got != want {
			t.Errorf("rows[1][%q] = %q, want %q", k, got, want)
		}
	}
}

func TestShards_yamlIsStable(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Shards(&buf, sampleShards(), output.FormatYAML); err != nil {
		t.Fatalf("Shards: %v", err)
	}
	var rows []map[string]any
	if err := yaml.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("not valid YAML: %v\noutput: %s", err, buf.String())
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
}

func TestShards_tableHasExpectedColumns(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Shards(&buf, sampleShards(), output.FormatTable); err != nil {
		t.Fatalf("Shards: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"INDEX", "SHARD", "ROLE", "STATE", "STORE", "NODE", "logs", "users", "UNASSIGNED"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestShards_wideIncludesDocsIPAndBytes(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Shards(&buf, sampleShards(), output.FormatWide); err != nil {
		t.Fatalf("Shards: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"DOCS", "IP", "STORE_BYTES", "REASON", "SINCE", "10.0.0.1", "34981", "NODE_LEFT", "12.3s"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide output missing %q:\n%s", want, out)
		}
	}
}

func TestShards_defaultTableAppendsReasonWhenUnassignedPresent(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Shards(&buf, sampleShards(), output.FormatTable); err != nil {
		t.Fatalf("Shards: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "REASON") {
		t.Errorf("default table should append REASON column when unassigned shards are present:\n%s", out)
	}
	if !strings.Contains(out, "NODE_LEFT") {
		t.Errorf("default table should show reason value for unassigned shards:\n%s", out)
	}
}

func TestShards_defaultTableOmitsReasonWhenAllHealthy(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Shards(&buf, allStartedShards(), output.FormatTable); err != nil {
		t.Fatalf("Shards: %v", err)
	}
	if strings.Contains(buf.String(), "REASON") {
		t.Errorf("default table should NOT show REASON when every shard is STARTED:\n%s", buf.String())
	}
}

func TestShards_unassignedRendersDashForNode(t *testing.T) {
	only := []types.Shard{{Index: "x", Shard: 0, Primary: true, State: "UNASSIGNED"}}
	var buf bytes.Buffer
	if err := output.Shards(&buf, only, output.FormatTable); err != nil {
		t.Fatalf("Shards: %v", err)
	}
	// tabwriter expands to spaces, so look for the dash at the end of the data row.
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) < 2 || !strings.HasSuffix(lines[1], "-") {
		t.Errorf("expected '-' at end of data row for empty node:\n%s", buf.String())
	}
}

func TestShards_rejectsUnknownFormat(t *testing.T) {
	err := output.Shards(&bytes.Buffer{}, sampleShards(), output.Format("bogus"))
	if err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func TestShards_emptyTableStillPrintsHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Shards(&buf, nil, output.FormatTable); err != nil {
		t.Fatalf("Shards: %v", err)
	}
	if !strings.Contains(buf.String(), "INDEX") {
		t.Errorf("empty result should still render header:\n%s", buf.String())
	}
}

func sampleIndexRows() []types.Index {
	return []types.Index{
		{
			Name: "logs", UUID: "abc123", Health: "yellow", Status: "open",
			Primaries: 3, Replicas: 1, DocsCount: 5000, DocsDeleted: 12,
			StoreBytes: 10 * 1024 * 1024, PrimaryStoreBytes: 5 * 1024 * 1024,
			CreationDateMillis: 1714003200000, // 2024-04-25T04:00:00Z
		},
		{
			Name: "users", Health: "green", Status: "open",
			Primaries: 1, Replicas: 1, DocsCount: 42, StoreBytes: 4096,
			PrimaryStoreBytes: 2048,
		},
	}
}

func TestIndices_jsonIsStable(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Indices(&buf, sampleIndexRows(), output.FormatJSON); err != nil {
		t.Fatalf("Indices: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, buf.String())
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	for _, field := range []string{"name", "health", "status", "primaries", "replicas", "docs_count", "store_bytes", "primary_store_bytes"} {
		if _, ok := rows[0][field]; !ok {
			t.Errorf("missing field %q in JSON row: %+v", field, rows[0])
		}
	}
	// omitempty on uuid: users row has no UUID, so the key must be absent.
	if _, ok := rows[1]["uuid"]; ok {
		t.Errorf("users row should not have uuid: %+v", rows[1])
	}
	// omitempty on creation_date_millis: users row has no creation date.
	if _, ok := rows[1]["creation_date_millis"]; ok {
		t.Errorf("users row should not have creation_date_millis: %+v", rows[1])
	}
}

func TestIndices_yamlIsStable(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Indices(&buf, sampleIndexRows(), output.FormatYAML); err != nil {
		t.Fatalf("Indices: %v", err)
	}
	var rows []map[string]any
	if err := yaml.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("not valid YAML: %v\n%s", err, buf.String())
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
}

func TestIndices_ndjsonOnePerLine(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Indices(&buf, sampleIndexRows(), output.FormatNDJSON); err != nil {
		t.Fatalf("Indices: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), buf.String())
	}
	for i, line := range lines {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Errorf("line %d is not valid JSON: %v\n%s", i, err, line)
		}
	}
}

func TestIndices_tableHasExpectedColumns(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Indices(&buf, sampleIndexRows(), output.FormatTable); err != nil {
		t.Fatalf("Indices: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"NAME", "HEALTH", "STATUS", "PRI", "REP", "DOCS", "STORE", "logs", "users", "yellow", "green"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestIndices_wideIncludesDeletedUUIDAndCreated(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Indices(&buf, sampleIndexRows(), output.FormatWide); err != nil {
		t.Fatalf("Indices: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"DELETED", "UUID", "CREATED", "PRI_STORE", "abc123", "2024-04-25"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide output missing %q:\n%s", want, out)
		}
	}
	// Rows without UUID/creation render "-" so the table stays rectangular.
	// tabwriter expands tabs to spaces, so the `users` row should end
	// with a dash (CREATED column) and contain a dash for the UUID column.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	var usersLine string
	for _, line := range lines {
		if strings.HasPrefix(line, "users") {
			usersLine = line
			break
		}
	}
	if usersLine == "" {
		t.Fatalf("no users row in wide output:\n%s", out)
	}
	if !strings.HasSuffix(usersLine, "-") {
		t.Errorf("users row must end with '-' for empty CREATED column:\n%s", usersLine)
	}
}

func TestIndices_rejectsUnknownFormat(t *testing.T) {
	err := output.Indices(&bytes.Buffer{}, sampleIndexRows(), output.Format("bogus"))
	if err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func TestIndices_emptyTableStillPrintsHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Indices(&buf, nil, output.FormatTable); err != nil {
		t.Fatalf("Indices: %v", err)
	}
	if !strings.Contains(buf.String(), "NAME") {
		t.Errorf("empty result should still render header:\n%s", buf.String())
	}
}

func sampleAssignedExplanation() types.AllocationExplanation {
	return types.AllocationExplanation{
		Index:        "esops-users",
		Shard:        0,
		Primary:      true,
		CurrentState: "started",
		CurrentNode: &types.AllocationNode{
			ID: "node-a", Name: "es01", TransportAddress: "10.0.0.1:9300", WeightRanking: 1,
		},
		CanRemainOnCurrentNode: "yes",
		CanRebalanceCluster:    "yes",
		RebalanceExplanation:   "shard is in a well-balanced location",
		NodeDecisions: []types.AllocationNodeDecision{
			{
				NodeID: "node-b", NodeName: "es02", NodeDecision: "worse_balance", WeightRanking: 1,
				Deciders: []types.AllocationDecider{
					{Decider: "same_shard", Decision: "YES", Explanation: "no copy here"},
					{Decider: "disk_threshold", Decision: "YES", Explanation: "plenty of disk"},
				},
			},
		},
	}
}

func sampleUnassignedExplanation() types.AllocationExplanation {
	return types.AllocationExplanation{
		Index:        "test-unassigned",
		Shard:        0,
		Primary:      false,
		CurrentState: "unassigned",
		UnassignedInfo: &types.AllocationUnassigned{
			Reason:               "INDEX_CREATED",
			At:                   "2026-04-24T10:00:00.000Z",
			LastAllocationStatus: "no_attempt",
		},
		CanAllocate:         "no",
		AllocateExplanation: "cannot allocate because all nodes already hold a copy",
		NodeDecisions: []types.AllocationNodeDecision{
			{
				NodeID: "node-a", NodeName: "es01", NodeDecision: "no",
				Deciders: []types.AllocationDecider{
					{Decider: "same_shard", Decision: "NO", Explanation: "copy already here"},
				},
			},
			{
				NodeID: "node-b", NodeName: "es02", NodeDecision: "no",
				Deciders: []types.AllocationDecider{
					{Decider: "same_shard", Decision: "NO", Explanation: "copy already here"},
				},
			},
		},
	}
}

func TestAllocationExplain_jsonIsStable(t *testing.T) {
	var buf bytes.Buffer
	if err := output.AllocationExplain(&buf, sampleAssignedExplanation(), output.FormatJSON); err != nil {
		t.Fatalf("AllocationExplain: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, buf.String())
	}
	for _, field := range []string{"index", "shard", "primary", "current_state", "current_node", "node_decisions"} {
		if _, ok := m[field]; !ok {
			t.Errorf("missing field %q in JSON: %+v", field, m)
		}
	}
	// omitempty on unassigned_info for assigned shards.
	if _, ok := m["unassigned_info"]; ok {
		t.Errorf("assigned shard must omit unassigned_info: %+v", m)
	}
}

func TestAllocationExplain_jsonUnassignedCarriesReason(t *testing.T) {
	var buf bytes.Buffer
	if err := output.AllocationExplain(&buf, sampleUnassignedExplanation(), output.FormatJSON); err != nil {
		t.Fatalf("AllocationExplain: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, buf.String())
	}
	// omitempty on current_node for unassigned shards.
	if _, ok := m["current_node"]; ok {
		t.Errorf("unassigned shard must omit current_node: %+v", m)
	}
	unassigned, ok := m["unassigned_info"].(map[string]any)
	if !ok {
		t.Fatalf("unassigned_info missing or wrong type: %+v", m["unassigned_info"])
	}
	if unassigned["reason"] != "INDEX_CREATED" {
		t.Errorf("reason = %v, want INDEX_CREATED", unassigned["reason"])
	}
	if m["can_allocate"] != "no" {
		t.Errorf("can_allocate = %v, want no", m["can_allocate"])
	}
}

func TestAllocationExplain_yamlIsValid(t *testing.T) {
	var buf bytes.Buffer
	if err := output.AllocationExplain(&buf, sampleAssignedExplanation(), output.FormatYAML); err != nil {
		t.Fatalf("AllocationExplain: %v", err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not valid YAML: %v\n%s", err, buf.String())
	}
	if m["index"] != "esops-users" {
		t.Errorf("index = %v, want esops-users", m["index"])
	}
}

func TestAllocationExplain_tableAssignedShardShowsCurrentNode(t *testing.T) {
	var buf bytes.Buffer
	if err := output.AllocationExplain(&buf, sampleAssignedExplanation(), output.FormatTable); err != nil {
		t.Fatalf("AllocationExplain: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"INDEX", "SHARD", "ROLE", "STATE", "NODE", "es01", "started", "REBALANCE:", "es02", "worse_balance"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestAllocationExplain_tableOmitsYesDecidersInDefault(t *testing.T) {
	// The default node-decision row summarises only NO/THROTTLE rulings —
	// the assigned-shard fixture has two YES deciders, so the summary
	// cell must render "-", not the decider names.
	var buf bytes.Buffer
	if err := output.AllocationExplain(&buf, sampleAssignedExplanation(), output.FormatTable); err != nil {
		t.Fatalf("AllocationExplain: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "same_shard=yes") || strings.Contains(out, "disk_threshold=yes") {
		t.Errorf("default table must compress YES deciders:\n%s", out)
	}
}

func TestAllocationExplain_tableUnassignedShowsReasonAndExplanation(t *testing.T) {
	var buf bytes.Buffer
	if err := output.AllocationExplain(&buf, sampleUnassignedExplanation(), output.FormatTable); err != nil {
		t.Fatalf("AllocationExplain: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"unassigned",
		"UNASSIGNED_REASON: INDEX_CREATED",
		"LAST_STATUS:       no_attempt",
		"EXPLANATION: cannot allocate",
		// NO-ruling deciders are surfaced in the default summary.
		"same_shard=no",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("unassigned table missing %q:\n%s", want, out)
		}
	}
}

func TestAllocationExplain_wideExpandsDeciders(t *testing.T) {
	var buf bytes.Buffer
	if err := output.AllocationExplain(&buf, sampleAssignedExplanation(), output.FormatWide); err != nil {
		t.Fatalf("AllocationExplain: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"DECIDER", "RULING", "EXPLANATION", "same_shard", "disk_threshold", "YES", "no copy here", "plenty of disk"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide output missing %q:\n%s", want, out)
		}
	}
}

func TestAllocationExplain_rejectsUnknownFormat(t *testing.T) {
	err := output.AllocationExplain(&bytes.Buffer{}, sampleAssignedExplanation(), output.Format("bogus"))
	if err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func TestAllocationExplain_rejectsNDJSON(t *testing.T) {
	// Single-record command, same precedent as Health: the renderer
	// refuses ndjson so the --output allowlist in cmdsetup has something
	// to bounce against even if future config plumbing slips.
	err := output.AllocationExplain(&bytes.Buffer{}, sampleAssignedExplanation(), output.FormatNDJSON)
	if err == nil {
		t.Fatal("AllocationExplain(ndjson) should return 'not supported'")
	}
}

func sampleAudit() types.SecurityAudit {
	return types.SecurityAudit{
		TLS: types.TLSPosture{
			Scheme:   "https",
			AuthType: "basic",
		},
		Status: types.SecurityStatus{
			Dialect: "elasticsearch",
			Enabled: true,
		},
		Users: []types.SecurityUser{
			{Username: "app-writer", Enabled: true, Reserved: false, Roles: []string{"writer"}},
			{Username: "elastic", Enabled: true, Reserved: true, Roles: []string{"superuser"}, FullName: "built-in"},
		},
		Roles: []types.SecurityRole{
			{Name: "writer", Reserved: false, ClusterPrivileges: []string{"monitor"}, IndexPatterns: []string{"logs-*"}},
			{Name: "superuser", Reserved: true, ClusterPrivileges: []string{"all"}, IndexPatterns: []string{"*"}, Description: "god mode"},
		},
	}
}

func TestAudit_jsonIsStable(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Audit(&buf, sampleAudit(), output.FormatJSON); err != nil {
		t.Fatalf("Audit: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not valid JSON: %v\noutput: %s", err, buf.String())
	}
	for _, field := range []string{"tls", "status", "users", "roles"} {
		if _, ok := m[field]; !ok {
			t.Errorf("missing field %q in JSON output:\n%s", field, buf.String())
		}
	}
}

func TestAudit_yamlIsValid(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Audit(&buf, sampleAudit(), output.FormatYAML); err != nil {
		t.Fatalf("Audit: %v", err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not valid YAML: %v\noutput: %s", err, buf.String())
	}
	if _, ok := m["tls"]; !ok {
		t.Errorf("yaml missing tls:\n%s", buf.String())
	}
}

func TestAudit_tableHasAllSections(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Audit(&buf, sampleAudit(), output.FormatTable); err != nil {
		t.Fatalf("Audit: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"TLS", "SECURITY", "USERS", "ROLES", "elastic", "superuser", "app-writer"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestAudit_tableSuppressesUsersAndRolesWhenDisabled(t *testing.T) {
	a := sampleAudit()
	a.Status.Enabled = false
	a.Status.Note = "security is disabled on this cluster"
	a.Users = nil
	a.Roles = nil

	var buf bytes.Buffer
	if err := output.Audit(&buf, a, output.FormatTable); err != nil {
		t.Fatalf("Audit: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "security is disabled") {
		t.Errorf("table missing disabled note:\n%s", out)
	}
	if strings.Contains(out, "USERS") || strings.Contains(out, "ROLES") {
		t.Errorf("disabled-security table must not emit USERS/ROLES sections:\n%s", out)
	}
}

func TestAudit_wideShowsBackendRolesAndDescription(t *testing.T) {
	a := sampleAudit()
	a.Users[0].BackendRoles = []string{"ldap-writers"}

	var buf bytes.Buffer
	if err := output.Audit(&buf, a, output.FormatWide); err != nil {
		t.Fatalf("Audit: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"BACKEND_ROLES", "ldap-writers", "DESCRIPTION", "god mode"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide output missing %q:\n%s", want, out)
		}
	}
}

func TestAudit_rejectsUnknownFormat(t *testing.T) {
	err := output.Audit(&bytes.Buffer{}, sampleAudit(), output.Format("bogus"))
	if err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func TestAudit_rejectsNDJSON(t *testing.T) {
	// Single-report command (one object, not a stream): renderer refuses
	// ndjson so the cmdsetup allowlist has a backstop.
	err := output.Audit(&bytes.Buffer{}, sampleAudit(), output.FormatNDJSON)
	if err == nil {
		t.Fatal("Audit(ndjson) should return 'not supported'")
	}
}

func sampleOptimize() types.IndexOptimizeResult {
	return types.IndexOptimizeResult{
		Action: "optimize", DryRun: false,
		Indices:        []string{"esops-logs-000001", "esops-products"},
		MaxNumSegments: 1,
		ShardsTotal:    3, ShardsSuccess: 3,
	}
}

func TestIndexOptimize_jsonHasStableFields(t *testing.T) {
	var buf bytes.Buffer
	if err := output.IndexOptimize(&buf, sampleOptimize(), output.FormatJSON); err != nil {
		t.Fatalf("IndexOptimize: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, buf.String())
	}
	for _, field := range []string{"action", "dry_run", "indices", "shards_total", "shards_successful", "shards_failed"} {
		if _, ok := m[field]; !ok {
			t.Errorf("missing field %q in JSON:\n%s", field, buf.String())
		}
	}
}

func TestIndexOptimize_tableHasHeaderAndIndices(t *testing.T) {
	var buf bytes.Buffer
	if err := output.IndexOptimize(&buf, sampleOptimize(), output.FormatTable); err != nil {
		t.Fatalf("IndexOptimize: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ACTION", "DRY_RUN", "INDICES", "SHARDS_OK", "esops-logs-000001"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestIndexOptimize_tableShowsFailures(t *testing.T) {
	r := sampleOptimize()
	r.Failures = []string{"esops-archive: skipped (closed index — open it first to optimize)"}
	var buf bytes.Buffer
	if err := output.IndexOptimize(&buf, r, output.FormatTable); err != nil {
		t.Fatalf("IndexOptimize: %v", err)
	}
	if !strings.Contains(buf.String(), "FAILURES") {
		t.Errorf("missing FAILURES section:\n%s", buf.String())
	}
}

func TestIndexOptimize_rejectsNDJSON(t *testing.T) {
	err := output.IndexOptimize(&bytes.Buffer{}, sampleOptimize(), output.FormatNDJSON)
	if err == nil {
		t.Fatal("IndexOptimize(ndjson) should return 'not supported'")
	}
}

func sampleRollover() types.IndexRolloverResult {
	return types.IndexRolloverResult{
		Alias: "esops-logs", RolledOver: true, DryRun: false, Acknowledged: true, ShardsAcknowledged: true,
		OldIndex: "esops-logs-000001", NewIndex: "esops-logs-000002",
		ConditionsMet: map[string]bool{"max_age": true, "max_docs": false},
	}
}

func TestIndexRollover_jsonHasStableFields(t *testing.T) {
	var buf bytes.Buffer
	if err := output.IndexRollover(&buf, sampleRollover(), output.FormatJSON); err != nil {
		t.Fatalf("IndexRollover: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, buf.String())
	}
	for _, field := range []string{"alias", "rolled_over", "dry_run", "acknowledged", "old_index", "new_index"} {
		if _, ok := m[field]; !ok {
			t.Errorf("missing field %q in JSON:\n%s", field, buf.String())
		}
	}
}

func TestIndexRollover_tableHasHeaderAndConditions(t *testing.T) {
	var buf bytes.Buffer
	if err := output.IndexRollover(&buf, sampleRollover(), output.FormatTable); err != nil {
		t.Fatalf("IndexRollover: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ALIAS", "ROLLED_OVER", "esops-logs-000002", "CONDITIONS", "max_age"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestIndexRollover_rejectsNDJSON(t *testing.T) {
	err := output.IndexRollover(&bytes.Buffer{}, sampleRollover(), output.FormatNDJSON)
	if err == nil {
		t.Fatal("IndexRollover(ndjson) should return 'not supported'")
	}
}

func sampleShrink() types.IndexShrinkResult {
	return types.IndexShrinkResult{
		Action: "shrink", Source: "logs-2024", Target: "logs-2024-shrunk",
		TargetShards: 1, Acknowledged: true, ShardsAcknowledged: true,
	}
}

func TestIndexShrink_jsonIsStable(t *testing.T) {
	var buf bytes.Buffer
	if err := output.IndexShrink(&buf, sampleShrink(), output.FormatJSON); err != nil {
		t.Fatalf("IndexShrink(json): %v", err)
	}
	var got types.IndexShrinkResult
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if got.Source != "logs-2024" || got.Target != "logs-2024-shrunk" {
		t.Errorf("Source/Target round-trip = %q/%q", got.Source, got.Target)
	}
	if got.TargetShards != 1 {
		t.Errorf("TargetShards = %d, want 1", got.TargetShards)
	}
}

func TestIndexShrink_tableHasHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := output.IndexShrink(&buf, sampleShrink(), output.FormatTable); err != nil {
		t.Fatalf("IndexShrink(table): %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ACTION", "SOURCE", "TARGET", "logs-2024", "logs-2024-shrunk"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestIndexShrink_rejectsNDJSON(t *testing.T) {
	err := output.IndexShrink(&bytes.Buffer{}, sampleShrink(), output.FormatNDJSON)
	if err == nil {
		t.Fatal("IndexShrink(ndjson) should return 'not supported'")
	}
}

func sampleAliases() []types.Alias {
	return []types.Alias{
		{Alias: "logs", Index: "logs-2024-12", IsWriteIndex: true},
		{Alias: "logs", Index: "logs-2024-11"},
		{Alias: "events-tenant-x", Index: "events-2024-12", Filter: `{"term":{"tenant":"x"}}`, RoutingIndex: "x", RoutingSearch: "x"},
	}
}

func TestAliases_jsonIsStable(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Aliases(&buf, sampleAliases(), output.FormatJSON); err != nil {
		t.Fatalf("Aliases(json): %v", err)
	}
	var got []types.Alias
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if len(got) != 3 {
		t.Errorf("len(got) = %d, want 3", len(got))
	}
}

func TestAliases_yamlIsValid(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Aliases(&buf, sampleAliases(), output.FormatYAML); err != nil {
		t.Fatalf("Aliases(yaml): %v", err)
	}
	var got []types.Alias
	if err := yaml.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid YAML: %v\n%s", err, buf.String())
	}
	if len(got) != 3 {
		t.Errorf("len(got) = %d, want 3", len(got))
	}
}

func TestAliases_ndjsonOnePerLine(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Aliases(&buf, sampleAliases(), output.FormatNDJSON); err != nil {
		t.Fatalf("Aliases(ndjson): %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("ndjson lines = %d, want 3:\n%s", len(lines), buf.String())
	}
	for i, line := range lines {
		var a types.Alias
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			t.Errorf("line %d invalid JSON: %v (%q)", i, err, line)
		}
	}
}

func TestAliases_tableHasExpectedColumns(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Aliases(&buf, sampleAliases(), output.FormatTable); err != nil {
		t.Fatalf("Aliases(table): %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ALIAS", "INDEX", "WRITE_INDEX", "FILTERED", "logs-2024-12"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestAliases_wideIncludesRoutingAndFilter(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Aliases(&buf, sampleAliases(), output.FormatWide); err != nil {
		t.Fatalf("Aliases(wide): %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ROUTING_INDEX", "ROUTING_SEARCH", "FILTER", `{"term":{"tenant":"x"}}`} {
		if !strings.Contains(out, want) {
			t.Errorf("wide table missing %q:\n%s", want, out)
		}
	}
}

func TestAliasUpdate_jsonIsStable(t *testing.T) {
	r := types.AliasUpdateResult{
		Action: "alias-update", Acknowledged: true,
		Actions: []types.AliasUpdateAction{
			{Action: types.AliasActionAdd, Alias: "logs", Index: "logs-2024-12"},
		},
	}
	var buf bytes.Buffer
	if err := output.AliasUpdate(&buf, r, output.FormatJSON); err != nil {
		t.Fatalf("AliasUpdate(json): %v", err)
	}
	var got types.AliasUpdateResult
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if !got.Acknowledged || len(got.Actions) != 1 {
		t.Errorf("round-trip = %+v", got)
	}
}

func TestAliasUpdate_tableShowsActionList(t *testing.T) {
	r := types.AliasUpdateResult{
		Action: "alias-update", Acknowledged: true,
		Actions: []types.AliasUpdateAction{
			{Action: types.AliasActionAdd, Alias: "logs", Index: "logs-2024-12"},
		},
	}
	var buf bytes.Buffer
	if err := output.AliasUpdate(&buf, r, output.FormatTable); err != nil {
		t.Fatalf("AliasUpdate(table): %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ACTION", "ACKNOWLEDGED", "add", "logs", "logs-2024-12"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func sampleIndexSettings() []types.IndexSettings {
	return []types.IndexSettings{
		{
			Index: "logs-2024-12",
			Settings: map[string]any{
				"index": map[string]any{
					"number_of_shards":   "1",
					"number_of_replicas": "2",
					"refresh_interval":   "30s",
				},
			},
		},
		{
			Index: "logs-2024-11",
			Settings: map[string]any{
				"index": map[string]any{
					"number_of_shards": "1",
				},
			},
		},
	}
}

func TestIndexSettings_jsonRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := output.IndexSettings(&buf, sampleIndexSettings(), output.FormatJSON); err != nil {
		t.Fatalf("IndexSettings: %v", err)
	}
	var rows []types.IndexSettings
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if len(rows) != 2 {
		t.Errorf("len = %d, want 2", len(rows))
	}
}

func TestIndexSettings_ndjsonOnePerLine(t *testing.T) {
	var buf bytes.Buffer
	if err := output.IndexSettings(&buf, sampleIndexSettings(), output.FormatNDJSON); err != nil {
		t.Fatalf("IndexSettings(ndjson): %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Errorf("ndjson lines = %d, want 2:\n%s", len(lines), buf.String())
	}
}

func TestIndexSettings_tableFlattensKeys(t *testing.T) {
	var buf bytes.Buffer
	if err := output.IndexSettings(&buf, sampleIndexSettings(), output.FormatTable); err != nil {
		t.Fatalf("IndexSettings(table): %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"INDEX", "KEY", "VALUE",
		"index.number_of_shards",
		"index.number_of_replicas",
		"logs-2024-12",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestIndexSettings_emptyShowsDash(t *testing.T) {
	rows := []types.IndexSettings{{Index: "empty"}}
	var buf bytes.Buffer
	if err := output.IndexSettings(&buf, rows, output.FormatTable); err != nil {
		t.Fatalf("IndexSettings: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "empty") || !strings.Contains(out, "-") {
		t.Errorf("expected dash for empty settings:\n%s", out)
	}
}

func TestIndexSettingsUpdate_table(t *testing.T) {
	r := types.IndexSettingsUpdateResult{
		Action:       "settings-update",
		Indices:      []string{"logs-2024-12", "logs-2024-11"},
		Acknowledged: true,
	}
	var buf bytes.Buffer
	if err := output.IndexSettingsUpdate(&buf, r, output.FormatTable); err != nil {
		t.Fatalf("IndexSettingsUpdate: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ACTION", "INDICES", "logs-2024-12"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestIndexSettingsUpdate_jsonContainsBody(t *testing.T) {
	r := types.IndexSettingsUpdateResult{
		Action:       "settings-update",
		DryRun:       true,
		Indices:      []string{"logs-*"},
		Acknowledged: true,
		Body:         `{"index":{"number_of_replicas":"2"}}`,
	}
	var buf bytes.Buffer
	if err := output.IndexSettingsUpdate(&buf, r, output.FormatJSON); err != nil {
		t.Fatalf("IndexSettingsUpdate: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if got["body"] != r.Body {
		t.Errorf("body = %v, want %v", got["body"], r.Body)
	}
}

func sampleIndexTemplates() []types.IndexTemplate {
	return []types.IndexTemplate{
		{
			Name:          "logs-v2",
			IndexPatterns: []string{"logs-*"},
			Priority:      100,
			Version:       3,
			ComposedOf:    []string{"logs-mappings", "logs-settings"},
			Template: map[string]any{
				"settings": map[string]any{
					"number_of_shards": "1",
				},
			},
		},
		{
			Name:          "events",
			IndexPatterns: []string{"events-*", "metrics-*"},
		},
	}
}

func TestIndexTemplates_jsonRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := output.IndexTemplates(&buf, sampleIndexTemplates(), output.FormatJSON); err != nil {
		t.Fatalf("IndexTemplates: %v", err)
	}
	var got []types.IndexTemplate
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
}

func TestIndexTemplates_table(t *testing.T) {
	var buf bytes.Buffer
	if err := output.IndexTemplates(&buf, sampleIndexTemplates(), output.FormatTable); err != nil {
		t.Fatalf("IndexTemplates(table): %v", err)
	}
	out := buf.String()
	for _, want := range []string{"NAME", "PRIORITY", "logs-v2", "events"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestIndexTemplates_wideAddsComposedOf(t *testing.T) {
	var buf bytes.Buffer
	if err := output.IndexTemplates(&buf, sampleIndexTemplates(), output.FormatWide); err != nil {
		t.Fatalf("IndexTemplates(wide): %v", err)
	}
	out := buf.String()
	for _, want := range []string{"COMPOSED_OF", "logs-mappings", "DATA_STREAM"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide missing %q:\n%s", want, out)
		}
	}
}

func TestIndexTemplate_singleDetail(t *testing.T) {
	tpl := sampleIndexTemplates()[0]
	var buf bytes.Buffer
	if err := output.IndexTemplate(&buf, tpl, output.FormatTable); err != nil {
		t.Fatalf("IndexTemplate: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"NAME", "PRIORITY", "logs-v2", "logs-*"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail missing %q:\n%s", want, out)
		}
	}
}

func TestIndexTemplate_wideShowsBody(t *testing.T) {
	tpl := sampleIndexTemplates()[0]
	var buf bytes.Buffer
	if err := output.IndexTemplate(&buf, tpl, output.FormatWide); err != nil {
		t.Fatalf("IndexTemplate: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"TEMPLATE", "number_of_shards"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide-detail missing %q:\n%s", want, out)
		}
	}
}

func TestIndexTemplatePut_table(t *testing.T) {
	r := types.IndexTemplatePutResult{
		Action:       "template-put",
		Name:         "logs-v2",
		Acknowledged: true,
	}
	var buf bytes.Buffer
	if err := output.IndexTemplatePut(&buf, r, output.FormatTable); err != nil {
		t.Fatalf("IndexTemplatePut: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ACTION", "NAME", "logs-v2"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestIndexTemplateDelete_jsonRoundTrips(t *testing.T) {
	r := types.IndexTemplateDeleteResult{
		Action:       "template-delete",
		Name:         "logs-v2",
		Acknowledged: true,
	}
	var buf bytes.Buffer
	if err := output.IndexTemplateDelete(&buf, r, output.FormatJSON); err != nil {
		t.Fatalf("IndexTemplateDelete: %v", err)
	}
	var got types.IndexTemplateDeleteResult
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if got.Name != "logs-v2" {
		t.Errorf("name = %q", got.Name)
	}
}

// ─────────────────────────── ILM / ISM ───────────────────────────

func sampleILMPolicies() []types.ILMPolicy {
	return []types.ILMPolicy{
		{Name: "logs-30d", Version: 1, ModifiedDate: "2026-04-30T00:00:00Z",
			InUseBy: types.ILMInUseBy{Indices: []string{"logs-2026-04-30"}}},
		{Name: "audit-90d", Version: 2, InUseBy: types.ILMInUseBy{}},
	}
}

func TestILMPolicies_tableHasExpectedColumns(t *testing.T) {
	var buf bytes.Buffer
	if err := output.ILMPolicies(&buf, sampleILMPolicies(), output.FormatTable); err != nil {
		t.Fatalf("ILMPolicies: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"NAME", "VERSION", "MODIFIED", "INDICES", "logs-30d", "audit-90d"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestILMPolicies_jsonRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := output.ILMPolicies(&buf, sampleILMPolicies(), output.FormatJSON); err != nil {
		t.Fatalf("ILMPolicies: %v", err)
	}
	var got []types.ILMPolicy
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if len(got) != 2 || got[0].Name != "logs-30d" {
		t.Errorf("round-trip: %+v", got)
	}
}

func TestILMPolicies_ndjsonOnePerLine(t *testing.T) {
	var buf bytes.Buffer
	if err := output.ILMPolicies(&buf, sampleILMPolicies(), output.FormatNDJSON); err != nil {
		t.Fatalf("ILMPolicies: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d:\n%s", len(lines), buf.String())
	}
	for _, line := range lines {
		var p types.ILMPolicy
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			t.Errorf("line not valid JSON: %v -- %q", err, line)
		}
	}
}

func TestILMPolicy_wideIncludesPolicyBody(t *testing.T) {
	p := types.ILMPolicy{
		Name:    "logs-30d",
		Version: 1,
		Definition: map[string]any{
			"phases": map[string]any{"hot": map[string]any{"actions": map[string]any{}}},
		},
	}
	var buf bytes.Buffer
	if err := output.ILMPolicy(&buf, p, output.FormatWide); err != nil {
		t.Fatalf("ILMPolicy: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "POLICY") || !strings.Contains(out, "phases") {
		t.Errorf("wide should embed policy body:\n%s", out)
	}
}

func TestILMPolicyPut_yamlIsValid(t *testing.T) {
	r := types.ILMPolicyPutResult{Action: "ilm-policy-put", Name: "logs-30d", Acknowledged: true}
	var buf bytes.Buffer
	if err := output.ILMPolicyPut(&buf, r, output.FormatYAML); err != nil {
		t.Fatalf("ILMPolicyPut: %v", err)
	}
	var got types.ILMPolicyPutResult
	if err := yaml.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not YAML: %v", err)
	}
	if got.Name != "logs-30d" || !got.Acknowledged {
		t.Errorf("round-trip: %+v", got)
	}
}

func TestILMPolicyDelete_tableShowsName(t *testing.T) {
	r := types.ILMPolicyDeleteResult{Action: "ilm-policy-delete", Name: "logs-30d", Acknowledged: true}
	var buf bytes.Buffer
	if err := output.ILMPolicyDelete(&buf, r, output.FormatTable); err != nil {
		t.Fatalf("ILMPolicyDelete: %v", err)
	}
	if !strings.Contains(buf.String(), "logs-30d") {
		t.Errorf("table missing name:\n%s", buf.String())
	}
}

func sampleILMExplain() []types.ILMExplainEntry {
	return []types.ILMExplainEntry{
		{Index: "logs-000001", Managed: true, Policy: "logs-30d", Phase: "hot", Action: "rollover", Step: "check-rollover-ready", Age: "12h"},
		{Index: "audit-000001", Managed: false},
	}
}

func TestILMExplain_tableColumns(t *testing.T) {
	var buf bytes.Buffer
	if err := output.ILMExplain(&buf, sampleILMExplain(), output.FormatTable); err != nil {
		t.Fatalf("ILMExplain: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"INDEX", "MANAGED", "POLICY", "PHASE", "ACTION", "STEP", "AGE",
		"logs-000001", "audit-000001", "logs-30d"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestILMExplain_ndjsonOnePerLine(t *testing.T) {
	var buf bytes.Buffer
	if err := output.ILMExplain(&buf, sampleILMExplain(), output.FormatNDJSON); err != nil {
		t.Fatalf("ILMExplain: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d:\n%s", len(lines), buf.String())
	}
}

func sampleISMPolicies() []types.ISMPolicy {
	return []types.ISMPolicy{
		{ID: "logs-rollover", SeqNo: 5, PrimaryTerm: 1,
			Definition: map[string]any{"policy": map[string]any{"description": "rotate logs daily"}}},
		{ID: "audit-90d"},
	}
}

func TestISMPolicies_tableShowsTokens(t *testing.T) {
	var buf bytes.Buffer
	if err := output.ISMPolicies(&buf, sampleISMPolicies(), output.FormatTable); err != nil {
		t.Fatalf("ISMPolicies: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ID", "SEQ_NO", "PRIMARY_TERM", "logs-rollover", "audit-90d"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestISMPolicies_wideShowsDescription(t *testing.T) {
	var buf bytes.Buffer
	if err := output.ISMPolicies(&buf, sampleISMPolicies(), output.FormatWide); err != nil {
		t.Fatalf("ISMPolicies: %v", err)
	}
	if !strings.Contains(buf.String(), "rotate logs daily") {
		t.Errorf("wide should show description:\n%s", buf.String())
	}
}

func TestISMPolicy_jsonRoundTrips(t *testing.T) {
	p := types.ISMPolicy{ID: "logs-rollover", SeqNo: 5, PrimaryTerm: 1}
	var buf bytes.Buffer
	if err := output.ISMPolicy(&buf, p, output.FormatJSON); err != nil {
		t.Fatalf("ISMPolicy: %v", err)
	}
	var got types.ISMPolicy
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if got.ID != "logs-rollover" || got.SeqNo != 5 || got.PrimaryTerm != 1 {
		t.Errorf("round-trip: %+v", got)
	}
}

func TestISMPolicyPut_tableShowsTokens(t *testing.T) {
	r := types.ISMPolicyPutResult{Action: "ism-policy-put", ID: "logs-rollover", SeqNo: 6, PrimaryTerm: 1}
	var buf bytes.Buffer
	if err := output.ISMPolicyPut(&buf, r, output.FormatTable); err != nil {
		t.Fatalf("ISMPolicyPut: %v", err)
	}
	for _, want := range []string{"ACTION", "ID", "SEQ_NO", "logs-rollover"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("table missing %q:\n%s", want, buf.String())
		}
	}
}

func TestISMPolicyDelete_jsonShowsOK(t *testing.T) {
	r := types.ISMPolicyDeleteResult{Action: "ism-policy-delete", ID: "logs-rollover", OK: true}
	var buf bytes.Buffer
	if err := output.ISMPolicyDelete(&buf, r, output.FormatJSON); err != nil {
		t.Fatalf("ISMPolicyDelete: %v", err)
	}
	var got types.ISMPolicyDeleteResult
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if !got.OK || got.ID != "logs-rollover" {
		t.Errorf("round-trip: %+v", got)
	}
}

func sampleISMExplain() []types.ISMExplainEntry {
	return []types.ISMExplainEntry{
		{Index: "logs-000001", Managed: true, PolicyID: "logs-rollover", State: "hot", Action: "rollover", Step: "attempt_rollover", Enabled: true},
		{Index: "audit-000001", Managed: false},
	}
}

func TestISMExplain_tableColumns(t *testing.T) {
	var buf bytes.Buffer
	if err := output.ISMExplain(&buf, sampleISMExplain(), output.FormatTable); err != nil {
		t.Fatalf("ISMExplain: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"INDEX", "MANAGED", "POLICY_ID", "STATE", "ACTION", "STEP",
		"logs-000001", "audit-000001", "logs-rollover"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestISMExplain_wideShowsEnabledAndStepInfo(t *testing.T) {
	rows := []types.ISMExplainEntry{
		{Index: "logs-000001", Managed: true, PolicyID: "logs-rollover", State: "hot",
			StepInfo: map[string]any{"message": "queued"}},
	}
	var buf bytes.Buffer
	if err := output.ISMExplain(&buf, rows, output.FormatWide); err != nil {
		t.Fatalf("ISMExplain: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ENABLED") || !strings.Contains(out, "STEP_INFO") {
		t.Errorf("wide missing ENABLED / STEP_INFO column:\n%s", out)
	}
}

func sampleSnapshotRepositories() []types.SnapshotRepository {
	return []types.SnapshotRepository{
		{Name: "fs", Type: "fs", Settings: map[string]any{"location": "/var/backups/es"}},
		{Name: "s3", Type: "s3", Settings: map[string]any{"bucket": "es-prod-snaps", "base_path": "eu-west-1/"}},
	}
}

func sampleSnapshotRows() []types.Snapshot {
	return []types.Snapshot{
		{Repository: "s3", Name: "nightly-2026-04-30", UUID: "uuid-a", State: "SUCCESS",
			Indices:         []string{"logs-000001", "logs-000002"},
			StartTimeMillis: 1714435200000, EndTimeMillis: 1714435320000, DurationMillis: 120_000,
			ShardsTotal: 10, ShardsSuccessful: 10},
		{Repository: "s3", Name: "nightly-2026-04-29", State: "FAILED",
			Indices: []string{"logs-000001"}, DurationMillis: 30_000,
			ShardsTotal: 10, ShardsSuccessful: 9, ShardsFailed: 1,
			Failures: []json.RawMessage{json.RawMessage(`{"index":"logs-000001","reason":"node left"}`)}},
	}
}

func TestSnapshotRepositories_jsonIsStable(t *testing.T) {
	var buf bytes.Buffer
	if err := output.SnapshotRepositories(&buf, sampleSnapshotRepositories(), output.FormatJSON); err != nil {
		t.Fatalf("SnapshotRepositories: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, buf.String())
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	for _, field := range []string{"name", "type", "settings"} {
		if _, ok := rows[0][field]; !ok {
			t.Errorf("missing field %q: %+v", field, rows[0])
		}
	}
}

func TestSnapshotRepositories_tableHasExpectedColumns(t *testing.T) {
	var buf bytes.Buffer
	if err := output.SnapshotRepositories(&buf, sampleSnapshotRepositories(), output.FormatTable); err != nil {
		t.Fatalf("SnapshotRepositories: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"NAME", "TYPE", "fs", "s3"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestSnapshotRepositories_wideIncludesSettingsPreview(t *testing.T) {
	var buf bytes.Buffer
	if err := output.SnapshotRepositories(&buf, sampleSnapshotRepositories(), output.FormatWide); err != nil {
		t.Fatalf("SnapshotRepositories: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"SETTINGS", "bucket=es-prod-snaps", "base_path=eu-west-1/", "location=/var/backups/es"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide missing %q:\n%s", want, out)
		}
	}
}

func TestSnapshotRepositories_rejectsUnknownFormat(t *testing.T) {
	if err := output.SnapshotRepositories(&bytes.Buffer{}, sampleSnapshotRepositories(), output.Format("bogus")); err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func TestSnapshots_jsonIsStable(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Snapshots(&buf, sampleSnapshotRows(), output.FormatJSON); err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, buf.String())
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	for _, field := range []string{"repository", "name", "state", "shards_total", "shards_successful", "shards_failed"} {
		if _, ok := rows[0][field]; !ok {
			t.Errorf("missing field %q: %+v", field, rows[0])
		}
	}
	// omitempty on uuid for the failed row.
	if _, ok := rows[1]["uuid"]; ok {
		t.Errorf("failed row should not have uuid: %+v", rows[1])
	}
}

func TestSnapshots_ndjsonOnePerLine(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Snapshots(&buf, sampleSnapshotRows(), output.FormatNDJSON); err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), buf.String())
	}
	for i, line := range lines {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Errorf("line %d invalid JSON: %v", i, err)
		}
	}
}

func TestSnapshots_tableHasExpectedColumns(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Snapshots(&buf, sampleSnapshotRows(), output.FormatTable); err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"REPOSITORY", "NAME", "STATE", "INDICES", "SHARDS", "DURATION",
		"nightly-2026-04-30", "SUCCESS", "FAILED", "10/10", "9/10"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestSnapshots_wideIncludesUUIDAndStart(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Snapshots(&buf, sampleSnapshotRows(), output.FormatWide); err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"UUID", "START", "FAILED", "uuid-a"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide missing %q:\n%s", want, out)
		}
	}
}

func TestSnapshots_inProgressDurationDash(t *testing.T) {
	rows := []types.Snapshot{
		{Repository: "s3", Name: "now-running", State: "IN_PROGRESS", DurationMillis: 0, ShardsTotal: 10},
	}
	var buf bytes.Buffer
	if err := output.Snapshots(&buf, rows, output.FormatTable); err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	if !strings.Contains(buf.String(), "now-running") {
		t.Errorf("table missing now-running: %s", buf.String())
	}
	// DurationMillis=0 should render as "-", not "0s".
	if strings.Contains(buf.String(), " 0s ") {
		t.Errorf("zero duration should render as '-':\n%s", buf.String())
	}
}

func TestSnapshots_rejectsUnknownFormat(t *testing.T) {
	if err := output.Snapshots(&bytes.Buffer{}, sampleSnapshotRows(), output.Format("bogus")); err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func sampleSnapshotCreateAccepted() types.SnapshotCreateResult {
	// Mirrors the cluster's response when wait_for_completion=false:
	// `{"accepted":true}` with no nested snapshot block.
	return types.SnapshotCreateResult{
		Action: "create", Repository: "fs", Name: "nightly-2026-04-30",
		Accepted: true, WaitForCompletion: false,
		Snapshot: types.Snapshot{Repository: "fs", Name: "nightly-2026-04-30", State: "STARTED"},
	}
}

func sampleSnapshotCreateCompleted() types.SnapshotCreateResult {
	return types.SnapshotCreateResult{
		Action: "create", Repository: "fs", Name: "nightly-2026-04-30",
		Accepted: true, WaitForCompletion: true,
		Snapshot: types.Snapshot{
			Repository: "fs", Name: "nightly-2026-04-30",
			State:            "SUCCESS",
			ShardsTotal:      10,
			ShardsSuccessful: 10,
			DurationMillis:   90_000,
			StartTimeMillis:  1714435200000,
		},
	}
}

func TestSnapshotCreate_jsonIsStable(t *testing.T) {
	var buf bytes.Buffer
	if err := output.SnapshotCreate(&buf, sampleSnapshotCreateAccepted(), output.FormatJSON); err != nil {
		t.Fatalf("SnapshotCreate: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal(buf.Bytes(), &row); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, buf.String())
	}
	for _, field := range []string{"action", "repository", "name", "accepted", "wait_for_completion"} {
		if _, ok := row[field]; !ok {
			t.Errorf("missing field %q: %+v", field, row)
		}
	}
}

func TestSnapshotCreate_tableShowsAcceptedFlag(t *testing.T) {
	var buf bytes.Buffer
	if err := output.SnapshotCreate(&buf, sampleSnapshotCreateAccepted(), output.FormatTable); err != nil {
		t.Fatalf("SnapshotCreate: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ACTION", "REPOSITORY", "SNAPSHOT", "ACCEPTED", "WAIT", "STATE", "create", "fs", "nightly-2026-04-30", "STARTED"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestSnapshotCreate_wideShowsShardsAndDuration(t *testing.T) {
	var buf bytes.Buffer
	if err := output.SnapshotCreate(&buf, sampleSnapshotCreateCompleted(), output.FormatWide); err != nil {
		t.Fatalf("SnapshotCreate: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"SHARDS", "DURATION", "10/10", "1m30s"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide missing %q:\n%s", want, out)
		}
	}
}

func TestSnapshotCreate_rejectsUnknownFormat(t *testing.T) {
	if err := output.SnapshotCreate(&bytes.Buffer{}, sampleSnapshotCreateAccepted(), output.Format("bogus")); err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func sampleSnapshotVerify() types.SnapshotVerifyResult {
	return types.SnapshotVerifyResult{
		Action: "verify", Repository: "fs",
		VerifiedNodes: []string{"node-id-1", "node-id-2"},
		Names:         []string{"es01", "es02"},
	}
}

func TestSnapshotVerify_jsonIsStable(t *testing.T) {
	var buf bytes.Buffer
	if err := output.SnapshotVerify(&buf, sampleSnapshotVerify(), output.FormatJSON); err != nil {
		t.Fatalf("SnapshotVerify: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal(buf.Bytes(), &row); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, buf.String())
	}
	for _, field := range []string{"action", "repository", "verified_nodes", "node_names"} {
		if _, ok := row[field]; !ok {
			t.Errorf("missing field %q: %+v", field, row)
		}
	}
}

func TestSnapshotVerify_tableShowsCount(t *testing.T) {
	var buf bytes.Buffer
	if err := output.SnapshotVerify(&buf, sampleSnapshotVerify(), output.FormatTable); err != nil {
		t.Fatalf("SnapshotVerify: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ACTION", "REPOSITORY", "NODES", "verify", "fs", "2"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestSnapshotVerify_wideListsNodeIDsAndNames(t *testing.T) {
	var buf bytes.Buffer
	if err := output.SnapshotVerify(&buf, sampleSnapshotVerify(), output.FormatWide); err != nil {
		t.Fatalf("SnapshotVerify: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"node-id-1", "node-id-2", "es01", "es02"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide missing %q:\n%s", want, out)
		}
	}
}

func TestSnapshotVerify_rejectsUnknownFormat(t *testing.T) {
	if err := output.SnapshotVerify(&bytes.Buffer{}, sampleSnapshotVerify(), output.Format("bogus")); err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func sampleNodes() []types.Node {
	return []types.Node{
		{
			Name:            "es01",
			Roles:           []string{"data", "master"},
			IsDataNode:      true,
			IP:              "10.0.0.1",
			Version:         "9.0.0",
			Master:          true,
			HeapPercent:     42,
			HeapUsedBytes:   2 * 1024 * 1024 * 1024,
			HeapMaxBytes:    8 * 1024 * 1024 * 1024,
			DiskUsedBytes:   100 * 1024 * 1024 * 1024,
			DiskAvailBytes:  900 * 1024 * 1024 * 1024,
			DiskUsedPercent: 10,
			CPUPercent:      4,
			Load1m:          "0.5",
			Uptime:          "3d",
		},
		{
			Name:       "es02",
			Roles:      []string{"data"},
			IsDataNode: true,
			Version:    "9.0.0",
		},
	}
}

func TestNodes_tableHasExpectedColumns(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Nodes(&buf, sampleNodes(), output.FormatTable); err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"NAME", "ROLES", "VERSION", "HEAP%", "DISK_USED", "es01", "es02", "9.0.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestNodes_wideAddsMasterFlagAndIP(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Nodes(&buf, sampleNodes(), output.FormatWide); err != nil {
		t.Fatalf("Nodes wide: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"MASTER", "IP", "10.0.0.1", "CPU", "LOAD_1M", "UPTIME"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide missing %q:\n%s", want, out)
		}
	}
}

func TestNodes_jsonRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Nodes(&buf, sampleNodes(), output.FormatJSON); err != nil {
		t.Fatalf("Nodes json: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("not valid JSON: %v\noutput: %s", err, buf.String())
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0]["name"] != "es01" || rows[0]["version"] != "9.0.0" {
		t.Errorf("name/version dropped: %+v", rows[0])
	}
	// omitempty: empty heap fields on es02 should not appear in JSON
	if _, ok := rows[1]["heap_used_bytes"]; ok {
		t.Errorf("zero heap_used_bytes should be omitted: %+v", rows[1])
	}
}

func TestNodes_yamlIsValid(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Nodes(&buf, sampleNodes(), output.FormatYAML); err != nil {
		t.Fatalf("Nodes yaml: %v", err)
	}
	var rows []map[string]any
	if err := yaml.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("not valid YAML: %v\noutput: %s", err, buf.String())
	}
}

func TestNodes_ndjsonOneRecordPerLine(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Nodes(&buf, sampleNodes(), output.FormatNDJSON); err != nil {
		t.Fatalf("Nodes ndjson: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Errorf("ndjson lines = %d, want 2:\n%s", len(lines), buf.String())
	}
	for _, line := range lines {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Errorf("invalid JSON line %q: %v", line, err)
		}
	}
}

func TestNodes_rejectsUnknownFormat(t *testing.T) {
	if err := output.Nodes(&bytes.Buffer{}, sampleNodes(), output.Format("bogus")); err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func sampleDrainResult() types.NodeDrainResult {
	return types.NodeDrainResult{
		NodeNames:       []string{"es01"},
		Acknowledged:    true,
		Persistent:      false,
		AlreadyExcluded: nil,
		Before:          []string{},
		After:           []string{"es01"},
		Waited:          true,
		ShardsRemaining: 0,
	}
}

func TestNodeDrain_tableShowsHeaderAndScope(t *testing.T) {
	var buf bytes.Buffer
	if err := output.NodeDrain(&buf, sampleDrainResult(), output.FormatTable); err != nil {
		t.Fatalf("NodeDrain: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ACTION", "NODES", "SCOPE", "drain", "es01", "transient", "EXCLUDE._NAME", "after:"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestNodeDrain_persistentScopeRendered(t *testing.T) {
	r := sampleDrainResult()
	r.Persistent = true
	var buf bytes.Buffer
	if err := output.NodeDrain(&buf, r, output.FormatTable); err != nil {
		t.Fatalf("NodeDrain: %v", err)
	}
	if !strings.Contains(buf.String(), "persistent") {
		t.Errorf("table should mention persistent scope:\n%s", buf.String())
	}
}

func TestNodeDrain_multiNodeShowsPerNodeBreakdown(t *testing.T) {
	r := sampleDrainResult()
	r.NodeNames = []string{"es01", "es02"}
	r.PerNodeShardsRemaining = map[string]int{"es01": 3, "es02": 0}
	r.ShardsRemaining = 3
	r.Before = []string{}
	r.After = []string{"es01", "es02"}

	var buf bytes.Buffer
	if err := output.NodeDrain(&buf, r, output.FormatTable); err != nil {
		t.Fatalf("NodeDrain: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"NODE", "SHARDS_LEFT", "es01", "es02", "3", "es01,es02"} {
		if !strings.Contains(out, want) {
			t.Errorf("multi-node table missing %q:\n%s", want, out)
		}
	}
}

func TestNodeDrain_votingExclusionsRendered(t *testing.T) {
	r := sampleDrainResult()
	r.VotingExclusionAcked = true
	r.VotingExclusionsAdded = []string{"es01"}

	var buf bytes.Buffer
	if err := output.NodeDrain(&buf, r, output.FormatTable); err != nil {
		t.Fatalf("NodeDrain: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"VOTING_CONFIG_EXCLUSIONS", "added:", "acknowledged: true"} {
		if !strings.Contains(out, want) {
			t.Errorf("voting section missing %q:\n%s", want, out)
		}
	}
}

func TestNodeDrain_jsonRoundTrips(t *testing.T) {
	r := sampleDrainResult()
	r.NodeNames = []string{"es01", "es02"}
	var buf bytes.Buffer
	if err := output.NodeDrain(&buf, r, output.FormatJSON); err != nil {
		t.Fatalf("NodeDrain json: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not valid JSON: %v\noutput: %s", err, buf.String())
	}
	names, ok := got["node_names"].([]any)
	if !ok || len(names) != 2 || names[0] != "es01" || names[1] != "es02" {
		t.Errorf("node_names = %v, want [es01 es02]", got["node_names"])
	}
}

func TestNodeDrain_rejectsUnknownFormat(t *testing.T) {
	if err := output.NodeDrain(&bytes.Buffer{}, sampleDrainResult(), output.Format("bogus")); err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func TestNodeUncordon_tableShowsHeader(t *testing.T) {
	r := types.NodeUncordonResult{
		NodeNames:    []string{"es01"},
		Acknowledged: true,
		NotInList:    nil,
		Before:       []string{"es01"},
		After:        []string{},
	}
	var buf bytes.Buffer
	if err := output.NodeUncordon(&buf, r, output.FormatTable); err != nil {
		t.Fatalf("NodeUncordon: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ACTION", "uncordon", "es01", "NOT_IN_LIST", "EXCLUDE._NAME", "before:"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestNodeUncordon_clearVotingRendered(t *testing.T) {
	r := types.NodeUncordonResult{
		Acknowledged:            true,
		VotingExclusionsCleared: true,
		VotingExclusionAcked:    true,
	}
	var buf bytes.Buffer
	if err := output.NodeUncordon(&buf, r, output.FormatTable); err != nil {
		t.Fatalf("NodeUncordon: %v", err)
	}
	if !strings.Contains(buf.String(), "VOTING_CLEARED") {
		t.Errorf("VOTING_CLEARED column missing:\n%s", buf.String())
	}
}

func TestNodeUncordon_jsonRoundTrips(t *testing.T) {
	r := types.NodeUncordonResult{NodeNames: []string{"es01"}}
	var buf bytes.Buffer
	if err := output.NodeUncordon(&buf, r, output.FormatJSON); err != nil {
		t.Fatalf("NodeUncordon json: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not valid JSON: %v\noutput: %s", err, buf.String())
	}
	names, ok := got["node_names"].([]any)
	if !ok || len(names) != 1 || names[0] != "es01" {
		t.Errorf("node_names = %v, want [es01]", got["node_names"])
	}
}

func TestNodeUncordon_rejectsUnknownFormat(t *testing.T) {
	if err := output.NodeUncordon(&bytes.Buffer{}, types.NodeUncordonResult{}, output.Format("bogus")); err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func TestNodeDrainList_tableShowsBothScopes(t *testing.T) {
	r := types.NodeDrainListResult{
		PersistentExcluded: []string{"es10"},
		TransientExcluded:  []string{"es01", "es02"},
		VotingExcluded:     []string{"es03"},
	}
	var buf bytes.Buffer
	if err := output.NodeDrainList(&buf, r, output.FormatTable); err != nil {
		t.Fatalf("NodeDrainList: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ALLOCATION_EXCLUDE", "persistent:", "transient:", "VOTING_CONFIG_EXCLUSIONS", "es01", "es02", "es03", "es10"} {
		if !strings.Contains(out, want) {
			t.Errorf("list table missing %q:\n%s", want, out)
		}
	}
}

func TestNodeDrainList_emptyListsRenderAsDash(t *testing.T) {
	var buf bytes.Buffer
	if err := output.NodeDrainList(&buf, types.NodeDrainListResult{}, output.FormatTable); err != nil {
		t.Fatalf("NodeDrainList: %v", err)
	}
	out := buf.String()
	// Three "-" placeholders, one per section, so the sections are
	// always present and grep-able.
	if strings.Count(out, "-") < 3 {
		t.Errorf("empty-list rendering should show three dashes:\n%s", out)
	}
}

func TestNodeDrainList_jsonRoundTrips(t *testing.T) {
	r := types.NodeDrainListResult{TransientExcluded: []string{"es01"}}
	var buf bytes.Buffer
	if err := output.NodeDrainList(&buf, r, output.FormatJSON); err != nil {
		t.Fatalf("NodeDrainList json: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not valid JSON: %v\noutput: %s", err, buf.String())
	}
	if names, _ := got["transient_excluded"].([]any); len(names) != 1 || names[0] != "es01" {
		t.Errorf("transient_excluded = %v, want [es01]", got["transient_excluded"])
	}
}

func TestNodeDrainList_rejectsUnknownFormat(t *testing.T) {
	if err := output.NodeDrainList(&bytes.Buffer{}, types.NodeDrainListResult{}, output.Format("bogus")); err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func sampleTasks() []types.Task {
	return []types.Task{
		{
			ID:               "nodeA:1",
			Node:             "nodeA",
			Type:             "transport",
			Action:           "indices:data/write/reindex",
			Description:      "reindex from logs-2024 to logs-2025",
			RunningTimeNanos: 90 * int64(1_000_000_000), // 90s
			Cancellable:      true,
			ParentTaskID:     "nodeA:0",
		},
		{
			ID:               "nodeB:5",
			Node:             "nodeB",
			Type:             "transport",
			Action:           "indices:admin/forcemerge",
			RunningTimeNanos: 45 * int64(1_000_000_000),
			Cancellable:      false,
		},
	}
}

func TestTasks_jsonIsStable(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Tasks(&buf, sampleTasks(), output.FormatJSON); err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("not valid JSON: %v\noutput: %s", err, buf.String())
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0]["id"] != "nodeA:1" || rows[0]["action"] != "indices:data/write/reindex" {
		t.Errorf("rows[0] = %v, want id=nodeA:1 action=…/reindex", rows[0])
	}
}

func TestTasks_ndjsonOnePerLine(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Tasks(&buf, sampleTasks(), output.FormatNDJSON); err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	for _, line := range lines {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Errorf("line not valid JSON: %v (%q)", err, line)
		}
	}
}

func TestTasks_tableHasExpectedColumns(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Tasks(&buf, sampleTasks(), output.FormatTable); err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ID", "ACTION", "RUNNING", "CANCELLABLE", "nodeA:1", "indices:data/write/reindex"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestTasks_wideAddsParentAndDescription(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Tasks(&buf, sampleTasks(), output.FormatWide); err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"PARENT", "DESCRIPTION", "nodeA:0", "reindex from logs-2024"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide table missing %q:\n%s", want, out)
		}
	}
}

func TestTasks_rejectsUnknownFormat(t *testing.T) {
	if err := output.Tasks(&bytes.Buffer{}, sampleTasks(), output.Format("bogus")); err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func TestTasksCancel_tableHeader(t *testing.T) {
	r := types.TasksCancelResult{
		Action:    "cancel",
		TaskIDs:   []string{"nodeA:1"},
		Cancelled: []types.Task{{ID: "nodeA:1", Action: "indices:data/write/reindex", Cancellable: true}},
	}
	var buf bytes.Buffer
	if err := output.TasksCancel(&buf, r, output.FormatTable); err != nil {
		t.Fatalf("TasksCancel: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ACTION", "TARGET", "DRY_RUN", "WAIT", "CANCELLED", "NODE_FAILURES", "nodeA:1"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestTasksCancel_filterTargetRendersAsFilter(t *testing.T) {
	r := types.TasksCancelResult{Action: "cancel"}
	var buf bytes.Buffer
	if err := output.TasksCancel(&buf, r, output.FormatTable); err != nil {
		t.Fatalf("TasksCancel: %v", err)
	}
	if !strings.Contains(buf.String(), "filter") {
		t.Errorf("expected TARGET=filter when no task ID, got:\n%s", buf.String())
	}
}

func TestTasksCancel_nodeFailuresSection(t *testing.T) {
	r := types.TasksCancelResult{
		Action:       "cancel",
		TaskIDs:      []string{"nodeA:1"},
		NodeFailures: []string{"nodeB: master not available"},
	}
	var buf bytes.Buffer
	if err := output.TasksCancel(&buf, r, output.FormatTable); err != nil {
		t.Fatalf("TasksCancel: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "NODE_FAILURES") || !strings.Contains(out, "master not available") {
		t.Errorf("expected node-failure section in output:\n%s", out)
	}
}

func TestTasksCancel_jsonRoundTrips(t *testing.T) {
	r := types.TasksCancelResult{
		Action:    "cancel",
		DryRun:    true,
		TaskIDs:   []string{"nodeA:1"},
		Cancelled: []types.Task{{ID: "nodeA:1", Action: "indices:data/write/reindex"}},
	}
	var buf bytes.Buffer
	if err := output.TasksCancel(&buf, r, output.FormatJSON); err != nil {
		t.Fatalf("TasksCancel: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not valid JSON: %v\noutput: %s", err, buf.String())
	}
	ids, _ := got["task_ids"].([]any)
	if got["dry_run"] != true || len(ids) != 1 || ids[0] != "nodeA:1" {
		t.Errorf("got %+v, want dry_run=true task_ids=[nodeA:1]", got)
	}
}

func TestTasksCancel_multiIDTargetCellJoinsForSmallSet(t *testing.T) {
	r := types.TasksCancelResult{
		Action:  "cancel",
		TaskIDs: []string{"n1:1", "n2:2", "n3:3"},
	}
	var buf bytes.Buffer
	if err := output.TasksCancel(&buf, r, output.FormatTable); err != nil {
		t.Fatalf("TasksCancel: %v", err)
	}
	if !strings.Contains(buf.String(), "n1:1,n2:2,n3:3") {
		t.Errorf("expected joined IDs in TARGET cell, got:\n%s", buf.String())
	}
}

func TestTasksCancel_multiIDTargetCellCountsForLargeSet(t *testing.T) {
	r := types.TasksCancelResult{
		Action:  "cancel",
		TaskIDs: []string{"n1:1", "n2:2", "n3:3", "n4:4", "n5:5"},
	}
	var buf bytes.Buffer
	if err := output.TasksCancel(&buf, r, output.FormatTable); err != nil {
		t.Fatalf("TasksCancel: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "5 ids") {
		t.Errorf("expected '5 ids' summary in TARGET cell when >3, got:\n%s", out)
	}
	// The full slice still appears in JSON output but we deliberately
	// keep the table column narrow.
	if strings.Contains(out, "n1:1,n2:2") {
		t.Errorf("table should NOT inline the full ID list when >3, got:\n%s", out)
	}
}

func TestTasksCancel_rejectsUnknownFormat(t *testing.T) {
	if err := output.TasksCancel(&bytes.Buffer{}, types.TasksCancelResult{}, output.Format("bogus")); err == nil {
		t.Fatal("expected error for unknown format")
	}
}
