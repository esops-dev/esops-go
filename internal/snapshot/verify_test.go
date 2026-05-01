package snapshot_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dj-wasabi/esops-go/internal/snapshot"
	"github.com/dj-wasabi/esops-go/internal/types"
)

type fakeVerifier struct {
	calls []types.SnapshotVerifyRequest
	resp  types.SnapshotVerifyResult
	err   error
}

func (f *fakeVerifier) VerifyRepository(_ context.Context, req types.SnapshotVerifyRequest) (types.SnapshotVerifyResult, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return types.SnapshotVerifyResult{}, f.err
	}
	r := f.resp
	if r.Repository == "" {
		r.Repository = req.Repository
	}
	return r, nil
}

func TestVerify_rejectsEmptyRepository(t *testing.T) {
	v := &fakeVerifier{}
	_, err := snapshot.Verify(context.Background(), v, snapshot.VerifyOptions{})
	if err == nil || !strings.Contains(err.Error(), "repository") {
		t.Errorf("expected error mentioning --repository, got: %v", err)
	}
	if len(v.calls) != 0 {
		t.Errorf("cluster contacted on validation failure: %+v", v.calls)
	}
}

func TestVerify_rejectsPatternishRepository(t *testing.T) {
	v := &fakeVerifier{}
	_, err := snapshot.Verify(context.Background(), v, snapshot.VerifyOptions{Repository: "*"})
	if err == nil {
		t.Errorf("expected pattern rejection")
	}
	if len(v.calls) != 0 {
		t.Errorf("cluster contacted on validation failure: %+v", v.calls)
	}
}

func TestVerify_forwardsRepository(t *testing.T) {
	v := &fakeVerifier{resp: types.SnapshotVerifyResult{
		Action: "verify", VerifiedNodes: []string{"n1"}, Names: []string{"node-a"},
	}}
	res, err := snapshot.Verify(context.Background(), v, snapshot.VerifyOptions{Repository: " fs "})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// Repository should be trimmed before being forwarded — operators
	// often paste names with surrounding whitespace from logs.
	if len(v.calls) != 1 || v.calls[0].Repository != "fs" {
		t.Errorf("forwarded repository = %+v, want [fs]", v.calls)
	}
	if len(res.VerifiedNodes) != 1 || res.VerifiedNodes[0] != "n1" {
		t.Errorf("nodes = %v, want [n1]", res.VerifiedNodes)
	}
}

func TestVerify_propagatesError(t *testing.T) {
	boom := errors.New("repo unreachable")
	v := &fakeVerifier{err: boom}
	_, err := snapshot.Verify(context.Background(), v, snapshot.VerifyOptions{Repository: "fs"})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
}
