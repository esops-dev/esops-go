package opensearch_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/pkg/types"
)

// smTransport is a scripted RoundTripper for the SM tests. It walks
// the queue in arrival order — each entry pairs a path predicate with
// the response. Tests use it to assert the adapter's create-or-update
// dance: one POST, optionally followed by GET + PUT on 409.
type smTransport struct {
	steps []smStep
	idx   int

	calls []smCall
}

type smStep struct {
	method string
	status int
	body   string
}

type smCall struct {
	method string
	path   string
	query  string
	body   string
}

func (s *smTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	s.calls = append(s.calls, smCall{
		method: req.Method,
		path:   req.URL.Path,
		query:  req.URL.RawQuery,
		body:   string(body),
	})

	if s.idx >= len(s.steps) {
		return &http.Response{
			StatusCode: 500,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":"unscripted call"}`)),
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
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(step.body)),
		Request:    req,
	}, nil
}

// smPolicyWireJSON is a hand-built response shape that mirrors the
// /_plugins/_sm/policies/<name> envelope: top-level _id, _seq_no,
// _primary_term, plus the inner sm_policy block. Used by both
// list-mode and per-id-mode tests.
const smPolicyWireJSON = `{
  "_id":"nightly-sm-policy",
  "_seq_no":42,
  "_primary_term":7,
  "sm_policy":{
    "name":"nightly",
    "enabled":true,
    "snapshot_config":{"repository":"local-fs","indices":"esops-*"},
    "creation":{"schedule":{"cron":{"expression":"30 1 * * *","timezone":"UTC"}}}
  }
}`

func TestSnapshotPolicy_GetStripsInternalSuffix(t *testing.T) {
	// Operator typed "nightly"; the cluster echoes _id "nightly-sm-policy".
	// The adapter must strip the suffix so the displayed name matches
	// what the operator passed in.
	tr := &smTransport{steps: []smStep{
		{method: http.MethodGet, body: smPolicyWireJSON},
	}}
	c := newProbedClient(t, tr)

	got, err := c.GetSnapshotPolicy(context.Background(), "nightly")
	if err != nil {
		t.Fatalf("GetSnapshotPolicy: %v", err)
	}
	if got.Name != "nightly" {
		t.Errorf("Name = %q, want %q (the -sm-policy suffix should be stripped)", got.Name, "nightly")
	}
	if got.Repository != "local-fs" {
		t.Errorf("Repository = %q, want local-fs", got.Repository)
	}
	if got.Schedule != "30 1 * * *" {
		t.Errorf("Schedule = %q, want cron expression from creation.schedule.cron", got.Schedule)
	}
	if !got.Enabled {
		t.Error("Enabled = false, want true (the wire body said enabled:true)")
	}
}

func TestSnapshotPolicy_ListAllStripsInternalSuffix(t *testing.T) {
	listJSON := `{"policies":[
		{"_id":"nightly-sm-policy","_seq_no":1,"_primary_term":1,"sm_policy":{"name":"nightly","enabled":true,"snapshot_config":{"repository":"local-fs"}}},
		{"_id":"hourly-sm-policy","_seq_no":2,"_primary_term":1,"sm_policy":{"name":"hourly","enabled":false,"snapshot_config":{"repository":"local-fs"}}}
	],"total_policies":2}`
	tr := &smTransport{steps: []smStep{
		{method: http.MethodGet, body: listJSON},
	}}
	c := newProbedClient(t, tr)

	rows, err := c.ListSnapshotPolicies(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListSnapshotPolicies: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2", len(rows))
	}
	if rows[0].Name != "nightly" || rows[1].Name != "hourly" {
		t.Errorf("names = [%q, %q], want [nightly, hourly]", rows[0].Name, rows[1].Name)
	}
	if rows[1].Enabled {
		t.Error("Enabled for 'hourly' = true, want false (the wire body said enabled:false)")
	}
}

func TestSnapshotPolicy_PutCreatesViaPOST(t *testing.T) {
	// Fresh create: POST 200 with the cluster's echo. No GET or PUT
	// should follow.
	tr := &smTransport{steps: []smStep{
		{method: http.MethodPost, status: 200, body: smPolicyWireJSON},
	}}
	c := newProbedClient(t, tr)

	got, err := c.PutSnapshotPolicy(context.Background(), types.SnapshotPolicyPutRequest{
		Name: "nightly",
		Body: []byte(`{"snapshot_config":{"repository":"local-fs"}}`),
	})
	if err != nil {
		t.Fatalf("PutSnapshotPolicy: %v", err)
	}
	if !got.Acknowledged || got.Name != "nightly" {
		t.Errorf("result = %+v", got)
	}
	if len(tr.calls) != 1 {
		t.Fatalf("calls = %d, want 1 (POST only)", len(tr.calls))
	}
	if tr.calls[0].method != http.MethodPost {
		t.Errorf("first call method = %s, want POST", tr.calls[0].method)
	}
}

