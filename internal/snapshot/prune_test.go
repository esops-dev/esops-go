package snapshot_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	esopsclient "github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/snapshot"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// fakePruner records bulk-delete requests and returns a canned ack.
type fakePruner struct {
	calls []types.SnapshotDeleteRequest
	resp  types.SnapshotDeleteResult
	err   error
}

func (f *fakePruner) DeleteSnapshots(_ context.Context, req types.SnapshotDeleteRequest) (types.SnapshotDeleteResult, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return types.SnapshotDeleteResult{}, f.err
	}
	r := f.resp
	if r.Repository == "" {
		r.Repository = req.Repository
	}
	if len(r.Snapshots) == 0 {
		r.Snapshots = append([]string(nil), req.Snapshots...)
	}
	return r, nil
}

// nightlyAt builds a snapshot record at the given start time with the given
// state. The default sample fleet exercises the keep-last/older-than/state
// rules without needing a separate fixture per test.
func nightlyAt(name, state string, start time.Time) types.Snapshot {
	return types.Snapshot{Repository: "fs", Name: name, State: state, StartTimeMillis: start.UnixMilli()}
}

// pruneFleet returns six nightly snapshots covering the rule matrix:
// four successes spaced 1d apart, one FAILED in the middle, and one PARTIAL
// at the tail. Times are anchored relative to a fixed "now" so the tests
// don't depend on the test runner's wall clock.
func pruneFleet(now time.Time) []types.Snapshot {
	day := 24 * time.Hour
	return []types.Snapshot{
		nightlyAt("nightly-2026-04-30", "SUCCESS", now.Add(-1*day)),
		nightlyAt("nightly-2026-04-29", "SUCCESS", now.Add(-2*day)),
		nightlyAt("nightly-2026-04-28", "FAILED", now.Add(-3*day)),
		nightlyAt("nightly-2026-04-27", "SUCCESS", now.Add(-4*day)),
		nightlyAt("nightly-2026-04-26", "SUCCESS", now.Add(-40*day)),
		nightlyAt("nightly-2026-04-25", "PARTIAL", now.Add(-60*day)),
	}
}

func TestPrune_requiresRetentionRule(t *testing.T) {
	ins := &fakeSnapshotInspector{snaps: pruneFleet(time.Now())}
	pr := &fakePruner{}
	_, err := snapshot.Prune(context.Background(), ins, pr, snapshot.PruneOptions{Repository: "fs"})
	if err == nil || !strings.Contains(err.Error(), "keep-last") {
		t.Errorf("expected error mentioning --keep-last, got: %v", err)
	}
	if len(pr.calls) != 0 {
		t.Errorf("cluster contacted on validation failure: %+v", pr.calls)
	}
}

func TestPrune_rejectsEmptyRepository(t *testing.T) {
	ins := &fakeSnapshotInspector{}
	pr := &fakePruner{}
	_, err := snapshot.Prune(context.Background(), ins, pr, snapshot.PruneOptions{KeepLast: 3})
	if err == nil || !strings.Contains(err.Error(), "repository") {
		t.Errorf("expected error mentioning --repository, got: %v", err)
	}
}

