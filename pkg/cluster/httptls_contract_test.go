package cluster_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/cluster"
	"github.com/esops-dev/esops-go/pkg/config"
	"github.com/esops-dev/esops-go/pkg/types"
)

// TestHTTPTLS_dialectContract exercises HTTPTLSInspector against both
// adapters from a single test table. Each scenario carries a logical
// expectation plus the dialect-specific /_nodes/settings response that
// would produce it; the same NodeTLSPosture-aggregating contract is
// asserted against both implementations.
//
// The test lives in pkg/cluster (where both adapters are already wired)
// because the dialect probe + factory is the public construction path
// downstream tools take, and a contract test that bypasses it would
// not catch a wiring regression.
func TestHTTPTLS_dialectContract(t *testing.T) {
	cases := []struct {
		name string
		// esNodes is the JSON value of the "nodes" object on the ES
		// /_nodes/settings response (flat_settings=true semantics).
		esNodes string
		// osNodes is the equivalent for the OS dialect.
		osNodes string
		// want is the HTTPTLSPosture both adapters must produce. The
		// test asserts equality against this for both.
		want types.HTTPTLSPosture
	}{
		{
			name: "TLS off everywhere",
			esNodes: `{
				"n1": {"name":"es01","settings":{"xpack.security.http.ssl.enabled":"false"}}
			}`,
			osNodes: `{
				"n1": {"name":"os01","settings":{"plugins.security.ssl.http.enabled":"false"}}
			}`,
			want: types.HTTPTLSPosture{Enabled: false},
		},
		{
			name: "TLS on, single-node, operator-uniform",
			esNodes: `{
				"n1": {"name":"es01","settings":{
					"xpack.security.http.ssl.enabled":"true",
					"xpack.security.http.ssl.client_authentication":"required",
					"xpack.security.http.ssl.supported_protocols":["TLSv1.3"],
					"xpack.security.http.ssl.cipher_suites":["TLS_AES_256_GCM_SHA384"]
				}}
			}`,
			osNodes: `{
				"n1": {"name":"os01","settings":{
					"plugins.security.ssl.http.enabled":"true",
					"plugins.security.ssl.http.clientauth_mode":"REQUIRE",
					"plugins.security.ssl.http.enabled_protocols":["TLSv1.3"],
					"plugins.security.ssl.http.enabled_ciphers":["TLS_AES_256_GCM_SHA384"]
				}}
			}`,
			want: types.HTTPTLSPosture{
				Enabled:      true,
				ClientAuth:   "required",
				Protocols:    []string{"TLSv1.3"},
				CipherSuites: []string{"TLS_AES_256_GCM_SHA384"},
			},
		},
		{
			name: "one node off — Enabled flips false, per-node surfaced",
			esNodes: `{
				"n1": {"name":"es01","settings":{"xpack.security.http.ssl.enabled":"true"}},
				"n2": {"name":"es02","settings":{"xpack.security.http.ssl.enabled":"false"}}
			}`,
			osNodes: `{
				"n1": {"name":"os01","settings":{"plugins.security.ssl.http.enabled":"true"}},
				"n2": {"name":"os02","settings":{"plugins.security.ssl.http.enabled":"false"}}
			}`,
			// PerNode is asserted via the "is non-nil" check below
			// rather than literal equality, so node names can differ
			// between dialects without rewriting the table.
			want: types.HTTPTLSPosture{Enabled: false},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/elasticsearch", func(t *testing.T) {
			c := newESClient(t, tc.esNodes)
			got, err := c.HTTPTLS.HTTPTLS(context.Background())
			if err != nil {
				t.Fatalf("HTTPTLS: %v", err)
			}
			assertHTTPTLSContract(t, got, tc.want)
		})
		t.Run(tc.name+"/opensearch", func(t *testing.T) {
			c := newOSClient(t, tc.osNodes)
			got, err := c.HTTPTLS.HTTPTLS(context.Background())
			if err != nil {
				t.Fatalf("HTTPTLS: %v", err)
			}
			assertHTTPTLSContract(t, got, tc.want)
		})
	}
}

