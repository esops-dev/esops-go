package cluster_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// TestDataStreams_dialectContract proves both adapters fold the shared
// /_data_stream envelope into the same neutral slice. Both
// Elasticsearch and OpenSearch return
// `{"data_streams":[{"name":..., "indices":[...], ...}]}`; the merge
// into types.DataStream is duplicated between the ES and OS adapters
// and a future drift must land here, not in production. The test
// lives in pkg/cluster so a dialect-probe + factory regression is
// caught at the same time.
func TestDataStreams_dialectContract(t *testing.T) {
	const body = `{
		"data_streams": [
			{
				"name": "logs-stream",
				"status": "GREEN",
				"generation": 2,
				"template": "logs-tmpl",
				"ilm_policy": "logs-policy",
				"ism_template": "logs-ism",
				"hidden": false,
				"system": false,
				"indices": [
					{"index_name": ".ds-logs-stream-000001", "index_uuid": "u1"},
					{"index_name": ".ds-logs-stream-000002", "index_uuid": "u2"}
				]
			},
			{
				"name": "metrics-stream",
				"status": "GREEN",
				"generation": 1,
				"template": "metrics-tmpl",
				"indices": [
					{"index_name": ".ds-metrics-stream-000001", "index_uuid": "u3"}
				]
			}
		]
	}`

	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			c := newDataStreamClient(t, dialect, body, http.StatusOK)
			got, err := c.DataStream.DataStreams(context.Background(), types.DataStreamFilter{})
			if err != nil {
				t.Fatalf("DataStreams: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("len(got) = %d, want 2", len(got))
			}
			byName := map[string]types.DataStream{}
			for _, d := range got {
				byName[d.Name] = d
			}
			logs, ok := byName["logs-stream"]
			if !ok {
				t.Fatalf("logs-stream missing from response: %+v", got)
			}
			if logs.Generation != 2 {
				t.Errorf("logs-stream.Generation = %d, want 2", logs.Generation)
			}
			if logs.IndexCount != 2 {
				t.Errorf("logs-stream.IndexCount = %d, want 2", logs.IndexCount)
			}
			if logs.WriteIndex != ".ds-logs-stream-000002" {
				t.Errorf("logs-stream.WriteIndex = %q, want last backing index", logs.WriteIndex)
			}
			if logs.BackingIndexOldest != ".ds-logs-stream-000001" {
				t.Errorf("logs-stream.BackingIndexOldest = %q", logs.BackingIndexOldest)
			}
			if logs.BackingIndexNewest != ".ds-logs-stream-000002" {
				t.Errorf("logs-stream.BackingIndexNewest = %q", logs.BackingIndexNewest)
			}
			// Dialect-specific fields: ILM is ES-only; ISM is OS-only.
			// The shared wire fixture carries both — each adapter is
			// expected to keep only the one its dialect surfaces.
			switch dialect {
			case dialectES:
				if logs.ILMPolicy != "logs-policy" {
					t.Errorf("ES logs-stream.ILMPolicy = %q, want logs-policy", logs.ILMPolicy)
				}
				if logs.ISMTemplate != "" {
					t.Errorf("ES logs-stream.ISMTemplate = %q, want empty (ES has no ISM)", logs.ISMTemplate)
				}
			case dialectOS:
				if logs.ISMTemplate != "logs-ism" {
					t.Errorf("OS logs-stream.ISMTemplate = %q, want logs-ism", logs.ISMTemplate)
				}
				if logs.ILMPolicy != "" {
					t.Errorf("OS logs-stream.ILMPolicy = %q, want empty (OS has no ILM)", logs.ILMPolicy)
				}
			}
		})
	}
}

// TestDataStreams_bareNotFoundIsEmpty proves a 404 on the bare
// endpoint (a cluster with no streams configured at all) normalises
// to an empty slice rather than an error. Some older builds 404
// rather than returning {"data_streams":[]}.
func TestDataStreams_bareNotFoundIsEmpty(t *testing.T) {
	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			c := newDataStreamClient(t, dialect, `{}`, http.StatusNotFound)
			got, err := c.DataStream.DataStreams(context.Background(), types.DataStreamFilter{})
			if err != nil {
				t.Fatalf("DataStreams: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("len(got) = %d, want 0 (404 on bare endpoint should be empty)", len(got))
			}
		})
	}
}

// TestDataStream_GetNotFoundSurfacesErrNotFound proves the single-id
// lookup wraps a 404 as ErrNotFound so the CLI can render "data
// stream not found" without scanning a list response.
func TestDataStream_GetNotFoundSurfacesErrNotFound(t *testing.T) {
	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			c := newDataStreamClient(t, dialect, `{}`, http.StatusNotFound)
			_, err := c.DataStream.GetDataStream(context.Background(), "nope")
			if !errors.Is(err, client.ErrNotFound) {
				t.Errorf("err = %v, want ErrNotFound", err)
			}
		})
	}
}

