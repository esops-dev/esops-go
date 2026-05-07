package elasticsearch

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// The APIKey/ServiceToken decoders cover three wire shapes:
//
//   - /_security/api_key returns epoch-millis ints with last_authenticated
//     and expiration as optional fields.
//   - /_security/service returns "<ns>/<svc>" map keys we split before
//     fanning out per credential.
//   - /_security/service/.../credential returns per-token metadata where
//     creation arrives as either an epoch-millis int or an RFC3339 string
//     depending on whether the token was just-created or carried over via
//     a security index reindex.
//
// The tests exercise the helpers directly rather than spinning a httptest
// server, matching the pattern the rest of this package follows.

func TestEpochMillisTime_zeroIsZeroTime(t *testing.T) {
	if got := epochMillisTime(0); !got.IsZero() {
		t.Errorf("epochMillisTime(0) = %v, want zero time", got)
	}
	if got := epochMillisTime(-1); !got.IsZero() {
		t.Errorf("epochMillisTime(-1) = %v, want zero time", got)
	}
}

func TestEpochMillisTime_positiveDecodes(t *testing.T) {
	got := epochMillisTime(1_700_000_000_000)
	want := time.UnixMilli(1_700_000_000_000).UTC()
	if !got.Equal(want) {
		t.Errorf("epochMillisTime = %v, want %v", got, want)
	}
}

func TestEpochMillisTimePtr_absentSurvivesAsNil(t *testing.T) {
	if got := epochMillisTimePtr(nil); got != nil {
		t.Errorf("epochMillisTimePtr(nil) = %v, want nil", got)
	}
	zero := int64(0)
	if got := epochMillisTimePtr(&zero); got != nil {
		t.Errorf("epochMillisTimePtr(&0) = %v, want nil (zero treated as absent)", got)
	}
}

func TestEpochMillisTimePtr_presentDecodes(t *testing.T) {
	v := int64(1_700_000_000_000)
	got := epochMillisTimePtr(&v)
	if got == nil || !got.Equal(time.UnixMilli(v).UTC()) {
		t.Errorf("epochMillisTimePtr = %v, want %v", got, time.UnixMilli(v).UTC())
	}
}

func TestSplitServiceAccountKey_validShape(t *testing.T) {
	ns, svc, ok := splitServiceAccountKey("elastic/fleet-server")
	if !ok || ns != "elastic" || svc != "fleet-server" {
		t.Errorf("split = (%q, %q, %v), want (elastic, fleet-server, true)", ns, svc, ok)
	}
}

func TestSplitServiceAccountKey_rejectsMalformed(t *testing.T) {
	cases := []string{"", "no-slash", "/leading", "trailing/", "/", "//"}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if _, _, ok := splitServiceAccountKey(in); ok {
				t.Errorf("split(%q) ok=true, want false", in)
			}
		})
	}
}

