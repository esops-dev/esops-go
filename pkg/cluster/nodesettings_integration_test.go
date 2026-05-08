//go:build integration

package cluster_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/esops-dev/esops-go/pkg/cluster"
	"github.com/esops-dev/esops-go/pkg/config"
)

// TestNodeSettings_integration_devContexts exercises NodeSettingsInspector
// and the JVM-ergonomics extension on NodeBootstrapInspector against
// the local dev compose stack. Same pattern as
// TestHTTPTLS_integration_devContexts: skip with a "make dev-up" hint
// when a cluster is unreachable.
//
// The integration tier catches wire-format regressions a contract
// test against an httptest.Server cannot — a real cluster build that
// rearranges keys under /_nodes/settings or /_nodes/jvm fails loudly
// here rather than silently muting a downstream rule.
func TestNodeSettings_integration_devContexts(t *testing.T) {
	repoRoot := repoRootFromCaller(t)
	t.Chdir(repoRoot)

	t.Setenv("ESOPS_ALLOW_INSECURE_CONFIG", "1")
	t.Setenv("ESOPS_ALLOW_PLAINTEXT_SECRETS", "1")
	if os.Getenv("ESOPS_LOCAL_OS_PASSWORD") == "" {
		t.Setenv("ESOPS_LOCAL_OS_PASSWORD", "EsopsDev123!")
	}

	cfg, err := config.Load(filepath.Join(repoRoot, "config", "esops.example.yaml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	for _, name := range []string{"local-es", "local-os", "local-es-mtls"} {
		t.Run(name, func(t *testing.T) {
			_, cc, err := cfg.ResolveContext(name)
			if err != nil {
				t.Fatalf("resolve %q: %v", name, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			c, err := cluster.New(ctx, cc)
			if err != nil {
				t.Skipf("cluster %q unreachable (run `make dev-up`): %v", name, err)
			}

			ns, err := c.NodeSettings.NodeSettings(ctx)
			if err != nil {
				t.Fatalf("NodeSettings: %v", err)
			}
			if len(ns) == 0 {
				t.Fatal("NodeSettings returned empty slice; expected at least one node")
			}
			for _, n := range ns {
				if n.Name == "" {
					t.Errorf("node has empty Name: %+v", n)
				}
				if n.Settings == nil {
					t.Errorf("node %q has nil Settings; flat-settings GET should always populate the map", n.Name)
				}
			}
			t.Logf("dialect=%s nodes=%d node-0=%s settings-keys=%d",
				c.Info.Dialect, len(ns), ns[0].Name, len(ns[0].Settings))

			nb, err := c.NodeBootstrap.NodeBootstrap(ctx)
			if err != nil {
				t.Fatalf("NodeBootstrap: %v", err)
			}
			if len(nb) == 0 {
				t.Fatal("NodeBootstrap returned empty slice; expected at least one node")
			}
			for _, n := range nb {
				if len(n.GCCollectors) == 0 {
					t.Errorf("node %q reported zero GCCollectors; the cluster should always emit jvm.gc_collectors[]", n.Name)
				}
				if len(n.JVMInputArguments) == 0 {
					t.Errorf("node %q reported zero JVMInputArguments; the cluster should always emit jvm.input_arguments[]", n.Name)
				}
			}
			t.Logf("dialect=%s GCCollectors=%v jvm-args=%d",
				c.Info.Dialect, nb[0].GCCollectors, len(nb[0].JVMInputArguments))
		})
	}
}
