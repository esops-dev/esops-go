package elasticsearch

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/esops-dev/esops-go/pkg/types"
)

// rawJSON is a small helper for assembling pointer-to-RawMessage
// values inline; the test data exercises the wire shape the cluster
// emits where order/enabled arrive as either JSON literals or
// quoted strings.
func rawJSON(s string) *json.RawMessage {
	r := json.RawMessage(s)
	return &r
}

func TestMergeUsageFacts_seedsFromXpackUsage(t *testing.T) {
	in := esXpackUsageWire{}
	in.Security.Realms = map[string]esRealmTypeUsage{
		"file": {
			Name:  []string{"file"},
			Order: []int{0},
		},
		"native": {
			Name:  []string{"native1"},
			Order: []int{1},
		},
		"ldap": {
			// Configured-but-empty realm type — ES reports the schema
			// even when no realms of that type exist. Should be skipped
			// rather than seeded as an empty-named realm.
			Name:  []string{},
			Order: []int{},
		},
	}

	got := map[realmKey]realmFacts{}
	mergeUsageFacts(got, in)

	if len(got) != 2 {
		t.Fatalf("expected 2 seeded realms (file, native1), got %d (%+v)", len(got), got)
	}
	if f, ok := got[realmKey{"file", "file"}]; !ok || f.order == nil || *f.order != 0 {
		t.Errorf("file realm not seeded with order=0: %+v", f)
	}
	if f, ok := got[realmKey{"native", "native1"}]; !ok || f.order == nil || *f.order != 1 {
		t.Errorf("native1 realm not seeded with order=1: %+v", f)
	}
}

func TestMergeSettingsFacts_overlaysOnUsage(t *testing.T) {
	// Seed from usage: file realm with order=0, no enabled override.
	facts := map[realmKey]realmFacts{}
	zero := 0
	facts[realmKey{"file", "file"}] = realmFacts{order: &zero}

	in := esRealmsSettingsWire{}
	in.Persistent.Xpack.Security.Authc.Realms = map[string]map[string]esRealmEntry{
		"file": {"file": esRealmEntry{Enabled: rawJSON("false")}},
	}
	mergeSettingsFacts(facts, in)

	f := facts[realmKey{"file", "file"}]
	if f.enabled == nil || *f.enabled {
		t.Errorf("expected enabled=false from settings overlay, got %+v", f)
	}
	if f.order == nil || *f.order != 0 {
		t.Errorf("expected usage's order=0 to survive overlay, got %+v", f.order)
	}
}

func TestMergeSettingsFacts_transientOverridesPersistent(t *testing.T) {
	facts := map[realmKey]realmFacts{}
	in := esRealmsSettingsWire{}
	in.Persistent.Xpack.Security.Authc.Realms = map[string]map[string]esRealmEntry{
		"ldap": {"corp_ldap": esRealmEntry{Order: rawJSON("10"), Enabled: rawJSON("true")}},
	}
	in.Transient.Xpack.Security.Authc.Realms = map[string]map[string]esRealmEntry{
		"ldap": {"corp_ldap": esRealmEntry{Order: rawJSON("99")}},
	}
	mergeSettingsFacts(facts, in)

	f := facts[realmKey{"ldap", "corp_ldap"}]
	if f.order == nil || *f.order != 99 {
		t.Errorf("expected transient's order=99 to win, got %+v", f.order)
	}
	if f.enabled == nil || !*f.enabled {
		t.Errorf("expected persistent's enabled=true to survive, got %+v", f.enabled)
	}
}

func TestMergeSettingsFacts_quotedNumberAndBoolDecoded(t *testing.T) {
	facts := map[realmKey]realmFacts{}
	in := esRealmsSettingsWire{}
	in.Defaults.Xpack.Security.Authc.Realms = map[string]map[string]esRealmEntry{
		"saml": {"corp_saml": esRealmEntry{Order: rawJSON(`"5"`), Enabled: rawJSON(`"true"`)}},
	}
	mergeSettingsFacts(facts, in)

	f := facts[realmKey{"saml", "corp_saml"}]
	if f.order == nil || *f.order != 5 {
		t.Errorf("expected order=5 from quoted string, got %+v", f.order)
	}
	if f.enabled == nil || !*f.enabled {
		t.Errorf("expected enabled=true from quoted string, got %+v", f.enabled)
	}
}

func TestSortRealms_byOrderThenTypeThenName(t *testing.T) {
	in := []types.Realm{
		{Name: "b_ldap", Type: "ldap", Order: 10},
		{Name: "native", Type: "native", Order: 1},
		{Name: "a_saml", Type: "saml", Order: 10},
	}
	sortRealms(in)
	want := []string{"native", "b_ldap", "a_saml"}
	got := []string{in[0].Name, in[1].Name, in[2].Name}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sort order = %v, want %v", got, want)
	}
}

func TestDecodeRealmInt_acceptsNumberAndQuotedString(t *testing.T) {
	if _, ok := decodeRealmInt(json.RawMessage("")); ok {
		t.Errorf("expected !ok on empty input")
	}
	if v, ok := decodeRealmInt(json.RawMessage("7")); !ok || v != 7 {
		t.Errorf("got %d/%v, want 7/true", v, ok)
	}
	if v, ok := decodeRealmInt(json.RawMessage(`"42"`)); !ok || v != 42 {
		t.Errorf("got %d/%v, want 42/true", v, ok)
	}
	if _, ok := decodeRealmInt(json.RawMessage(`"not-a-number"`)); ok {
		t.Errorf("expected !ok on garbage")
	}
}

func TestDecodeRealmBool_acceptsBoolAndQuotedString(t *testing.T) {
	if _, ok := decodeRealmBool(json.RawMessage("")); ok {
		t.Errorf("expected !ok on empty input")
	}
	if v, ok := decodeRealmBool(json.RawMessage("true")); !ok || !v {
		t.Errorf("got %v/%v, want true/true", v, ok)
	}
	if v, ok := decodeRealmBool(json.RawMessage(`"false"`)); !ok || v {
		t.Errorf("got %v/%v, want false/true", v, ok)
	}
	if _, ok := decodeRealmBool(json.RawMessage(`"maybe"`)); ok {
		t.Errorf("expected !ok on non-bool string")
	}
}

func TestESRealmIsDeprecated_emptyVersionFallsThrough(t *testing.T) {
	if esRealmIsDeprecated("native", "") {
		t.Errorf("expected false on empty version (do-not-flag fallthrough)")
	}
}

func TestESRealmIsDeprecated_supportedTypesReturnFalse(t *testing.T) {
	for _, rt := range []string{"native", "file", "ldap", "active_directory", "pki", "saml", "oidc", "kerberos", "jwt", "reserved"} {
		if esRealmIsDeprecated(rt, "9.0.0") {
			t.Errorf("type %q reported deprecated on 9.0.0; expected false", rt)
		}
	}
}

func TestMajorVersion(t *testing.T) {
	cases := []struct {
		in     string
		want   int
		wantOK bool
	}{
		{"", 0, false},
		{"8.13.0", 8, true},
		{"2.11.0", 2, true},
		{"9", 9, true},
		{"not-a-version", 0, false},
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