// TestDataStreams_namesAreForwardedInPath proves both adapters stitch
// the names list into the request path at /_data_stream/<comma-joined>.
// Both products accept this multi-id form; a typo collapsing names
// into a query string would silently match every stream — fail loud
// here instead.
func TestDataStreams_namesAreForwardedInPath(t *testing.T) {
	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			var gotPath string
			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				writeDialectHeaders(w, dialect)
				if strings.HasPrefix(r.URL.Path, "/_data_stream") {
					gotPath = r.URL.Path
					_, _ = w.Write([]byte(`{"data_streams":[]}`))
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
			c := newProbedContractClient(t, s.URL)

			_, err := c.DataStream.DataStreams(context.Background(), types.DataStreamFilter{Names: []string{"a", "b"}})
			if err != nil {
				t.Fatalf("DataStreams: %v", err)
			}
			if gotPath != "/_data_stream/a,b" {
				t.Errorf("request path = %q, want /_data_stream/a,b", gotPath)
			}
		})
	}
}

// TestDataStreamRollover_contractMethodAndPath proves both adapters
// POST to /<name>/_rollover (not to /_data_stream/<name>/_rollover
// or a query-string variant) and forward dry_run as a query
// parameter. The cluster shape is shared with index rollover.
func TestDataStreamRollover_contractMethodAndPath(t *testing.T) {
	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			var gotMethod, gotPath string
			var gotBody []byte
			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				writeDialectHeaders(w, dialect)
				if strings.Contains(r.URL.Path, "_rollover") {
					gotMethod = r.Method
					gotPath = r.URL.Path
					gotBody, _ = io.ReadAll(r.Body)
					_, _ = w.Write([]byte(`{"acknowledged":true,"shards_acknowledged":true,"old_index":".ds-logs-000001","new_index":".ds-logs-000002","rolled_over":true,"dry_run":false}`))
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
			c := newProbedContractClient(t, s.URL)

			out, err := c.DataStreamManage.RolloverDataStream(context.Background(), types.DataStreamRolloverRequest{
				Name: "logs-stream",
				Conditions: types.IndexRolloverConditions{
					MaxDocs: 1000,
				},
			})
			if err != nil {
				t.Fatalf("RolloverDataStream: %v", err)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			if gotPath != "/logs-stream/_rollover" {
				t.Errorf("path = %q, want /logs-stream/_rollover", gotPath)
			}
			if !strings.Contains(string(gotBody), `"max_docs"`) {
				t.Errorf("body = %s, want max_docs condition forwarded", string(gotBody))
			}
			if !out.RolledOver {
				t.Errorf("RolledOver = false, want true")
			}
		})
	}
}

// TestDataStreamDelete_contractMethodAndPath proves both adapters
// DELETE /_data_stream/<names> and surface acknowledged in the
// result. The cluster's response is just an ack flag — Names is
// stamped by the adapter so captured records are self-describing.
func TestDataStreamDelete_contractMethodAndPath(t *testing.T) {
	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			var gotMethod, gotPath string
			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				writeDialectHeaders(w, dialect)
				if strings.HasPrefix(r.URL.Path, "/_data_stream/") {
					gotMethod = r.Method
					gotPath = r.URL.Path
					_, _ = w.Write([]byte(`{"acknowledged":true}`))
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
			c := newProbedContractClient(t, s.URL)

			out, err := c.DataStreamManage.DeleteDataStream(context.Background(), types.DataStreamDeleteRequest{
				Names: []string{"logs-stream", "metrics-stream"},
			})
			if err != nil {
				t.Fatalf("DeleteDataStream: %v", err)
			}
			if gotMethod != http.MethodDelete {
				t.Errorf("method = %q, want DELETE", gotMethod)
			}
			if gotPath != "/_data_stream/logs-stream,metrics-stream" {
				t.Errorf("path = %q, want /_data_stream/logs-stream,metrics-stream", gotPath)
			}
			if !out.Acknowledged {
				t.Errorf("Acknowledged = false, want true")
			}
			if len(out.Names) != 2 {
				t.Errorf("Names len = %d, want 2 (adapter must echo the resolved list)", len(out.Names))
			}
		})
	}
}

// TestDataStreamModify_contractMethodAndPath proves both adapters
// POST to /_data_stream/_modify with the {"actions":[...]} envelope.
func TestDataStreamModify_contractMethodAndPath(t *testing.T) {
	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			var gotMethod, gotPath string
			var gotBody []byte
			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				writeDialectHeaders(w, dialect)
				if r.URL.Path == "/_data_stream/_modify" {
					gotMethod = r.Method
					gotPath = r.URL.Path
					gotBody, _ = io.ReadAll(r.Body)
					_, _ = w.Write([]byte(`{"acknowledged":true}`))
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
			c := newProbedContractClient(t, s.URL)

			out, err := c.DataStreamManage.ModifyDataStream(context.Background(), types.DataStreamModifyRequest{
				Actions: []types.DataStreamModifyEntry{
					{Action: types.DataStreamModifyAdd, DataStream: "logs-stream", Index: ".ds-logs-000003"},
				},
			})
			if err != nil {
				t.Fatalf("ModifyDataStream: %v", err)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			if gotPath != "/_data_stream/_modify" {
				t.Errorf("path = %q, want /_data_stream/_modify", gotPath)
			}
			if !strings.Contains(string(gotBody), `"add_backing_index"`) {
				t.Errorf("body = %s, want add_backing_index action", string(gotBody))
			}
			if !out.Acknowledged {
				t.Errorf("Acknowledged = false, want true")
			}
		})
	}
}

func newDataStreamClient(t *testing.T, dialect, body string, status int) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeDialectHeaders(w, dialect)
		if strings.HasPrefix(r.URL.Path, "/_data_stream") {
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