func TestPrune_keepLastDeletesPastWindow(t *testing.T) {
	now := time.Now()
	ins := &fakeSnapshotInspector{snaps: pruneFleet(now)}
	pr := &fakePruner{}
	res, err := snapshot.Prune(context.Background(), ins, pr, snapshot.PruneOptions{
		Repository: "fs", KeepLast: 3, AllowLargePrune: true,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(res.Kept) != 3 {
		t.Errorf("kept = %v, want 3 entries", res.Kept)
	}
	// Three most-recent (by start_time) are kept; the rest are deleted.
	wantDeleted := []string{"nightly-2026-04-27", "nightly-2026-04-26", "nightly-2026-04-25"}
	if !equalStringSlices(res.Deleted, wantDeleted) {
		t.Errorf("deleted = %v, want %v", res.Deleted, wantDeleted)
	}
	if len(pr.calls) != 1 {
		t.Fatalf("expected one delete call, got %d", len(pr.calls))
	}
	if !equalStringSlices(pr.calls[0].Snapshots, wantDeleted) {
		t.Errorf("forwarded snapshots = %v, want %v", pr.calls[0].Snapshots, wantDeleted)
	}
}

func TestPrune_olderThanDeletesAged(t *testing.T) {
	now := time.Now()
	ins := &fakeSnapshotInspector{snaps: pruneFleet(now)}
	pr := &fakePruner{}
	res, err := snapshot.Prune(context.Background(), ins, pr, snapshot.PruneOptions{
		Repository: "fs", OlderThan: 30 * 24 * time.Hour, OlderThanRaw: "30d",
		AllowLargePrune: true,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	// Only the two oldest (40d and 60d) cross the 30d cutoff.
	wantDeleted := []string{"nightly-2026-04-26", "nightly-2026-04-25"}
	if !equalStringSlices(res.Deleted, wantDeleted) {
		t.Errorf("deleted = %v, want %v", res.Deleted, wantDeleted)
	}
}

func TestPrune_keepLastAndOlderThanCompose(t *testing.T) {
	// keep-last 2 pins the two most recent; older-than 30d only deletes
	// what's past both rules. The 4d-old SUCCESS sits inside neither
	// window for deletion (older than the keep-last window of 2 but
	// inside the 30d cutoff), so it's kept.
	now := time.Now()
	ins := &fakeSnapshotInspector{snaps: pruneFleet(now)}
	pr := &fakePruner{}
	res, err := snapshot.Prune(context.Background(), ins, pr, snapshot.PruneOptions{
		Repository: "fs", KeepLast: 2, OlderThan: 30 * 24 * time.Hour, OlderThanRaw: "30d",
		AllowLargePrune: true,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	wantDeleted := []string{"nightly-2026-04-26", "nightly-2026-04-25"}
	if !equalStringSlices(res.Deleted, wantDeleted) {
		t.Errorf("deleted = %v, want %v", res.Deleted, wantDeleted)
	}
}

func TestPrune_stateFilterPinsOthers(t *testing.T) {
	// state=success means only SUCCESS rows are eligible for keep-last;
	// FAILED/PARTIAL stay kept because they're outside the rule entirely.
	now := time.Now()
	ins := &fakeSnapshotInspector{snaps: pruneFleet(now)}
	pr := &fakePruner{}
	res, err := snapshot.Prune(context.Background(), ins, pr, snapshot.PruneOptions{
		Repository: "fs", KeepLast: 2, StateFilter: "success",
		AllowLargePrune: true,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	// Two most-recent successes pin: 04-30, 04-29. The other two SUCCESS
	// rows (04-27, 04-26) are deleted. FAILED 04-28 and PARTIAL 04-25 are
	// pinned by the state filter.
	wantDeleted := []string{"nightly-2026-04-27", "nightly-2026-04-26"}
	if !equalStringSlices(res.Deleted, wantDeleted) {
		t.Errorf("deleted = %v, want %v", res.Deleted, wantDeleted)
	}
}

func TestPrune_dryRunDoesNotDelete(t *testing.T) {
	now := time.Now()
	ins := &fakeSnapshotInspector{snaps: pruneFleet(now)}
	pr := &fakePruner{}
	res, err := snapshot.Prune(context.Background(), ins, pr, snapshot.PruneOptions{
		Repository: "fs", KeepLast: 3, DryRun: true, AllowLargePrune: true,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if !res.DryRun {
		t.Errorf("dry_run not surfaced in result: %+v", res)
	}
	if res.Acknowledged {
		t.Errorf("acknowledged=true on dry-run; should be false")
	}
	if len(pr.calls) != 0 {
		t.Errorf("cluster contacted on dry-run: %+v", pr.calls)
	}
}

func TestPrune_blastRadiusGuardRefusesLargePrune(t *testing.T) {
	now := time.Now()
	ins := &fakeSnapshotInspector{snaps: pruneFleet(now)}
	pr := &fakePruner{}
	// keep-last 1 -> deletes 5 of 6 (83%) -> over 50% threshold.
	_, err := snapshot.Prune(context.Background(), ins, pr, snapshot.PruneOptions{
		Repository: "fs", KeepLast: 1,
	})
	if !errors.Is(err, esopsclient.ErrBlastRadius) {
		t.Errorf("err = %v, want ErrBlastRadius", err)
	}
	if len(pr.calls) != 0 {
		t.Errorf("cluster contacted despite blast-radius refusal: %+v", pr.calls)
	}
}

func TestPrune_blastRadiusOptInProceeds(t *testing.T) {
	now := time.Now()
	ins := &fakeSnapshotInspector{snaps: pruneFleet(now)}
	pr := &fakePruner{}
	_, err := snapshot.Prune(context.Background(), ins, pr, snapshot.PruneOptions{
		Repository: "fs", KeepLast: 1, AllowLargePrune: true,
	})
	if err != nil {
		t.Fatalf("expected --allow-large-prune to permit the run: %v", err)
	}
	if len(pr.calls) != 1 {
		t.Fatalf("expected one delete call, got %d", len(pr.calls))
	}
}

func TestPrune_inProgressSnapshotIsAlwaysKept(t *testing.T) {
	// A snapshot without a start time (in-progress on some versions) must
	// not be deletable by the older-than rule; an unfinished snapshot is
	// the worst regret in this whole tree.
	now := time.Now()
	rows := pruneFleet(now)
	rows = append(rows, types.Snapshot{Repository: "fs", Name: "in-progress", State: "IN_PROGRESS", StartTimeMillis: 0})
	ins := &fakeSnapshotInspector{snaps: rows}
	pr := &fakePruner{}
	res, err := snapshot.Prune(context.Background(), ins, pr, snapshot.PruneOptions{
		Repository: "fs", OlderThan: 1 * time.Second, OlderThanRaw: "1s", AllowLargePrune: true,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	for _, n := range res.Deleted {
		if n == "in-progress" {
			t.Errorf("in-progress snapshot scheduled for deletion: %v", res.Deleted)
		}
	}
}

func TestParseRetentionDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"30d":   30 * 24 * time.Hour,
		"2w":    14 * 24 * time.Hour,
		"1y":    365 * 24 * time.Hour,
		"720h":  720 * time.Hour,
		"":      0,
	}
	for in, want := range cases {
		got, err := snapshot.ParseRetentionDuration(in)
		if err != nil {
			t.Errorf("ParseRetentionDuration(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseRetentionDuration(%q) = %v, want %v", in, got, want)
		}
	}
	for _, bad := range []string{"zzz", "0d", "-1d", "30dd"} {
		if _, err := snapshot.ParseRetentionDuration(bad); err == nil {
			t.Errorf("ParseRetentionDuration(%q): want error, got nil", bad)
		}
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, v := range a {
		if v != b[i] {
			return false
		}
	}
	return true
}
