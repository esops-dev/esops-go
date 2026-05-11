package elasticsearch_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/esops-dev/esops-go/pkg/types"
)

// slmTransport scripts the responses for SLM tests. Like
// captureTransport it serves the product-probe envelope before any
// real call so the upstream typed client lets the request through.
type slmTransport struct {
	probeServed atomic.Bool
	steps       []slmStep
	idx         int

	calls []slmCall
}

type slmStep struct {
	method string
	status int
	body   string
}

type slmCall struct {
	method string
	path   string
	body   string
}

func (s *slmTransport) RoundTrip(req *http.Request) (*http.Response, error) {
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
	s.calls = append(s.calls, slmCall{
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

// slmGetLifecycleBody mirrors the ES SLM `_get_lifecycle` envelope:
// top-level object keyed by policy name, value carrying version,
// modified_date_millis, the policy block and last_success/last_failure
// markers.
const slmGetLifecycleBody = `{
  "nightly": {
    "version": 3,
    "modified_date_millis": 1700000000000,
    "next_execution_millis": 1900000000000,
    "policy": {
      "name": "<nightly-{now/d}>",
      "schedule": "0 30 1 * * ?",
      "repository": "s3-prod",
      "config": {"indices": ["esops-*"]},
      "retention": {"expire_after": "30d", "min_count": 5, "max_count": 50}
    },
    "last_success": {"snapshot_name": "nightly-2026.05.09", "time_in_millis": 1715000000000},
    "last_failure": {"snapshot_name": "nightly-2026.05.08", "time_in_millis": 1714900000000}
  }
}`

func TestSnapshotPolicy_ListSurfacesScheduleAndRepository(t *testing.T) {
	tr := &slmTransport{steps: []slmStep{
		{method: http.MethodGet, body: slmGetLifecycleBody},
	}}
	c := newProbedClient(t, tr, false)

	rows, err := c.ListSnapshotPolicies(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListSnapshotPolicies: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len = %d, want 1", len(rows))
	}
	p := rows[0]
	if p.Name != "nightly" {
		t.Errorf("Name = %q, want nightly", p.Name)
	}
	if p.Schedule != "0 30 1 * * ?" {
		t.Errorf("Schedule = %q, want '0 30 1 * * ?'", p.Schedule)
	}
	if p.Repository != "s3-prod" {
		t.Errorf("Repository = %q, want s3-prod", p.Repository)
	}
	if !p.Enabled {
		t.Error("Enabled = false, want true (ES SLM treats every present policy as enabled)")
	}
	if p.Version != 3 {
		t.Errorf("Version = %d, want 3", p.Version)
	}
	if p.LastSuccessMillis != 1715000000000 {
		t.Errorf("LastSuccessMillis = %d, want 1715000000000", p.LastSuccessMillis)
	}
	if p.LastFailureMillis != 1714900000000 {
		t.Errorf("LastFailureMillis = %d, want 1714900000000", p.LastFailureMillis)
	}
	if p.NextExecutionMillis != 1900000000000 {
		t.Errorf("NextExecutionMillis = %d, want 1900000000000", p.NextExecutionMillis)
	}
}

func TestSnapshotPolicy_GetUsesPathSegment(t *testing.T) {
	tr := &slmTransport{steps: []slmStep{
		{method: http.MethodGet, body: slmGetLifecycleBody},
	}}
	c := newProbedClient(t, tr, false)

	got, err := c.GetSnapshotPolicy(context.Background(), "nightly")
	if err != nil {
		t.Fatalf("GetSnapshotPolicy: %v", err)
	}
	if got.Name != "nightly" {
		t.Errorf("Name = %q, want nightly", got.Name)
	}
	// The single-name call rides the same /_slm/policy path with the
	// name as the final segment; the body envelope is identical, the
	// adapter just filters.
	if len(tr.calls) != 1 || !strings.Contains(tr.calls[0].path, "/_slm/policy/nightly") {
		t.Errorf("path = %q, want /_slm/policy/nightly", tr.calls[0].path)
	}
}

func TestSnapshotPolicy_ExecuteReturnsSynthesisedName(t *testing.T) {
	tr := &slmTransport{steps: []slmStep{
		{method: http.MethodPost, body: `{"snapshot_name":"nightly-2026.05.10-abcdef"}`},
	}}
	c := newProbedClient(t, tr, false)

	got, err := c.ExecuteSnapshotPolicy(context.Background(), types.SnapshotPolicyExecuteRequest{Name: "nightly"})
	if err != nil {
		t.Fatalf("ExecuteSnapshotPolicy: %v", err)
	}
	if got.SnapshotName != "nightly-2026.05.10-abcdef" {
		t.Errorf("SnapshotName = %q, want the synthesised name forwarded from the cluster", got.SnapshotName)
	}
	if !got.Acknowledged {
		t.Error("Acknowledged = false, want true on a 200 OK from _execute")
	}
}

func TestSnapshotPolicy_PutForwardsBodyToSLMEndpoint(t *testing.T) {
	tr := &slmTransport{steps: []slmStep{
		{method: http.MethodPut, body: `{"acknowledged":true}`},
	}}
	c := newProbedClient(t, tr, false)

	body := []byte(`{"schedule":"0 30 1 * * ?","repository":"s3-prod"}`)
	got, err := c.PutSnapshotPolicy(context.Background(), types.SnapshotPolicyPutRequest{
		Name: "nightly",
		Body: body,
	})
	if err != nil {
		t.Fatalf("PutSnapshotPolicy: %v", err)
	}
	if !got.Acknowledged || got.Name != "nightly" {
		t.Errorf("result = %+v", got)
	}
	if len(tr.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(tr.calls))
	}
	if !strings.Contains(tr.calls[0].path, "/_slm/policy/nightly") {
		t.Errorf("PUT path = %q, want /_slm/policy/nightly", tr.calls[0].path)
	}
	if tr.calls[0].body != string(body) {
		t.Errorf("body forwarded as %q, want verbatim", tr.calls[0].body)
	}
}

func TestSnapshotPolicy_DeleteReturnsAcknowledged(t *testing.T) {
	tr := &slmTransport{steps: []slmStep{
		{method: http.MethodDelete, body: `{"acknowledged":true}`},
	}}
	c := newProbedClient(t, tr, false)

	got, err := c.DeleteSnapshotPolicy(context.Background(), types.SnapshotPolicyDeleteRequest{Name: "nightly"})
	if err != nil {
		t.Fatalf("DeleteSnapshotPolicy: %v", err)
	}
	if !got.Acknowledged {
		t.Error("Acknowledged = false, want true")
	}
}
