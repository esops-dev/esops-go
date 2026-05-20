package snapshot_test

import (
	"context"
	"errors"
	"testing"

	"github.com/esops-dev/esops-go/internal/snapshot"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

type descSnapshotInspector struct {
	rows   []types.Snapshot
	repos  []types.SnapshotRepository
	status types.SnapshotStatus
	listErr     error
	statusErr   error
	repoErr     error
}

func (f *descSnapshotInspector) Repositories(_ context.Context, _ types.SnapshotRepositoryFilter) ([]types.SnapshotRepository, error) {
	return f.repos, f.repoErr
}
func (f *descSnapshotInspector) Snapshots(_ context.Context, _ types.SnapshotFilter) ([]types.Snapshot, error) {
	return f.rows, f.listErr
}
func (f *descSnapshotInspector) SnapshotStatus(_ context.Context, _ types.SnapshotStatusRequest) (types.SnapshotStatus, error) {
	return f.status, f.statusErr
}
func (f *descSnapshotInspector) SnapshotsStream(_ context.Context, _ types.SnapshotFilter, _ func(types.Snapshot) error, _ func(string, error)) error {
	return nil
}

func TestSnapshotDescribe_rejectsEmptySpec(t *testing.T) {
	_, err := snapshot.Describe(context.Background(), snapshot.DescribeCapabilities{
		Snapshot: &descSnapshotInspector{},
	}, snapshot.DescribeOptions{Spec: ""})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("want ErrUsage, got %v", err)
	}
}

func TestSnapshotDescribe_rejectsMissingSeparator(t *testing.T) {
	_, err := snapshot.Describe(context.Background(), snapshot.DescribeCapabilities{
		Snapshot: &descSnapshotInspector{},
	}, snapshot.DescribeOptions{Spec: "myrepo-and-name"})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("want ErrUsage on missing /, got %v", err)
	}
}

func TestSnapshotDescribe_rejectsPatternOnEitherSide(t *testing.T) {
	for _, spec := range []string{"repo-*/snap", "repo/snap-*"} {
		_, err := snapshot.Describe(context.Background(), snapshot.DescribeCapabilities{
			Snapshot: &descSnapshotInspector{},
		}, snapshot.DescribeOptions{Spec: spec})
		if err == nil || !errors.Is(err, client.ErrUsage) {
			t.Errorf("spec=%q: want ErrUsage, got %v", spec, err)
		}
	}
}

func TestSnapshotDescribe_notFound(t *testing.T) {
	_, err := snapshot.Describe(context.Background(), snapshot.DescribeCapabilities{
		Snapshot: &descSnapshotInspector{rows: nil},
	}, snapshot.DescribeOptions{Spec: "repo/snap"})
	if err == nil || !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestSnapshotDescribe_happyPath(t *testing.T) {
	caps := snapshot.DescribeCapabilities{
		Snapshot: &descSnapshotInspector{
			rows: []types.Snapshot{
				{Repository: "backup", Name: "daily-2025-05", State: "SUCCESS", ShardsTotal: 12, ShardsSuccessful: 12},
			},
			status: types.SnapshotStatus{State: "SUCCESS", ShardsDone: 12, ShardsTotal: 12},
		},
		Dialect: "elasticsearch",
	}
	desc, err := snapshot.Describe(context.Background(), caps, snapshot.DescribeOptions{Spec: "backup/daily-2025-05"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.Info.State != "SUCCESS" {
		t.Errorf("Info.State = %q, want SUCCESS", desc.Info.State)
	}
	if desc.Status == nil || desc.Status.ShardsDone != 12 {
		t.Errorf("Status block missing or wrong: %+v", desc.Status)
	}
}

func TestSnapshotDescribe_statusNotFoundIsNotAWarning(t *testing.T) {
	// MISSING snapshot — the cluster 404s /_status while the listing
	// call returned metadata. Describe should swallow the 404 silently
	// rather than putting it in SectionWarnings.
	caps := snapshot.DescribeCapabilities{
		Snapshot: &descSnapshotInspector{
			rows: []types.Snapshot{
				{Repository: "backup", Name: "ghost", State: "SUCCESS"},
			},
			statusErr: client.ErrNotFound,
		},
	}
	desc, err := snapshot.Describe(context.Background(), caps, snapshot.DescribeOptions{Spec: "backup/ghost"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.SectionWarnings != nil {
		t.Errorf("status 404 should not surface as a warning, got %v", desc.SectionWarnings)
	}
	if desc.Status != nil {
		t.Errorf("Status should be nil on 404, got %+v", desc.Status)
	}
}

func TestSnapshotDescribe_statusErrorIsAWarning(t *testing.T) {
	caps := snapshot.DescribeCapabilities{
		Snapshot: &descSnapshotInspector{
			rows: []types.Snapshot{
				{Repository: "backup", Name: "daily", State: "SUCCESS"},
			},
			statusErr: errors.New("boom"),
		},
	}
	desc, err := snapshot.Describe(context.Background(), caps, snapshot.DescribeOptions{Spec: "backup/daily"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.SectionWarnings["status"] != "boom" {
		t.Errorf("SectionWarnings[status] = %q", desc.SectionWarnings["status"])
	}
}

// SLM is ES-only and SM is OS-only; ErrUnsupported from the
// listing call (e.g. running snapshot describe against a cluster
// whose adapter is the wrong side of the divide for some reason)
// must propagate so the operator sees the dialect mismatch.
func TestSnapshotDescribe_propagatesErrUnsupported(t *testing.T) {
	caps := snapshot.DescribeCapabilities{
		Snapshot: &descSnapshotInspector{listErr: client.ErrUnsupported},
	}
	_, err := snapshot.Describe(context.Background(), caps, snapshot.DescribeOptions{Spec: "backup/daily"})
	if !errors.Is(err, client.ErrUnsupported) {
		t.Errorf("want ErrUnsupported, got %v", err)
	}
}
