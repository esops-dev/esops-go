package snapshot_test

import (
	"errors"
	"testing"
	"time"

	"github.com/dj-wasabi/esops-go/internal/snapshot"
)

func TestSaveAndLoadCreateState_roundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	want := snapshot.CreateState{
		Action:            "create",
		Context:           "prod-eu",
		Repository:        "s3",
		Snapshot:          "nightly-2026-04-30",
		StartedAt:         time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
		SubmittedAccepted: true,
	}
	if err := snapshot.SaveCreateState(want); err != nil {
		t.Fatalf("SaveCreateState: %v", err)
	}
	got, err := snapshot.LoadCreateState(want.Context, want.Repository, want.Snapshot)
	if err != nil {
		t.Fatalf("LoadCreateState: %v", err)
	}
	if got.Snapshot != want.Snapshot || got.Repository != want.Repository {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, want)
	}
	if !got.SubmittedAccepted {
		t.Errorf("SubmittedAccepted not preserved: %+v", got)
	}
}

func TestLoadCreateState_missingFileIsErrNoState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, err := snapshot.LoadCreateState("ctx", "repo", "snap")
	if !errors.Is(err, snapshot.ErrNoState) {
		t.Errorf("missing file: got %v, want ErrNoState", err)
	}
}

func TestClearCreateState_removesFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	state := snapshot.CreateState{Action: "create", Context: "c", Repository: "r", Snapshot: "s"}
	if err := snapshot.SaveCreateState(state); err != nil {
		t.Fatalf("SaveCreateState: %v", err)
	}
	if err := snapshot.ClearCreateState("c", "r", "s"); err != nil {
		t.Fatalf("ClearCreateState: %v", err)
	}
	if _, err := snapshot.LoadCreateState("c", "r", "s"); !errors.Is(err, snapshot.ErrNoState) {
		t.Errorf("after clear: got %v, want ErrNoState", err)
	}
}

func TestCreateStatePath_isolatesByContextAndName(t *testing.T) {
	a := snapshot.CreateStatePath("prod", "s3", "nightly")
	b := snapshot.CreateStatePath("staging", "s3", "nightly")
	c := snapshot.CreateStatePath("prod", "s3", "weekly")
	if a == b || a == c {
		t.Errorf("state paths collided: a=%q b=%q c=%q", a, b, c)
	}
}

func TestCreateStatePath_sanitisesPathSeparators(t *testing.T) {
	// A snapshot name containing `/` must not escape the state directory.
	got := snapshot.CreateStatePath("prod", "s3", "nightly/2026-04-30")
	want := snapshot.CreateStatePath("prod", "s3", "nightly_2026-04-30")
	if got != want {
		t.Errorf("path-separator not sanitised: got %q, want %q", got, want)
	}
}

func TestSaveAndLoadRestoreState_roundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	want := snapshot.RestoreState{
		Action:            "restore",
		Context:           "prod-eu",
		Repository:        "s3",
		Snapshot:          "pre-upgrade-20260430",
		StartedAt:         time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
		SubmittedAccepted: true,
		TargetIndices:     []string{"restored-logs-1", "restored-logs-2"},
	}
	if err := snapshot.SaveRestoreState(want); err != nil {
		t.Fatalf("SaveRestoreState: %v", err)
	}
	got, err := snapshot.LoadRestoreState(want.Context, want.Repository, want.Snapshot)
	if err != nil {
		t.Fatalf("LoadRestoreState: %v", err)
	}
	if got.Snapshot != want.Snapshot || got.Repository != want.Repository {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, want)
	}
	if !got.SubmittedAccepted {
		t.Errorf("SubmittedAccepted not preserved: %+v", got)
	}
	if len(got.TargetIndices) != 2 || got.TargetIndices[0] != "restored-logs-1" {
		t.Errorf("TargetIndices not preserved: %+v", got.TargetIndices)
	}
}

func TestRestoreStatePath_isolatedFromCreateStatePath(t *testing.T) {
	// A parallel `snapshot create` and `snapshot restore` against the
	// same (context, repo, name) tuple must not stomp on each other's
	// state file. The file names use distinct prefixes.
	create := snapshot.CreateStatePath("prod", "s3", "x")
	restore := snapshot.RestoreStatePath("prod", "s3", "x")
	if create == restore {
		t.Errorf("create and restore state paths collided: %q", create)
	}
}

func TestLoadRestoreState_missingFileIsErrNoState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, err := snapshot.LoadRestoreState("ctx", "repo", "snap")
	if !errors.Is(err, snapshot.ErrNoState) {
		t.Errorf("missing file: got %v, want ErrNoState", err)
	}
}
