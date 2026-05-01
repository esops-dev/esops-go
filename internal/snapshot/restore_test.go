package snapshot_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dj-wasabi/esops-go/internal/snapshot"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// fakeRestorer mirrors fakeCreator: it records the requests the business
// layer issues and returns a canned response. Same shape as the Create
// fake — keeps the tests focused on Restore's validation/dry-run paths
// rather than how the cluster's response is parsed (covered by adapter
// tests).
type fakeRestorer struct {
	calls []types.SnapshotRestoreRequest
	resp  types.SnapshotRestoreResult
	err   error
}

func (f *fakeRestorer) RestoreSnapshot(_ context.Context, req types.SnapshotRestoreRequest) (types.SnapshotRestoreResult, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return types.SnapshotRestoreResult{}, f.err
	}
	r := f.resp
	if r.Repository == "" {
		r.Repository = req.Repository
	}
	if r.Name == "" {
		r.Name = req.Snapshot
	}
	r.WaitForCompletion = req.WaitForCompletion
	return r, nil
}

func okRestoreResp() types.SnapshotRestoreResult {
	return types.SnapshotRestoreResult{Action: "restore", Accepted: true}
}

func TestRestore_rejectsEmptyRepository(t *testing.T) {
	c := &fakeRestorer{resp: okRestoreResp()}
	_, err := snapshot.Restore(context.Background(), c, snapshot.RestoreOptions{Snapshot: "nightly"})
	if err == nil || !strings.Contains(err.Error(), "repository") {
		t.Errorf("expected error mentioning --repository, got: %v", err)
	}
	if len(c.calls) != 0 {
		t.Errorf("cluster contacted on validation failure: %+v", c.calls)
	}
}

func TestRestore_rejectsEmptySnapshot(t *testing.T) {
	c := &fakeRestorer{resp: okRestoreResp()}
	_, err := snapshot.Restore(context.Background(), c, snapshot.RestoreOptions{Repository: "fs"})
	if err == nil || !strings.Contains(err.Error(), "snapshot") {
		t.Errorf("expected error mentioning --snapshot, got: %v", err)
	}
}

func TestRestore_rejectsPatternishNames(t *testing.T) {
	cases := []struct {
		name             string
		repository, snap string
	}{
		{"wildcard repo", "fs-*", "nightly"},
		{"wildcard snap", "fs", "nightly-*"},
		{"match-all repo", "*", "nightly"},
		{"_all snap", "fs", "_all"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeRestorer{resp: okRestoreResp()}
			_, err := snapshot.Restore(context.Background(), c, snapshot.RestoreOptions{
				Repository: tc.repository, Snapshot: tc.snap,
			})
			if err == nil {
				t.Errorf("expected pattern rejection for repository=%q snapshot=%q", tc.repository, tc.snap)
			}
			if len(c.calls) != 0 {
				t.Errorf("cluster contacted on validation failure: %+v", c.calls)
			}
		})
	}
}

func TestRestore_rejectsHalfRenamePair(t *testing.T) {
	// The cluster's own error on a missing rename half is a terse 500 that
	// doesn't say which half is missing; intercept it client-side.
	cases := []struct {
		name              string
		pattern, replace  string
	}{
		{"pattern without replacement", "(.+)", ""},
		{"replacement without pattern", "", "restored-$1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeRestorer{resp: okRestoreResp()}
			_, err := snapshot.Restore(context.Background(), c, snapshot.RestoreOptions{
				Repository: "fs", Snapshot: "nightly",
				RenamePattern: tc.pattern, RenameReplacement: tc.replace,
			})
			if err == nil || !strings.Contains(err.Error(), "rename") {
				t.Errorf("expected rename-pair rejection, got: %v", err)
			}
			if len(c.calls) != 0 {
				t.Errorf("cluster contacted on validation failure: %+v", c.calls)
			}
		})
	}
}

func TestRestore_dryRunDoesNotContactCluster(t *testing.T) {
	c := &fakeRestorer{resp: okRestoreResp()}
	res, err := snapshot.Restore(context.Background(), c, snapshot.RestoreOptions{
		Repository:        "fs",
		Snapshot:          "nightly",
		Indices:           "logs-*",
		RenamePattern:     "(.+)",
		RenameReplacement: "restored-$1",
		DryRun:            true,
	})
	if err != nil {
		t.Fatalf("Restore dry-run: %v", err)
	}
	if !res.DryRun || !res.Accepted {
		t.Errorf("dry-run result not marked accepted=true,dry_run=true: %+v", res)
	}
	if res.RenamePattern != "(.+)" || res.RenameReplacement != "restored-$1" {
		t.Errorf("dry-run did not echo the rename pair: %+v", res)
	}
	if len(res.Snapshot.Indices) != 1 || res.Snapshot.Indices[0] != "logs-*" {
		t.Errorf("dry-run indices preview = %v, want [logs-*]", res.Snapshot.Indices)
	}
	if len(c.calls) != 0 {
		t.Errorf("cluster contacted in dry-run: %+v", c.calls)
	}
}

func TestRestore_forwardsAllOptions(t *testing.T) {
	c := &fakeRestorer{resp: okRestoreResp()}
	tr, fa := true, false
	_, err := snapshot.Restore(context.Background(), c, snapshot.RestoreOptions{
		Repository:          "fs",
		Snapshot:            "nightly",
		Indices:             "logs-*,events-*",
		IncludeGlobalState:  &tr,
		Partial:             &fa,
		IgnoreUnavailable:   &tr,
		RenamePattern:       "(.+)",
		RenameReplacement:   "restored-$1",
		IndexSettings:       map[string]any{"index.number_of_replicas": "0"},
		IgnoreIndexSettings: []string{"index.refresh_interval"},
		WaitForCompletion:   true,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(c.calls) != 1 {
		t.Fatalf("expected one cluster call, got %d", len(c.calls))
	}
	got := c.calls[0]
	if got.Repository != "fs" || got.Snapshot != "nightly" {
		t.Errorf("repo/snap = %q/%q, want fs/nightly", got.Repository, got.Snapshot)
	}
	if got.RenamePattern != "(.+)" || got.RenameReplacement != "restored-$1" {
		t.Errorf("rename pair not forwarded: %+v", got)
	}
	if got.IncludeGlobalState == nil || !*got.IncludeGlobalState {
		t.Errorf("include_global_state not forwarded as true")
	}
	if got.Partial == nil || *got.Partial {
		t.Errorf("partial not forwarded as false")
	}
	if !got.WaitForCompletion {
		t.Errorf("wait_for_completion not forwarded")
	}
	if got.IndexSettings["index.number_of_replicas"] != "0" {
		t.Errorf("index_settings not forwarded: %+v", got.IndexSettings)
	}
	if len(got.IgnoreIndexSettings) != 1 || got.IgnoreIndexSettings[0] != "index.refresh_interval" {
		t.Errorf("ignore_index_settings not forwarded: %+v", got.IgnoreIndexSettings)
	}
}

func TestRestore_propagatesError(t *testing.T) {
	boom := errors.New("repo unreachable")
	c := &fakeRestorer{err: boom}
	_, err := snapshot.Restore(context.Background(), c, snapshot.RestoreOptions{
		Repository: "fs", Snapshot: "nightly",
	})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
}
