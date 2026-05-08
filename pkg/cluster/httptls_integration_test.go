//go:build integration

package cluster_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/esops-dev/esops-go/pkg/cluster"
	"github.com/esops-dev/esops-go/pkg/config"
	"github.com/esops-dev/esops-go/pkg/types"
)

// TestHTTPTLS_integration_devContexts exercises both TransportTLSInspector
// and HTTPTLSInspector against the local dev compose stack
// (`make dev-up`). Three clusters are expected to be reachable:
// local-es (plaintext), local-os (plaintext, security plugin), and
// local-es-mtls (HTTPS + mTLS).
//
// The integration tier is what catches wire-format regressions a
// contract test against an httptest.Server cannot — a real cluster
// build that adds, renames, or restructures keys under
// /_nodes/settings is the regression we want to fail loudly here.
//
// The test skips with a clear "make dev-up" hint when a cluster is
// unreachable rather than failing, so a developer running the
// integration suite without the compose stack gets pointed at the
// fix rather than a transport-error stack trace.
func TestHTTPTLS_integration_devContexts(t *testing.T) {
	repoRoot := repoRootFromCaller(t)
	t.Chdir(repoRoot) // certs in esops.example.yaml are repo-relative

	// ESOPS_ALLOW_INSECURE_CONFIG turns off the loader's
	// world-readable-config-with-inline-secrets guard. The example
	// file uses env-indirected secrets, not inline plaintext, but on
	// a dev workstation the file is often mode 0644 and the loader's
	// guard would refuse to start. Same intent for ALLOW_PLAINTEXT —
	// belt-and-braces in case the loader's check tightens.
	t.Setenv("ESOPS_ALLOW_INSECURE_CONFIG", "1")
	t.Setenv("ESOPS_ALLOW_PLAINTEXT_SECRETS", "1")
	if os.Getenv("ESOPS_LOCAL_OS_PASSWORD") == "" {
		t.Setenv("ESOPS_LOCAL_OS_PASSWORD", "EsopsDev123!")
	}

	cfg, err := config.Load(filepath.Join(repoRoot, "config", "esops.example.yaml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	cases := []struct {
		name             string
		wantHTTPTLS      *bool // nil = don't assert
		wantTransportTLS *bool
	}{
		// Plaintext HTTP, security disabled — both layers must be
		// reported as off; the rule that flags an unauthenticated
		// 9200 listener fires here.
		{name: "local-es", wantHTTPTLS: ptr(false), wantTransportTLS: ptr(false)},
		// OpenSearch single-node with the security plugin in
		// development demo mode — the plugin's HTTP listener is the
		// container's plaintext entry; the inspector must report it
		// as off so a dev cluster doesn't masquerade as compliant.
		{name: "local-os", wantHTTPTLS: ptr(false)},
		// HTTPS + mTLS — the load-bearing positive case. If the
		// cluster reports anything other than enabled here, the
		// inspector has a wire-format regression.
		{name: "local-es-mtls", wantHTTPTLS: ptr(true)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, cc, err := cfg.ResolveContext(tc.name)
			if err != nil {
				t.Fatalf("resolve %q: %v", tc.name, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			c, err := cluster.New(ctx, cc)
			if err != nil {
				t.Skipf("cluster %q unreachable (run `make dev-up`): %v", tc.name, err)
			}

			ttls, err := c.TransportTLS.TransportTLS(ctx)
			if err != nil {
				t.Fatalf("TransportTLS: %v", err)
			}
			htls, err := c.HTTPTLS.HTTPTLS(ctx)
			if err != nil {
				t.Fatalf("HTTPTLS: %v", err)
			}
			t.Logf("dialect=%s TransportTLS=%+v HTTPTLS=%+v", c.Info.Dialect, ttls, htls)

			if tc.wantHTTPTLS != nil && htls.Enabled != *tc.wantHTTPTLS {
				t.Errorf("HTTPTLS.Enabled = %v, want %v", htls.Enabled, *tc.wantHTTPTLS)
			}
			if tc.wantTransportTLS != nil && ttls.Enabled != *tc.wantTransportTLS {
				t.Errorf("TransportTLS.Enabled = %v, want %v", ttls.Enabled, *tc.wantTransportTLS)
			}
			// Sanity: a non-empty fleet must materialise at least one
			// node in PerNode when the aggregate flips from a
			// homogeneous-uniform shape. We don't assert it strictly
			// (single-node dev clusters are uniform by construction),
			// but PerNode being nil for a single-node fleet is the
			// expected case and worth a t.Logf for visibility.
			if len(htls.PerNode) > 0 {
				t.Logf("PerNode breakdown: %+v", htls.PerNode)
			}

			// Ensure the type assertion still works for the
			// HTTPTLSPosture surface as a whole — guards against a
			// future field rename in pkg/types breaking downstream
			// consumers without a compile error.
			var _ types.HTTPTLSPosture = htls
		})
	}
}

func ptr[T any](v T) *T { return &v }

// repoRootFromCaller resolves the repo root by walking upward from
// this file's on-disk location until a go.mod is found. Avoids
// hardcoding the path so the test stays portable across checkout
// locations.
func repoRootFromCaller(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root (no go.mod found)")
		}
		dir = parent
	}
}
