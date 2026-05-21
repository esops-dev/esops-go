package elasticsearch_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// apikeyTransport scripts responses for the API-key manager tests
// (create / invalidate / get). Like the SLM/segments transports it
// serves the product-probe envelope first so the upstream client lets
// the real request through; subsequent calls fall through to the next
// scripted step.
type apikeyTransport struct {
	probeServed atomic.Bool
	steps       []apikeyStep

	calls []apikeyCall
	idx   int
}

type apikeyStep struct {
	status int
	body   string
}

type apikeyCall struct {
	method string
	path   string
	body   string
}

func (s *apikeyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !s.probeServed.Load() && (req.Method == http.MethodGet || req.Method == http.MethodHead) && (req.URL.Path == "/" || req.URL.Path == "") {
		s.probeServed.Store(true)
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

	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	s.calls = append(s.calls, apikeyCall{
		method: req.Method,
		path:   req.URL.Path,
		body:   string(body),
	})

	if s.idx >= len(s.steps) {
		return &http.Response{
			StatusCode: 500,
			Header:     http.Header{"X-Elastic-Product": []string{"Elasticsearch"}, "Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":"unscripted"}`)),
			Request:    req,
		}, nil
	}
	step := s.steps[s.idx]
	s.idx++
	status := step.status
	if status == 0 {
		status = 200
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"X-Elastic-Product": []string{"Elasticsearch"}, "Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(step.body)),
		Request:    req,
	}, nil
}

func TestCreateAPIKey_postsExpectedBodyAndDecodesSecret(t *testing.T) {
	tr := &apikeyTransport{steps: []apikeyStep{
		{body: `{"id":"abc","name":"k1","api_key":"raw-secret","encoded":"enc-secret","expiration":1900000000000}`},
	}}
	c := newProbedClient(t, tr, false)

	got, err := c.CreateAPIKey(context.Background(), types.APIKeyCreateRequest{
		Name:            "k1",
		Expiration:      "1d",
		RoleDescriptors: json.RawMessage(`{"r":{"cluster":["monitor"]}}`),
	})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if got.ID != "abc" || got.Name != "k1" {
		t.Errorf("returned id/name = %q/%q, want abc/k1", got.ID, got.Name)
	}
	if got.Key != "raw-secret" || got.Encoded != "enc-secret" {
		t.Errorf("returned key/encoded = %q/%q", got.Key, got.Encoded)
	}
	if got.Expiration == nil {
		t.Fatalf("expected non-nil expiration")
	}

	// Verify the request body shape: name, expiration, role_descriptors
	// must round-trip; we don't care about field order.
	if len(tr.calls) == 0 {
		t.Fatalf("expected at least one call")
	}
	last := tr.calls[len(tr.calls)-1]
	if last.method != http.MethodPost {
		t.Errorf("method = %s, want POST", last.method)
	}
	if last.path != "/_security/api_key" {
		t.Errorf("path = %s, want /_security/api_key", last.path)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(last.body), &parsed); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, last.body)
	}
	if parsed["name"] != "k1" {
		t.Errorf("body name = %v, want k1", parsed["name"])
	}
	if parsed["expiration"] != "1d" {
		t.Errorf("body expiration = %v, want 1d", parsed["expiration"])
	}
	if parsed["role_descriptors"] == nil {
		t.Errorf("role_descriptors missing from body: %s", last.body)
	}
}

func TestCreateAPIKey_rejectsEmptyName(t *testing.T) {
	tr := &apikeyTransport{}
	c := newProbedClient(t, tr, false)

	_, err := c.CreateAPIKey(context.Background(), types.APIKeyCreateRequest{Name: "  "})
	if err == nil {
		t.Fatalf("expected usage error on empty name")
	}
}

func TestInvalidateAPIKey_postsIDsAndDecodesResponse(t *testing.T) {
	tr := &apikeyTransport{steps: []apikeyStep{
		{body: `{"invalidated_api_keys":["abc"],"previously_invalidated_api_keys":["def"],"error_count":0}`},
	}}
	c := newProbedClient(t, tr, false)

	got, err := c.InvalidateAPIKey(context.Background(), types.APIKeyInvalidateRequest{IDs: []string{"abc", "def"}})
	if err != nil {
		t.Fatalf("InvalidateAPIKey: %v", err)
	}
	if len(got.Invalidated) != 1 || got.Invalidated[0] != "abc" {
		t.Errorf("Invalidated = %v, want [abc]", got.Invalidated)
	}
	if len(got.PreviouslyInvalidated) != 1 || got.PreviouslyInvalidated[0] != "def" {
		t.Errorf("PreviouslyInvalidated = %v, want [def]", got.PreviouslyInvalidated)
	}

	last := tr.calls[len(tr.calls)-1]
	if last.method != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", last.method)
	}
	if last.path != "/_security/api_key" {
		t.Errorf("path = %s, want /_security/api_key", last.path)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(last.body), &parsed); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, last.body)
	}
	ids, ok := parsed["ids"].([]any)
	if !ok || len(ids) != 2 || ids[0] != "abc" || ids[1] != "def" {
		t.Errorf("body ids = %v, want [abc def]", parsed["ids"])
	}
}

func TestInvalidateAPIKey_rejectsEmptyIDList(t *testing.T) {
	tr := &apikeyTransport{}
	c := newProbedClient(t, tr, false)

	_, err := c.InvalidateAPIKey(context.Background(), types.APIKeyInvalidateRequest{})
	if err == nil {
		t.Fatalf("expected usage error on empty id list")
	}
}

func TestGetAPIKey_filtersByIDFromInspectorResponse(t *testing.T) {
	tr := &apikeyTransport{steps: []apikeyStep{
		{body: `{"api_keys":[{"id":"abc","name":"k1","username":"u1","creation":1700000000000,"invalidated":false}]}`},
	}}
	c := newProbedClient(t, tr, false)

	got, err := c.GetAPIKey(context.Background(), "abc")
	if err != nil {
		t.Fatalf("GetAPIKey: %v", err)
	}
	if got.ID != "abc" || got.Name != "k1" {
		t.Errorf("got = %+v, want id=abc name=k1", got)
	}

	last := tr.calls[len(tr.calls)-1]
	if last.method != http.MethodGet {
		t.Errorf("method = %s, want GET", last.method)
	}
	if !strings.Contains(last.path, "/_security/api_key") {
		t.Errorf("path = %s, want /_security/api_key", last.path)
	}
}

func TestGetAPIKey_returnsErrNotFoundOnEmptyList(t *testing.T) {
	tr := &apikeyTransport{steps: []apikeyStep{
		{body: `{"api_keys":[]}`},
	}}
	c := newProbedClient(t, tr, false)

	_, err := c.GetAPIKey(context.Background(), "missing")
	if err == nil {
		t.Fatalf("expected error on empty result")
	}
	if !errors.Is(err, esopsclient.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
