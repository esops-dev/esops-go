package index_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/internal/index"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeISM is the ISM equivalent of fakeILM (above) — a hand-rolled
// stand-in for client.ISMManager that records every call so tests
// don't need a live cluster.
type fakeISM struct {
	gotIDs      []string
	policies    []types.ISMPolicy
	puts        []types.ISMPolicyPutRequest
	deletes     []types.ISMPolicyDeleteRequest
	explainReqs []types.ISMExplainRequest
	explainOut  []types.ISMExplainEntry
	err         error
}

func (f *fakeISM) ListPolicies(_ context.Context, ids []string) ([]types.ISMPolicy, error) {
	f.gotIDs = append([]string(nil), ids...)
	return f.policies, f.err
}

func (f *fakeISM) GetPolicy(_ context.Context, id string) (types.ISMPolicy, error) {
	f.gotIDs = append([]string(nil), id)
	if f.err != nil {
		return types.ISMPolicy{}, f.err
	}
	for _, p := range f.policies {
		if p.ID == id {
			return p, nil
		}
	}
	return types.ISMPolicy{}, fmt.Errorf("%w: ism policy %q", client.ErrNotFound, id)
}

func (f *fakeISM) PutPolicy(_ context.Context, req types.ISMPolicyPutRequest) (types.ISMPolicyPutResult, error) {
	f.puts = append(f.puts, req)
	if f.err != nil {
		return types.ISMPolicyPutResult{}, f.err
	}
	return types.ISMPolicyPutResult{Action: "ism-policy-put", ID: req.ID, SeqNo: 1, PrimaryTerm: 1}, nil
}

func (f *fakeISM) DeletePolicy(_ context.Context, req types.ISMPolicyDeleteRequest) (types.ISMPolicyDeleteResult, error) {
	f.deletes = append(f.deletes, req)
	if f.err != nil {
		return types.ISMPolicyDeleteResult{}, f.err
	}
	return types.ISMPolicyDeleteResult{Action: "ism-policy-delete", ID: req.ID, OK: true}, nil
}

func (f *fakeISM) ExplainPolicyStream(_ context.Context, req types.ISMExplainRequest, yield func(types.ISMExplainEntry) error) error {
	f.explainReqs = append(f.explainReqs, req)
	if f.err != nil {
		return f.err
	}
	for _, e := range f.explainOut {
		if err := yield(e); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeISM) ExplainPolicy(_ context.Context, req types.ISMExplainRequest) ([]types.ISMExplainEntry, error) {
	f.explainReqs = append(f.explainReqs, req)
	return f.explainOut, f.err
}

func sampleISMPolicies() []types.ISMPolicy {
	return []types.ISMPolicy{
		{ID: "logs-rollover"},
		{ID: "audit-90d"},
		{ID: "metrics-7d"},
	}
}

func TestListISMPolicies_sortsByID(t *testing.T) {
	m := &fakeISM{policies: sampleISMPolicies()}
	out, err := index.ListISMPolicies(context.Background(), m, index.ISMListOptions{})
	if err != nil {
		t.Fatalf("ListISMPolicies: %v", err)
	}
	if len(out) != 3 || out[0].ID != "audit-90d" || out[1].ID != "logs-rollover" || out[2].ID != "metrics-7d" {
		t.Errorf("sort order: %v", policyIDs(out))
	}
}

func TestListISMPolicies_passesIDsThrough(t *testing.T) {
	m := &fakeISM{}
	_, err := index.ListISMPolicies(context.Background(), m, index.ISMListOptions{
		IDs: []string{"logs-rollover", "audit-90d"},
	})
	if err != nil {
		t.Fatalf("ListISMPolicies: %v", err)
	}
	if len(m.gotIDs) != 2 || m.gotIDs[0] != "logs-rollover" || m.gotIDs[1] != "audit-90d" {
		t.Errorf("ids forwarded = %v", m.gotIDs)
	}
}

func TestGetISMPolicy_requiresID(t *testing.T) {
	m := &fakeISM{policies: sampleISMPolicies()}
	_, err := index.GetISMPolicy(context.Background(), m, "")
	if err == nil || !strings.Contains(err.Error(), "--id") {
		t.Errorf("expected --id error, got: %v", err)
	}
}

func TestGetISMPolicy_rejectsPattern(t *testing.T) {
	m := &fakeISM{policies: sampleISMPolicies()}
	_, err := index.GetISMPolicy(context.Background(), m, "logs-*")
	if err == nil || !strings.Contains(err.Error(), "literal") {
		t.Errorf("expected literal-id error, got: %v", err)
	}
}

func TestGetISMPolicy_returnsMatchingEntry(t *testing.T) {
	m := &fakeISM{policies: sampleISMPolicies()}
	got, err := index.GetISMPolicy(context.Background(), m, "audit-90d")
	if err != nil {
		t.Fatalf("GetISMPolicy: %v", err)
	}
	if got.ID != "audit-90d" {
		t.Errorf("got %q, want audit-90d", got.ID)
	}
}

func TestGetISMPolicy_notFound(t *testing.T) {
	m := &fakeISM{policies: sampleISMPolicies()}
	_, err := index.GetISMPolicy(context.Background(), m, "missing")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got: %v", err)
	}
}

func TestPutISMPolicy_validatesID(t *testing.T) {
	m := &fakeISM{}
	_, err := index.PutISMPolicy(context.Background(), m, index.ISMPutOptions{
		Body: []byte(`{"policy":{"states":[]}}`),
	})
	if err == nil || !strings.Contains(err.Error(), "--id") {
		t.Errorf("expected --id error, got: %v", err)
	}
}

