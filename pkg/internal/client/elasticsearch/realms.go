package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// RealmsInspector wires the ES side of the deprecated-realms rule.
// Two endpoints feed it because either one alone misses cases:
//
//   - GET /_xpack/usage?filter_path=security.realms reports realms the
//     cluster has actually loaded — including ones declared only in
//     elasticsearch.yml, which are the common case in production. This
//     is the source of truth for "what realms exist".
//   - GET /_cluster/settings?include_defaults=true&filter_path=*.xpack.security.authc.realms.*
//     reports per-realm dynamic overrides (order, enabled). Settings
//     alone misses yaml-only realms; usage alone misses runtime
//     overrides — the two together model what the cluster actually
//     evaluates.
//
// Each realm is keyed in the cluster's settings tree as
// xpack.security.authc.realms.<type>.<name>; usage groups names by
// type with a parallel `name`/`order` array. We surface the
// type/name/order/enabled tuple plus a version-aware Deprecated flag
// — anything further (LDAP URLs, SAML metadata, OIDC client IDs) is
// realm-level configuration the capability deliberately does not
// expose.

// esXpackUsageWire is the slice of /_xpack/usage we read for the realm
// list. The endpoint groups configured realms by type in parallel
// arrays: name[i] is the configured realm name, order[i] its order.
// Available is the license-level signal (true when the cluster's
// license allows realms of this type); we expose it indirectly through
// the Deprecated flag rather than as its own field, since the suggested
// Realm shape from the spec doesn't include it.
type esXpackUsageWire struct {
	Security struct {
		Realms map[string]esRealmTypeUsage `json:"realms"`
	} `json:"security"`
}

type esRealmTypeUsage struct {
	Available *bool    `json:"available"`
	Enabled   *bool    `json:"enabled"`
	Name      []string `json:"name"`
	Order     []int    `json:"order"`
}

// esRealmsSettingsWire mirrors the filter_path-narrowed
// /_cluster/settings response. Each scope is decoded independently so
// transient → persistent → defaults precedence can be resolved without
// losing source-of-truth info. Defaults is requested via
// include_defaults=true so the cluster's reserved realm chain
// (file/native) shows up even on a stock cluster.
type esRealmsSettingsWire struct {
	Persistent esRealmsScope `json:"persistent"`
	Transient  esRealmsScope `json:"transient"`
	Defaults   esRealmsScope `json:"defaults"`
}

type esRealmsScope struct {
	Xpack struct {
		Security struct {
			Authc struct {
				// Keyed first by realm type ("native", "ldap", …) then
				// by realm name. The inner value is the per-realm
				// settings block — only order/enabled are surfaced;
				// the rest is decoded into a passthrough map only so
				// json.Unmarshal does not error on unknown keys.
				Realms map[string]map[string]esRealmEntry `json:"realms"`
			} `json:"authc"`
		} `json:"security"`
	} `json:"xpack"`
}

// esRealmEntry holds the order/enabled fields. ES reports both as
// either booleans/numbers or quoted strings depending on the version
// and which scope the value came from (transient and defaults
// frequently quote, persistent does not). Pointer-typed so a missing
// key (nothing set in this scope) is distinct from "explicitly false /
// zero".
type esRealmEntry struct {
	Order   *json.RawMessage `json:"order"`
	Enabled *json.RawMessage `json:"enabled"`
}

// realmKey is the (type, name) pair we merge across the two endpoints.
// Internal to this file — neither end of the pipeline cares about the
// composite, only the merged result.
type realmKey struct {
	rtype string
	name  string
}

// realmFacts is the merged view per realm. Order/Enabled are pointer-
// typed so "the cluster did not report this field" is distinct from
// the zero value, which lets the merge prefer the more authoritative
// source over a default fill-in.
type realmFacts struct {
	order   *int
	enabled *bool
}

// Realms implements client.RealmsInspector. The cluster is the source
// of truth: when no realms are reported anywhere we return an empty
// slice, the same shape rules see when security is genuinely off.
func (c *Client) Realms(ctx context.Context) ([]types.Realm, error) {
	usageBody, usageStatus, err := c.doRawGET(ctx, "/_xpack/usage?filter_path=security.realms")
	if err != nil {
		return nil, err
	}
	switch usageStatus {
	case http.StatusUnauthorized:
		return nil, esopsclient.ErrAuth
	case http.StatusForbidden:
		return nil, esopsclient.ErrForbidden
	}
	// 404 on /_xpack/usage is the basic-license / older-build case;
	// fall through to the cluster_settings reader, which still works.
	usageOK := usageStatus == http.StatusOK
	if usageStatus >= 400 && usageStatus != http.StatusNotFound {
		return nil, clientutil.ClassifyStatus("realms usage", usageStatus, usageBody)
	}

	settingsBody, settingsStatus, err := c.doRawGET(ctx, "/_cluster/settings?include_defaults=true&filter_path=*.xpack.security.authc.realms.*")
	if err != nil {
		return nil, err
	}
	switch settingsStatus {
	case http.StatusUnauthorized:
		return nil, esopsclient.ErrAuth
	case http.StatusForbidden:
		return nil, esopsclient.ErrForbidden
	}
	if settingsStatus >= 400 {
		return nil, clientutil.ClassifyStatus("realms settings", settingsStatus, settingsBody)
	}

	facts := map[realmKey]realmFacts{}
	if usageOK {
		var usage esXpackUsageWire
		if err := json.Unmarshal(usageBody, &usage); err != nil {
			return nil, errors.Join(errors.New("decoding realms usage response"), err)
		}
		mergeUsageFacts(facts, usage)
	}

	var settings esRealmsSettingsWire
	if err := json.Unmarshal(settingsBody, &settings); err != nil {
		return nil, errors.Join(errors.New("decoding realms settings response"), err)
	}
	mergeSettingsFacts(facts, settings)

	out := make([]types.Realm, 0, len(facts))
	for k, f := range facts {
		out = append(out, types.Realm{
			Name:       k.name,
			Type:       k.rtype,
			Order:      derefInt(f.order, 0),
			Enabled:    derefBool(f.enabled, true),
			Deprecated: esRealmIsDeprecated(k.rtype, c.version),
		})
	}
	sortRealms(out)
	return out, nil
}

