package config

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/output"
	esopsconfig "github.com/esops-dev/esops-go/pkg/config"
)

func TestSortedNames_alphabetical(t *testing.T) {
	in := map[string]esopsconfig.Context{
		"zeta":  {},
		"alpha": {},
		"mid":   {},
	}
	got := sortedNames(in)
	want := []string{"alpha", "mid", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q, want %q", i, got[i], want[i])
		}
	}
}

func sampleEntries() []contextEntry {
	return []contextEntry{
		{
			Name:        "local-es",
			Current:     true,
			URL:         "http://localhost:9200",
			Protection:  "none",
			MaxInFlight: 4,
		},
		{
			Name:       "local-os",
			URL:        "http://localhost:9202",
			AuthType:   "basic",
			Protection: "none",
		},
	}
}

func TestRenderTable_narrow(t *testing.T) {
	var buf bytes.Buffer
	if err := renderTable(&buf, sampleEntries(), false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	header := "CURRENT"
	if !strings.HasPrefix(out, header) {
		t.Errorf("output should start with CURRENT header, got:\n%s", out)
	}
	// Column headers present, wide-only columns absent.
	for _, want := range []string{"NAME", "URL", "AUTH", "PROTECTION"} {
		if !strings.Contains(out, want) {
			t.Errorf("narrow output missing %q:\n%s", want, out)
		}
	}
	for _, unwant := range []string{"MAX_IN_FLIGHT", "TLS_INSECURE", "CACERT"} {
		if strings.Contains(out, unwant) {
			t.Errorf("narrow output should not include %q:\n%s", unwant, out)
		}
	}
	// Current marker attached to local-es, not local-os.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 1 header + 2 rows, got %d lines:\n%s", len(lines), out)
	}
	if !strings.HasPrefix(lines[1], "*") {
		t.Errorf("row 1 (local-es) should carry current marker, got: %q", lines[1])
	}
	if strings.HasPrefix(lines[2], "*") {
		t.Errorf("row 2 (local-os) should NOT carry current marker, got: %q", lines[2])
	}
	// Empty auth renders as "none".
	if !strings.Contains(lines[1], "none") {
		t.Errorf("row 1 should render empty auth as 'none', got: %q", lines[1])
	}
}

func TestRenderTable_wide(t *testing.T) {
	var buf bytes.Buffer
	if err := renderTable(&buf, sampleEntries(), true); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	for _, want := range []string{"MAX_IN_FLIGHT", "TLS_INSECURE", "CACERT"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide output missing %q:\n%s", want, out)
		}
	}
	// MaxInFlight=4 from local-es should appear, and missing CACERT renders as "-".
	if !strings.Contains(out, "  4  ") {
		t.Errorf("expected max_in_flight=4 column, got:\n%s", out)
	}
	if !strings.Contains(out, "-\n") {
		t.Errorf("expected empty CACERT to render as '-' at end of row, got:\n%s", out)
	}
}

func TestRenderTable_multipleURLsWideJoined(t *testing.T) {
	entries := []contextEntry{
		{
			Name:       "multi",
			URLs:       []string{"http://a", "http://b"},
			Protection: "none",
		},
	}

	var narrow, wide bytes.Buffer
	if err := renderTable(&narrow, entries, false); err != nil {
		t.Fatal(err)
	}
	if err := renderTable(&wide, entries, true); err != nil {
		t.Fatal(err)
	}

	// Narrow uses only the first URL.
	if !strings.Contains(narrow.String(), "http://a") || strings.Contains(narrow.String(), "http://b") {
		t.Errorf("narrow should show only first URL, got:\n%s", narrow.String())
	}
	// Wide joins all URLs with a comma.
	if !strings.Contains(wide.String(), "http://a,http://b") {
		t.Errorf("wide should show joined URLs, got:\n%s", wide.String())
	}
}

