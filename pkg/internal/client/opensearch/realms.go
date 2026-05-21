package opensearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// RealmsInspector wires the OS side of the deprecated-realms rule. The
// security plugin models authentication as "authc domains" rather than
// the typed realm chain ES uses; each domain pairs an
// http_authenticator (the inbound surface — basic, openid, saml, …)
// with an authentication_backend (the identity store — internal, ldap,
// noop, …) plus an order/http_enabled flag.
//
// The capability normalises a domain into a single Realm: Type carries
// the http_authenticator type when one is configured (the surface a
// rule actually wants to flag), with the authentication_backend type
// appended ("openid/internal") when both are present. Order is the
// domain's order; Enabled is its http_enabled flag (the security
// plugin's term for "consult this domain"). Anything richer (LDAP
// hosts, SAML metadata URLs, OIDC client IDs) is not surfaced.
//
// A 404 from the security config endpoint is the "security plugin not
// installed" case — surfaced as ErrUnsupported so the rule's
// translation to ErrProbeNotApplicable keeps working unchanged. A 500
// the plugin returns when called as a non-admin (the security plugin
// gates this endpoint behind admin certs / restapi role) is forwarded
// verbatim so the operator sees the cluster's own reason.

const osRealmsConfigPath = "/_plugins/_security/api/securityconfig"

// osSecurityConfigWire mirrors the relevant slice of
// /_plugins/_security/api/securityconfig. The endpoint returns the
// whole security configuration; we only decode dynamic.authc and
// drop the rest. The plugin has shipped two response shapes:
//
//   - Newer plugins wrap the config under a top-level "config" key
//     (with "dynamic" beneath it).
//   - Older plugins return "dynamic" at the top level.
//
// Both forms are accepted; the resolver picks the first shape that
// supplies a non-nil Authc map.
type osSecurityConfigWire struct {
	Config *struct {
		Dynamic *osSecurityDynamicWire `json:"dynamic"`
	} `json:"config"`
	Dynamic *osSecurityDynamicWire `json:"dynamic"`
}

type osSecurityDynamicWire struct {
	Authc map[string]osAuthcDomainWire `json:"authc"`
}

// osAuthcDomainWire mirrors one authc domain. http_enabled controls
// whether the domain is consulted for HTTP requests; transport_enabled
// controls the transport layer. We surface http_enabled as the
// "Enabled" of the realm — RealmsInspector is shaped after the typed
// realm chain on ES, where "enabled" means "consult this for incoming
// authentication".
//
// The Order/HTTPEnabled fields are pointer-typed because the plugin
// reports them as either booleans/numbers or quoted strings depending
// on the plugin version, and a missing key is distinct from
// "explicitly false / zero".
type osAuthcDomainWire struct {
	Order              *json.RawMessage     `json:"order"`
	HTTPEnabled        *json.RawMessage     `json:"http_enabled"`
	TransportEnabled   *json.RawMessage     `json:"transport_enabled"`
	HTTPAuthenticator  osAuthnComponentWire `json:"http_authenticator"`
	AuthenticationBack osAuthnComponentWire `json:"authentication_backend"`
	Description        string               `json:"description"`
}

// osAuthnComponentWire is the {type, ...} envelope shared by
// http_authenticator and authentication_backend. We only surface the
// type — the per-component config is intentionally not exposed.
type osAuthnComponentWire struct {
	Type      string `json:"type"`
	Challenge bool   `json:"challenge"`
}

// Realms implements client.RealmsInspector.
func (c *Client) Realms(ctx context.Context) ([]types.Realm, error) {
	body, status, err := c.doSecurityGET(ctx, osRealmsConfigPath)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusUnauthorized:
		return nil, esopsclient.ErrAuth
	case http.StatusForbidden:
		return nil, esopsclient.ErrForbidden
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w: /_plugins/_security/api/securityconfig not exposed by this cluster build", esopsclient.ErrUnsupported)
	}
	if status >= 400 {
		return nil, clientutil.ClassifyStatus("realms", status, body)
	}

	var wire osSecurityConfigWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, errors.Join(errors.New("decoding realms response"), err)
	}
	return projectOSRealms(wire, c.version), nil
}

