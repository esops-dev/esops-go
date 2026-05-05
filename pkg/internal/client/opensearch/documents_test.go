package opensearch_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/config"
	"github.com/esops-dev/esops-go/pkg/internal/auth"
	osadapter "github.com/esops-dev/esops-go/pkg/internal/client/opensearch"
	"github.com/esops-dev/esops-go/pkg/types"
)

// docsTransport replays a fixed /_search response for the OS adapter.
// Unlike the ES typed client, opensearch-go does not issue a product
// probe before the first call, so there's no special-case to serve.
type docsTransport struct {
	status int
	body   string

	gotPath   string
	gotMethod string
	gotQuery  url.Values
	gotBody   []byte
}

func (d *docsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	d.gotPath = req.URL.Path
	d.gotMethod = req.Method
	d.gotQuery = req.URL.Query()
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		d.gotBody = body
	}
	status := d.status
	if status == 0 {
		status = 200
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(d.body)),
		Request:    req,
	}, nil
}

func newProbedClient(t *testing.T, transport http.RoundTripper) *osadapter.Client {
	t.Helper()
	provider, err := auth.From(config.Auth{Type: auth.TypeBasic, Username: "u", Password: "p"})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	c, err := osadapter.New([]string{"http://example.invalid:9200"}, provider, transport, osadapter.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestDocuments_emptyIndexIsUsageError(t *testing.T) {
	tr := &docsTransport{body: `{"hits":{"hits":[]}}`}
	c := newProbedClient(t, tr)

	_, err := c.Documents(context.Background(), types.DocumentListRequest{Index: ""})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("expected ErrUsage, got %v", err)
	}
}

func TestDocumentsStream_nilYieldIsUsageError(t *testing.T) {
	tr := &docsTransport{body: `{"hits":{"hits":[]}}`}
	c := newProbedClient(t, tr)

	err := c.DocumentsStream(context.Background(), types.DocumentListRequest{Index: "x"}, nil)
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("expected ErrUsage, got %v", err)
	}
}

func TestDocuments_sendsExpectedQueryParamsAndBody(t *testing.T) {
	tr := &docsTransport{body: `{"hits":{"hits":[]}}`}
	c := newProbedClient(t, tr)

	if _, err := c.Documents(context.Background(), types.DocumentListRequest{Index: "waivers"}); err != nil {
		t.Fatalf("Documents: %v", err)
	}
	if !strings.HasPrefix(tr.gotPath, "/waivers/_search") {
		t.Errorf("path = %q, want /waivers/_search…", tr.gotPath)
	}
	if got := tr.gotQuery.Get("size"); got != "10000" {
		t.Errorf("size = %q, want 10000", got)
	}
	if got := tr.gotQuery.Get("track_total_hits"); got != "false" {
		t.Errorf("track_total_hits = %q, want false", got)
	}
	if got := tr.gotQuery.Get("version"); got != "true" {
		t.Errorf("version = %q, want true", got)
	}
	if got := tr.gotQuery.Get("seq_no_primary_term"); got != "true" {
		t.Errorf("seq_no_primary_term = %q, want true", got)
	}
	var body map[string]any
	if err := json.Unmarshal(tr.gotBody, &body); err != nil {
		t.Fatalf("body not JSON: %v (raw=%s)", err, tr.gotBody)
	}
	q, ok := body["query"].(map[string]any)
	if !ok {
		t.Fatalf("query missing or wrong shape: %v", body)
	}
	if _, ok := q["match_all"]; !ok {
		t.Errorf("expected match_all query, got %v", q)
	}
}

func TestDocuments_sendsIDsQueryWhenIDsPresent(t *testing.T) {
	tr := &docsTransport{body: `{"hits":{"hits":[]}}`}
	c := newProbedClient(t, tr)

	_, err := c.Documents(context.Background(), types.DocumentListRequest{
		Index: "waivers",
		IDs:   []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("Documents: %v", err)
	}
	var body struct {
		Query struct {
			IDs struct {
				Values []string `json:"values"`
			} `json:"ids"`
		} `json:"query"`
	}
	if err := json.Unmarshal(tr.gotBody, &body); err != nil {
		t.Fatalf("body not JSON: %v (raw=%s)", err, tr.gotBody)
	}
	if got := body.Query.IDs.Values; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("ids.values = %v, want [a b]", got)
	}
}