func TestRenderTable_emptyList(t *testing.T) {
	var buf bytes.Buffer
	if err := renderTable(&buf, nil, false); err != nil {
		t.Fatal(err)
	}
	// Header only, no rows.
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Errorf("expected header-only output, got %d lines:\n%s", len(lines), buf.String())
	}
}

func TestBuildEntries_sortsAndMarksCurrent(t *testing.T) {
	cfg := esopsconfig.Config{
		CurrentContext: "beta",
		Contexts: map[string]esopsconfig.Context{
			"alpha": {URL: "http://a"},
			"beta":  {URL: "http://b", Auth: esopsconfig.Auth{Type: "basic"}},
			"gamma": {URLs: []string{"http://g1", "http://g2"}, Protection: "prod"},
		},
	}
	got := buildEntries(cfg)

	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	wantNames := []string{"alpha", "beta", "gamma"}
	for i, e := range got {
		if e.Name != wantNames[i] {
			t.Errorf("[%d] name = %q, want %q", i, e.Name, wantNames[i])
		}
	}
	if got[0].Current || !got[1].Current || got[2].Current {
		t.Errorf("only 'beta' should be Current, got currents: %v %v %v",
			got[0].Current, got[1].Current, got[2].Current)
	}
	if got[1].AuthType != "basic" {
		t.Errorf("beta auth type = %q, want basic", got[1].AuthType)
	}
	if len(got[2].URLs) != 2 || got[2].Protection != "prod" {
		t.Errorf("gamma projection wrong: %+v", got[2])
	}
}

func TestBuildEntries_emptyConfig(t *testing.T) {
	got := buildEntries(esopsconfig.Config{})
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %d entries", len(got))
	}
}

func TestRenderContexts_json(t *testing.T) {
	var buf bytes.Buffer
	err := renderContexts(&buf, sampleEntries(), output.FormatJSON)
	if err != nil {
		t.Fatal(err)
	}

	var decoded []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if len(decoded) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(decoded))
	}
	if decoded[0]["name"] != "local-es" {
		t.Errorf("[0].name = %v, want local-es", decoded[0]["name"])
	}
	if decoded[0]["current"] != true {
		t.Errorf("[0].current = %v, want true", decoded[0]["current"])
	}
	// max_in_flight:4 round-trips as JSON number (float64 via any).
	if n, _ := decoded[0]["max_in_flight"].(float64); n != 4 {
		t.Errorf("[0].max_in_flight = %v, want 4", decoded[0]["max_in_flight"])
	}
	// Omitempty keeps zero MaxInFlight off the second entry.
	if _, present := decoded[1]["max_in_flight"]; present {
		t.Errorf("[1] should omit max_in_flight when zero, got: %v", decoded[1])
	}
}

func TestRenderContexts_yaml(t *testing.T) {
	var buf bytes.Buffer
	if err := renderContexts(&buf, sampleEntries(), output.FormatYAML); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	for _, want := range []string{
		"name: local-es",
		"current: true",
		"name: local-os",
		"auth_type: basic",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("YAML output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderContexts_wideAndTableDispatch(t *testing.T) {
	var wide, narrow bytes.Buffer
	if err := renderContexts(&wide, sampleEntries(), output.FormatWide); err != nil {
		t.Fatal(err)
	}
	if err := renderContexts(&narrow, sampleEntries(), output.FormatTable); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wide.String(), "MAX_IN_FLIGHT") {
		t.Errorf("wide dispatch should include wide columns:\n%s", wide.String())
	}
	if strings.Contains(narrow.String(), "MAX_IN_FLIGHT") {
		t.Errorf("table dispatch should not include wide columns:\n%s", narrow.String())
	}
}

func TestRenderContexts_unknownFormatErrors(t *testing.T) {
	var buf bytes.Buffer
	err := renderContexts(&buf, sampleEntries(), output.Format("weirdness"))
	if err == nil {
		t.Fatal("expected error for unknown format")
	}
	if !strings.Contains(err.Error(), "weirdness") {
		t.Errorf("error should mention the bad format: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output on unknown format, got:\n%s", buf.String())
	}
}
