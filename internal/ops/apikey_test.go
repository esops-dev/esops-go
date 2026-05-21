package ops_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

type fakeAPIKeyInspector struct {
	keys []types.APIKey
	one  types.APIKey
	err  error

	gotID string
}

func (f *fakeAPIKeyInspector) APIKeys(_ context.Context) ([]types.APIKey, error) {
	return f.keys, f.err
}

func (f *fakeAPIKeyInspector) GetAPIKey(_ context.Context, id string) (types.APIKey, error) {
	f.gotID = id
	return f.one, f.err
}

type fakeAPIKeyManager struct {
	created      types.APIKeyCreated
	createErr    error
	invalidated  types.APIKeyInvalidated
	invalidErr   error
	createReq    types.APIKeyCreateRequest
	invalidReq   types.APIKeyInvalidateRequest
	createCalled bool
	invalidCalls int
}

func (f *fakeAPIKeyManager) CreateAPIKey(_ context.Context, req types.APIKeyCreateRequest) (types.APIKeyCreated, error) {
	f.createCalled = true
	f.createReq = req
	return f.created, f.createErr
}

func (f *fakeAPIKeyManager) InvalidateAPIKey(_ context.Context, req types.APIKeyInvalidateRequest) (types.APIKeyInvalidated, error) {
	f.invalidCalls++
	f.invalidReq = req
	return f.invalidated, f.invalidErr
}

func TestListAPIKeys_propagatesError(t *testing.T) {
	want := errors.New("boom")
	fake := &fakeAPIKeyInspector{err: want}
	_, err := ops.ListAPIKeys(context.Background(), fake)
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

func TestGetAPIKey_requiresID(t *testing.T) {
	_, err := ops.GetAPIKey(context.Background(), &fakeAPIKeyInspector{}, "   ")
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
}

func TestGetAPIKey_passesIDToInspector(t *testing.T) {
	fake := &fakeAPIKeyInspector{one: types.APIKey{ID: "abc", Name: "k"}}
	got, err := ops.GetAPIKey(context.Background(), fake, "abc")
	if err != nil {
		t.Fatalf("GetAPIKey: %v", err)
	}
	if fake.gotID != "abc" {
		t.Errorf("inspector got id %q, want %q", fake.gotID, "abc")
	}
	if got.ID != "abc" {
		t.Errorf("returned key id = %q, want %q", got.ID, "abc")
	}
}

func TestCreateAPIKey_requiresName(t *testing.T) {
	fake := &fakeAPIKeyManager{}
	insp := &fakeAPIKeyInspector{}
	_, err := ops.CreateAPIKey(context.Background(), fake, insp, ops.CreateAPIKeyOptions{})
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
	if fake.createCalled {
		t.Errorf("CreateAPIKey should not have called the adapter on usage error")
	}
}

func TestCreateAPIKey_passesNameAndExpiration(t *testing.T) {
	fake := &fakeAPIKeyManager{
		created: types.APIKeyCreated{ID: "id1", Name: "k1", Key: "secret", Encoded: "enc"},
	}
	insp := &fakeAPIKeyInspector{one: types.APIKey{ID: "id1", Name: "k1", Username: "kibana_user"}}
	got, err := ops.CreateAPIKey(context.Background(), fake, insp, ops.CreateAPIKeyOptions{
		Name:       "k1",
		Expiration: "1d",
	})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if fake.createReq.Name != "k1" {
		t.Errorf("Name forwarded = %q, want k1", fake.createReq.Name)
	}
	if fake.createReq.Expiration != "1d" {
		t.Errorf("Expiration forwarded = %q, want 1d", fake.createReq.Expiration)
	}
	if got.Key != "secret" {
		t.Errorf("returned Key = %q, want secret", got.Key)
	}
	// Owner enrichment: the follow-up GET should populate the field.
	if got.Owner != "kibana_user" {
		t.Errorf("Owner = %q, want kibana_user (from follow-up GET)", got.Owner)
	}
	if insp.gotID != "id1" {
		t.Errorf("inspector got id %q, want id1 (follow-up GET to fill owner)", insp.gotID)
	}
}

func TestCreateAPIKey_ownerEnrichmentBestEffort(t *testing.T) {
	// Follow-up GET failure must not abort the create — the key already
	// exists in the cluster, the secret is in `created`, and the
	// operator can resolve the owner later. Owner stays empty.
	fake := &fakeAPIKeyManager{
		created: types.APIKeyCreated{ID: "id1", Name: "k1", Key: "secret"},
	}
	insp := &fakeAPIKeyInspector{err: errors.New("transient lookup failure")}
	got, err := ops.CreateAPIKey(context.Background(), fake, insp, ops.CreateAPIKeyOptions{Name: "k1"})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if got.Key != "secret" {
		t.Errorf("Key = %q, want secret (create result must survive owner-lookup failure)", got.Key)
	}
	if got.Owner != "" {
		t.Errorf("Owner = %q, want empty when follow-up failed", got.Owner)
	}
}

func TestCreateAPIKey_dryRunSkipsClusterCall(t *testing.T) {
	fake := &fakeAPIKeyManager{}
	insp := &fakeAPIKeyInspector{}
	got, err := ops.CreateAPIKey(context.Background(), fake, insp, ops.CreateAPIKeyOptions{
		Name:       "k1",
		Expiration: "1d",
		DryRun:     true,
	})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if fake.createCalled {
		t.Errorf("dry-run must not call the adapter")
	}
	if !got.DryRun {
		t.Errorf("returned DryRun=false on dry-run path")
	}
	if got.Key != "" || got.Encoded != "" {
		t.Errorf("dry-run leaked secret-shaped fields: key=%q encoded=%q", got.Key, got.Encoded)
	}
	if got.Name != "k1" {
		t.Errorf("dry-run dropped name: got %q", got.Name)
	}
	if got.Expiration == nil {
		t.Errorf("dry-run with 1d expiration should compute a non-nil expiry preview")
	}
}

func TestCreateAPIKey_loadsRoleDescriptorsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rd.json")
	rd := `{"role1":{"cluster":["monitor"]}}`
	if err := os.WriteFile(path, []byte(rd), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	fake := &fakeAPIKeyManager{created: types.APIKeyCreated{ID: "id1", Name: "k1"}}
	insp := &fakeAPIKeyInspector{one: types.APIKey{ID: "id1"}}
	_, err := ops.CreateAPIKey(context.Background(), fake, insp, ops.CreateAPIKeyOptions{
		Name:                "k1",
		RoleDescriptorsPath: path,
	})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if !json.Valid(fake.createReq.RoleDescriptors) {
		t.Errorf("RoleDescriptors forwarded is not valid JSON: %s", fake.createReq.RoleDescriptors)
	}
	if !strings.Contains(string(fake.createReq.RoleDescriptors), "role1") {
		t.Errorf("RoleDescriptors forwarded = %s, want to contain 'role1'", fake.createReq.RoleDescriptors)
	}
}

func TestCreateAPIKey_rejectsMalformedRoleDescriptorsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	fake := &fakeAPIKeyManager{}
	insp := &fakeAPIKeyInspector{}
	_, err := ops.CreateAPIKey(context.Background(), fake, insp, ops.CreateAPIKeyOptions{
		Name:                "k1",
		RoleDescriptorsPath: path,
	})
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("err = %v, want ErrUsage on bad JSON", err)
	}
	if fake.createCalled {
		t.Errorf("CreateAPIKey should not call adapter on bad role-descriptors JSON")
	}
}

