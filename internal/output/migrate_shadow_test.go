package output_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/types"
)

func sampleShadow() types.MigrationShadowResult {
	return types.MigrationShadowResult{
		Action:  "migrate-verify-shadow",
		Source:  types.MigrationEndpoint{Context: "es-old", Dialect: "elasticsearch", Version: "8.13.0", URL: "http://a"},
		Target:  types.MigrationEndpoint{Context: "es-new", Dialect: "elasticsearch", Version: "9.0.0", URL: "http://b"},
		Indices: "logs-*",
		TopK:    100,
		Queries: []types.MigrationShadowEntry{
			{Name: "ok-q", SourceHits: 10, TargetHits: 10, Status: types.ShadowStatusOK},
			{Name: "diff-q", SourceHits: 10, TargetHits: 9, SetDiff: 3, OrderDiff: 1, Status: types.ShadowStatusMismatch, Detail: "3 id(s) differ between the top-K sets"},
			{Name: "err-q", Status: types.ShadowStatusError, Detail: "source search failed: boom"},
		},
		Status:     types.MigrationStatusBlock,
		Mismatches: 1,
		Errors:     1,
	}
}

func TestMigrationShadow_Table(t *testing.T) {
	var buf bytes.Buffer
	if err := output.MigrationShadow(&buf, sampleShadow(), output.FormatTable); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"SOURCE  context=es-old", "TARGET  context=es-new",
		"STATUS", "NAME", "SET_DIFF", "ORDER_DIFF", "SCORE_DIFF",
		"OK", "MISMATCH", "ERROR",
		"SUMMARY  status=BLOCK  mismatches=1  errors=1  top_k=100  strict_scores=false",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q\n---\n%s", want, out)
		}
	}
}

func TestMigrationShadow_JSONRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := output.MigrationShadow(&buf, sampleShadow(), output.FormatJSON); err != nil {
		t.Fatalf("render: %v", err)
	}
	var got types.MigrationShadowResult
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json not valid: %v", err)
	}
	if got.Mismatches != 1 || got.Errors != 1 || len(got.Queries) != 3 {
		t.Errorf("decoded = %+v", got)
	}
}

func TestMigrationShadow_YAMLBodyIsStructuredNotBytes(t *testing.T) {
	r := sampleShadow()
	r.Queries[0].Body = types.RawJSON(`{"query":{"match_all":{}}}`)

	var buf bytes.Buffer
	if err := output.MigrationShadow(&buf, r, output.FormatYAML); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	// The body must render as a nested mapping, not a base64/byte list
	// (the failure mode of a bare json.RawMessage under YAML).
	if !strings.Contains(out, "match_all") {
		t.Errorf("YAML body not rendered as structure:\n%s", out)
	}
	if strings.Contains(out, "- 123") || strings.Contains(out, "!!binary") {
		t.Errorf("YAML body rendered as raw bytes:\n%s", out)
	}
}

func TestMigrationShadow_NDJSONOnePerQuery(t *testing.T) {
	var buf bytes.Buffer
	if err := output.MigrationShadow(&buf, sampleShadow(), output.FormatNDJSON); err != nil {
		t.Fatalf("render: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("ndjson lines = %d, want 3 (one per query)", len(lines))
	}
	var first types.MigrationShadowEntry
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("ndjson line not a query record: %v", err)
	}
	if first.Name != "ok-q" {
		t.Errorf("first record = %q, want ok-q", first.Name)
	}
}
