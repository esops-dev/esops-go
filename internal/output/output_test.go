package output_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/dj-wasabi/esops-go/internal/output"
	"github.com/dj-wasabi/esops-go/internal/types"
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
