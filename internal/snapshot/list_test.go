package snapshot_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dj-wasabi/esops-go/internal/snapshot"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// fakeSnapshotInspector mimics the adapter return: a flat list of
// snapshots (already fanned out across repos) and the registered repos.
type fakeSnapshotInspector struct {
	gotSnapFilter types.SnapshotFilter
	gotRepoFilter types.SnapshotRepositoryFilter
	repos         []types.SnapshotRepository
	snaps         []types.Snapshot
	err           error
}

func (f *fakeSnapshotInspector) Repositories(_ context.Context, filter types.SnapshotRepositoryFilter) ([]types.SnapshotRepository, error) {
	f.gotRepoFilter = filter
	return f.repos, f.err
}

func (f *fakeSnapshotInspector) Snapshots(_ context.Context, filter types.SnapshotFilter) ([]types.Snapshot, error) {
	f.gotSnapFilter = filter
	return f.snaps, f.err
}

func sampleSnapshots() []types.Snapshot {
	return []types.Snapshot{
		// Most recent: a successful nightly snapshot in the s3 repo.
		{Repository: "s3", Name: "nightly-2026-04-30", State: "SUCCESS", StartTimeMillis: 1714435200000, DurationMillis: 120_000, ShardsTotal: 10, ShardsSuccessful: 10},
		// A failed run earlier the same day in the same repo.
		{Repository: "s3", Name: "nightly-2026-04-29", State: "FAILED", StartTimeMillis: 1714348800000, DurationMillis: 30_000, ShardsTotal: 10, ShardsSuccessful: 9, ShardsFailed: 1},
		// An older partial in a different fs-backed repo.
		{Repository: "fs", Name: "manual-pre-upgrade", State: "PARTIAL", StartTimeMillis: 1713830400000, DurationMillis: 480_000, ShardsTotal: 10, ShardsSuccessful: 8, ShardsFailed: 2},
		// An in-progress snapshot — no end time, short start time gap.
		{Repository: "s3", Name: "now-running", State: "IN_PROGRESS", StartTimeMillis: 1714521600000, DurationMillis: 0, ShardsTotal: 10, ShardsSuccessful: 0},
	}
}

func TestParseSort(t *testing.T) {
	cases := map[string]types.SnapshotSort{
		"":          types.SnapshotSortStartTime,
		"start_time": types.SnapshotSortStartTime,
		"NAME":       types.SnapshotSortName,
		"duration":   types.SnapshotSortDuration,
	}
	for in, want := range cases {
		got, err := snapshot.ParseSort(in)
		if err != nil {
			t.Errorf("ParseSort(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseSort(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := snapshot.ParseSort("zzz"); err == nil {
		t.Errorf("ParseSort(\"zzz\"): want error, got nil")
	}
}

func TestValidateState(t *testing.T) {
	for _, in := range []string{"", "success", "FAILED", "in_progress", "in progress", "Partial"} {
		if err := snapshot.ValidateState(in); err != nil {
			t.Errorf("ValidateState(%q) = %v, want nil", in, err)
		}
	}
	if err := snapshot.ValidateState("zzz"); err == nil {
		t.Errorf("ValidateState(\"zzz\"): want error, got nil")
	}
}

func TestList_defaultSortIsMostRecentFirst(t *testing.T) {
	fake := &fakeSnapshotInspector{snaps: sampleSnapshots()}
	got, err := snapshot.List(context.Background(), fake, types.SnapshotFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"now-running", "nightly-2026-04-30", "nightly-2026-04-29", "manual-pre-upgrade"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, s := range got {
		if s.Name != want[i] {
			t.Errorf("got[%d].Name = %q, want %q", i, s.Name, want[i])
		}
	}
}

func TestList_stateFilterCaseInsensitive(t *testing.T) {
	fake := &fakeSnapshotInspector{snaps: sampleSnapshots()}
	got, err := snapshot.List(context.Background(), fake, types.SnapshotFilter{State: "success"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "nightly-2026-04-30" {
		t.Errorf("got %d rows, want only nightly-2026-04-30", len(got))
	}
}

func TestList_stateFilterNormalisesUnderscoreAndSpace(t *testing.T) {
	fake := &fakeSnapshotInspector{snaps: sampleSnapshots()}
	got, err := snapshot.List(context.Background(), fake, types.SnapshotFilter{State: "in progress"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "now-running" {
		t.Errorf("got %v, want [now-running]", names(got))
	}
}

func TestList_sortByName_groupsByRepository(t *testing.T) {
	fake := &fakeSnapshotInspector{snaps: sampleSnapshots()}
	got, err := snapshot.List(context.Background(), fake, types.SnapshotFilter{Sort: types.SnapshotSortName})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// fs-repo's single snapshot first (alphabetic); then s3-repo
	// snapshots in lexical order within the repo.
	want := []string{"manual-pre-upgrade", "nightly-2026-04-29", "nightly-2026-04-30", "now-running"}
	for i, s := range got {
		if s.Name != want[i] {
			t.Errorf("got[%d].Name = %q, want %q", i, s.Name, want[i])
		}
	}
}

func TestList_sortByDuration(t *testing.T) {
	fake := &fakeSnapshotInspector{snaps: sampleSnapshots()}
	got, err := snapshot.List(context.Background(), fake, types.SnapshotFilter{Sort: types.SnapshotSortDuration})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got[0].Name != "manual-pre-upgrade" {
		t.Errorf("longest-running first: got %q, want manual-pre-upgrade", got[0].Name)
	}
}

func TestList_passesFilterThrough(t *testing.T) {
	fake := &fakeSnapshotInspector{snaps: sampleSnapshots()}
	_, err := snapshot.List(context.Background(), fake, types.SnapshotFilter{Repository: "s3", Snapshot: "nightly-*", IgnoreUnavailable: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if fake.gotSnapFilter.Repository != "s3" {
		t.Errorf("filter.Repository = %q, want s3", fake.gotSnapFilter.Repository)
	}
	if fake.gotSnapFilter.Snapshot != "nightly-*" {
		t.Errorf("filter.Snapshot = %q, want nightly-*", fake.gotSnapFilter.Snapshot)
	}
	if !fake.gotSnapFilter.IgnoreUnavailable {
		t.Errorf("filter.IgnoreUnavailable not forwarded")
	}
}

func TestList_propagatesError(t *testing.T) {
	boom := errors.New("repo unreachable")
	fake := &fakeSnapshotInspector{err: boom}
	_, err := snapshot.List(context.Background(), fake, types.SnapshotFilter{})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestListRepositories_sortsByName(t *testing.T) {
	fake := &fakeSnapshotInspector{repos: []types.SnapshotRepository{
		{Name: "s3", Type: "s3"},
		{Name: "fs", Type: "fs"},
	}}
	got, err := snapshot.ListRepositories(context.Background(), fake, types.SnapshotRepositoryFilter{})
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if len(got) != 2 || got[0].Name != "fs" || got[1].Name != "s3" {
		t.Errorf("got %v, want [fs s3]", repoNames(got))
	}
}

func names(s []types.Snapshot) []string {
	out := make([]string, 0, len(s))
	for _, x := range s {
		out = append(out, x.Name)
	}
	return out
}

func repoNames(r []types.SnapshotRepository) []string {
	out := make([]string, 0, len(r))
	for _, x := range r {
		out = append(out, x.Name)
	}
	return out
}
