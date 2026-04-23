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
		"":      output.FormatTable,
		"table": output.FormatTable,
		"TABLE": output.FormatTable,
		"json":  output.FormatJSON,
		"yaml":  output.FormatYAML,
		"yml":   output.FormatYAML,
		"wide":  output.FormatWide,
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

func TestParse_ndjsonNotImplemented(t *testing.T) {
	_, err := output.Parse("ndjson")
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Errorf("err = %v, want 'not implemented'", err)
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