func TestDecodeServiceTokenCreation_acceptsEpochMillisAndRFC3339(t *testing.T) {
	want := time.UnixMilli(1_700_000_000_000).UTC()
	cases := []struct {
		name string
		raw  string
	}{
		{"epoch-millis", `1700000000000`},
		{"rfc3339", `"` + want.Format(time.RFC3339) + `"`},
		{"rfc3339nano", `"` + want.Format(time.RFC3339Nano) + `"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rm := json.RawMessage(tc.raw)
			got := decodeServiceTokenCreation(&rm)
			if !got.Equal(want) {
				t.Errorf("decode(%s) = %v, want %v", tc.name, got, want)
			}
		})
	}
}

func TestDecodeServiceTokenCreation_absentOrGarbageFallsThroughToZero(t *testing.T) {
	if got := decodeServiceTokenCreation(nil); !got.IsZero() {
		t.Errorf("decode(nil) = %v, want zero time", got)
	}
	empty := json.RawMessage{}
	if got := decodeServiceTokenCreation(&empty); !got.IsZero() {
		t.Errorf("decode(empty) = %v, want zero time", got)
	}
	garbage := json.RawMessage(`"not-a-timestamp"`)
	if got := decodeServiceTokenCreation(&garbage); !got.IsZero() {
		t.Errorf("decode(garbage) = %v, want zero time", got)
	}
}

func TestAPIKeyWire_unmarshalsExpectedShape(t *testing.T) {
	// Sample shape derived from /_security/api_key documentation: a key
	// with no expiration and no last_authenticated must round-trip to
	// nil pointers.
	raw := `{
		"api_keys":[
			{
				"id":"abc",
				"name":"k1",
				"username":"u1",
				"realm":"native1",
				"creation":1700000000000,
				"invalidated":false,
				"role_templates":[]
			},
			{
				"id":"def",
				"name":"k2",
				"username":"u2",
				"realm":"native1",
				"creation":1700000001000,
				"expiration":1900000000000,
				"last_authenticated":1700000005000,
				"invalidated":true
			}
		]
	}`

	var wrap struct {
		APIKeys []esAPIKeyWire `json:"api_keys"`
	}
	if err := json.Unmarshal([]byte(raw), &wrap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(wrap.APIKeys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(wrap.APIKeys))
	}

	k1 := wrap.APIKeys[0]
	if k1.ExpirationMS != nil {
		t.Errorf("k1 expected nil ExpirationMS, got %v", *k1.ExpirationMS)
	}
	if k1.LastAuthMS != nil {
		t.Errorf("k1 expected nil LastAuthMS, got %v", *k1.LastAuthMS)
	}
	if k1.CreationMS != 1_700_000_000_000 {
		t.Errorf("k1 CreationMS = %d", k1.CreationMS)
	}

	k2 := wrap.APIKeys[1]
	if k2.ExpirationMS == nil || *k2.ExpirationMS != 1_900_000_000_000 {
		t.Errorf("k2 ExpirationMS = %v", k2.ExpirationMS)
	}
	if k2.LastAuthMS == nil || *k2.LastAuthMS != 1_700_000_005_000 {
		t.Errorf("k2 LastAuthMS = %v", k2.LastAuthMS)
	}
	if !k2.Invalidated {
		t.Errorf("k2 Invalidated should be true")
	}
}

func TestServiceCredentialsWire_unmarshalsBothTokenSources(t *testing.T) {
	raw := `{
		"service_account":"elastic/fleet-server",
		"count":3,
		"tokens":{
			"token-a":{"creation":1700000000000},
			"token-b":{"creation":"2024-01-15T12:34:56.789Z"}
		},
		"nodes_credentials":{
			"file_tokens":{
				"file-token-1":{"nodes":["node-1"]}
			}
		}
	}`

	var wire esServiceCredentialsWire
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := wire.ServiceAccount; got != "elastic/fleet-server" {
		t.Errorf("ServiceAccount = %q", got)
	}
	if len(wire.Tokens) != 2 {
		t.Errorf("tokens len = %d, want 2", len(wire.Tokens))
	}
	if len(wire.NodesCreds.FileTokens) != 1 {
		t.Errorf("file_tokens len = %d, want 1", len(wire.NodesCreds.FileTokens))
	}

	// "token-a" was an epoch-millis number; "token-b" an RFC3339 string —
	// both must decode through the projector to the same time.
	wantA := time.UnixMilli(1_700_000_000_000).UTC()
	wantB, _ := time.Parse(time.RFC3339Nano, "2024-01-15T12:34:56.789Z")
	wantB = wantB.UTC()

	gotA := decodeServiceTokenCreation(wire.Tokens["token-a"].Creation)
	if !gotA.Equal(wantA) {
		t.Errorf("token-a creation = %v, want %v", gotA, wantA)
	}
	gotB := decodeServiceTokenCreation(wire.Tokens["token-b"].Creation)
	if !gotB.Equal(wantB) {
		t.Errorf("token-b creation = %v, want %v", gotB, wantB)
	}
}

func TestServiceAccountListWire_unmarshalsKeyShape(t *testing.T) {
	// /_security/service returns each account keyed as "<ns>/<svc>" with
	// a small per-account block we don't surface — just assert we can
	// pluck the keys and split them.
	raw := `{
		"elastic/fleet-server":{"role_descriptor":{}},
		"elastic/kibana":{"role_descriptor":{}}
	}`
	var accounts esServiceAccountListWire
	if err := json.Unmarshal([]byte(raw), &accounts); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := make([]string, 0, len(accounts))
	for k := range accounts {
		ns, svc, ok := splitServiceAccountKey(k)
		if !ok {
			t.Errorf("split(%q) failed", k)
			continue
		}
		got = append(got, ns+":"+svc)
	}
	want := map[string]bool{"elastic:fleet-server": true, "elastic:kibana": true}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected account %q", g)
		}
	}
	if len(got) != len(want) {
		t.Errorf("accounts = %v, want set %v", got, want)
	}
}

func TestIsSecurityDisabledBody_recognisesNoHandlerFoundOnSecurityPath(t *testing.T) {
	// ES 9.x with xpack.security.enabled=false 400s with this exact
	// body shape on /_security/api_key and /_security/service. The
	// API-key/service-token capabilities depend on the audit pipeline's
	// classifier to fold this into ErrUnsupported rather than a generic
	// 400 — a regression in either side breaks the dialect contract.
	body := []byte(`{"error":"no handler found for uri [/_security/api_key?owner=false] and method [GET]"}`)
	if !isSecurityDisabledBody(body) {
		t.Errorf("expected isSecurityDisabledBody=true for ES 9.x security-disabled body, got false")
	}
}

func TestServiceCredentialsWire_emptySectionsDecode(t *testing.T) {
	// A service with no credentials returns empty maps under both
	// sections — the implementation must accept this without erroring
	// or fabricating entries.
	raw := `{"service_account":"elastic/fleet-server","count":0,"tokens":{},"nodes_credentials":{"file_tokens":{}}}`
	var wire esServiceCredentialsWire
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(wire.Tokens, map[string]esServiceTokenMetaWire{}) {
		t.Errorf("Tokens = %v, want empty map", wire.Tokens)
	}
	if !reflect.DeepEqual(wire.NodesCreds.FileTokens, map[string]json.RawMessage{}) {
		t.Errorf("FileTokens = %v, want empty map", wire.NodesCreds.FileTokens)
	}
}
