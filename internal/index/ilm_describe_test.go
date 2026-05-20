package index_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/index"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

type ilmDescribeFake struct {
	policy        types.ILMPolicy
	policyErr     error
	explainRows   []types.ILMExplainEntry
	explainErr    error
	gotExplainReq types.ILMExplainRequest
}

func (f *ilmDescribeFake) ListPolicies(_ context.Context, _ []string) ([]types.ILMPolicy, error) {
	return []types.ILMPolicy{f.policy}, nil
}
func (f *ilmDescribeFake) GetPolicy(_ context.Context, _ string) (types.ILMPolicy, error) {
	if f.policyErr != nil {
		return types.ILMPolicy{}, f.policyErr
	}
	return f.policy, nil
}
func (f *ilmDescribeFake) PutPolicy(_ context.Context, _ types.ILMPolicyPutRequest) (types.ILMPolicyPutResult, error) {
	return types.ILMPolicyPutResult{}, client.ErrUnsupported
}
func (f *ilmDescribeFake) DeletePolicy(_ context.Context, _ types.ILMPolicyDeleteRequest) (types.ILMPolicyDeleteResult, error) {
	return types.ILMPolicyDeleteResult{}, client.ErrUnsupported
}
func (f *ilmDescribeFake) ExplainPolicy(_ context.Context, req types.ILMExplainRequest) ([]types.ILMExplainEntry, error) {
	f.gotExplainReq = req
	return f.explainRows, f.explainErr
}
func (f *ilmDescribeFake) ExplainPolicyStream(_ context.Context, _ types.ILMExplainRequest, _ func(types.ILMExplainEntry) error) error {
	return nil
}

func TestDescribeILM_rejectsEmpty(t *testing.T) {
	f := &ilmDescribeFake{}
	_, err := index.DescribeILM(context.Background(), index.ILMDescribeCapabilities{ILM: f, Dialect: "elasticsearch"}, index.ILMDescribeOptions{Name: " "})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("want ErrUsage, got %v", err)
	}
}

func TestDescribeILM_rejectsPattern(t *testing.T) {
	f := &ilmDescribeFake{}
	_, err := index.DescribeILM(context.Background(), index.ILMDescribeCapabilities{ILM: f, Dialect: "elasticsearch"}, index.ILMDescribeOptions{Name: "logs-*"})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("want ErrUsage, got %v", err)
	}
}

func TestDescribeILM_skipsExplainWhenNoInUseBy(t *testing.T) {
	f := &ilmDescribeFake{
		policy:      types.ILMPolicy{Name: "logs-30d"},
		explainRows: []types.ILMExplainEntry{{Index: "should-not-appear"}},
	}
	desc, err := index.DescribeILM(context.Background(), index.ILMDescribeCapabilities{ILM: f, Dialect: "elasticsearch"}, index.ILMDescribeOptions{Name: "logs-30d"})
	if err != nil {
		t.Fatalf("DescribeILM: %v", err)
	}
	if desc.ManagedCount != 0 || len(desc.ManagedIndices) != 0 {
		t.Errorf("expected empty managed when InUseBy is empty, got count=%d rows=%v", desc.ManagedCount, desc.ManagedIndices)
	}
	if f.gotExplainReq.Index != "" {
		t.Errorf("explain should not be called; got Index=%q", f.gotExplainReq.Index)
	}
}