func TestInvalidateAPIKey_requiresAtLeastOneID(t *testing.T) {
	fake := &fakeAPIKeyManager{}
	_, err := ops.InvalidateAPIKey(context.Background(), fake, ops.InvalidateAPIKeyOptions{})
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
	if fake.invalidCalls != 0 {
		t.Errorf("expected 0 invalidate calls, got %d", fake.invalidCalls)
	}
}

func TestInvalidateAPIKey_trimsWhitespaceAndForwards(t *testing.T) {
	fake := &fakeAPIKeyManager{}
	_, err := ops.InvalidateAPIKey(context.Background(), fake, ops.InvalidateAPIKeyOptions{
		IDs: []string{"  abc  ", "", "def"},
	})
	if err != nil {
		t.Fatalf("InvalidateAPIKey: %v", err)
	}
	if len(fake.invalidReq.IDs) != 2 {
		t.Fatalf("forwarded ids = %v, want 2", fake.invalidReq.IDs)
	}
	if fake.invalidReq.IDs[0] != "abc" || fake.invalidReq.IDs[1] != "def" {
		t.Errorf("forwarded ids = %v, want [abc def]", fake.invalidReq.IDs)
	}
}

func TestInvalidateAPIKey_refusesPastDefaultBlastRadius(t *testing.T) {
	ids := make([]string, ops.DefaultMaxInvalidateKeys+1)
	for i := range ids {
		ids[i] = "id" + string(rune('a'+i%26))
	}
	fake := &fakeAPIKeyManager{}
	_, err := ops.InvalidateAPIKey(context.Background(), fake, ops.InvalidateAPIKeyOptions{IDs: ids})
	if !errors.Is(err, client.ErrBlastRadius) {
		t.Errorf("err = %v, want ErrBlastRadius", err)
	}
	if fake.invalidCalls != 0 {
		t.Errorf("adapter must not be called on blast-radius refusal, got %d calls", fake.invalidCalls)
	}
	if !strings.Contains(err.Error(), "--max-keys") {
		t.Errorf("err message should mention --max-keys; got %q", err.Error())
	}
}

