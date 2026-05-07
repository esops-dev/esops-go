package elasticsearch_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/esops-dev/esops-go/pkg/client"
)

// licenseTransport replays canned /_license responses. Same probe
// special-case as the other ES tests.
type licenseTransport struct {
	probeServed atomic.Bool

	body   string
	status int
}

func (t *licenseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.probeServed.Load() && (req.Method == http.MethodGet || req.Method == http.MethodHead) && (req.URL.Path == "/" || req.URL.Path == "") {
		t.probeServed.Store(true)
		return &http.Response{
			StatusCode: 200,
			Header: http.Header{
				"Content-Type":      []string{"application/json"},
				"X-Elastic-Product": []string{"Elasticsearch"},
			},
			Body:    io.NopCloser(strings.NewReader(`{"version":{"number":"9.0.0","build_flavor":"default"},"tagline":"You Know, for Search"}`)),
			Request: req,
		}, nil
	}
	if strings.HasPrefix(req.URL.Path, "/_license") {
		status := t.status
		if status == 0 {
			status = 200
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"X-Elastic-Product": []string{"Elasticsearch"}, "Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(t.body)),
			Request:    req,
		}, nil
	}
	return &http.Response{
		StatusCode: 404,
		Header:     http.Header{"X-Elastic-Product": []string{"Elasticsearch"}, "Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"unhandled path"}`)),
		Request:    req,
	}, nil
}

func TestLicense_paidLicenceParsesAllFields(t *testing.T) {
	const body = `{
		"license": {
			"status": "active",
			"uid": "abc-123",
			"type": "platinum",
			"issued_to": "ACME Corp",
			"issuer": "elasticsearch",
			"issue_date_in_millis": 1704067200000,
			"expiry_date_in_millis": 1735689600000,
			"max_nodes": 100
		}
	}`
	tr := &licenseTransport{body: body}
	c := newProbedClient(t, tr, false)
	got, err := c.License(context.Background())
	if err != nil {
		t.Fatalf("License: %v", err)
	}
	if got.Status != "active" {
		t.Errorf("Status = %q, want active", got.Status)
	}
	if got.Type != "platinum" {
		t.Errorf("Type = %q, want platinum", got.Type)
	}
	if got.UID != "abc-123" {
		t.Errorf("UID = %q, want abc-123", got.UID)
	}
	if got.IssuedTo != "ACME Corp" {
		t.Errorf("IssuedTo = %q, want ACME Corp", got.IssuedTo)
	}
	if got.MaxNodes != 100 {
		t.Errorf("MaxNodes = %d, want 100", got.MaxNodes)
	}
	wantIssued := time.UnixMilli(1704067200000).UTC()
	if !got.IssuedAt.Equal(wantIssued) {
		t.Errorf("IssuedAt = %v, want %v", got.IssuedAt, wantIssued)
	}
	wantExpiry := time.UnixMilli(1735689600000).UTC()
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, wantExpiry)
	}
}

func TestLicense_basicLicenceHasNilExpiry(t *testing.T) {
	// Basic licence: cluster omits expiry_date_in_millis. The capability
	// must surface ExpiresAt=nil so the rule consumer renders "no expiry
	// concern" without a special-case branch.
	const body = `{"license":{"status":"active","type":"basic","uid":"u","issue_date_in_millis":1704067200000}}`
	tr := &licenseTransport{body: body}
	c := newProbedClient(t, tr, false)
	got, err := c.License(context.Background())
	if err != nil {
		t.Fatalf("License: %v", err)
	}
	if got.ExpiresAt != nil {
		t.Errorf("ExpiresAt = %v, want nil for basic licence", got.ExpiresAt)
	}
	if got.Type != "basic" {
		t.Errorf("Type = %q, want basic", got.Type)
	}
}

func TestLicense_zeroExpiryStaysNil(t *testing.T) {
	// Belt-and-braces: an explicit `expiry_date_in_millis: 0` (which the
	// cluster emits on some basic-licence revisions) must also produce
	// ExpiresAt=nil. A non-nil ExpiresAt pointing at the unix epoch
	// would surface as "expired in 1970" in a renderer.
	const body = `{"license":{"status":"active","type":"basic","uid":"u","issue_date_in_millis":1704067200000,"expiry_date_in_millis":0}}`
	tr := &licenseTransport{body: body}
	c := newProbedClient(t, tr, false)
	got, err := c.License(context.Background())
	if err != nil {
		t.Fatalf("License: %v", err)
	}
	if got.ExpiresAt != nil {
		t.Errorf("ExpiresAt = %v, want nil for zero expiry millis", got.ExpiresAt)
	}
}

func TestLicense_404IsErrUnsupported(t *testing.T) {
	tr := &licenseTransport{status: 404, body: `{"error":"no handler"}`}
	c := newProbedClient(t, tr, false)
	_, err := c.License(context.Background())
	if !errors.Is(err, client.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestLicense_unauthorizedIsErrAuth(t *testing.T) {
	tr := &licenseTransport{status: 401, body: `{"error":"unauthorized"}`}
	c := newProbedClient(t, tr, false)
	_, err := c.License(context.Background())
	if !errors.Is(err, client.ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

func TestLicense_forbiddenIsErrForbidden(t *testing.T) {
	tr := &licenseTransport{status: 403, body: `{"error":"forbidden"}`}
	c := newProbedClient(t, tr, false)
	_, err := c.License(context.Background())
	if !errors.Is(err, client.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}
