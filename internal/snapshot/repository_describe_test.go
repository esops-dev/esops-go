package snapshot_test

import (
	"context"
	"errors"
	"testing"

	"github.com/esops-dev/esops-go/internal/snapshot"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

type descVerifier struct {
	result types.SnapshotVerifyResult
	err    error
}

func (f *descVerifier) VerifyRepository(_ context.Context, _ types.SnapshotVerifyRequest) (types.SnapshotVerifyResult, error) {
	return f.result, f.err
}
func (f *descVerifier) AnalyzeRepository(_ context.Context, _ types.SnapshotAnalyzeRequest) (types.SnapshotAnalyzeResult, error) {
	return types.SnapshotAnalyzeResult{}, client.ErrUnsupported
}

type descPolicyList struct {
	policies []types.SnapshotPolicy
	err      error
}

func (f *descPolicyList) ListSnapshotPolicies(_ context.Context, _ []string) ([]types.SnapshotPolicy, error) {
	return f.policies, f.err
}
func (f *descPolicyList) GetSnapshotPolicy(_ context.Context, _ string) (types.SnapshotPolicy, error) {
	return types.SnapshotPolicy{}, client.ErrUnsupported
}

func TestDescribeRepository_rejectsEmpty(t *testing.T) {
	_, err := snapshot.DescribeRepository(context.Background(), snapshot.RepositoryDescribeCapabilities{
		Snapshots: &descSnapshotInspector{},
	}, snapshot.RepositoryDescribeOptions{Name: ""})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("want ErrUsage, got %v", err)
	}
}

func TestDescribeRepository_rejectsPattern(t *testing.T) {
	_, err := snapshot.DescribeRepository(context.Background(), snapshot.RepositoryDescribeCapabilities{
		Snapshots: &descSnapshotInspector{},
	}, snapshot.RepositoryDescribeOptions{Name: "backup-*"})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("want ErrUsage on pattern, got %v", err)
	}
}

