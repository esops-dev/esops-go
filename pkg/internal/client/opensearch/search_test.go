package opensearch_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

func decodeSearchBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("request body not JSON: %v (raw=%s)", err, raw)
	}
	return m
}

func TestSearch_mergesComparisonWindowIntoBody(t *testing.T) {
	tr := &docsTransport{body: `{"hits":{"total":{"value":0},"hits":[]}}`}
	c := newProbedClient(t, tr)

	_, err := c.Search(context.Background(), types.SearchRequest{
		Indices: []string{"esops-products"},
		Body:    json.RawMessage(`{"query":{"match_all":{}},"size":7,"from":50,"_source":true}`),
		Size:    100,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if tr.gotPath != "/esops-products/_search" {
		t.Errorf("path = %q, want /esops-products/_search", tr.gotPath)
	}
	body := decodeSearchBody(t, tr.gotBody)
	if got, ok := body["size"].(float64); !ok || got != 100 {
		t.Errorf("body size = %v, want 100", body["size"])
	}
	if got, ok := body["from"].(float64); !ok || got != 0 {
		t.Errorf("body from = %v, want 0", body["from"])
	}
	if got, ok := body["_source"].(bool); !ok || got {
		t.Errorf("body _source = %v, want false", body["_source"])
	}
	if got, ok := body["track_total_hits"].(bool); !ok || !got {
		t.Errorf("body track_total_hits = %v, want true", body["track_total_hits"])
	}
}

func TestSearch_multiIndexPatternJoinedWithComma(t *testing.T) {
	tr := &docsTransport{body: `{"hits":{"total":{"value":0},"hits":[]}}`}
	c := newProbedClient(t, tr)

	if _, err := c.Search(context.Background(), types.SearchRequest{
		Indices:           []string{"logs-*", "events-*"},
		Size:              10,
		IgnoreUnavailable: true,
	}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	// Wildcards survive into the path (no per-segment escaping that
	// would turn `*` into %2A and defeat expansion).
	if tr.gotPath != "/logs-*,events-*/_search" {
		t.Errorf("path = %q, want /logs-*,events-*/_search", tr.gotPath)
	}
	if got := tr.gotQuery.Get("ignore_unavailable"); got != "true" {
		t.Errorf("ignore_unavailable = %q, want true", got)
	}
}

func TestSearch_clusterWideWhenNoIndices(t *testing.T) {
	tr := &docsTransport{body: `{"hits":{"total":{"value":0},"hits":[]}}`}
	c := newProbedClient(t, tr)

	if _, err := c.Search(context.Background(), types.SearchRequest{
		Body: json.RawMessage(`{"query":{"match_all":{}}}`),
		Size: 10,
	}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if tr.gotPath != "/_search" {
		t.Errorf("path = %q, want /_search", tr.gotPath)
	}
}

func TestSearch_decodesTotalAndHits(t *testing.T) {
	tr := &docsTransport{body: `{
		"hits":{
			"total":{"value":9,"relation":"eq"},
			"hits":[
				{"_index":"esops-products","_id":"a","_score":2.0},
				{"_index":"esops-products","_id":"b","_score":null}
			]
		}
	}`}
	c := newProbedClient(t, tr)

	res, err := c.Search(context.Background(), types.SearchRequest{Indices: []string{"esops-products"}, Size: 100})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.TotalHits != 9 {
		t.Errorf("TotalHits = %d, want 9", res.TotalHits)
	}
	if len(res.Hits) != 2 || res.Hits[0].ID != "a" || res.Hits[0].Score != 2.0 {
		t.Fatalf("hits = %+v", res.Hits)
	}
	if res.Hits[1].Score != 0 {
		t.Errorf("null _score should decode to 0, got %v", res.Hits[1].Score)
	}
}

func TestSearch_invalidBodyIsUsageError(t *testing.T) {
	tr := &docsTransport{body: `{"hits":{"total":{"value":0},"hits":[]}}`}
	c := newProbedClient(t, tr)

	_, err := c.Search(context.Background(), types.SearchRequest{
		Indices: []string{"x"},
		Body:    json.RawMessage(`"a string"`),
	})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("expected ErrUsage, got %v", err)
	}
}

func TestSearch_400IsWrappedError(t *testing.T) {
	tr := &docsTransport{
		status: 400,
		body:   `{"error":{"type":"parsing_exception","reason":"unknown query"},"status":400}`,
	}
	c := newProbedClient(t, tr)

	_, err := c.Search(context.Background(), types.SearchRequest{Indices: []string{"x"}})
	if err == nil || !strings.Contains(err.Error(), "parsing_exception") {
		t.Fatalf("expected wrapped cluster error, got %v", err)
	}
}

func TestSearch_403IsErrForbidden(t *testing.T) {
	tr := &docsTransport{status: 403, body: `{"error":"forbidden"}`}
	c := newProbedClient(t, tr)

	_, err := c.Search(context.Background(), types.SearchRequest{Indices: []string{"x"}})
	if err == nil || !errors.Is(err, client.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}
