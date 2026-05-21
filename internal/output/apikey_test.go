package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/esops-dev/esops-go/pkg/types"
)

func TestAPIKeyList_jsonRoundTrips(t *testing.T) {
	exp := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	keys := []types.APIKey{
		{ID: "abc", Name: "k1", Username: "u1", Realm: "native1", Creation: time.Unix(1_700_000_000, 0).UTC(), Expiration: &exp},
	}
	var buf bytes.Buffer
	if err := APIKeyList(&buf, keys, FormatJSON); err != nil {
		t.Fatalf("APIKeyList: %v", err)
	}
	var got []types.APIKey
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if len(got) != 1 || got[0].ID != "abc" || got[0].Name != "k1" {
		t.Errorf("decoded = %+v, want id=abc name=k1", got)
	}
}

func TestAPIKeyList_tableHasOwnerColumn(t *testing.T) {
	keys := []types.APIKey{
		{ID: "abc", Name: "k1", Username: "kibana_user", Creation: time.Unix(1_700_000_000, 0).UTC()},
	}
	var buf bytes.Buffer
	if err := APIKeyList(&buf, keys, FormatTable); err != nil {
		t.Fatalf("APIKeyList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ID") || !strings.Contains(out, "OWNER") || !strings.Contains(out, "STATUS") {
		t.Errorf("table missing expected headers: %s", out)
	}
	if !strings.Contains(out, "kibana_user") {
		t.Errorf("table missing owner: %s", out)
	}
}

func TestAPIKeyCreated_tablePrintsSecret(t *testing.T) {
	// The table renderer is the only one that prints the raw secret;
	// callers are responsible for routing this output to stdout only.
	// This test pins the contract: the renderer DOES emit the secret
	// (otherwise the operator can't use the key it just created).
	created := types.APIKeyCreated{
		ID:      "abc",
		Name:    "k1",
		Owner:   "kibana_user",
		Key:     "raw-secret-payload",
		Encoded: "base64-payload",
	}
	var buf bytes.Buffer
	if err := APIKeyCreated(&buf, created, FormatTable); err != nil {
		t.Fatalf("APIKeyCreated: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "raw-secret-payload") {
		t.Errorf("table renderer dropped api_key — operator can't use the new key: %s", out)
	}
	if !strings.Contains(out, "base64-payload") {
		t.Errorf("table renderer dropped encoded: %s", out)
	}
	if !strings.Contains(out, "kibana_user") {
		t.Errorf("table renderer dropped owner: %s", out)
	}
}

func TestAPIKeyCreated_dryRunBannerAndNoSecret(t *testing.T) {
	created := types.APIKeyCreated{
		Name:   "k1",
		DryRun: true,
	}
	var buf bytes.Buffer
	if err := APIKeyCreated(&buf, created, FormatTable); err != nil {
		t.Fatalf("APIKeyCreated: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "(dry-run, no cluster call)") {
		t.Errorf("dry-run banner missing: %s", out)
	}
	// On dry-run, both secret fields are empty by construction; the
	// table renderer must render them as "-" (not the empty string,
	// not the word "<nil>", not anything that looks like a key).
	if !strings.Contains(out, "api_key:    -") {
		t.Errorf("dry-run api_key should render as dash: %s", out)
	}
	if !strings.Contains(out, "encoded:    -") {
		t.Errorf("dry-run encoded should render as dash: %s", out)
	}
}

func TestAPIKeyCreated_jsonShapeIsStable(t *testing.T) {
	// The JSON output is the scriptable contract; pin the field names so
	// downstream `jq .api_key key.json` keeps working across refactors.
	created := types.APIKeyCreated{
		ID:      "abc",
		Name:    "k1",
		Key:     "raw",
		Encoded: "enc",
	}
	var buf bytes.Buffer
	if err := APIKeyCreated(&buf, created, FormatJSON); err != nil {
		t.Fatalf("APIKeyCreated: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	for _, k := range []string{"id", "name", "api_key", "encoded"} {
		if _, ok := parsed[k]; !ok {
			t.Errorf("missing field %q in JSON: %s", k, buf.String())
		}
	}
}

func TestAPIKeyInvalidated_tableSummarisesCounts(t *testing.T) {
	result := types.APIKeyInvalidated{
		Requested:             []string{"abc", "def", "old1"},
		Invalidated:           []string{"abc", "def"},
		PreviouslyInvalidated: []string{"old1"},
		ErrorCount:            0,
	}
	var buf bytes.Buffer
	if err := APIKeyInvalidated(&buf, result, FormatTable); err != nil {
		t.Fatalf("APIKeyInvalidated: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "REQUESTED") || !strings.Contains(out, "INVALIDATED") || !strings.Contains(out, "PREVIOUSLY_INVALIDATED") {
		t.Errorf("missing headers: %s", out)
	}
	if !strings.Contains(out, "abc") || !strings.Contains(out, "def") || !strings.Contains(out, "old1") {
		t.Errorf("table missing affected ids: %s", out)
	}
}

func TestAPIKeyInvalidated_dryRunBannerAndWouldInvalidateList(t *testing.T) {
	result := types.APIKeyInvalidated{
		Requested: []string{"abc", "def"},
		DryRun:    true,
	}
	var buf bytes.Buffer
	if err := APIKeyInvalidated(&buf, result, FormatTable); err != nil {
		t.Fatalf("APIKeyInvalidated: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "(dry-run, no cluster call)") {
		t.Errorf("dry-run banner missing: %s", out)
	}
	if !strings.Contains(out, "WOULD INVALIDATE") {
		t.Errorf("dry-run header missing: %s", out)
	}
	if !strings.Contains(out, "abc") || !strings.Contains(out, "def") {
		t.Errorf("dry-run ids missing: %s", out)
	}
	// Dry-run path must not pretend ids were already invalidated.
	if strings.Contains(out, "INVALIDATED IDS") || strings.Contains(out, "PREVIOUSLY INVALIDATED IDS") {
		t.Errorf("dry-run leaked live-call section headers: %s", out)
	}
}

func TestAPIKeyDescribe_tableShowsSingleRow(t *testing.T) {
	key := types.APIKey{
		ID: "abc", Name: "k1", Username: "u1", Realm: "native1",
		Creation: time.Unix(1_700_000_000, 0).UTC(),
	}
	var buf bytes.Buffer
	if err := APIKeyDescribe(&buf, key, FormatTable); err != nil {
		t.Fatalf("APIKeyDescribe: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "abc") || !strings.Contains(out, "k1") {
		t.Errorf("describe missing key fields: %s", out)
	}
}
