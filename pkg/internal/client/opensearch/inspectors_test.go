package opensearch

import (
	"reflect"
	"testing"

	"github.com/esops-dev/esops-go/pkg/types"
)

// osTransportTLSEnabled scans for any `plugins.security.ssl.transport.*`
// or `opendistro_security.ssl.transport.*` keypath. The keystore /
// pemcert / pemkey trio is the strongest signal — operators who set
// those have transport TLS on by construction. A missing-or-empty
// trio is "the operator didn't configure transport TLS", which is
// the exact case the rule has to flag.
func TestOSTransportTLSEnabled(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want bool
	}{
		{name: "nil map", in: nil, want: false},
		{name: "no signals", in: map[string]any{"x": "y"}, want: false},
		{
			name: "plugins keystore set",
			in:   map[string]any{"plugins.security.ssl.transport.keystore_filepath": "/etc/ks.jks"},
			want: true,
		},
		{
			name: "plugins pemcert set",
			in:   map[string]any{"plugins.security.ssl.transport.pemcert_filepath": "/etc/cert.pem"},
			want: true,
		},
		{
			name: "legacy opendistro keystore",
			in:   map[string]any{"opendistro_security.ssl.transport.keystore_filepath": "/etc/ks.jks"},
			want: true,
		},
		{
			name: "empty string is no signal",
			in:   map[string]any{"plugins.security.ssl.transport.keystore_filepath": ""},
			want: false,
		},
		{
			name: "non-string value ignored",
			in:   map[string]any{"plugins.security.ssl.transport.keystore_filepath": true},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := osTransportTLSEnabled(tc.in); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// osHostnameVerification defaults to true (the plugin's own default)
// when the key is absent. Only an explicit false disables it; a
// non-bool string is ignored so a malformed setting falls through
// to the default rather than silently flipping the rule's result.
func TestOSHostnameVerification(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want bool
	}{
		{name: "nil map → default true", in: nil, want: true},
		{name: "absent key → default true", in: map[string]any{}, want: true},
		{
			name: "plugins explicit true",
			in:   map[string]any{"plugins.security.ssl.transport.enforce_hostname_verification": true},
			want: true,
		},
		{
			name: "plugins explicit false",
			in:   map[string]any{"plugins.security.ssl.transport.enforce_hostname_verification": false},
			want: false,
		},
		{
			name: "plugins string \"false\"",
			in:   map[string]any{"plugins.security.ssl.transport.enforce_hostname_verification": "false"},
			want: false,
		},
		{
			name: "legacy opendistro false",
			in:   map[string]any{"opendistro_security.ssl.transport.enforce_hostname_verification": false},
			want: false,
		},
		{
			name: "garbage string falls through to default",
			in:   map[string]any{"plugins.security.ssl.transport.enforce_hostname_verification": "maybe"},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := osHostnameVerification(tc.in); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// osHTTPTLSEnabled honours `plugins.security.ssl.http.enabled` (and
// the legacy alias). The plugin's default is false — the operator
// must turn HTTP TLS on explicitly even when transport TLS is
// configured — so a missing key means an unauthenticated 9200
// listener, exactly the case the rule has to flag.
func TestOSHTTPTLSEnabled(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want bool
	}{
		{name: "nil map", in: nil, want: false},
		{name: "absent key → default off", in: map[string]any{}, want: false},
		{
			name: "plugins explicit true",
			in:   map[string]any{"plugins.security.ssl.http.enabled": true},
			want: true,
		},
		{
			name: "plugins string true",
			in:   map[string]any{"plugins.security.ssl.http.enabled": "true"},
			want: true,
		},
		{
			name: "plugins explicit false",
			in:   map[string]any{"plugins.security.ssl.http.enabled": false},
			want: false,
		},
		{
			name: "legacy opendistro true",
			in:   map[string]any{"opendistro_security.ssl.http.enabled": true},
			want: true,
		},
		{
			name: "garbage string falls through",
			in:   map[string]any{"plugins.security.ssl.http.enabled": "maybe"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := osHTTPTLSEnabled(tc.in); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// osHTTPClientAuth normalises the OS plugin's vocabulary
// ("REQUIRE" / "OPTIONAL" / "NONE") to the shared "required" /
// "optional" / "none" set so a CEL rule comparing ES and OS sides
// sees one alphabet. Empty when unset; passes unknowns through
// (lower-cased) so a plugin update that adds a fourth mode doesn't
// silently get coerced to one of the three known values.
func TestOSHTTPClientAuth(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want string
	}{
		{name: "nil map", in: nil, want: ""},
		{name: "absent", in: map[string]any{}, want: ""},
		{
			name: "REQUIRE → required",
			in:   map[string]any{"plugins.security.ssl.http.clientauth_mode": "REQUIRE"},
			want: "required",
		},
		{
			name: "OPTIONAL → optional",
			in:   map[string]any{"plugins.security.ssl.http.clientauth_mode": "OPTIONAL"},
			want: "optional",
		},
		{
			name: "NONE → none",
			in:   map[string]any{"plugins.security.ssl.http.clientauth_mode": "NONE"},
			want: "none",
		},
		{
			name: "legacy opendistro",
			in:   map[string]any{"opendistro_security.ssl.http.clientauth_mode": "REQUIRE"},
			want: "required",
		},
		{
			name: "unknown value passes through lower-cased",
			in:   map[string]any{"plugins.security.ssl.http.clientauth_mode": "FUTURE"},
			want: "future",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := osHTTPClientAuth(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// osReadSliceSetting checks the same flat-settings list rules as the
// ES helper: list values arrive as []any under the dotted-path key,
// non-string members must be skipped, and the result is sorted so
// per-node equality compares cleanly. The OS variant accepts a
// candidate list (modern + legacy alias) so the rule covers both
// pre- and post-rebrand clusters in one call.
func TestOSReadSliceSetting(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		keys []string
		want []string
	}{
		{name: "nil map", in: nil, keys: []string{"x"}, want: nil},
		{name: "no key matches", in: map[string]any{"y": []any{"a"}}, keys: []string{"x", "z"}, want: nil},
		{
			name: "first key wins",
			in:   map[string]any{"plugins.security.ssl.http.enabled_protocols": []any{"TLSv1.3", "TLSv1.2"}},
			keys: []string{"plugins.security.ssl.http.enabled_protocols", "opendistro_security.ssl.http.enabled_protocols"},
			want: []string{"TLSv1.2", "TLSv1.3"},
		},
		{
			name: "falls through to legacy alias",
			in:   map[string]any{"opendistro_security.ssl.http.enabled_protocols": []any{"TLSv1.2"}},
			keys: []string{"plugins.security.ssl.http.enabled_protocols", "opendistro_security.ssl.http.enabled_protocols"},
			want: []string{"TLSv1.2"},
		},
		{
			name: "non-string members skipped",
			in:   map[string]any{"x": []any{"TLSv1.3", 42, "TLSv1.2"}},
			keys: []string{"x"},
			want: []string{"TLSv1.2", "TLSv1.3"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := osReadSliceSetting(tc.in, tc.keys...)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

// aggregateOSHTTPTLS mirrors the ES aggregator; the duplicate exists
// to avoid an adapter-to-adapter import. Same load-bearing rules:
// all-or-nothing on Enabled, mixed-on-disagreement for ClientAuth,
// union for Protocols/CipherSuites, PerNode populated only when the
// fleet is not unanimous.
func TestAggregateOSHTTPTLS(t *testing.T) {
	t.Run("homogeneous fleet has no per-node block", func(t *testing.T) {
		per := []types.NodeTLSPosture{
			{Name: "os01", Enabled: true, ClientAuth: "required", Protocols: []string{"TLSv1.3"}},
			{Name: "os02", Enabled: true, ClientAuth: "required", Protocols: []string{"TLSv1.3"}},
		}
		got := aggregateOSHTTPTLS(per)
		if !got.Enabled || got.ClientAuth != "required" || got.PerNode != nil {
			t.Errorf("homogeneous fleet aggregated wrong: %#v", got)
		}
	})

	t.Run("disabled node flips Enabled", func(t *testing.T) {
		per := []types.NodeTLSPosture{
			{Name: "os01", Enabled: true, ClientAuth: "none"},
			{Name: "os02", Enabled: false, ClientAuth: "none"},
		}
		got := aggregateOSHTTPTLS(per)
		if got.Enabled {
			t.Errorf("Enabled = true; one off node must flip the aggregate")
		}
		if got.PerNode == nil {
			t.Errorf("PerNode must surface when nodes disagree on Enabled")
		}
	})

	t.Run("client_auth disagreement becomes mixed", func(t *testing.T) {
		per := []types.NodeTLSPosture{
			{Name: "os01", Enabled: true, ClientAuth: "required"},
			{Name: "os02", Enabled: true, ClientAuth: "optional"},
		}
		got := aggregateOSHTTPTLS(per)
		if got.ClientAuth != "mixed" {
			t.Errorf("ClientAuth = %q, want mixed", got.ClientAuth)
		}
	})

	t.Run("protocols are the sorted union", func(t *testing.T) {
		per := []types.NodeTLSPosture{
			{Name: "os01", Enabled: true, ClientAuth: "none", Protocols: []string{"TLSv1.2"}},
			{Name: "os02", Enabled: true, ClientAuth: "none", Protocols: []string{"TLSv1.3"}},
		}
		got := aggregateOSHTTPTLS(per)
		if !reflect.DeepEqual(got.Protocols, []string{"TLSv1.2", "TLSv1.3"}) {
			t.Errorf("Protocols = %#v, want union", got.Protocols)
		}
	})

	t.Run("empty input yields zero value", func(t *testing.T) {
		got := aggregateOSHTTPTLS(nil)
		if !reflect.DeepEqual(got, types.HTTPTLSPosture{}) {
			t.Errorf("empty input: got %#v, want zero value", got)
		}
	})
}
