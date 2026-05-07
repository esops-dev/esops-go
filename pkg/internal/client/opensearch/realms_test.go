package opensearch

import (
	"encoding/json"
	"reflect"
	"testing"
)

func rawJSON(s string) *json.RawMessage {
	r := json.RawMessage(s)
	return &r
}

func TestProjectOSRealms_emptyConfigProducesEmpty(t *testing.T) {
	got := projectOSRealms(osSecurityConfigWire{}, "2.11.0")
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %+v", got)
	}
}

func TestProjectOSRealms_wrappedConfigPicked(t *testing.T) {
	in := osSecurityConfigWire{}
	in.Config = &struct {
		Dynamic *osSecurityDynamicWire `json:"dynamic"`
	}{
		Dynamic: &osSecurityDynamicWire{
			Authc: map[string]osAuthcDomainWire{
				"basic_internal_auth_domain": {
					Order:              rawJSON("0"),
					HTTPEnabled:        rawJSON("true"),
					HTTPAuthenticator:  osAuthnComponentWire{Type: "basic"},
					AuthenticationBack: osAuthnComponentWire{Type: "internal"},
				},
			},
		},
	}
	got := projectOSRealms(in, "2.11.0")
	if len(got) != 1 {
		t.Fatalf("expected 1 realm, got %d", len(got))
	}
	if got[0].Type != "basic/internal" {
		t.Errorf("Type = %q, want basic/internal", got[0].Type)
	}
	if got[0].Order != 0 || !got[0].Enabled {
		t.Errorf("Order/Enabled = %d/%v, want 0/true", got[0].Order, got[0].Enabled)
	}
}

func TestProjectOSRealms_unwrappedDynamicAccepted(t *testing.T) {
	in := osSecurityConfigWire{
		Dynamic: &osSecurityDynamicWire{
			Authc: map[string]osAuthcDomainWire{
				"openid_auth_domain": {
					Order:              rawJSON("1"),
					HTTPEnabled:        rawJSON("true"),
					HTTPAuthenticator:  osAuthnComponentWire{Type: "openid"},
					AuthenticationBack: osAuthnComponentWire{Type: "noop"},
				},
			},
		},
	}
	got := projectOSRealms(in, "2.11.0")
	if len(got) != 1 {
		t.Fatalf("expected 1 realm, got %d", len(got))
	}
	if got[0].Type != "openid/noop" {
		t.Errorf("Type = %q, want openid/noop", got[0].Type)
	}
}

func TestProjectOSRealms_dropsDomainsWithoutAnyType(t *testing.T) {
	in := osSecurityConfigWire{
		Dynamic: &osSecurityDynamicWire{
			Authc: map[string]osAuthcDomainWire{
				"good": {
					Order:             rawJSON("0"),
					HTTPAuthenticator: osAuthnComponentWire{Type: "basic"},
				},
				"malformed": {
					Order: rawJSON("99"),
				},
			},
		},
	}
	got := projectOSRealms(in, "2.11.0")
	if len(got) != 1 {
		t.Fatalf("expected only the well-formed realm, got %d (%+v)", len(got), got)
	}
	if got[0].Name != "good" {
		t.Errorf("kept realm = %q, want good", got[0].Name)
	}
}

func TestProjectOSRealms_sortByOrderThenTypeThenName(t *testing.T) {
	in := osSecurityConfigWire{
		Dynamic: &osSecurityDynamicWire{
			Authc: map[string]osAuthcDomainWire{
				"saml_b": {
					Order:             rawJSON("10"),
					HTTPAuthenticator: osAuthnComponentWire{Type: "saml"},
				},
				"openid_a": {
					Order:             rawJSON("10"),
					HTTPAuthenticator: osAuthnComponentWire{Type: "openid"},
				},
				"basic": {
					Order:             rawJSON("1"),
					HTTPAuthenticator: osAuthnComponentWire{Type: "basic"},
				},
			},
		},
	}
	got := projectOSRealms(in, "2.11.0")
	wantNames := []string{"basic", "openid_a", "saml_b"}
	gotNames := []string{got[0].Name, got[1].Name, got[2].Name}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Errorf("sort order = %v, want %v", gotNames, wantNames)
	}
}

func TestComposeOSRealmType_allShapes(t *testing.T) {
	cases := []struct {
		http, backend, want string
	}{
		{"basic", "internal", "basic/internal"},
		{"OPENID", "Internal", "openid/internal"}, // lowercased
		{"basic", "", "basic"},
		{"", "noop", "noop"},
		{"", "", ""},
		{"  saml  ", " internal ", "saml/internal"}, // trimmed
	}
	for _, tc := range cases {
		t.Run(tc.http+"+"+tc.backend, func(t *testing.T) {
			if got := composeOSRealmType(tc.http, tc.backend); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecodeOSRealmEnabled_defaultsToTrueWhenAbsent(t *testing.T) {
	if !decodeOSRealmEnabled(nil) {
		t.Errorf("nil → false; expected true (key absent → on)")
	}
	if decodeOSRealmEnabled(rawJSON("false")) {
		t.Errorf("false → true; expected false")
	}
	if !decodeOSRealmEnabled(rawJSON(`"true"`)) {
		t.Errorf("quoted true → false; expected true")
	}
}

func TestDecodeOSRealmOrder_acceptsNumberAndQuotedString(t *testing.T) {
	if got := decodeOSRealmOrder(nil); got != 0 {
		t.Errorf("nil → %d; want 0", got)
	}
	if got := decodeOSRealmOrder(rawJSON("7")); got != 7 {
		t.Errorf("7 → %d; want 7", got)
	}
	if got := decodeOSRealmOrder(rawJSON(`"42"`)); got != 42 {
		t.Errorf("\"42\" → %d; want 42", got)
	}
	if got := decodeOSRealmOrder(rawJSON(`"not-a-number"`)); got != 0 {
		t.Errorf("garbage → %d; want 0 fallthrough", got)
	}
}

func TestOSRealmIsDeprecated_supportedTypesReturnFalse(t *testing.T) {
	cases := []struct{ http, backend string }{
		{"basic", "internal"},
		{"openid", "noop"},
		{"saml", "noop"},
		{"jwt", "noop"},
		{"clientcert", "internal"},
		{"kerberos", "internal"},
	}
	for _, tc := range cases {
		if osRealmIsDeprecated(tc.http, tc.backend, "2.11.0") {
			t.Errorf("%s/%s reported deprecated on 2.11.0; want false", tc.http, tc.backend)
		}
	}
}

func TestOSRealmIsDeprecated_emptyVersionFallsThrough(t *testing.T) {
	if osRealmIsDeprecated("openid", "internal", "") {
		t.Errorf("expected false on empty version (do-not-flag fallthrough)")
	}
}

func TestMajorVersion(t *testing.T) {
	cases := []struct {
		in     string
		want   int
		wantOK bool
	}{
		{"", 0, false},
		{"2.11.0", 2, true},
		{"3.0.0", 3, true},
		{"x.y.z", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := majorVersion(tc.in)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}