func TestPutISMPolicy_validatesBody(t *testing.T) {
	m := &fakeISM{}
	cases := []struct {
		name    string
		body    []byte
		wantErr string
	}{
		{"empty body", nil, "body is required"},
		{"non-JSON", []byte(`{ broken`), "valid JSON"},
		{"missing policy key", []byte(`{"description":"x"}`), "policy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := index.PutISMPolicy(context.Background(), m, index.ISMPutOptions{
				ID:   "logs-rollover",
				Body: c.body,
			})
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("err = %v, want substring %q", err, c.wantErr)
			}
			if len(m.puts) != 0 {
				t.Errorf("cluster contacted on validation failure: %+v", m.puts)
			}
		})
	}
}

func TestPutISMPolicy_rejectsHalfOptimisticConcurrency(t *testing.T) {
	m := &fakeISM{}
	_, err := index.PutISMPolicy(context.Background(), m, index.ISMPutOptions{
		ID:    "logs-rollover",
		Body:  []byte(`{"policy":{"states":[]}}`),
		SeqNo: 5,
		// PrimaryTerm omitted on purpose.
	})
	if err == nil || !strings.Contains(err.Error(), "primary_term") {
		t.Errorf("expected combined-flag error, got: %v", err)
	}
}

func TestPutISMPolicy_dryRun_doesNotCallCluster(t *testing.T) {
	m := &fakeISM{}
	got, err := index.PutISMPolicy(context.Background(), m, index.ISMPutOptions{
		ID:     "logs-rollover",
		Body:   []byte(`{"policy":{"states":[]}}`),
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("PutISMPolicy: %v", err)
	}
	if !got.DryRun || got.ID != "logs-rollover" {
		t.Errorf("expected synthetic dry-run, got %+v", got)
	}
	if len(m.puts) != 0 {
		t.Errorf("dry-run contacted cluster: %+v", m.puts)
	}
}

func TestPutISMPolicy_passesRequestThrough(t *testing.T) {
	m := &fakeISM{}
	body := []byte(`{"policy":{"states":[{"name":"hot"}]}}`)
	_, err := index.PutISMPolicy(context.Background(), m, index.ISMPutOptions{
		ID:          "logs-rollover",
		Body:        body,
		SeqNo:       7,
		PrimaryTerm: 2,
	})
	if err != nil {
		t.Fatalf("PutISMPolicy: %v", err)
	}
	if len(m.puts) != 1 {
		t.Fatalf("expected 1 call, got %d", len(m.puts))
	}
	got := m.puts[0]
	if got.ID != "logs-rollover" || got.SeqNo != 7 || got.PrimaryTerm != 2 || string(got.Body) != string(body) {
		t.Errorf("request not forwarded: %+v", got)
	}
}

func TestDeleteISMPolicy_validatesID(t *testing.T) {
	m := &fakeISM{}
	_, err := index.DeleteISMPolicy(context.Background(), m, index.ISMDeleteOptions{})
	if err == nil || !strings.Contains(err.Error(), "--id") {
		t.Errorf("expected --id error, got: %v", err)
	}
	_, err = index.DeleteISMPolicy(context.Background(), m, index.ISMDeleteOptions{ID: "logs-*"})
	if err == nil || !strings.Contains(err.Error(), "literal") {
		t.Errorf("expected literal-id error, got: %v", err)
	}
}

func TestDeleteISMPolicy_dryRun(t *testing.T) {
	m := &fakeISM{}
	got, err := index.DeleteISMPolicy(context.Background(), m, index.ISMDeleteOptions{
		ID:     "logs-rollover",
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("DeleteISMPolicy: %v", err)
	}
	if !got.DryRun || !got.OK || got.ID != "logs-rollover" {
		t.Errorf("expected synthetic dry-run, got %+v", got)
	}
	if len(m.deletes) != 0 {
		t.Errorf("dry-run contacted cluster: %+v", m.deletes)
	}
}

func TestDeleteISMPolicy_propagatesAdapterError(t *testing.T) {
	sentinel := errors.New("boom")
	m := &fakeISM{err: sentinel}
	_, err := index.DeleteISMPolicy(context.Background(), m, index.ISMDeleteOptions{ID: "logs-rollover"})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

func TestExplainISM_sortsByIndex(t *testing.T) {
	m := &fakeISM{
		explainOut: []types.ISMExplainEntry{
			{Index: "logs-2024-12", Managed: true, State: "hot"},
			{Index: "audit-2024-12", Managed: true, State: "warm"},
			{Index: "metrics-2024-12", Managed: false},
		},
	}
	out, err := index.ExplainISM(context.Background(), m, index.ISMExplainOptions{})
	if err != nil {
		t.Fatalf("ExplainISM: %v", err)
	}
	if len(out) != 3 || out[0].Index != "audit-2024-12" || out[1].Index != "logs-2024-12" || out[2].Index != "metrics-2024-12" {
		t.Errorf("sort order: %v", explainISMIndices(out))
	}
}

func TestExplainISM_forwardsKnobs(t *testing.T) {
	m := &fakeISM{}
	_, err := index.ExplainISM(context.Background(), m, index.ISMExplainOptions{
		Index: "logs-*", ShowPolicy: true,
	})
	if err != nil {
		t.Fatalf("ExplainISM: %v", err)
	}
	if len(m.explainReqs) != 1 {
		t.Fatalf("expected 1 call, got %d", len(m.explainReqs))
	}
	got := m.explainReqs[0]
	if got.Index != "logs-*" || !got.ShowPolicy {
		t.Errorf("knobs not forwarded: %+v", got)
	}
}

func policyIDs(ps []types.ISMPolicy) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

func explainISMIndices(es []types.ISMExplainEntry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Index)
	}
	return out
}
