package ops_test

import (
	"context"
	"errors"
	"testing"

	"github.com/esops-dev/esops-go/internal/ops"
	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

type fakeRemoteInspector struct {
	remotes        []types.RemoteCluster
	remotesErr     error
	followers      []types.FollowerStats
	followersErr   error
	autoFollows    []types.AutoFollowPattern
	autoFollowsErr error
}

func (f *fakeRemoteInspector) RemoteClusters(_ context.Context) ([]types.RemoteCluster, error) {
	return f.remotes, f.remotesErr
}

func (f *fakeRemoteInspector) FollowerStats(_ context.Context) ([]types.FollowerStats, error) {
	return f.followers, f.followersErr
}

func (f *fakeRemoteInspector) AutoFollowPatterns(_ context.Context) ([]types.AutoFollowPattern, error) {
	return f.autoFollows, f.autoFollowsErr
}

func TestListRemotes_happyPath(t *testing.T) {
	fake := &fakeRemoteInspector{
		remotes:   []types.RemoteCluster{{Name: "a", Connected: true}},
		followers: []types.FollowerStats{{Index: "idx-a", FollowerLagOps: 3}},
	}
	got, err := ops.ListRemotes(context.Background(), fake)
	if err != nil {
		t.Fatalf("ListRemotes: %v", err)
	}
	if got.CCRStatus != "ok" {
		t.Errorf("CCRStatus = %q, want ok", got.CCRStatus)
	}
	if len(got.Remotes) != 1 || len(got.Followers) != 1 {
		t.Errorf("unexpected lengths: remotes=%d followers=%d", len(got.Remotes), len(got.Followers))
	}
}

func TestListRemotes_unsupportedCCRSurfacesAsHeader(t *testing.T) {
	// OpenSearch / basic-licence ES: FollowerStats returns
	// ErrUnsupported. ListRemotes must keep the CCS half and surface
	// the reason in CCRStatus rather than aborting.
	fake := &fakeRemoteInspector{
		remotes:      []types.RemoteCluster{{Name: "a", Connected: true}},
		followersErr: esopsclient.ErrUnsupported,
	}
	got, err := ops.ListRemotes(context.Background(), fake)
	if err != nil {
		t.Fatalf("ListRemotes: %v", err)
	}
	if got.CCRStatus != "unsupported" {
		t.Errorf("CCRStatus = %q, want unsupported", got.CCRStatus)
	}
	if len(got.Followers) != 0 {
		t.Errorf("Followers should be empty when unsupported, got %d", len(got.Followers))
	}
	if len(got.Remotes) != 1 {
		t.Errorf("CCS half should still render: got %d remotes", len(got.Remotes))
	}
}

func TestListRemotes_forbiddenCCRSurfacesAsHeader(t *testing.T) {
	// Auth user can read /_remote/info but not /_ccr/stats. Distinct
	// from "unsupported" so the operator knows to fix permissions.
	fake := &fakeRemoteInspector{
		remotes:      []types.RemoteCluster{{Name: "a"}},
		followersErr: esopsclient.ErrForbidden,
	}
	got, err := ops.ListRemotes(context.Background(), fake)
	if err != nil {
		t.Fatalf("ListRemotes: %v", err)
	}
	if got.CCRStatus != "forbidden" {
		t.Errorf("CCRStatus = %q, want forbidden", got.CCRStatus)
	}
}

func TestListRemotes_unauthorizedCCRSurfacesAsHeader(t *testing.T) {
	fake := &fakeRemoteInspector{
		remotes:      []types.RemoteCluster{{Name: "a"}},
		followersErr: esopsclient.ErrAuth,
	}
	got, err := ops.ListRemotes(context.Background(), fake)
	if err != nil {
		t.Fatalf("ListRemotes: %v", err)
	}
	if got.CCRStatus != "unauthorized" {
		t.Errorf("CCRStatus = %q, want unauthorized", got.CCRStatus)
	}
}

func TestListRemotes_remoteClustersErrorPropagates(t *testing.T) {
	want := errors.New("transport boom")
	fake := &fakeRemoteInspector{remotesErr: want}
	_, err := ops.ListRemotes(context.Background(), fake)
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

func TestListRemotes_unexpectedCCRErrorPropagates(t *testing.T) {
	// A non-classified error from FollowerStats (e.g. a 5xx) must
	// fail the whole command rather than silently rendering an empty
	// follower list — operators count on that signal.
	want := errors.New("ccr 500")
	fake := &fakeRemoteInspector{
		remotes:      []types.RemoteCluster{{Name: "a"}},
		followersErr: want,
	}
	_, err := ops.ListRemotes(context.Background(), fake)
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

func TestListRemotes_happyPathIncludesAutoFollows(t *testing.T) {
	fake := &fakeRemoteInspector{
		remotes:     []types.RemoteCluster{{Name: "a", Connected: true}},
		followers:   []types.FollowerStats{{Index: "idx-a", FollowerLagOps: 3}},
		autoFollows: []types.AutoFollowPattern{{Name: "p1", RemoteCluster: "a", LeaderIndexPatterns: []string{"logs-*"}, Active: true}},
	}
	got, err := ops.ListRemotes(context.Background(), fake)
	if err != nil {
		t.Fatalf("ListRemotes: %v", err)
	}
	if got.CCRStatus != "ok" {
		t.Errorf("CCRStatus = %q, want ok", got.CCRStatus)
	}
	if len(got.AutoFollows) != 1 || got.AutoFollows[0].Name != "p1" {
		t.Errorf("AutoFollows = %#v, want one entry named p1", got.AutoFollows)
	}
	if got.AutoFollowError != "" {
		t.Errorf("AutoFollowError = %q, want empty", got.AutoFollowError)
	}
}

func TestListRemotes_skipsAutoFollowWhenCCRUnsupported(t *testing.T) {
	// On a CCRStatus other than "ok" the AutoFollow call is skipped
	// entirely — saves a round trip on basic-licence/OS clusters
	// where the answer is "not applicable" by construction.
	called := false
	fake := &countingAutoFollowInspector{
		fakeRemoteInspector: fakeRemoteInspector{
			remotes:      []types.RemoteCluster{{Name: "a"}},
			followersErr: esopsclient.ErrUnsupported,
		},
		called: &called,
	}
	got, err := ops.ListRemotes(context.Background(), fake)
	if err != nil {
		t.Fatalf("ListRemotes: %v", err)
	}
	if called {
		t.Errorf("AutoFollowPatterns was called despite CCRStatus=unsupported")
	}
	if got.CCRStatus != "unsupported" {
		t.Errorf("CCRStatus = %q, want unsupported", got.CCRStatus)
	}
	if got.AutoFollowError != "" {
		t.Errorf("AutoFollowError = %q, want empty (skipped, not failed)", got.AutoFollowError)
	}
}

func TestListRemotes_autoFollowDivergentErrorRecordedNotPropagated(t *testing.T) {
	// FollowerStats succeeded but AutoFollow failed with an auth
	// error — surface the reason but keep CCRStatus="ok" so the
	// FollowerStats view still renders.
	fake := &fakeRemoteInspector{
		remotes:        []types.RemoteCluster{{Name: "a"}},
		followers:      []types.FollowerStats{{Index: "idx-a"}},
		autoFollowsErr: esopsclient.ErrForbidden,
	}
	got, err := ops.ListRemotes(context.Background(), fake)
	if err != nil {
		t.Fatalf("ListRemotes: %v", err)
	}
	if got.CCRStatus != "ok" {
		t.Errorf("CCRStatus = %q, want ok (FollowerStats succeeded)", got.CCRStatus)
	}
	if got.AutoFollowError == "" {
		t.Errorf("AutoFollowError = empty; want the underlying reason")
	}
	if len(got.AutoFollows) != 0 {
		t.Errorf("AutoFollows = %#v, want empty when call failed", got.AutoFollows)
	}
}

func TestListRemotes_autoFollowUnexpectedErrorPropagates(t *testing.T) {
	// 5xx from AutoFollow must fail the command — same signal
	// guarantee as the FollowerStats unexpected-error path.
	want := errors.New("auto-follow 500")
	fake := &fakeRemoteInspector{
		remotes:        []types.RemoteCluster{{Name: "a"}},
		followers:      []types.FollowerStats{{Index: "idx-a"}},
		autoFollowsErr: want,
	}
	_, err := ops.ListRemotes(context.Background(), fake)
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

// countingAutoFollowInspector wraps fakeRemoteInspector with a hook
// that records whether AutoFollowPatterns was invoked. Used by the
// "skipped on non-ok CCRStatus" test.
type countingAutoFollowInspector struct {
	fakeRemoteInspector
	called *bool
}

func (c *countingAutoFollowInspector) AutoFollowPatterns(ctx context.Context) ([]types.AutoFollowPattern, error) {
	if c.called != nil {
		*c.called = true
	}
	return c.fakeRemoteInspector.AutoFollowPatterns(ctx)
}
