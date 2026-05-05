package opensearch

import "testing"

// These cover the small parsers feeding `ops nodes`. The parsers are
// "best effort": a missing /_cat/nodes column on a coordinating-only
// node legitimately yields zero, and the disk-percent column changed
// from int to float somewhere in the 7.x → 9.x window. Lock both
// behaviours so a future refactor preserves them.

func TestParseInt64(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"  ", 0},
		{"0", 0},
		{"12345", 12345},
		{" 12345 ", 12345},
		{"abc", 0}, // garbage stays as zero, not an error
	}
	for _, tc := range cases {
		if got := parseInt64(tc.in); got != tc.want {
			t.Errorf("parseInt64(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParsePercent(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"42", 42},
		{"42.4", 42}, // round down
		{"42.5", 43}, // round up
		{"0", 0},
		{"abc", 0},
	}
	for _, tc := range cases {
		if got := parsePercent(tc.in); got != tc.want {
			t.Errorf("parsePercent(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseAllocationExcludeName(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want []string
	}{
		{"nil map", nil, nil},
		{"missing key", map[string]any{}, nil},
		{"empty value", map[string]any{"cluster.routing.allocation.exclude._name": ""}, nil},
		{"single", map[string]any{"cluster.routing.allocation.exclude._name": "es01"}, []string{"es01"}},
		{"comma-separated with whitespace", map[string]any{"cluster.routing.allocation.exclude._name": "es01, es02 ,es03"}, []string{"es01", "es02", "es03"}},
		{"non-string ignored", map[string]any{"cluster.routing.allocation.exclude._name": 42}, nil},
	}
	for _, tc := range cases {
		got := parseAllocationExcludeName(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("%s: len = %d, want %d (got %v)", tc.name, len(got), len(tc.want), got)
			continue
		}
		for i, v := range got {
			if v != tc.want[i] {
				t.Errorf("%s: [%d] = %q, want %q", tc.name, i, v, tc.want[i])
			}
		}
	}
}
