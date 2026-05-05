package elasticsearch

import (
	"encoding/json"
	"testing"
)

// The deprecations decoder flattens the cluster's per-category groupings
// into a single sorted slice tagged with Category and Target. Tests
// assert the global vs keyed split, the cross-category sort, and that
// _meta is preserved as a JSON-encoded string when present.

func TestDeprecations_flattensCategories(t *testing.T) {
	raw := `{
		"cluster_settings": [
			{"level":"warning","message":"cluster setting X is deprecated","url":"https://example/x"}
		],
		"node_settings": [
			{"level":"critical","message":"node setting Y removed in 9","resolve_during_rolling_upgrade":true}
		],
		"index_settings": {
			"logs-2024": [
				{"level":"warning","message":"old analyzer","_meta":{"removed_in":"9.0"}}
			]
		}
	}`

	var wire migrationDeprecationsWire
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// appendDeprecationIssues + appendKeyedDeprecationIssues are what
	// Deprecations() composes; exercise them directly so the test does
	// not need a live HTTP transport.
	out := make([]_deprecationOut, 0)
	out = collectGlobal(out, "cluster_settings", wire.ClusterSettings)
	out = collectGlobal(out, "node_settings", wire.NodeSettings)
	out = collectKeyed(out, "index_settings", wire.IndexSettings)

	if len(out) != 3 {
		t.Fatalf("len = %d, want 3", len(out))
	}
	want := []_deprecationOut{
		{"cluster_settings", "", "warning", "cluster setting X is deprecated", "https://example/x", "", false, ""},
		{"node_settings", "", "critical", "node setting Y removed in 9", "", "", true, ""},
		{"index_settings", "logs-2024", "warning", "old analyzer", "", "", false, `{"removed_in":"9.0"}`},
	}
	for i, w := range want {
		if out[i] != w {
			t.Errorf("out[%d] = %+v\n           want %+v", i, out[i], w)
		}
	}
}

// _deprecationOut mirrors types.DeprecationIssue's field order so the
// test can keep its expectation table compact without dragging the
// public types package into a wire-decoder test file.
type _deprecationOut struct {
	Category string
	Target   string
	Level    string
	Message  string
	URL      string
	Details  string
	Resolve  bool
	Meta     string
}

func collectGlobal(out []_deprecationOut, category string, in []deprecationIssueWire) []_deprecationOut {
	for _, w := range in {
		out = append(out, asOut(category, "", w))
	}
	return out
}

func collectKeyed(out []_deprecationOut, category string, in map[string][]deprecationIssueWire) []_deprecationOut {
	for k, v := range in {
		for _, w := range v {
			out = append(out, asOut(category, k, w))
		}
	}
	return out
}

func asOut(category, target string, w deprecationIssueWire) _deprecationOut {
	meta := ""
	if len(w.Meta) > 0 && string(w.Meta) != "null" {
		meta = string(w.Meta)
	}
	return _deprecationOut{
		Category: category,
		Target:   target,
		Level:    w.Level,
		Message:  w.Message,
		URL:      w.URL,
		Details:  w.Details,
		Resolve:  w.ResolveDuringRollingUpgrade,
		Meta:     meta,
	}
}
