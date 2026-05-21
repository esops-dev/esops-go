package snapshot_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/snapshot"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakePolicyManager records calls and returns canned responses. Both
// the inspector and the manager surface land here so the same fake
// covers list/get and put/delete/execute test cases.
type fakePolicyManager struct {
	listNames []string
	listOut   []types.SnapshotPolicy
	listErr   error

	gotPut    types.SnapshotPolicyPutRequest
	putOut    types.SnapshotPolicyPutResult
	putErr    error
	putCalled bool

	gotDelete    types.SnapshotPolicyDeleteRequest
	deleteOut    types.SnapshotPolicyDeleteResult
	deleteErr    error
	deleteCalled bool

	gotExecute    types.SnapshotPolicyExecuteRequest
	executeOut    types.SnapshotPolicyExecuteResult
	executeErr    error
	executeCalled bool
}

func (f *fakePolicyManager) ListSnapshotPolicies(_ context.Context, names []string) ([]types.SnapshotPolicy, error) {
	f.listNames = names
	return f.listOut, f.listErr
}
func (f *fakePolicyManager) GetSnapshotPolicy(_ context.Context, name string) (types.SnapshotPolicy, error) {
	for _, p := range f.listOut {
		if p.Name == name {
			return p, nil
		}
	}
	return types.SnapshotPolicy{}, client.ErrNotFound
}
func (f *fakePolicyManager) PutSnapshotPolicy(_ context.Context, req types.SnapshotPolicyPutRequest) (types.SnapshotPolicyPutResult, error) {
	f.putCalled = true
	f.gotPut = req
	return f.putOut, f.putErr
}
func (f *fakePolicyManager) DeleteSnapshotPolicy(_ context.Context, req types.SnapshotPolicyDeleteRequest) (types.SnapshotPolicyDeleteResult, error) {
	f.deleteCalled = true
	f.gotDelete = req
	return f.deleteOut, f.deleteErr
}
func (f *fakePolicyManager) ExecuteSnapshotPolicy(_ context.Context, req types.SnapshotPolicyExecuteRequest) (types.SnapshotPolicyExecuteResult, error) {
	f.executeCalled = true
	f.gotExecute = req
	return f.executeOut, f.executeErr
}

func samplePolicies() []types.SnapshotPolicy {
	// Deliberately out of name order so the business layer's sort is
	// observable in TestListPolicies_sortsByName.
	return []types.SnapshotPolicy{
		{Name: "weekly-archive", Repository: "s3-archive", Schedule: "0 0 0 ? * SUN", Enabled: true},
		{Name: "nightly-s3", Repository: "s3-prod", Schedule: "0 30 1 * * ?", Enabled: true, LastSuccessMillis: 1},
		{Name: "hourly-fs", Repository: "local-fs", Schedule: "0 0 * * * ?", Enabled: false},
	}
}

