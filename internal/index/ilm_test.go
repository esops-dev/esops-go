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

// fakeILM is a hand-rolled stand-in for client.ILMManager. It records
// every call so tests can assert "the business layer asked the cluster
// the right question" without spinning a real cluster.
type fakeILM struct {
	gotNames    []string
	policies    []types.ILMPolicy
	puts        []types.ILMPolicyPutRequest
	deletes     []types.ILMPolicyDeleteRequest
	explainReqs []types.ILMExplainRequest
	explainOut  []types.ILMExplainEntry
	err         error
}

func (f *fakeILM) ListPolicies(_ context.Context, names []string) ([]types.ILMPolicy, error) {
	f.gotNames = append([]string(nil), names...)
	return f.policies, f.err
}

func (f *fakeILM) GetPolicy(_ context.Context, name string) (types.ILMPolicy, error) {
	f.gotNames = append([]string(nil), name)
	if f.err != nil {
		return types.ILMPolicy{}, f.err
	}
	for _, p := range f.policies {
		if p.Name == name {
			return p, nil
		}
	}
	return types.ILMPolicy{}, fmt.Errorf("%w: ilm policy %q", client.ErrNotFound, name)
}

func (f *fakeILM) PutPolicy(_ context.Context, req types.ILMPolicyPutRequest) (types.ILMPolicyPutResult, error) {
	f.puts = append(f.puts, req)
	if f.err != nil {
		return types.ILMPolicyPutResult{}, f.err
	}
	return types.ILMPolicyPutResult{Action: "ilm-policy-put", Name: req.Name, Acknowledged: true}, nil
}

func (f *fakeILM) DeletePolicy(_ context.Context, req types.ILMPolicyDeleteRequest) (types.ILMPolicyDeleteResult, error) {
	f.deletes = append(f.deletes, req)
	if f.err != nil {
		return types.ILMPolicyDeleteResult{}, f.err
	}
	return types.ILMPolicyDeleteResult{Action: "ilm-policy-delete", Name: req.Name, Acknowledged: true}, nil
}

func (f *fakeILM) ExplainPolicy(_ context.Context, req types.ILMExplainRequest) ([]types.ILMExplainEntry, error) {
	f.explainReqs = append(f.explainReqs, req)
	return f.explainOut, f.err
}