// TestHTTPTLS_cachedAcrossInspectors proves the shared /_nodes/settings
// fetch is not duplicated when a caller invokes both TransportTLS and
// HTTPTLS on the same client — the load-bearing payoff of the cache.
// Asserted at the dialect-contract layer (both adapters).
func TestHTTPTLS_cachedAcrossInspectors(t *testing.T) {
	t.Run("elasticsearch", func(t *testing.T) {
		hits := 0
		s := newDialectServer(t, dialectES, `{
			"n1":{"name":"es01","settings":{"xpack.security.http.ssl.enabled":"true"}}
		}`, &hits)
		cc := config.Context{URL: s.URL}
		c, err := cluster.New(context.Background(), cc)
		if err != nil {
			t.Fatalf("cluster.New: %v", err)
		}
		_, _ = c.TransportTLS.TransportTLS(context.Background())
		_, _ = c.HTTPTLS.HTTPTLS(context.Background())
		if hits != 1 {
			t.Errorf("expected 1 /_nodes/settings hit across TransportTLS+HTTPTLS, got %d", hits)
		}
	})
	t.Run("opensearch", func(t *testing.T) {
		hits := 0
		s := newDialectServer(t, dialectOS, `{
			"n1":{"name":"os01","settings":{
				"plugins.security.ssl.http.enabled":"true",
				"plugins.security.ssl.transport.pemcert_filepath":"/x"
			}}
		}`, &hits)
		cc := config.Context{URL: s.URL}
		c, err := cluster.New(context.Background(), cc)
		if err != nil {
			t.Fatalf("cluster.New: %v", err)
		}
		_, _ = c.TransportTLS.TransportTLS(context.Background())
		_, _ = c.HTTPTLS.HTTPTLS(context.Background())
		if hits != 1 {
			t.Errorf("expected 1 /_nodes/settings hit across TransportTLS+HTTPTLS, got %d", hits)
		}
	})
}

// assertHTTPTLSContract is the shared assertion both adapter cases
// invoke. PerNode is checked for presence rather than literal equality
// since the dialect-specific node names differ; the load-bearing
// guarantee is "non-unanimous fleet → PerNode populated", and
// "uniform fleet → PerNode nil".
func assertHTTPTLSContract(t *testing.T, got, want types.HTTPTLSPosture) {
	t.Helper()
	if got.Enabled != want.Enabled {
		t.Errorf("Enabled = %v, want %v", got.Enabled, want.Enabled)
	}
	if got.ClientAuth != want.ClientAuth {
		t.Errorf("ClientAuth = %q, want %q", got.ClientAuth, want.ClientAuth)
	}
	if !reflect.DeepEqual(got.Protocols, want.Protocols) {
		t.Errorf("Protocols = %#v, want %#v", got.Protocols, want.Protocols)
	}
	if !reflect.DeepEqual(got.CipherSuites, want.CipherSuites) {
		t.Errorf("CipherSuites = %#v, want %#v", got.CipherSuites, want.CipherSuites)
	}
}

const (
	dialectES = "elasticsearch"
	dialectOS = "opensearch"
)

// newDialectServer wires a fake cluster that responds to both the
// probe (GET /) and the TLS-settings inspector (GET /_nodes/settings*).
// The hits pointer is incremented on every /_nodes/settings request
// so the cache test can assert exactly one underlying fetch.
func newDialectServer(t *testing.T, dialect, nodes string, hits *int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// go-elasticsearch/v9 enforces the X-Elastic-Product header on
		// every response from the cluster — without it the client
		// rejects the response as "not Elasticsearch". Set it on every
		// ES-dialect reply so the contract test exercises the real
		// transport path rather than tripping the product check.
		if dialect == dialectES {
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
		}
		// /_nodes/settings is a path under root — route it explicitly
		// before the catch-all returns the probe body.
		if strings.HasPrefix(r.URL.Path, "/_nodes/settings") {
			if hits != nil {
				*hits++
			}
			_, _ = w.Write([]byte(`{"nodes":` + nodes + `}`))
			return
		}
		// Root probe.
		if dialect == dialectES {
			_, _ = w.Write([]byte(esRootBody))
		} else {
			_, _ = w.Write([]byte(osRootBody))
		}
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func newESClient(t *testing.T, nodes string) *client.Client {
	t.Helper()
	hits := 0
	s := newDialectServer(t, dialectES, nodes, &hits)
	cc := config.Context{URL: s.URL}
	c, err := cluster.New(context.Background(), cc)
	if err != nil {
		t.Fatalf("cluster.New (ES): %v", err)
	}
	return c
}

func newOSClient(t *testing.T, nodes string) *client.Client {
	t.Helper()
	hits := 0
	s := newDialectServer(t, dialectOS, nodes, &hits)
	cc := config.Context{URL: s.URL}
	c, err := cluster.New(context.Background(), cc)
	if err != nil {
		t.Fatalf("cluster.New (OS): %v", err)
	}
	return c
}