// mergeUsageFacts seeds the facts map from /_xpack/usage. Names with
// no configured realms (the cluster reports an empty `name` array for
// realm types that exist in the schema but aren't in use) are skipped
// — there's nothing operator-relevant to report. usage's order array
// runs parallel to its name array; any divergence is treated as "no
// order reported" rather than a hard error since the cluster's own
// schema guarantees the parallel shape.
func mergeUsageFacts(into map[realmKey]realmFacts, w esXpackUsageWire) {
	for rtype, info := range w.Security.Realms {
		for i, name := range info.Name {
			k := realmKey{rtype: rtype, name: name}
			f := into[k]
			if f.order == nil && i < len(info.Order) {
				ord := info.Order[i]
				f.order = &ord
			}
			into[k] = f
		}
	}
}

// mergeSettingsFacts overlays /_cluster/settings on top of the usage
// facts. Settings carry the per-realm enabled override and may report
// order at the per-realm scope (which usage already aggregated); we
// prefer the settings-side values because /_cluster/settings is what
// an operator just wrote when they ran `esops ops settings set`.
//
// Precedence inside settings is the cluster's own: transient overrides
// persistent overrides defaults. Each field of each realm picks the
// first scope that supplies a non-nil value.
func mergeSettingsFacts(into map[realmKey]realmFacts, w esRealmsSettingsWire) {
	apply := func(scope esRealmsScope) {
		for rtype, byName := range scope.Xpack.Security.Authc.Realms {
			for name, entry := range byName {
				k := realmKey{rtype: rtype, name: name}
				f := into[k]
				if f.order == nil && entry.Order != nil {
					if v, ok := decodeRealmInt(*entry.Order); ok {
						f.order = &v
					}
				}
				if f.enabled == nil && entry.Enabled != nil {
					if v, ok := decodeRealmBool(*entry.Enabled); ok {
						f.enabled = &v
					}
				}
				into[k] = f
			}
		}
	}
	apply(w.Transient)
	apply(w.Persistent)
	apply(w.Defaults)
}

// decodeRealmInt accepts either a JSON number or a quoted integer
// string — both shapes appear in /_cluster/settings depending on
// the scope and ES version. Returns ok=false on a missing/garbage
// value so the caller can fall through.
func decodeRealmInt(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return v, true
		}
	}
	return 0, false
}

// decodeRealmBool accepts either a JSON bool or a quoted "true"/"false"
// — the cluster reports the same key both ways depending on the
// settings scope. Returns ok=false on a value that isn't recognisably a
// bool so the caller falls through to the realm default.
func decodeRealmBool(raw json.RawMessage) (bool, bool) {
	if len(raw) == 0 {
		return false, false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.ToLower(strings.TrimSpace(s))
		switch s {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

func derefInt(p *int, d int) int {
	if p == nil {
		return d
	}
	return *p
}

func derefBool(p *bool, d bool) bool {
	if p == nil {
		return d
	}
	return *p
}

// sortRealms orders by Order, then Type, then Name — stable across
// runs so a captured slice diffs cleanly.
func sortRealms(rs []types.Realm) {
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Order != rs[j].Order {
			return rs[i].Order < rs[j].Order
		}
		if rs[i].Type != rs[j].Type {
			return rs[i].Type < rs[j].Type
		}
		return rs[i].Name < rs[j].Name
	})
}

// esRealmIsDeprecated answers "is this realm type deprecated for the
// cluster on the wire?". An empty/unparseable version falls through to
// false — the function relies on the cluster's reported version, not a
// static catalog, so a do-not-flag default beats guessing.
//
// No ES realm types are currently flagged: the built-in chain
// (file, native, ldap, active_directory, pki, kerberos, saml, oidc,
// jwt, reserved) remains supported across 7.x/8.x/9.x in a way the
// operator can act on. When ES 10 or a future major retires one, a
// single case lands here and the function picks it up automatically.
func esRealmIsDeprecated(realmType, clusterVersion string) bool {
	major, ok := majorVersion(clusterVersion)
	if !ok {
		return false
	}
	return esRealmDeprecatedInMajor(strings.ToLower(realmType), major)
}

// esRealmDeprecatedInMajor is the version-aware switch. Split out from
// esRealmIsDeprecated so that future deprecations land as a one-line
// case without the surrounding parse-and-fallthrough plumbing.
func esRealmDeprecatedInMajor(realmType string, major int) bool {
	switch realmType {
	// case "old_realm_type":
	//     return major >= 10
	}
	return false
}

// majorVersion returns the leading integer of a semantic version string
// ("8.13.0" → 8). The probe surfaces versions in the cluster's reported
// shape (always major.minor.patch on supported ES and OS builds), so a
// plain dotted-prefix split is enough — we deliberately do not pull in
// a semver library for one parse.
func majorVersion(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if i := strings.IndexByte(v, '.'); i >= 0 {
		v = v[:i]
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}
