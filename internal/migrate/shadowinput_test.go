package migrate_test

import (
	"errors"
	"strings"
	"testing"

	esopsmigrate "github.com/esops-dev/esops-go/internal/migrate"
	"github.com/esops-dev/esops-go/pkg/client"
)

func TestParseShadowQueriesNDJSON_NamesAndBodies(t *testing.T) {
	in := strings.NewReader(`
{"name":"by-name","body":{"query":{"match_all":{}}}}

{"body":{"query":{"term":{"k":"v"}}}}
`)
	qs, err := esopsmigrate.ParseShadowQueriesNDJSON(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("got %d queries, want 2 (blank lines skipped)", len(qs))
	}
	if qs[0].Name != "by-name" {
		t.Errorf("q0 name = %q, want by-name", qs[0].Name)
	}
	if qs[1].Name != "q2" { // auto-named by input position
		t.Errorf("q1 name = %q, want q2", qs[1].Name)
	}
}

func TestParseShadowQueriesNDJSON_RejectsNonObjectBody(t *testing.T) {
	_, err := esopsmigrate.ParseShadowQueriesNDJSON(strings.NewReader(`{"name":"x","body":[1,2]}`))
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("expected usage error for array body, got %v", err)
	}
}

func TestParseShadowQueriesNDJSON_RejectsMalformedLineWithNumber(t *testing.T) {
	_, err := esopsmigrate.ParseShadowQueriesNDJSON(strings.NewReader("{\"name\":\"ok\",\"body\":{}}\nnot json\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("expected error citing line 2, got %v", err)
	}
}

func TestParseShadowQueriesNDJSON_EmptyIsUsageError(t *testing.T) {
	_, err := esopsmigrate.ParseShadowQueriesNDJSON(strings.NewReader("\n\n"))
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("expected usage error for empty input, got %v", err)
	}
}

func TestParseShadowSlowlog_PlainTextDedupAndRank(t *testing.T) {
	// match_all appears twice, term once. Frequency ranking puts
	// match_all first; the unrelated line is ignored.
	in := strings.NewReader(strings.Join([]string{
		`[2026-06-01T12:00:00,000][WARN][index.search.slowlog.query] [n1] [products][0] took[1s], total_hits[5 hits], source[{"query":{"match_all":{}}}], id[]`,
		`[2026-06-01T12:00:01,000][WARN][index.search.slowlog.query] [n1] [products][0] took[1s], total_hits[5 hits], source[{"query":{"match_all":{}}}], id[]`,
		`[2026-06-01T12:00:02,000][WARN][index.search.slowlog.query] [n1] [products][0] took[1s], total_hits[2 hits], source[{"query":{"term":{"k":"v"}}}], id[]`,
		`unrelated line, no source here`,
	}, "\n"))

	qs, err := esopsmigrate.ParseShadowSlowlog(in, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("got %d distinct queries, want 2", len(qs))
	}
	if qs[0].Name != "slowlog-1" || !strings.Contains(string(qs[0].Body), "match_all") {
		t.Errorf("q0 = %+v, want the match_all (most frequent) first", qs[0])
	}
	if !strings.Contains(string(qs[1].Body), "term") {
		t.Errorf("q1 = %s, want the term query", qs[1].Body)
	}
}

func TestParseShadowSlowlog_StructuredJSONSourceField(t *testing.T) {
	in := strings.NewReader(`{"type":"index_search_slowlog","level":"WARN","source":"{\"query\":{\"match\":{\"title\":\"go\"}}}"}`)
	qs, err := esopsmigrate.ParseShadowSlowlog(in, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(qs) != 1 || !strings.Contains(string(qs[0].Body), `"match"`) {
		t.Fatalf("expected one match query from structured slowlog, got %+v", qs)
	}
}

func TestParseShadowSlowlog_TopNLimits(t *testing.T) {
	in := strings.NewReader(strings.Join([]string{
		`x source[{"query":{"a":{}}}]`,
		`x source[{"query":{"a":{}}}]`, // a x2
		`x source[{"query":{"b":{}}}]`,
		`x source[{"query":{"c":{}}}]`,
	}, "\n"))
	qs, err := esopsmigrate.ParseShadowSlowlog(in, 1)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(qs) != 1 {
		t.Fatalf("top-N=1 should yield 1 query, got %d", len(qs))
	}
	if !strings.Contains(string(qs[0].Body), `"a"`) {
		t.Errorf("top query = %s, want the most frequent (a)", qs[0].Body)
	}
}

func TestParseShadowSlowlog_AnchorsSourceMarkerPastDecoys(t *testing.T) {
	// A `datasource[...]` decoy and a `note_source[...]` decoy whose body
	// doesn't parse must both be skipped in favour of the real
	// `source[...]` later on the same line.
	in := strings.NewReader(`x datasource[{"not":"it"}] note_source[broken {nope] source[{"query":{"match_all":{}}}]`)
	qs, err := esopsmigrate.ParseShadowSlowlog(in, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(qs) != 1 {
		t.Fatalf("got %d queries, want 1", len(qs))
	}
	if !strings.Contains(string(qs[0].Body), "match_all") || strings.Contains(string(qs[0].Body), `"not"`) {
		t.Errorf("extracted the wrong source: %s", qs[0].Body)
	}
}

func TestParseShadowSlowlog_PreservesLargeIntegerInReplayBody(t *testing.T) {
	// The replayed body must keep an integer literal verbatim — a
	// float64 round-trip would mangle it and replay a different query.
	in := strings.NewReader(`x source[{"query":{"term":{"id":12345678901234567890}}}]`)
	qs, err := esopsmigrate.ParseShadowSlowlog(in, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.Contains(string(qs[0].Body), "12345678901234567890") {
		t.Errorf("large integer mangled in replay body: %s", qs[0].Body)
	}
}

func TestParseShadowSlowlog_NoQueriesIsUsageError(t *testing.T) {
	_, err := esopsmigrate.ParseShadowSlowlog(strings.NewReader("nothing useful here\n"), 0)
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestParseShadowSlowlog_DedupNormalizesKeyOrder(t *testing.T) {
	// Same query, different key order — must collapse to one entry.
	in := strings.NewReader(strings.Join([]string{
		`x source[{"size":1,"query":{"match_all":{}}}]`,
		`x source[{"query":{"match_all":{}},"size":1}]`,
	}, "\n"))
	qs, err := esopsmigrate.ParseShadowSlowlog(in, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(qs) != 1 {
		t.Fatalf("key-order variants should dedupe to 1, got %d", len(qs))
	}
}
