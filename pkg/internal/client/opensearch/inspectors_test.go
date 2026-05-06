package opensearch

import (
	"testing"
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
