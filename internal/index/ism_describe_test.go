package index_test

import (
	"context"
	"errors"
	"testing"

	"github.com/esops-dev/esops-go/internal/index"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

type ismDescribeFake struct {
	policy      types.ISMPolicy
	policyErr   error
	explainRows []types.ISMExplainEntry
	explainErr  error
}

func (f *ismDescribeFake) ListPolicies(_ context.Context, _ []string) ([]types.ISMPolicy, error) {
	return []types.ISMPolicy{f.policy}, nil
}
func (f *ismDescribeFake) GetPolicy(_ context.Context, _ string) (types.ISMPolicy, error) {
	if f.policyErr != nil {
		return types.ISMPolicy{}, f.policyErr
	}
	return f.policy, nil
}
func (f *ismDescribeFake) PutPolicy(_ context.Context, _ types.ISMPolicyPutRequest) (types.ISMPolicyPutResult, error) {
	return types.ISMPolicyPutResult{}, client.ErrUnsupported
}
func (f *ismDescribeFake) DeletePolicy(_ context.Context, _ types.ISMPolicyDeleteRequest) (types.ISMPolicyDeleteResult, error) {
	return types.ISMPolicyDeleteResult{}, client.ErrUnsupported
}
func (f *ismDescribeFake) ExplainPolicy(_ context.Context, _ types.ISMExplainRequest) ([]types.ISMExplainEntry, error) {
	return f.explainRows, f.explainErr
}
func (f *ismDescribeFake) ExplainPolicyStream(_ context.Context, _ types.ISMExplainRequest, _ func(types.ISMExplainEntry) error) error {
	return nil
}

func TestDescribeISM_rejectsEmpty(t *testing.T) {
	f := &ismDescribeFake{}
	_, err := index.DescribeISM(context.Background(), index.ISMDescribeCapabilities{ISM: f, Dialect: "opensearch"}, index.ISMDescribeOptions{ID: " "})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("want ErrUsage, got %v", err)
	}
}

func TestDescribeISM_aggregatesAndFiltersExplain(t *testing.T) {
	f := &ismDescribeFake{
		policy: types.ISMPolicy{ID: "logs-policy"},
		explainRows: []types.ISMExplainEntry{
			{Index: "logs-2025-05", PolicyID: "logs-policy", State: "warm"},
			{Index: "logs-2025-04", PolicyID: "logs-policy", State: "warm", Step: "ERROR"},
			{Index: "users", PolicyID: "users-policy", State: "hot"},
		},
	}
	desc, err := index.DescribeISM(context.Background(), index.ISMDescribeCapabilities{ISM: f, Dialect: "opensearch"}, index.ISMDescribeOptions{ID: "logs-policy"})
	if err != nil {
		t.Fatalf("DescribeISM: %v", err)
	}
	if desc.ManagedCount != 2 {
		t.Errorf("ManagedCount = %d, want 2 (logs entries only)", desc.ManagedCount)
	}
	if desc.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", desc.ErrorCount)
	}
}

func TestDescribeISM_stepInfoErrorCounts(t *testing.T) {
	f := &ismDescribeFake{
		policy: types.ISMPolicy{ID: "x"},
		explainRows: []types.ISMExplainEntry{
			// ISM frequently surfaces failures via step_info.error rather
			// than Step=ERROR — the aggregator counts both.
			{Index: "logs", PolicyID: "x", State: "warm", StepInfo: map[string]any{"error": "rollover failed"}},
		},
	}
	desc, err := index.DescribeISM(context.Background(), index.ISMDescribeCapabilities{ISM: f, Dialect: "opensearch"}, index.ISMDescribeOptions{ID: "x"})
	if err != nil {
		t.Fatalf("DescribeISM: %v", err)
	}
	if desc.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1 (step_info.error counted)", desc.ErrorCount)
	}
}

func TestDescribeISM_explainErrorSurfacesAsWarning(t *testing.T) {
	f := &ismDescribeFake{
		policy:     types.ISMPolicy{ID: "x"},
		explainErr: errors.New("boom"),
	}
	desc, err := index.DescribeISM(context.Background(), index.ISMDescribeCapabilities{ISM: f, Dialect: "opensearch"}, index.ISMDescribeOptions{ID: "x"})
	if err != nil {
		t.Fatalf("DescribeISM: %v", err)
	}
	if desc.SectionWarnings["managed_indices"] != "boom" {
		t.Errorf("SectionWarnings[managed_indices] = %q", desc.SectionWarnings["managed_indices"])
	}
}

// ErrUnsupported from GetPolicy (ES adapter answering ISM calls) must
// propagate unchanged so the operator sees the dialect mismatch with
// the right exit code.
func TestDescribeISM_propagatesErrUnsupported(t *testing.T) {
	f := &ismDescribeFake{policyErr: client.ErrUnsupported}
	_, err := index.DescribeISM(context.Background(), index.ISMDescribeCapabilities{ISM: f, Dialect: "elasticsearch"}, index.ISMDescribeOptions{ID: "x"})
	if !errors.Is(err, client.ErrUnsupported) {
		t.Errorf("want ErrUnsupported, got %v", err)
	}
}
