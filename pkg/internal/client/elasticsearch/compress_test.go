package elasticsearch_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/esops-dev/esops-go/pkg/internal/auth"
	esadapter "github.com/esops-dev/esops-go/pkg/internal/client/elasticsearch"
	"github.com/esops-dev/esops-go/pkg/config"
	"github.com/esops-dev/esops-go/pkg/types"
)

// captureTransport records outbound requests and returns canned
// responses, with a special-case for the upstream client's product
// probe (`HEAD /` etc.) so the snapshot create request actually fires.
type captureTransport struct {
	probeServed atomic.Bool
	last        *http.Request
	lastBody    []byte
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// The Elasticsearch typed client issues a product-check probe before
	// any real call. Serve a minimal valid envelope so the snapshot
	// create is allowed to proceed.
	if !c.probeServed.Load() && (req.Method == http.MethodGet || req.Method == http.MethodHead) && (req.URL.Path == "/" || req.URL.Path == "") {
		c.probeServed.Store(true)
		body := `{"version":{"number":"9.0.0","build_flavor":"default"},"tagline":"You Know, for Search"}`
		return &http.Response{
			StatusCode: 200,
			Header: http.Header{
				"Content-Type":      []string{"application/json"},
				"X-Elastic-Product": []string{"Elasticsearch"},
			},
			Body:    io.NopCloser(strings.NewReader(body)),
			Request: req,
		}, nil
	}

	c.last = req
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		c.lastBody = body
	}
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"X-Elastic-Product": []string{"Elasticsearch"}, "Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"accepted":true}`)),
		Request:    req,
	}, nil
}

func newProbedClient(t *testing.T, transport http.RoundTripper, compress bool) *esadapter.Client {
	t.Helper()
	provider, err := auth.From(config.Auth{Type: auth.TypeBasic, Username: "u", Password: "p"})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	c, err := esadapter.New([]string{"http://example.invalid:9200"}, provider, transport, esadapter.Options{
		CompressRequestBody: compress,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestSnapshotCreate_compressFlagFiresGzipOnTheWire(t *testing.T) {
	tr := &captureTransport{}
	c := newProbedClient(t, tr, true /*compress*/)

	_, err := c.CreateSnapshot(context.Background(), types.SnapshotCreateRequest{
		Repository: "fs", Snapshot: "x",
		Indices: "logs-*",
	})
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if tr.last == nil {
		t.Fatalf("no request was captured (probe may have errored)")
	}
	if got := tr.last.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip (compress=true didn't reach the wire)", got)
	}
	// Body should round-trip through gunzip back to the JSON we built.
	gz, err := gzip.NewReader(bytes.NewReader(tr.lastBody))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	plain, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(plain, &decoded); err != nil {
		t.Fatalf("decoded body is not JSON: %v\n%s", err, plain)
	}
	if decoded["indices"] != "logs-*" {
		t.Errorf("body indices = %v, want logs-*", decoded["indices"])
	}
}

func TestSnapshotCreate_noCompressFlagSendsPlainBody(t *testing.T) {
	tr := &captureTransport{}
	c := newProbedClient(t, tr, false /*compress*/)

	_, err := c.CreateSnapshot(context.Background(), types.SnapshotCreateRequest{
		Repository: "fs", Snapshot: "x", Indices: "logs-*",
	})
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if got := tr.last.Header.Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want empty (compress=false)", got)
	}
	// Plain body should already be JSON.
	var decoded map[string]any
	if err := json.Unmarshal(tr.lastBody, &decoded); err != nil {
		t.Fatalf("plain body is not JSON: %v\n%s", err, tr.lastBody)
	}
	if decoded["indices"] != "logs-*" {
		t.Errorf("body indices = %v, want logs-*", decoded["indices"])
	}
}

// silence unused
var _ = errors.New