func TestListPolicies_sortsByName(t *testing.T) {
	fake := &fakePolicyManager{listOut: samplePolicies()}
	got, err := snapshot.ListPolicies(context.Background(), fake, snapshot.PolicyListOptions{})
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	want := []string{"hourly-fs", "nightly-s3", "weekly-archive"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, p := range got {
		if p.Name != want[i] {
			t.Errorf("got[%d].Name = %q, want %q", i, p.Name, want[i])
		}
	}
}

func TestListPolicies_passesNamesThrough(t *testing.T) {
	fake := &fakePolicyManager{listOut: samplePolicies()}
	_, err := snapshot.ListPolicies(context.Background(), fake, snapshot.PolicyListOptions{Names: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	if got := fake.listNames; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("listNames = %v, want [a b]", got)
	}
}

func TestGetPolicy_emptyNameRejected(t *testing.T) {
	fake := &fakePolicyManager{listOut: samplePolicies()}
	_, err := snapshot.GetPolicy(context.Background(), fake, "  ")
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("err = %v, want '--name is required'", err)
	}
}

func TestGetPolicy_patternRejected(t *testing.T) {
	fake := &fakePolicyManager{listOut: samplePolicies()}
	_, err := snapshot.GetPolicy(context.Background(), fake, "nightly-*")
	if err == nil || !strings.Contains(err.Error(), "literal policy name") {
		t.Errorf("err = %v, want pattern-rejection", err)
	}
}

func TestPutPolicy_dryRunDoesNotCallCluster(t *testing.T) {
	fake := &fakePolicyManager{}
	got, err := snapshot.PutPolicy(context.Background(), fake, snapshot.PolicyPutOptions{
		Name:   "nightly-s3",
		Body:   []byte(`{"schedule":"0 30 1 * * ?","name":"<n-{now/d}>","repository":"s3-prod"}`),
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}
	if fake.putCalled {
		t.Error("put was called despite --dry-run")
	}
	if !got.DryRun || !got.Acknowledged || got.Name != "nightly-s3" {
		t.Errorf("dry-run result = %+v", got)
	}
}

func TestPutPolicy_emptyBodyRejected(t *testing.T) {
	fake := &fakePolicyManager{}
	_, err := snapshot.PutPolicy(context.Background(), fake, snapshot.PolicyPutOptions{
		Name: "nightly-s3",
		Body: nil,
	})
	if err == nil || !strings.Contains(err.Error(), "policy body is required") {
		t.Errorf("err = %v, want 'policy body is required'", err)
	}
}

func TestPutPolicy_nonJSONObjectRejected(t *testing.T) {
	fake := &fakePolicyManager{}
	cases := []string{
		`[]`,       // array, not object
		`"hello"`,  // string
		`not json`, // not parseable
		`123`,      // number
	}
	for _, body := range cases {
		_, err := snapshot.PutPolicy(context.Background(), fake, snapshot.PolicyPutOptions{
			Name: "p",
			Body: []byte(body),
		})
		if err == nil {
			t.Errorf("body %q: expected error, got nil", body)
		}
	}
}

func TestPutPolicy_patternNameRejected(t *testing.T) {
	fake := &fakePolicyManager{}
	_, err := snapshot.PutPolicy(context.Background(), fake, snapshot.PolicyPutOptions{
		Name: "nightly-*",
		Body: []byte(`{}`),
	})
	if err == nil || !strings.Contains(err.Error(), "literal policy name") {
		t.Errorf("err = %v, want pattern-rejection", err)
	}
	if fake.putCalled {
		t.Error("put called for a pattern name — should have been rejected pre-flight")
	}
}

func TestPutPolicy_forwardsBodyToCapability(t *testing.T) {
	fake := &fakePolicyManager{
		putOut: types.SnapshotPolicyPutResult{Action: "policy-put", Name: "nightly-s3", Acknowledged: true},
	}
	body := []byte(`{"schedule":"0 30 1 * * ?","repository":"s3-prod"}`)
	_, err := snapshot.PutPolicy(context.Background(), fake, snapshot.PolicyPutOptions{
		Name: "nightly-s3",
		Body: body,
	})
	if err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}
	if !fake.putCalled {
		t.Fatal("PutSnapshotPolicy was not called")
	}
	if fake.gotPut.Name != "nightly-s3" {
		t.Errorf("got.Name = %q", fake.gotPut.Name)
	}
	if string(fake.gotPut.Body) != string(body) {
		t.Errorf("body forwarded as %s, want %s", fake.gotPut.Body, body)
	}
}

func TestDeletePolicy_dryRunDoesNotCallCluster(t *testing.T) {
	fake := &fakePolicyManager{}
	got, err := snapshot.DeletePolicy(context.Background(), fake, snapshot.PolicyDeleteOptions{
		Name:   "nightly-s3",
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("DeletePolicy: %v", err)
	}
	if fake.deleteCalled {
		t.Error("delete was called despite --dry-run")
	}
	if !got.DryRun || !got.Acknowledged {
		t.Errorf("dry-run result = %+v", got)
	}
}

func TestDeletePolicy_capabilityErrorSurfaces(t *testing.T) {
	wantErr := errors.New("policy still in use")
	fake := &fakePolicyManager{deleteErr: wantErr}
	_, err := snapshot.DeletePolicy(context.Background(), fake, snapshot.PolicyDeleteOptions{Name: "nightly-s3"})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}

func TestExecutePolicy_dryRunDoesNotCallCluster(t *testing.T) {
	fake := &fakePolicyManager{}
	got, err := snapshot.ExecutePolicy(context.Background(), fake, snapshot.PolicyExecuteOptions{
		Name:   "nightly-s3",
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("ExecutePolicy: %v", err)
	}
	if fake.executeCalled {
		t.Error("execute was called despite --dry-run")
	}
	if !got.DryRun || !got.Acknowledged {
		t.Errorf("dry-run result = %+v", got)
	}
}

func TestExecutePolicy_surfacesSnapshotName(t *testing.T) {
	fake := &fakePolicyManager{
		executeOut: types.SnapshotPolicyExecuteResult{
			Action:       "policy-execute",
			Name:         "nightly-s3",
			SnapshotName: "nightly-s3-2026.05.10-abc",
			Acknowledged: true,
		},
	}
	got, err := snapshot.ExecutePolicy(context.Background(), fake, snapshot.PolicyExecuteOptions{Name: "nightly-s3"})
	if err != nil {
		t.Fatalf("ExecutePolicy: %v", err)
	}
	if got.SnapshotName != "nightly-s3-2026.05.10-abc" {
		t.Errorf("SnapshotName = %q, want the synthesised name from the cluster", got.SnapshotName)
	}
}