func TestDescribeILM_aggregatesManagedAndErrors(t *testing.T) {
	f := &ilmDescribeFake{
		policy: types.ILMPolicy{
			Name:    "logs-30d",
			InUseBy: types.ILMInUseBy{Indices: []string{"logs-2025-05", "logs-2025-04"}},
		},
		explainRows: []types.ILMExplainEntry{
			{Index: "logs-2025-05", Managed: true, Policy: "logs-30d", Phase: "warm"},
			{Index: "logs-2025-04", Managed: true, Policy: "logs-30d", Phase: "warm", Step: "ERROR", FailedStep: "forcemerge"},
		},
	}
	desc, err := index.DescribeILM(context.Background(), index.ILMDescribeCapabilities{ILM: f, Dialect: "elasticsearch"}, index.ILMDescribeOptions{Name: "logs-30d"})
	if err != nil {
		t.Fatalf("DescribeILM: %v", err)
	}
	if desc.ManagedCount != 2 {
		t.Errorf("ManagedCount = %d, want 2", desc.ManagedCount)
	}
	if desc.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", desc.ErrorCount)
	}
	if got := f.gotExplainReq.Index; !strings.Contains(got, "logs-2025-04") || !strings.Contains(got, "logs-2025-05") {
		t.Errorf("explain request did not include both InUseBy indices: %q", got)
	}
}

func TestDescribeILM_explainErrorSurfacesAsWarning(t *testing.T) {
	f := &ilmDescribeFake{
		policy:     types.ILMPolicy{Name: "x", InUseBy: types.ILMInUseBy{Indices: []string{"a"}}},
		explainErr: errors.New("boom"),
	}
	desc, err := index.DescribeILM(context.Background(), index.ILMDescribeCapabilities{ILM: f, Dialect: "elasticsearch"}, index.ILMDescribeOptions{Name: "x"})
	if err != nil {
		t.Fatalf("DescribeILM: %v", err)
	}
	if desc.SectionWarnings["managed_indices"] != "boom" {
		t.Errorf("SectionWarnings[managed_indices] = %q", desc.SectionWarnings["managed_indices"])
	}
}

// When in_use_by.indices is empty but data_streams is populated, the
// aggregator must still run /_ilm/explain (cluster-wide) so the
// data-stream backing indices surface — otherwise a policy attached
// only via a data stream would silently report 0 managed indices.
func TestDescribeILM_dataStreamOnlyAttachmentTriggersClusterWideExplain(t *testing.T) {
	f := &ilmDescribeFake{
		policy: types.ILMPolicy{
			Name:    "logs-30d",
			InUseBy: types.ILMInUseBy{DataStreams: []string{"logs"}},
		},
		explainRows: []types.ILMExplainEntry{
			{Index: ".ds-logs-2025.05.13-000001", Managed: true, Policy: "logs-30d", Phase: "hot"},
			{Index: ".ds-logs-2025.05.19-000002", Managed: true, Policy: "logs-30d", Phase: "hot"},
			// Different policy — must be filtered out client-side.
			{Index: "users", Managed: true, Policy: "users-policy"},
		},
	}
	desc, err := index.DescribeILM(context.Background(), index.ILMDescribeCapabilities{ILM: f, Dialect: "elasticsearch"}, index.ILMDescribeOptions{Name: "logs-30d"})
	if err != nil {
		t.Fatalf("DescribeILM: %v", err)
	}
	if f.gotExplainReq.Index != "" {
		t.Errorf("data-stream-only attachment should trigger cluster-wide explain (empty Index); got %q", f.gotExplainReq.Index)
	}
	if !f.gotExplainReq.OnlyManaged {
		t.Errorf("cluster-wide explain should set OnlyManaged=true")
	}
	if desc.ManagedCount != 2 {
		t.Errorf("ManagedCount = %d, want 2 (logs-* only)", desc.ManagedCount)
	}
}

// ErrUnsupported from GetPolicy (ES context running against the OS
// adapter, or vice versa) must propagate unchanged so the operator
// sees the dialect mismatch with the right exit code.
func TestDescribeILM_propagatesErrUnsupported(t *testing.T) {
	f := &ilmDescribeFake{policyErr: client.ErrUnsupported}
	_, err := index.DescribeILM(context.Background(), index.ILMDescribeCapabilities{ILM: f, Dialect: "opensearch"}, index.ILMDescribeOptions{Name: "x"})
	if !errors.Is(err, client.ErrUnsupported) {
		t.Errorf("want ErrUnsupported, got %v", err)
	}
}
