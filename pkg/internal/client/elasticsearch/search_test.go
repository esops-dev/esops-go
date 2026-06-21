package elasticsearch_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// decodeSearchBody unmarshals the captured request body so assertions
// can reach the merged comparison-window keys.
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
	c := newProbedClient(t, tr, false)

	_, err := c.Search(context.Background(), types.SearchRequest{
		Indices: []string{"esops-products"},
		Body:    json.RawMessage(`{"query":{"match_all":{}},"size":7,"from":50,"_source":true}`),
		Size:    100,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.HasPrefix(tr.gotPath, "/esops-products/_search") {
		t.Errorf("path = %q, want /esops-products/_search…", tr.gotPath)
	}
	body := decodeSearchBody(t, tr.gotBody)
	// size is forced to the top-K window, overriding the body's own 7.
	if got, ok := body["size"].(float64); !ok || got != 100 {
		t.Errorf("body size = %v, want 100", body["size"])
	}
	// from is reset to 0 so the window starts at the first hit, even
	// though the body asked for from:50.
	if got, ok := body["from"].(float64); !ok || got != 0 {
		t.Errorf("body from = %v, want 0", body["from"])
	}
	// _source forced off, track_total_hits forced on — regardless of body.
	if got, ok := body["_source"].(bool); !ok || got {
		t.Errorf("body _source = %v, want false", body["_source"])
	}
	if got, ok := body["track_total_hits"].(bool); !ok || !got {
		t.Errorf("body track_total_hits = %v, want true", body["track_total_hits"])
	}
	// query is preserved verbatim.
	if _, ok := body["query"].(map[string]any)["match_all"]; !ok {
		t.Errorf("query not preserved: %v", body["query"])
	}
}

func TestSearch_clusterWideWhenNoIndices(t *testing.T) {
	tr := &docsTransport{body: `{"hits":{"total":{"value":0},"hits":[]}}`}
	c := newProbedClient(t, tr, false)

	if _, err := c.Search(context.Background(), types.SearchRequest{
		Body: json.RawMessage(`{"query":{"match_all":{}}}`),
		Size: 10,
	}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if tr.gotPath != "/_search" {
		t.Errorf("path = %q, want /_search (cluster-wide)", tr.gotPath)
	}
}

func TestSearch_emptyBodyBecomesObject(t *testing.T) {
	tr := &docsTransport{body: `{"hits":{"total":{"value":0},"hits":[]}}`}
	c := newProbedClient(t, tr, false)

	if _, err := c.Search(context.Background(), types.SearchRequest{
		Indices: []string{"x"},
		Size:    5,
	}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	body := decodeSearchBody(t, tr.gotBody)
	if _, ok := body["track_total_hits"]; !ok {
		t.Errorf("empty body should still carry the comparison window: %v", body)
	}
}

func TestSearch_decodesTotalAndHits(t *testing.T) {
	tr := &docsTransport{body: `{
		"hits":{
			"total":{"value":42,"relation":"eq"},
			"hits":[
				{"_index":"esops-products","_id":"a","_score":1.5},
				{"_index":"esops-products","_id":"b","_score":null}
			]
		}
	}`}
	c := newProbedClient(t, tr, false)

	res, err := c.Search(context.Background(), types.SearchRequest{Indices: []string{"esops-products"}, Size: 100})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.TotalHits != 42 {
		t.Errorf("TotalHits = %d, want 42", res.TotalHits)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("len(Hits) = %d, want 2", len(res.Hits))
	}
	if res.Hits[0].ID != "a" || res.Hits[0].Score != 1.5 {
		t.Errorf("hit[0] = %+v, want {a 1.5}", res.Hits[0])
	}
	// A null _score decodes to 0 (e.g. a field-sorted query).
	if res.Hits[1].ID != "b" || res.Hits[1].Score != 0 {
		t.Errorf("hit[1] = %+v, want {b 0}", res.Hits[1])
	}
	if res.Hits[0].Index != "esops-products" {
		t.Errorf("hit[0].Index = %q, want esops-products", res.Hits[0].Index)
	}
}

func TestSearch_invalidBodyIsUsageError(t *testing.T) {
	tr := &docsTransport{body: `{"hits":{"total":{"value":0},"hits":[]}}`}
	c := newProbedClient(t, tr, false)

	_, err := c.Search(context.Background(), types.SearchRequest{
		Indices: []string{"x"},
		Body:    json.RawMessage(`[1,2,3]`), // array, not an object
	})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("expected ErrUsage for non-object body, got %v", err)
	}
}

func TestSearch_400IsWrappedError(t *testing.T) {
	tr := &docsTransport{
		status: 400,
		body:   `{"error":{"type":"search_phase_execution_exception","reason":"all shards failed"},"status":400}`,
	}
	c := newProbedClient(t, tr, false)

	_, err := c.Search(context.Background(), types.SearchRequest{Indices: []string{"x"}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "search_phase_execution_exception") {
		t.Errorf("err = %v, want cluster reason in message", err)
	}
	if errors.Is(err, client.ErrUnsupported) {
		t.Errorf("a 400 must not be ErrUnsupported (got %v)", err)
	}
}

func TestSearch_handlerMissingIsUnsupported(t *testing.T) {
	tr := &docsTransport{
		status: 404,
		body:   `{"error":"no handler found for uri [/x/_search] and method [POST]"}`,
	}
	c := newProbedClient(t, tr, false)

	_, err := c.Search(context.Background(), types.SearchRequest{Indices: []string{"x"}})
	if err == nil || !errors.Is(err, client.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

func TestSearch_401IsErrAuth(t *testing.T) {
	tr := &docsTransport{status: 401, body: `{"error":"unauthorized"}`}
	c := newProbedClient(t, tr, false)

	_, err := c.Search(context.Background(), types.SearchRequest{Indices: []string{"x"}})
	if err == nil || !errors.Is(err, client.ErrAuth) {
		t.Fatalf("expected ErrAuth, got %v", err)
	}
}