// projectOSRealms folds the wrapped/unwrapped config envelopes into a
// flat realm list. A domain with no http_authenticator AND no
// authentication_backend type is dropped — it's a malformed config
// row, not a realm we can usefully report.
func projectOSRealms(w osSecurityConfigWire, clusterVersion string) []types.Realm {
	var dyn *osSecurityDynamicWire
	switch {
	case w.Config != nil && w.Config.Dynamic != nil:
		dyn = w.Config.Dynamic
	default:
		dyn = w.Dynamic
	}
	if dyn == nil || len(dyn.Authc) == 0 {
		return []types.Realm{}
	}

	out := make([]types.Realm, 0, len(dyn.Authc))
	for name, dom := range dyn.Authc {
		realmType := composeOSRealmType(dom.HTTPAuthenticator.Type, dom.AuthenticationBack.Type)
		if realmType == "" {
			continue
		}
		out = append(out, types.Realm{
			Name:       name,
			Type:       realmType,
			Order:      decodeOSRealmOrder(dom.Order),
			Enabled:    decodeOSRealmEnabled(dom.HTTPEnabled),
			Deprecated: osRealmIsDeprecated(dom.HTTPAuthenticator.Type, dom.AuthenticationBack.Type, clusterVersion),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// composeOSRealmType combines the http_authenticator and
// authentication_backend types into the realm-shaped Type string.
// Always lowercased so consumer matches stay stable across plugin
// versions that occasionally upper-case enum values.
//
//   - both present: "http/backend" (e.g. "openid/internal")
//   - only http:    "http"
//   - only backend: "backend"
//   - neither:      ""
func composeOSRealmType(httpAuth, backend string) string {
	httpAuth = strings.ToLower(strings.TrimSpace(httpAuth))
	backend = strings.ToLower(strings.TrimSpace(backend))
	switch {
	case httpAuth != "" && backend != "":
		return httpAuth + "/" + backend
	case httpAuth != "":
		return httpAuth
	case backend != "":
		return backend
	default:
		return ""
	}
}

// decodeOSRealmOrder reads the order field which the plugin reports as
// a JSON number on most versions and a quoted integer on a few older
// builds. Falls through to 0 — the plugin's own default — on a missing
// or unparseable value.
func decodeOSRealmOrder(raw *json.RawMessage) int {
	if raw == nil || len(*raw) == 0 {
		return 0
	}
	var n int
	if err := json.Unmarshal(*raw, &n); err == nil {
		return n
	}
	var s string
	if err := json.Unmarshal(*raw, &s); err == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return v
		}
	}
	return 0
}

// decodeOSRealmEnabled reads http_enabled. Defaults to true when the
// key is absent — the plugin's behaviour is to treat an authc domain
// as on for HTTP unless explicitly disabled, and the rule needs to see
// the same view the cluster acts on.
func decodeOSRealmEnabled(raw *json.RawMessage) bool {
	if raw == nil || len(*raw) == 0 {
		return true
	}
	var b bool
	if err := json.Unmarshal(*raw, &b); err == nil {
		return b
	}
	var s string
	if err := json.Unmarshal(*raw, &s); err == nil {
		return parseBoolish(s, true)
	}
	return true
}

// osRealmIsDeprecated answers "is this realm type deprecated for the
// cluster on the wire?". An empty/unparseable version falls through to
// false — the function relies on the cluster's reported version, not a
// static catalog, so a do-not-flag default beats guessing.
//
// No OS realm types are currently flagged: the built-in
// http_authenticator set (basic, openid, saml, jwt, kerberos,
// clientcert) and authentication_backend set (internal, ldap,
// kerberos, noop) remain supported across OS 1.x/2.x/3.x. When a
// future major retires one, a single case lands in
// osRealmDeprecatedInMajor and the function picks it up automatically.
func osRealmIsDeprecated(httpAuth, backend, clusterVersion string) bool {
	major, ok := majorVersion(clusterVersion)
	if !ok {
		return false
	}
	return osRealmDeprecatedInMajor(strings.ToLower(httpAuth), strings.ToLower(backend), major)
}

// osRealmDeprecatedInMajor is the version-aware switch. Split out from
// osRealmIsDeprecated so that future deprecations land as a one-line
// case without the surrounding parse-and-fallthrough plumbing.
func osRealmDeprecatedInMajor(httpAuth, backend string, major int) bool {
	switch httpAuth {
	// case "old_http_authenticator":
	//     return major >= 4
	}
	switch backend {
	// case "old_backend":
	//     return major >= 4
	}
	return false
}

// majorVersion returns the leading integer of a semantic version
// string. Mirrors the ES helper — duplicated rather than shared because
// the two adapter packages are peer-internal with no shared dependency
// they can cross.
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
