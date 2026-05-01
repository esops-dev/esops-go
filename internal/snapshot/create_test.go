package snapshot_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dj-wasabi/esops-go/internal/snapshot"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// fakeCreator records the requests the business layer issues and
// returns a canned response. Same shape as the other fakes in this
// package — keeps the tests focused on the validation and routing
// logic in `snapshot.Create`.
type fakeCreator struct {
	calls []types.SnapshotCreateRequest
	resp  types.SnapshotCreateResult
	err   error
}

func (f *fakeCreator) CreateSnapshot(_ context.Context, req types.SnapshotCreateRequest) (types.SnapshotCreateResult, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return types.SnapshotCreateResult{}, f.err
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

func okCreateResp() types.SnapshotCreateResult {
	return types.SnapshotCreateResult{Action: "create", Accepted: true}
}

func TestCreate_rejectsEmptyRepository(t *testing.T) {
	c := &fakeCreator{resp: okCreateResp()}
	_, err := snapshot.Create(context.Background(), c, snapshot.CreateOptions{Snapshot: "nightly"})
	if err == nil || !strings.Contains(err.Error(), "repository") {
		t.Errorf("expected error mentioning --repository, got: %v", err)
	}
	if len(c.calls) != 0 {
		t.Errorf("cluster contacted on validation failure: %+v", c.calls)
	}
}

func TestCreate_rejectsEmptySnapshot(t *testing.T) {
	c := &fakeCreator{resp: okCreateResp()}
	_, err := snapshot.Create(context.Background(), c, snapshot.CreateOptions{Repository: "fs"})
	if err == nil || !strings.Contains(err.Error(), "snapshot") {
		t.Errorf("expected error mentioning --snapshot, got: %v", err)
	}
}

func TestCreate_rejectsPatternishNames(t *testing.T) {
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
			c := &fakeCreator{resp: okCreateResp()}
			_, err := snapshot.Create(context.Background(), c, snapshot.CreateOptions{
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

func TestCreate_rejectsWhitespaceInSnapshot(t *testing.T) {
	c := &fakeCreator{resp: okCreateResp()}
	_, err := snapshot.Create(context.Background(), c, snapshot.CreateOptions{
		Repository: "fs", Snapshot: "nightly 2026-04-30",
	})
	if err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Errorf("expected whitespace rejection, got: %v", err)
	}
}

func TestCreate_dryRunDoesNotContactCluster(t *testing.T) {
	c := &fakeCreator{resp: okCreateResp()}
	res, err := snapshot.Create(context.Background(), c, snapshot.CreateOptions{
		Repository: "fs", Snapshot: "nightly", Indices: "logs-*", DryRun: true,
	})
	if err != nil {
		t.Fatalf("Create dry-run: %v", err)
	}
	if !res.DryRun || !res.Accepted {
		t.Errorf("dry-run result not marked accepted=true,dry_run=true: %+v", res)
	}
	if res.Snapshot.State != "DRY_RUN" {
		t.Errorf("dry-run state = %q, want DRY_RUN", res.Snapshot.State)
	}
	if len(res.Snapshot.Indices) != 1 || res.Snapshot.Indices[0] != "logs-*" {
		t.Errorf("dry-run indices preview = %v, want [logs-*]", res.Snapshot.Indices)
	}
	if len(c.calls) != 0 {
		t.Errorf("cluster contacted in dry-run: %+v", c.calls)
	}
}

func TestCreate_forwardsAllOptions(t *testing.T) {
	c := &fakeCreator{resp: okCreateResp()}
	tr, fa := true, false
	_, err := snapshot.Create(context.Background(), c, snapshot.CreateOptions{
		Repository:         "fs",
		Snapshot:           "nightly",
		Indices:            "logs-*,events-*",
		IncludeGlobalState: &tr,
		Partial:            &fa,
		IgnoreUnavailable:  &tr,
		WaitForCompletion:  true,
		Metadata:           map[string]any{"reason": "pre-upgrade"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(c.calls) != 1 {
		t.Fatalf("expected one cluster call, got %d", len(c.calls))
	}
	got := c.calls[0]
	if got.Repository != "fs" || got.Snapshot != "nightly" {
		t.Errorf("repo/snap = %q/%q, want fs/nightly", got.Repository, got.Snapshot)
	}
	if got.Indices != "logs-*,events-*" {
		t.Errorf("indices = %q, want logs-*,events-*", got.Indices)
	}
	if got.IncludeGlobalState == nil || !*got.IncludeGlobalState {
		t.Errorf("include_global_state not forwarded as true")
	}
	if got.Partial == nil || *got.Partial {
		t.Errorf("partial not forwarded as false")
	}
	if got.IgnoreUnavailable == nil || !*got.IgnoreUnavailable {
		t.Errorf("ignore_unavailable not forwarded as true")
	}
	if !got.WaitForCompletion {
		t.Errorf("wait_for_completion not forwarded")
	}
	if got.Metadata["reason"] != "pre-upgrade" {
		t.Errorf("metadata not forwarded: %+v", got.Metadata)
	}
}

func TestCreate_propagatesError(t *testing.T) {
	boom := errors.New("repo unreachable")
	c := &fakeCreator{err: boom}
	_, err := snapshot.Create(context.Background(), c, snapshot.CreateOptions{
		Repository: "fs", Snapshot: "nightly",
	})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
}
