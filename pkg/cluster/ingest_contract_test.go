package cluster_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/pkg/client"
)

// TestIngestPipelines_dialectContract proves both adapters fold the
// shared /_ingest/pipeline envelope into the same neutral slice.
// Both dialects expose the same wire shape (`{"<id>":{
// "description":..., "processors":[...], "version":..., "_meta":...,
// "on_failure":[...]}}`); the merge into types.IngestPipeline is
// duplicated between the ES and OS adapters and a future drift must
// land here, not in production.
//
// The test lives in pkg/cluster so a dialect-probe + factory
// regression is caught at the same time — the path downstream
// consumers actually take.
func TestIngestPipelines_dialectContract(t *testing.T) {
	const pipelineBody = `{
		"esops-logs-enrich": {
			"description": "normalise level",
			"version": 1,
			"_meta": {"owner": "platform"},
			"processors": [
				{"lowercase": {"field": "level"}},
				{"set": {"field": "datacenter", "value": "dev-local"}}
			],
			"on_failure": [
				{"set": {"field": "_ingest_error", "value": "{{ _ingest.on_failure_message }}"}}
			]
		},
		"audit-tagger": {
			"description": "tag audit events",
			"processors": [
				{"set": {"field": "audit", "value": true}}
			]
		}
	}`

	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			c := newIngestPipelineClient(t, dialect, pipelineBody, http.StatusOK)
			got, err := c.IngestPipeline.ListPipelines(context.Background(), nil)
			if err != nil {
				t.Fatalf("ListPipelines: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("len(got) = %d, want 2", len(got))
			}
			// The two adapters iterate the wire map in a different
			// order than the cluster wrote it (Go map randomisation);
			// sort by name client-side just to make the assertions
			// position-independent.
			byName := map[string]int{}
			for i, p := range got {
				byName[p.Name] = i
			}
			if i, ok := byName["esops-logs-enrich"]; ok {
				p := got[i]
				if p.Version != 1 {
					t.Errorf("esops-logs-enrich.Version = %d, want 1", p.Version)
				}
				if p.Description != "normalise level" {
					t.Errorf("esops-logs-enrich.Description = %q", p.Description)
				}
				if len(p.Processors) != 2 {
					t.Errorf("len(processors) = %d, want 2", len(p.Processors))
				}
				if len(p.OnFailure) != 1 {
					t.Errorf("len(on_failure) = %d, want 1", len(p.OnFailure))
				}
				if v, ok := p.Meta["owner"].(string); !ok || v != "platform" {
					t.Errorf("_meta.owner = %v, want platform", p.Meta["owner"])
				}
			} else {
				t.Errorf("esops-logs-enrich missing from response: %+v", got)
			}

			// GetPipeline goes through the same code path but with a
			// single-id list — also covers the post-list filter.
			one, err := c.IngestPipeline.GetPipeline(context.Background(), "esops-logs-enrich")
			if err != nil {
				t.Fatalf("GetPipeline: %v", err)
			}
			if one.Name != "esops-logs-enrich" {
				t.Errorf("Get returned %q, want esops-logs-enrich", one.Name)
			}
			if !reflect.DeepEqual(one.Meta, map[string]any{"owner": "platform"}) {
				t.Errorf("Get returned _meta = %#v, want owner=platform map", one.Meta)
			}
		})
	}
}

// TestIngestPipelines_bareNotFoundIsEmpty proves a 404 on the bare
// endpoint (a cluster with no pipelines configured at all) normalises
// to an empty slice rather than an error. Operators expect "no
// pipelines" to render as an empty table, not as an exit-1 failure.
func TestIngestPipelines_bareNotFoundIsEmpty(t *testing.T) {
	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			c := newIngestPipelineClient(t, dialect, `{}`, http.StatusNotFound)
			got, err := c.IngestPipeline.ListPipelines(context.Background(), nil)
			if err != nil {
				t.Fatalf("ListPipelines: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("len(got) = %d, want 0 (404 on bare endpoint should be empty)", len(got))
			}
		})
	}
}

// TestIngestPipeline_GetNotFoundSurfacesErrNotFound proves the
// single-id lookup wraps a 404 as ErrNotFound so the CLI can render
// "pipeline not found" without scanning a list response.
func TestIngestPipeline_GetNotFoundSurfacesErrNotFound(t *testing.T) {
	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			c := newIngestPipelineClient(t, dialect, `{}`, http.StatusNotFound)
			_, err := c.IngestPipeline.GetPipeline(context.Background(), "nope")
			if !errors.Is(err, client.ErrNotFound) {
				t.Errorf("err = %v, want ErrNotFound", err)
			}
		})
	}
}

// TestIngestPipelines_namesAreForwardedInPath proves both adapters
// stitch the names list into the request path at /_ingest/pipeline/
// <comma-joined-names>. Both products accept this multi-id form on
// the path segment, so a typo collapsing names into the query string
// (e.g. `?ids=a,b`) would silently match every pipeline — fail loud
// here instead.
func TestIngestPipelines_namesAreForwardedInPath(t *testing.T) {
	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			var gotPath string
			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				writeDialectHeaders(w, dialect)
				if strings.HasPrefix(r.URL.Path, "/_ingest/pipeline") {
					gotPath = r.URL.Path
					_, _ = w.Write([]byte(`{"a":{"description":"x"},"b":{"description":"y"}}`))
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

			_, err := c.IngestPipeline.ListPipelines(context.Background(), []string{"a", "b"})
			if err != nil {
				t.Fatalf("ListPipelines: %v", err)
			}
			if gotPath != "/_ingest/pipeline/a,b" {
				t.Errorf("request path = %q, want /_ingest/pipeline/a,b", gotPath)
			}
		})
	}
}

func newIngestPipelineClient(t *testing.T, dialect, pipelineBody string, pipelineStatus int) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeDialectHeaders(w, dialect)
		if strings.HasPrefix(r.URL.Path, "/_ingest/pipeline") {
			w.WriteHeader(pipelineStatus)
			_, _ = w.Write([]byte(pipelineBody))
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
