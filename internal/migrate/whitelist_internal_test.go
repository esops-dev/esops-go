package migrate

// Internal-package tests for the unexported whitelist-matching helpers.
// The behaviour-style tests in migrate_test.go cover the happy paths via
// Check; this file exercises edge cases (multi-star wildcards, scheme
// prefixes, malformed hosts) that the integration cases would obscure.

import "testing"

func TestHostPortFromURL(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{in: "", want: "", wantOK: false},
		{in: "http://host:9200", want: "host:9200", wantOK: true},
		{in: "https://host:9200/", want: "host:9200", wantOK: true},
		// No scheme — treat as a bare host:port literal (the cluster's
		// own match accepts this shape too, modulo a stripped scheme).
		{in: "host:9200", want: "host:9200", wantOK: true},
		// A scheme-only URL (no host) parses but produces an empty
		// Host; we fall back to the trimmed raw input.
		{in: "host", want: "host", wantOK: true},
		// Trailing slash on a bare host is stripped to keep the
		// downstream matcher's job simple.
		{in: "host:9200/", want: "host:9200", wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := hostPortFromURL(tc.in)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("got (%q, %v), want (%q, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestMatchWhitelist(t *testing.T) {
	cases := []struct {
		name     string
		entries  []string
		hostPort string
		want     bool
	}{
		{name: "empty list", entries: nil, hostPort: "host:9200", want: false},
		{name: "literal match", entries: []string{"host:9200"}, hostPort: "host:9200", want: true},
		{
			name:     "literal match case-insensitive",
			entries:  []string{"HOST:9200"},
			hostPort: "host:9200",
			want:     true,
		},
		{
			name:     "scheme prefix on entry is stripped",
			entries:  []string{"https://host:9200"},
			hostPort: "host:9200",
			want:     true,
		},
		{
			name:     "subdomain wildcard",
			entries:  []string{"*.internal:9200"},
			hostPort: "es01.internal:9200",
			want:     true,
		},
		{
			name:     "any-port wildcard",
			entries:  []string{"host:*"},
			hostPort: "host:9201",
			want:     true,
		},
		{
			name:     "multi-star pattern",
			entries:  []string{"*-prod-*:9200"},
			hostPort: "es01-prod-eu:9200",
			want:     true,
		},
		{
			name:     "no match",
			entries:  []string{"other:9200"},
			hostPort: "host:9200",
			want:     false,
		},
		{
			name:     "blank entries skipped",
			entries:  []string{"", "  ", "host:9200"},
			hostPort: "host:9200",
			want:     true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchWhitelist(tc.entries, tc.hostPort); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWildcardMatch(t *testing.T) {
	cases := []struct {
		pat, s string
		want   bool
	}{
		{pat: "abc", s: "abc", want: true},
		{pat: "abc", s: "abd", want: false},
		{pat: "*", s: "anything", want: true},
		{pat: "*", s: "", want: true},
		{pat: "a*", s: "abc", want: true},
		{pat: "a*", s: "x", want: false},
		{pat: "*c", s: "abc", want: true},
		{pat: "*c", s: "abd", want: false},
		{pat: "a*c", s: "abc", want: true},
		{pat: "a*c", s: "axyzc", want: true},
		{pat: "a*c", s: "axyzd", want: false},
		// Multi-star — the matcher is recursive on its tail half.
		{pat: "a*b*c", s: "axxbyyc", want: true},
		{pat: "a*b*c", s: "axxc", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.pat+"/"+tc.s, func(t *testing.T) {
			if got := wildcardMatch(tc.pat, tc.s); got != tc.want {
				t.Errorf("wildcardMatch(%q, %q) = %v, want %v", tc.pat, tc.s, got, tc.want)
			}
		})
	}
}

func TestMajorOf(t *testing.T) {
	cases := []struct {
		in     string
		want   int
		wantOK bool
	}{
		{in: "9.3.3", want: 9, wantOK: true},
		{in: "7", want: 7, wantOK: true},
		{in: "1.0", want: 1, wantOK: true},
		{in: "  9.3.3  ", want: 9, wantOK: true},
		{in: "", want: 0, wantOK: false},
		{in: "x.y.z", want: 0, wantOK: false},
		{in: "v9.3", want: 0, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := majorOf(tc.in)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("got (%d, %v), want (%d, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestEstimateSeconds(t *testing.T) {
	cases := []struct {
		docs       int64
		throughput int
		want       float64
	}{
		{docs: 0, throughput: 1000, want: 0},
		{docs: 1000, throughput: 0, want: 0},
		{docs: 1000, throughput: 1000, want: 1},
		{docs: 1_500_000, throughput: 5000, want: 300},
		{docs: -5, throughput: 1000, want: 0},
	}
	for _, tc := range cases {
		got := estimateSeconds(tc.docs, tc.throughput)
		if got != tc.want {
			t.Errorf("estimateSeconds(%d, %d) = %v, want %v", tc.docs, tc.throughput, got, tc.want)
		}
	}
}