func (f *fakeILM) ExplainPolicyStream(_ context.Context, req types.ILMExplainRequest, yield func(types.ILMExplainEntry) error) error {
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

func sampleILMPolicies() []types.ILMPolicy {
	return []types.ILMPolicy{
		// Out-of-order on purpose — ListILMPolicies sorts client-side.
		{Name: "logs-30d"},
		{Name: "audit-90d"},
		{Name: "metrics-7d"},
	}
}

func TestListILMPolicies_sortsByName(t *testing.T) {
	m := &fakeILM{policies: sampleILMPolicies()}
	out, err := index.ListILMPolicies(context.Background(), m, index.ILMListOptions{})
	if err != nil {
		t.Fatalf("ListILMPolicies: %v", err)
	}
	if len(out) != 3 || out[0].Name != "audit-90d" || out[1].Name != "logs-30d" || out[2].Name != "metrics-7d" {
		t.Errorf("sort order: %v", policyNames(out))
	}
}

func TestListILMPolicies_passesNamesThrough(t *testing.T) {
	m := &fakeILM{policies: nil}
	_, err := index.ListILMPolicies(context.Background(), m, index.ILMListOptions{
		Names: []string{"logs-30d", "audit-90d"},
	})
	if err != nil {
		t.Fatalf("ListILMPolicies: %v", err)
	}
	if len(m.gotNames) != 2 || m.gotNames[0] != "logs-30d" || m.gotNames[1] != "audit-90d" {
		t.Errorf("names forwarded = %v", m.gotNames)
	}
}

func TestListILMPolicies_propagatesError(t *testing.T) {
	sentinel := errors.New("boom")
	m := &fakeILM{err: sentinel}
	_, err := index.ListILMPolicies(context.Background(), m, index.ILMListOptions{})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

func TestGetILMPolicy_requiresName(t *testing.T) {
	m := &fakeILM{policies: sampleILMPolicies()}
	_, err := index.GetILMPolicy(context.Background(), m, "")
	if err == nil || !strings.Contains(err.Error(), "--name") {
		t.Errorf("expected --name error, got: %v", err)
	}
}

func TestGetILMPolicy_rejectsPattern(t *testing.T) {
	m := &fakeILM{policies: sampleILMPolicies()}
	_, err := index.GetILMPolicy(context.Background(), m, "logs-*")
	if err == nil || !strings.Contains(err.Error(), "literal") {
		t.Errorf("expected literal-name error, got: %v", err)
	}
}

func TestGetILMPolicy_returnsMatchingEntry(t *testing.T) {
	m := &fakeILM{policies: sampleILMPolicies()}
	got, err := index.GetILMPolicy(context.Background(), m, "audit-90d")
	if err != nil {
		t.Fatalf("GetILMPolicy: %v", err)
	}
	if got.Name != "audit-90d" {
		t.Errorf("got %q, want audit-90d", got.Name)
	}
}

func TestGetILMPolicy_notFound(t *testing.T) {
	m := &fakeILM{policies: sampleILMPolicies()}
	_, err := index.GetILMPolicy(context.Background(), m, "missing")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got: %v", err)
	}
}

func TestPutILMPolicy_validatesName(t *testing.T) {
	m := &fakeILM{}
	_, err := index.PutILMPolicy(context.Background(), m, index.ILMPutOptions{
		Body: []byte(`{"policy":{"phases":{}}}`),
	})
	if err == nil || !strings.Contains(err.Error(), "--name") {
		t.Errorf("expected --name error, got: %v", err)
	}
}

func TestPutILMPolicy_rejectsPatternName(t *testing.T) {
	m := &fakeILM{}
	_, err := index.PutILMPolicy(context.Background(), m, index.ILMPutOptions{
		Name: "logs-*",
		Body: []byte(`{"policy":{"phases":{}}}`),
	})
	if err == nil || !strings.Contains(err.Error(), "literal") {
		t.Errorf("expected literal-name error, got: %v", err)
	}
}

func TestPutILMPolicy_validatesBody(t *testing.T) {
	m := &fakeILM{}
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
			_, err := index.PutILMPolicy(context.Background(), m, index.ILMPutOptions{
				Name: "logs-30d",
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

func TestPutILMPolicy_dryRun_doesNotCallCluster(t *testing.T) {
	m := &fakeILM{}
	got, err := index.PutILMPolicy(context.Background(), m, index.ILMPutOptions{
		Name:   "logs-30d",
		Body:   []byte(`{"policy":{"phases":{}}}`),
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("PutILMPolicy: %v", err)
	}
	if !got.DryRun || !got.Acknowledged || got.Name != "logs-30d" {
		t.Errorf("expected synthetic dry-run ack, got %+v", got)
	}
	if len(m.puts) != 0 {
		t.Errorf("dry-run contacted cluster: %+v", m.puts)
	}
}

func TestPutILMPolicy_passesRequestThrough(t *testing.T) {
	m := &fakeILM{}
	body := []byte(`{"policy":{"phases":{"hot":{}}}}`)
	_, err := index.PutILMPolicy(context.Background(), m, index.ILMPutOptions{
		Name: "logs-30d",
		Body: body,
	})
	if err != nil {
		t.Fatalf("PutILMPolicy: %v", err)
	}
	if len(m.puts) != 1 || m.puts[0].Name != "logs-30d" || string(m.puts[0].Body) != string(body) {
		t.Errorf("request not forwarded: %+v", m.puts)
	}
}

func TestDeleteILMPolicy_validatesName(t *testing.T) {
	m := &fakeILM{}
	_, err := index.DeleteILMPolicy(context.Background(), m, index.ILMDeleteOptions{})
	if err == nil || !strings.Contains(err.Error(), "--name") {
		t.Errorf("expected --name error, got: %v", err)
	}
	_, err = index.DeleteILMPolicy(context.Background(), m, index.ILMDeleteOptions{Name: "logs-*"})
	if err == nil || !strings.Contains(err.Error(), "literal") {
		t.Errorf("expected literal-name error, got: %v", err)
	}
}

func TestDeleteILMPolicy_dryRun_doesNotCallCluster(t *testing.T) {
	m := &fakeILM{}
	got, err := index.DeleteILMPolicy(context.Background(), m, index.ILMDeleteOptions{
		Name:   "logs-30d",
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("DeleteILMPolicy: %v", err)
	}
	if !got.DryRun || !got.Acknowledged {
		t.Errorf("expected synthetic dry-run ack, got %+v", got)
	}
	if len(m.deletes) != 0 {
		t.Errorf("dry-run contacted cluster: %+v", m.deletes)
	}
}

func TestDeleteILMPolicy_propagatesAdapterError(t *testing.T) {
	sentinel := errors.New("boom")
	m := &fakeILM{err: sentinel}
	_, err := index.DeleteILMPolicy(context.Background(), m, index.ILMDeleteOptions{Name: "logs-30d"})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

func TestExplainILM_sortsByIndex(t *testing.T) {
	m := &fakeILM{
		explainOut: []types.ILMExplainEntry{
			{Index: "logs-2024-12", Managed: true, Phase: "hot"},
			{Index: "audit-2024-12", Managed: true, Phase: "warm"},
			{Index: "metrics-2024-12", Managed: false},
		},
	}
	out, err := index.ExplainILM(context.Background(), m, index.ILMExplainOptions{Index: "*"})
	if err != nil {
		t.Fatalf("ExplainILM: %v", err)
	}
	if len(out) != 3 || out[0].Index != "audit-2024-12" || out[1].Index != "logs-2024-12" || out[2].Index != "metrics-2024-12" {
		t.Errorf("sort order: %v", explainIndices(out))
	}
}

func TestExplainILM_forwardsKnobs(t *testing.T) {
	m := &fakeILM{}
	_, err := index.ExplainILM(context.Background(), m, index.ILMExplainOptions{
		Index: "logs-*", OnlyErrors: true, OnlyManaged: true,
	})
	if err != nil {
		t.Fatalf("ExplainILM: %v", err)
	}
	if len(m.explainReqs) != 1 {
		t.Fatalf("expected 1 call, got %d", len(m.explainReqs))
	}
	got := m.explainReqs[0]
	if got.Index != "logs-*" || !got.OnlyErrors || !got.OnlyManaged {
		t.Errorf("knobs not forwarded: %+v", got)
	}
}

func policyNames(ps []types.ILMPolicy) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

func explainIndices(es []types.ILMExplainEntry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Index)
	}
	return out
}
