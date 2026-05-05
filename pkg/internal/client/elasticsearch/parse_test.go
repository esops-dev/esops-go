package elasticsearch

import "testing"

// Same parser shape as the opensearch sibling — the helpers are
// duplicated across adapters because the Go visibility rules + the
// "no dialect leak" rule from internal/client/capabilities.go don't
// allow a shared internal/client/parsing.go that both adapters import.
// These tests mirror the OS package so a future refactor that unifies
// them preserves the exact rounding / empty-cell behaviour.

func TestParseInt64(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{" ", 0},
		{"0", 0},
		{"12345", 12345},
		{"abc", 0},
	}
	for _, tc := range cases {
		if got := parseInt64(tc.in); got != tc.want {
			t.Errorf("parseInt64(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseInt(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"42", 42},
		{"abc", 0},
	}
	for _, tc := range cases {
		if got := parseInt(tc.in); got != tc.want {
			t.Errorf("parseInt(%q) = %d, want %d", tc.in, got, tc.want)
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
		{"42.4", 42},
		{"42.5", 43},
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
