package snapshot_test

import (
	"context"
	"errors"
	"testing"

	"github.com/esops-dev/esops-go/internal/snapshot"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

type descPolicyInspector struct {
	policy  types.SnapshotPolicy
	err     error
	listErr error
}

func (f *descPolicyInspector) ListSnapshotPolicies(_ context.Context, _ []string) ([]types.SnapshotPolicy, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return []types.SnapshotPolicy{f.policy}, nil
}
func (f *descPolicyInspector) GetSnapshotPolicy(_ context.Context, _ string) (types.SnapshotPolicy, error) {
	return f.policy, f.err
}

func TestDescribePolicy_rejectsEmpty(t *testing.T) {
	_, err := snapshot.DescribePolicy(context.Background(), snapshot.PolicyDescribeCapabilities{
		Policies: &descPolicyInspector{},
	}, snapshot.PolicyDescribeOptions{Name: ""})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("want ErrUsage, got %v", err)
	}
}

func TestDescribePolicy_rejectsNegativeLimit(t *testing.T) {
	_, err := snapshot.DescribePolicy(context.Background(), snapshot.PolicyDescribeCapabilities{
		Policies: &descPolicyInspector{},
	}, snapshot.PolicyDescribeOptions{Name: "p", RecentLimit: -1})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("want ErrUsage on negative limit, got %v", err)
	}
}

func TestDescribePolicy_aggregatesRecentSnapshots(t *testing.T) {
	caps := snapshot.PolicyDescribeCapabilities{
		Policies: &descPolicyInspector{
			policy: types.SnapshotPolicy{Name: "daily", Repository: "backup", Schedule: "0 0 * * *"},
		},
		Snapshots: &descSnapshotInspector{
			rows: []types.Snapshot{
				{Repository: "backup", Name: "daily-1", State: "SUCCESS", StartTimeMillis: 3000},
				{Repository: "backup", Name: "daily-2", State: "SUCCESS", StartTimeMillis: 2000},
				{Repository: "backup", Name: "daily-3", State: "SUCCESS", StartTimeMillis: 1000},
			},
		},
		Dialect: "elasticsearch",
	}
	desc, err := snapshot.DescribePolicy(context.Background(), caps, snapshot.PolicyDescribeOptions{Name: "daily", RecentLimit: 2})
	if err != nil {
		t.Fatalf("DescribePolicy: %v", err)
	}
	if desc.RecentLimit != 2 {
		t.Errorf("RecentLimit = %d, want 2", desc.RecentLimit)
	}
	if len(desc.RecentSnapshots) != 2 {
		t.Fatalf("want 2 recent, got %d", len(desc.RecentSnapshots))
	}
	if desc.RecentSnapshots[0].Name != "daily-1" {
		t.Errorf("recent[0].Name = %q, want daily-1 (most-recent-first)", desc.RecentSnapshots[0].Name)
	}
}

func TestDescribePolicy_recentSnapshotErrorIsWarning(t *testing.T) {
	caps := snapshot.PolicyDescribeCapabilities{
		Policies: &descPolicyInspector{
			policy: types.SnapshotPolicy{Name: "daily", Repository: "backup"},
		},
		Snapshots: &descSnapshotInspector{listErr: errors.New("boom")},
	}
	desc, err := snapshot.DescribePolicy(context.Background(), caps, snapshot.PolicyDescribeOptions{Name: "daily"})
	if err != nil {
		t.Fatalf("DescribePolicy: %v", err)
	}
	if desc.SectionWarnings["recent_snapshots"] != "boom" {
		t.Errorf("SectionWarnings[recent_snapshots] = %q", desc.SectionWarnings["recent_snapshots"])
	}
}

func TestDescribePolicy_policyWithoutRepositorySkipsRecent(t *testing.T) {
	caps := snapshot.PolicyDescribeCapabilities{
		Policies:  &descPolicyInspector{policy: types.SnapshotPolicy{Name: "broken"}},
		Snapshots: &descSnapshotInspector{},
	}
	desc, err := snapshot.DescribePolicy(context.Background(), caps, snapshot.PolicyDescribeOptions{Name: "broken"})
	if err != nil {
		t.Fatalf("DescribePolicy: %v", err)
	}
	if len(desc.RecentSnapshots) != 0 {
		t.Errorf("policy without a repository should have no recent snapshots, got %d", len(desc.RecentSnapshots))
	}
}

// ErrUnsupported from GetPolicy (SLM on an OS context, or SM on an
// ES context where the older OS-only call would be wrong) must
// propagate unchanged so the operator sees the dialect mismatch.
func TestDescribePolicy_propagatesErrUnsupported(t *testing.T) {
	caps := snapshot.PolicyDescribeCapabilities{
		Policies: &descPolicyInspector{err: client.ErrUnsupported},
	}
	_, err := snapshot.DescribePolicy(context.Background(), caps, snapshot.PolicyDescribeOptions{Name: "daily"})
	if !errors.Is(err, client.ErrUnsupported) {
		t.Errorf("want ErrUnsupported, got %v", err)
	}
}