func TestDescribeRepository_notFound(t *testing.T) {
	_, err := snapshot.DescribeRepository(context.Background(), snapshot.RepositoryDescribeCapabilities{
		Snapshots: &descSnapshotInspector{repos: nil},
	}, snapshot.RepositoryDescribeOptions{Name: "backup"})
	if err == nil || !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestDescribeRepository_aggregatesSummaryAndPolicies(t *testing.T) {
	caps := snapshot.RepositoryDescribeCapabilities{
		Snapshots: &descSnapshotInspector{
			repos: []types.SnapshotRepository{
				{Name: "backup", Type: "s3", Settings: map[string]any{"bucket": "my-bucket"}},
			},
			rows: []types.Snapshot{
				{Repository: "backup", Name: "ok-1", State: "SUCCESS"},
				{Repository: "backup", Name: "ok-2", State: "SUCCESS"},
				{Repository: "backup", Name: "broken", State: "PARTIAL"},
				{Repository: "backup", Name: "running", State: "IN_PROGRESS"},
			},
		},
		Policies: &descPolicyList{
			policies: []types.SnapshotPolicy{
				{Name: "daily", Repository: "backup", Schedule: "0 0 * * *", Enabled: true},
				{Name: "off-target", Repository: "other-repo", Schedule: "0 0 * * *", Enabled: true},
			},
		},
		Dialect: "elasticsearch",
	}
	desc, err := snapshot.DescribeRepository(context.Background(), caps, snapshot.RepositoryDescribeOptions{Name: "backup"})
	if err != nil {
		t.Fatalf("DescribeRepository: %v", err)
	}
	if desc.Summary.SnapshotCount != 4 || desc.Summary.SuccessCount != 2 || desc.Summary.FailedCount != 1 || desc.Summary.InProgressCount != 1 {
		t.Errorf("summary wrong: %+v", desc.Summary)
	}
	if len(desc.Policies) != 1 || desc.Policies[0].Name != "daily" {
		t.Errorf("policies wrong: %+v", desc.Policies)
	}
	if desc.Verify != nil {
		t.Errorf("verify should be nil when --verify is off, got %+v", desc.Verify)
	}
}

func TestDescribeRepository_verifyWhenRequested(t *testing.T) {
	caps := snapshot.RepositoryDescribeCapabilities{
		Snapshots: &descSnapshotInspector{
			repos: []types.SnapshotRepository{{Name: "backup", Type: "s3"}},
		},
		Verifier: &descVerifier{result: types.SnapshotVerifyResult{VerifiedNodes: []string{"node-1", "node-2"}}},
	}
	desc, err := snapshot.DescribeRepository(context.Background(), caps, snapshot.RepositoryDescribeOptions{Name: "backup", Verify: true})
	if err != nil {
		t.Fatalf("DescribeRepository: %v", err)
	}
	if desc.Verify == nil || len(desc.Verify.VerifiedNodes) != 2 {
		t.Errorf("verify result missing or wrong: %+v", desc.Verify)
	}
}

// Bucket math sanity: SuccessCount + FailedCount + InProgressCount +
// OtherCount must equal SnapshotCount. ABORTED and MISSING (rare but
// real) land in OtherCount so the header arithmetic stays honest.
func TestDescribeRepository_summaryBucketMath(t *testing.T) {
	caps := snapshot.RepositoryDescribeCapabilities{
		Snapshots: &descSnapshotInspector{
			repos: []types.SnapshotRepository{{Name: "backup", Type: "s3"}},
			rows: []types.Snapshot{
				{Repository: "backup", Name: "ok-1", State: "SUCCESS"},
				{Repository: "backup", Name: "broken", State: "PARTIAL"},
				{Repository: "backup", Name: "cancelled", State: "ABORTED"},
				{Repository: "backup", Name: "gone", State: "MISSING"},
				{Repository: "backup", Name: "running", State: "IN_PROGRESS"},
			},
		},
	}
	desc, err := snapshot.DescribeRepository(context.Background(), caps, snapshot.RepositoryDescribeOptions{Name: "backup"})
	if err != nil {
		t.Fatalf("DescribeRepository: %v", err)
	}
	s := desc.Summary
	sum := s.SuccessCount + s.FailedCount + s.InProgressCount + s.OtherCount
	if sum != s.SnapshotCount {
		t.Errorf("bucket math: success(%d)+failed(%d)+in_progress(%d)+other(%d) = %d, want SnapshotCount %d",
			s.SuccessCount, s.FailedCount, s.InProgressCount, s.OtherCount, sum, s.SnapshotCount)
	}
	if s.OtherCount != 2 {
		t.Errorf("OtherCount = %d, want 2 (ABORTED + MISSING)", s.OtherCount)
	}
}

// Credential-like setting keys must be redacted by default; the raw
// values only surface when --show-secrets is set on the CLI side
// (mirrored here as ShowSecrets: true).
func TestDescribeRepository_redactsSecretsByDefault(t *testing.T) {
	caps := snapshot.RepositoryDescribeCapabilities{
		Snapshots: &descSnapshotInspector{
			repos: []types.SnapshotRepository{{
				Name: "backup",
				Type: "s3",
				Settings: map[string]any{
					"bucket":     "my-bucket",
					"access_key": "AKIA-DEADBEEF",
					"secret_key": "topsecret",
				},
			}},
		},
	}
	desc, err := snapshot.DescribeRepository(context.Background(), caps, snapshot.RepositoryDescribeOptions{Name: "backup"})
	if err != nil {
		t.Fatalf("DescribeRepository: %v", err)
	}
	if got := desc.Info.Settings["access_key"]; got != snapshot.RedactedPlaceholder {
		t.Errorf("access_key = %v, want %q", got, snapshot.RedactedPlaceholder)
	}
	if got := desc.Info.Settings["secret_key"]; got != snapshot.RedactedPlaceholder {
		t.Errorf("secret_key = %v, want %q", got, snapshot.RedactedPlaceholder)
	}
	if got := desc.Info.Settings["bucket"]; got != "my-bucket" {
		t.Errorf("bucket leaked: got %v", got)
	}
}

func TestDescribeRepository_showSecretsLeavesUntouched(t *testing.T) {
	caps := snapshot.RepositoryDescribeCapabilities{
		Snapshots: &descSnapshotInspector{
			repos: []types.SnapshotRepository{{
				Name: "backup",
				Type: "s3",
				Settings: map[string]any{
					"access_key": "AKIA-DEADBEEF",
				},
			}},
		},
	}
	desc, err := snapshot.DescribeRepository(context.Background(), caps, snapshot.RepositoryDescribeOptions{Name: "backup", ShowSecrets: true})
	if err != nil {
		t.Fatalf("DescribeRepository: %v", err)
	}
	if got := desc.Info.Settings["access_key"]; got != "AKIA-DEADBEEF" {
		t.Errorf("--show-secrets should leave value untouched; got %v", got)
	}
}