func TestInvalidateAPIKey_dryRunSkipsClusterCall(t *testing.T) {
	fake := &fakeAPIKeyManager{}
	got, err := ops.InvalidateAPIKey(context.Background(), fake, ops.InvalidateAPIKeyOptions{
		IDs:    []string{"abc", "def"},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("InvalidateAPIKey: %v", err)
	}
	if fake.invalidCalls != 0 {
		t.Errorf("dry-run must not call the adapter, got %d calls", fake.invalidCalls)
	}
	if !got.DryRun {
		t.Errorf("returned DryRun=false on dry-run path")
	}
	if len(got.Requested) != 2 || got.Requested[0] != "abc" || got.Requested[1] != "def" {
		t.Errorf("Requested = %v, want [abc def]", got.Requested)
	}
	if len(got.Invalidated) != 0 || len(got.PreviouslyInvalidated) != 0 {
		t.Errorf("dry-run leaked non-zero invalidated/previously slices: %+v", got)
	}
}

func TestInvalidateAPIKey_dedupsAndPropagatesRequested(t *testing.T) {
	fake := &fakeAPIKeyManager{
		invalidated: types.APIKeyInvalidated{Invalidated: []string{"abc"}},
	}
	got, err := ops.InvalidateAPIKey(context.Background(), fake, ops.InvalidateAPIKeyOptions{
		IDs: []string{"abc", " abc ", "abc", "def"},
	})
	if err != nil {
		t.Fatalf("InvalidateAPIKey: %v", err)
	}
	// Dedup happens before the adapter is called — the cluster should
	// see two ids, not four.
	if len(fake.invalidReq.IDs) != 2 {
		t.Errorf("adapter saw %d ids, want 2 (post-dedup): %v", len(fake.invalidReq.IDs), fake.invalidReq.IDs)
	}
	// Requested round-trips back on the result so the CLI's audit log
	// records the resolved set.
	if len(got.Requested) != 2 || got.Requested[0] != "abc" || got.Requested[1] != "def" {
		t.Errorf("Requested = %v, want [abc def]", got.Requested)
	}
}

func TestInvalidateAPIKey_explicitMaxKeysOverridesDefault(t *testing.T) {
	ids := make([]string, ops.DefaultMaxInvalidateKeys+5)
	for i := range ids {
		ids[i] = "id" + string(rune('a'+i%26))
	}
	fake := &fakeAPIKeyManager{}
	_, err := ops.InvalidateAPIKey(context.Background(), fake, ops.InvalidateAPIKeyOptions{
		IDs:     ids,
		MaxKeys: 100,
	})
	if err != nil {
		t.Fatalf("InvalidateAPIKey with explicit cap: %v", err)
	}
	if fake.invalidCalls != 1 {
		t.Errorf("expected 1 adapter call past explicit cap, got %d", fake.invalidCalls)
	}
}

func TestSummariseCreated_omitsSecret(t *testing.T) {
	exp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	created := types.APIKeyCreated{
		ID:         "id1",
		Name:       "svc-foo",
		Owner:      "kibana_user",
		Key:        "raw-secret-must-not-leak",
		Encoded:    "base64-secret-must-not-leak",
		Expiration: &exp,
	}
	summary := ops.SummariseCreated(created)
	if summary.ID != "id1" || summary.Name != "svc-foo" {
		t.Errorf("summary id/name = %q/%q, want id1/svc-foo", summary.ID, summary.Name)
	}
	if summary.Owner != "kibana_user" {
		t.Errorf("summary owner = %q, want kibana_user", summary.Owner)
	}
	if summary.Expiration != "2026-01-02T03:04:05Z" {
		t.Errorf("summary expiration = %q, want RFC3339 UTC", summary.Expiration)
	}
	// The summary type has no fields for the secret; encode it and
	// double-check the raw key never appears in the JSON.
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	if strings.Contains(string(raw), "raw-secret-must-not-leak") {
		t.Errorf("summary JSON leaked the api key: %s", raw)
	}
	if strings.Contains(string(raw), "base64-secret-must-not-leak") {
		t.Errorf("summary JSON leaked the encoded key: %s", raw)
	}
}

func TestSummariseCreated_dryRunFlagPropagates(t *testing.T) {
	summary := ops.SummariseCreated(types.APIKeyCreated{
		Name:   "svc-foo",
		DryRun: true,
	})
	if !summary.DryRun {
		t.Errorf("DryRun=false on summary of a dry-run create")
	}
	if summary.ID != "" {
		t.Errorf("dry-run summary should not have an ID; got %q", summary.ID)
	}
}
