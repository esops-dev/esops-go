package cluster_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// TestSegments_dialectContract proves both adapters fold the same
// /_segments envelope into the same neutral aggregate. The
// nested-shards-to-per-index roll-up lives in segmentswire and is
// shared between adapters; the test catches a future drift in either
// adapter's request path or status-code translation.
func TestSegments_dialectContract(t *testing.T) {
	const segmentsBody = `{
		"indices": {
			"logs-1": {
				"shards": {
					"0": [
						{
							"routing": {"primary": true, "node": "n1"},
							"num_committed_segments": 2,
							"num_search_segments":    2,
							"segments": {
								"_a": {"num_docs": 10, "deleted_docs": 1, "size_in_bytes": 100, "committed": true, "search": true},
								"_b": {"num_docs": 20, "deleted_docs": 0, "size_in_bytes": 200, "committed": true, "search": true}
							}
						},
						{
							"routing": {"primary": false, "node": "n2"},
							"num_committed_segments": 2,
							"num_search_segments":    2,
							"segments": {
								"_a": {"num_docs": 10, "deleted_docs": 1, "size_in_bytes": 100, "committed": true, "search": true},
								"_b": {"num_docs": 20, "deleted_docs": 0, "size_in_bytes": 200, "committed": true, "search": true}
							}
						}
					]
				}
			}
		}
	}`
	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			c := newSegmentsClient(t, dialect, segmentsBody, http.StatusOK)
			got, err := c.Segments.Segments(context.Background(), types.SegmentsRequest{Indices: "logs-*"})
			if err != nil {
				t.Fatalf("Segments: %v", err)
			}
			if len(got.Indices) != 1 || got.Indices[0].Index != "logs-1" {
				t.Fatalf("unexpected indices: %+v", got.Indices)
			}
			ix := got.Indices[0]
			if ix.SegmentsPrimary != 2 || ix.SegmentsTotal != 4 {
				t.Errorf("primary=%d total=%d, want 2/4", ix.SegmentsPrimary, ix.SegmentsTotal)
			}
			if ix.DocsTotal != 30 || ix.DocsDeleted != 1 || ix.Bytes != 300 {
				t.Errorf("docs/deleted/bytes = %d/%d/%d, want 30/1/300", ix.DocsTotal, ix.DocsDeleted, ix.Bytes)
			}
		})
	}
}

// TestSegments_404IsEmptyResult — both adapters translate a 404 (no
// matching index) to an empty report rather than an error so a pattern
// that resolves to nothing produces a stable "nothing to merge"
// outcome on either dialect.
func TestSegments_404IsEmptyResult(t *testing.T) {
	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			c := newSegmentsClient(t, dialect, `{"error":"index_not_found"}`, http.StatusNotFound)
			got, err := c.Segments.Segments(context.Background(), types.SegmentsRequest{Indices: "missing-*"})
			if err != nil {
				t.Fatalf("Segments: %v", err)
			}
			if len(got.Indices) != 0 {
				t.Errorf("404 produced %d indices, want 0", len(got.Indices))
			}
		})
	}
}

func newSegmentsClient(t *testing.T, dialect, body string, status int) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeDialectHeaders(w, dialect)
		if strings.HasSuffix(r.URL.Path, "/_segments") {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		if dialect == dialectES {
			_, _ = w.Write([]byte(esRootBody))
		} else {
			_, _ = w.Write([]byte(osRootBody))
		}
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return newProbedContractClient(t, s.URL)
}