func TestDocuments_returnsHitsSortedByID(t *testing.T) {
	tr := &docsTransport{body: `{
		"hits":{"hits":[
			{"_index":"w","_id":"c","_source":{"v":3},"_version":7,"_seq_no":11},
			{"_index":"w","_id":"a","_source":{"v":1},"_version":4,"_seq_no":9},
			{"_index":"w","_id":"b","_source":{"v":2},"_version":5,"_seq_no":10}
		]}
	}`}
	c := newProbedClient(t, tr)

	got, err := c.Documents(context.Background(), types.DocumentListRequest{Index: "w"})
	if err != nil {
		t.Fatalf("Documents: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	wantIDs := []string{"a", "b", "c"}
	for i, w := range wantIDs {
		if got[i].ID != w {
			t.Errorf("got[%d].ID = %q, want %q", i, got[i].ID, w)
		}
	}
	if got[0].Version != 4 {
		t.Errorf("got[0].Version = %d, want 4", got[0].Version)
	}
	if got[0].SequenceNumber != 9 {
		t.Errorf("got[0].SequenceNumber = %d, want 9", got[0].SequenceNumber)
	}
	if string(got[0].Source) != `{"v":1}` {
		t.Errorf("got[0].Source = %s, want {\"v\":1}", got[0].Source)
	}
}

func TestDocumentsStream_yieldsEachHitAndStopsOnError(t *testing.T) {
	tr := &docsTransport{body: `{
		"hits":{"hits":[
			{"_index":"w","_id":"a","_source":{"v":1}},
			{"_index":"w","_id":"b","_source":{"v":2}},
			{"_index":"w","_id":"c","_source":{"v":3}}
		]}
	}`}
	c := newProbedClient(t, tr)

	var got []string
	err := c.DocumentsStream(context.Background(), types.DocumentListRequest{Index: "w"},
		func(d types.Document) error {
			got = append(got, d.ID)
			return nil
		})
	if err != nil {
		t.Fatalf("DocumentsStream: %v", err)
	}
	sort.Strings(got)
	want := []string{"a", "b", "c"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %q, want %q", i, got[i], w)
		}
	}

	tr2 := &docsTransport{body: `{"hits":{"hits":[
		{"_index":"w","_id":"a"},
		{"_index":"w","_id":"b"},
		{"_index":"w","_id":"c"}
	]}}`}
	c2 := newProbedClient(t, tr2)
	stop := errors.New("stop")
	count := 0
	err = c2.DocumentsStream(context.Background(), types.DocumentListRequest{Index: "w"},
		func(d types.Document) error {
			count++
			if count == 2 {
				return stop
			}
			return nil
		})
	if !errors.Is(err, stop) {
		t.Fatalf("expected stop error, got %v", err)
	}
	if count != 2 {
		t.Errorf("yield invoked %d times, want 2 (early-exit on yield error)", count)
	}
}

func TestDocuments_404IsWrappedError(t *testing.T) {
	tr := &docsTransport{
		status: 404,
		body:   `{"error":{"type":"index_not_found_exception","reason":"no such index [missing]"},"status":404}`,
	}
	c := newProbedClient(t, tr)

	_, err := c.Documents(context.Background(), types.DocumentListRequest{Index: "missing"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "index_not_found_exception") {
		t.Errorf("err = %v, want body in message", err)
	}
	if errors.Is(err, client.ErrUnsupported) {
		t.Errorf("err must not be ErrUnsupported for an index_not_found 404 (got %v)", err)
	}
}

func TestDocuments_searchEndpointMissingIsErrUnsupported(t *testing.T) {
	tr := &docsTransport{
		status: 404,
		body:   `{"error":"no handler found for uri [/waivers/_search] and method [POST]"}`,
	}
	c := newProbedClient(t, tr)

	_, err := c.Documents(context.Background(), types.DocumentListRequest{Index: "waivers"})
	if err == nil || !errors.Is(err, client.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

func TestDocuments_setsSourceTrueExplicitly(t *testing.T) {
	tr := &docsTransport{body: `{"hits":{"hits":[]}}`}
	c := newProbedClient(t, tr)

	if _, err := c.Documents(context.Background(), types.DocumentListRequest{Index: "w"}); err != nil {
		t.Fatalf("Documents: %v", err)
	}
	if got := tr.gotQuery.Get("_source"); got != "true" {
		t.Errorf("_source = %q, want true (the spec asks for it on the wire even though it's the cluster default)", got)
	}
}