func TestSnapshotPolicy_PutOnConflictRefreshesTokens(t *testing.T) {
	// Update path: POST 409 → GET (token refresh) → PUT with
	// if_seq_no=42&if_primary_term=7. The conflict body shape mirrors
	// the OS plugin's actual response.
	conflictBody := `{"error":{"reason":"document already exists"},"status":409}`
	tr := &smTransport{steps: []smStep{
		{method: http.MethodPost, status: http.StatusConflict, body: conflictBody},
		{method: http.MethodGet, body: smPolicyWireJSON},
		{method: http.MethodPut, status: 200, body: smPolicyWireJSON},
	}}
	c := newProbedClient(t, tr)

	got, err := c.PutSnapshotPolicy(context.Background(), types.SnapshotPolicyPutRequest{
		Name: "nightly",
		Body: []byte(`{"snapshot_config":{"repository":"local-fs"}}`),
	})
	if err != nil {
		t.Fatalf("PutSnapshotPolicy (update path): %v", err)
	}
	if !got.Acknowledged {
		t.Errorf("Acknowledged = false, want true on a successful update")
	}
	if len(tr.calls) != 3 {
		t.Fatalf("calls = %d, want 3 (POST→GET→PUT)", len(tr.calls))
	}
	// The PUT must carry the tokens parsed out of the GET response.
	put := tr.calls[2]
	if put.method != http.MethodPut {
		t.Errorf("third call method = %s, want PUT", put.method)
	}
	if !strings.Contains(put.query, "if_seq_no=42") || !strings.Contains(put.query, "if_primary_term=7") {
		t.Errorf("PUT query = %q, want if_seq_no=42&if_primary_term=7 forwarded from the GET", put.query)
	}
}

func TestSnapshotPolicy_PutOnConflictGetFailureSurfaces(t *testing.T) {
	// If the token-refresh GET itself fails, the operator sees a
	// wrapped error rather than a generic 4xx — that helps debug a
	// real race (policy was deleted between the POST and the GET).
	tr := &smTransport{steps: []smStep{
		{method: http.MethodPost, status: http.StatusConflict, body: `{"error":"conflict"}`},
		{method: http.MethodGet, status: http.StatusNotFound, body: `{"error":{"reason":"policy missing"}}`},
	}}
	c := newProbedClient(t, tr)

	_, err := c.PutSnapshotPolicy(context.Background(), types.SnapshotPolicyPutRequest{
		Name: "nightly",
		Body: []byte(`{}`),
	})
	if err == nil || !strings.Contains(err.Error(), "refreshing concurrency tokens") {
		t.Fatalf("err = %v, want 'refreshing concurrency tokens' wrap", err)
	}
}

func TestSnapshotPolicy_DeleteSurfacesResult(t *testing.T) {
	tr := &smTransport{steps: []smStep{
		{method: http.MethodDelete, status: 200, body: `{"result":"deleted"}`},
	}}
	c := newProbedClient(t, tr)

	got, err := c.DeleteSnapshotPolicy(context.Background(), types.SnapshotPolicyDeleteRequest{Name: "nightly"})
	if err != nil {
		t.Fatalf("DeleteSnapshotPolicy: %v", err)
	}
	if !got.Acknowledged {
		t.Error("Acknowledged = false, want true on result:deleted")
	}
}

func TestSnapshotPolicy_DeleteUnacknowledgedNotAck(t *testing.T) {
	// Plugin reports result:"not_found" — surface as unacknowledged
	// rather than fabricating success.
	tr := &smTransport{steps: []smStep{
		{method: http.MethodDelete, status: 200, body: `{"result":"not_found"}`},
	}}
	c := newProbedClient(t, tr)

	got, err := c.DeleteSnapshotPolicy(context.Background(), types.SnapshotPolicyDeleteRequest{Name: "nightly"})
	if err != nil {
		t.Fatalf("DeleteSnapshotPolicy: %v", err)
	}
	if got.Acknowledged {
		t.Error("Acknowledged = true, want false when the plugin reports result != 'deleted'")
	}
}

func TestSnapshotPolicy_ExecuteUsesStartEndpoint(t *testing.T) {
	tr := &smTransport{steps: []smStep{
		{method: http.MethodPost, body: `{"acknowledged":true}`},
	}}
	c := newProbedClient(t, tr)

	got, err := c.ExecuteSnapshotPolicy(context.Background(), types.SnapshotPolicyExecuteRequest{Name: "nightly"})
	if err != nil {
		t.Fatalf("ExecuteSnapshotPolicy: %v", err)
	}
	if !got.Acknowledged {
		t.Error("Acknowledged = false, want true")
	}
	if got.SnapshotName != "" {
		t.Errorf("SnapshotName = %q, want empty (OS SM does not synthesise one)", got.SnapshotName)
	}
	if len(tr.calls) != 1 || !strings.HasSuffix(tr.calls[0].path, "/_start") {
		t.Errorf("call path = %q, want trailing /_start", tr.calls[0].path)
	}
}
