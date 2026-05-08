package cluster_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/cluster"
	"github.com/esops-dev/esops-go/pkg/config"
)

// TestNodeSettings_dialectContract proves both adapters fold an
// identical /_nodes/settings?flat_settings=true wire shape into the
// same []NodeSettings slice. The keys used by doctor's
// network_host_wildcard, node_store_allow_mmap, and
// suspect_discovery_settings rules must arrive on the per-node
// Settings map exactly as the cluster reported them — a silent
// rewrite or filter would mute the rules without breaking the build.
func TestNodeSettings_dialectContract(t *testing.T) {
	const nodes = `{
		"n1": {"name":"node-a","settings":{
			"network.host":"0.0.0.0",
			"node.store.allow_mmap":"false",
			"discovery.seed_hosts":["host1:9300","host2:9300"],
			"discovery.type":"single-node"
		}},
		"n2": {"name":"node-b","settings":{
			"network.host":"10.0.0.2",
			"node.store.allow_mmap":"true"
		}}
	}`

	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			var c *client.Client
			if dialect == dialectES {
				c = newESClient(t, nodes)
			} else {
				c = newOSClient(t, nodes)
			}
			got, err := c.NodeSettings.NodeSettings(context.Background())
			if err != nil {
				t.Fatalf("NodeSettings: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("len(got) = %d, want 2", len(got))
			}
			// Sort guarantee: alphabetical by node name.
			if got[0].Name != "node-a" || got[1].Name != "node-b" {
				t.Errorf("unsorted: got[0]=%q got[1]=%q", got[0].Name, got[1].Name)
			}
			// Per-node settings flow through unchanged — keys arrive
			// flat (dotted-path) and lists keep their array shape.
			if got[0].Settings["network.host"] != "0.0.0.0" {
				t.Errorf("node-a network.host = %v, want 0.0.0.0", got[0].Settings["network.host"])
			}
			if got[0].Settings["node.store.allow_mmap"] != "false" {
				t.Errorf("node-a node.store.allow_mmap = %v, want \"false\"", got[0].Settings["node.store.allow_mmap"])
			}
			if got[0].Settings["discovery.type"] != "single-node" {
				t.Errorf("node-a discovery.type = %v, want single-node", got[0].Settings["discovery.type"])
			}
			seeds, ok := got[0].Settings["discovery.seed_hosts"].([]any)
			if !ok {
				t.Fatalf("node-a discovery.seed_hosts not []any: %T", got[0].Settings["discovery.seed_hosts"])
			}
			wantSeeds := []any{"host1:9300", "host2:9300"}
			if !reflect.DeepEqual(seeds, wantSeeds) {
				t.Errorf("node-a discovery.seed_hosts = %#v, want %#v", seeds, wantSeeds)
			}
			if got[1].Settings["node.store.allow_mmap"] != "true" {
				t.Errorf("node-b node.store.allow_mmap = %v, want \"true\"", got[1].Settings["node.store.allow_mmap"])
			}
		})
	}
}

// TestNodeSettings_cachedAcrossInspectors proves the shared
// /_nodes/settings fetch is not duplicated when a caller invokes
// TransportTLS, HTTPTLS, and NodeSettings on the same client — the
// load-bearing payoff of lifting the cache from TLS-only to a
// general nodes-settings cache.
func TestNodeSettings_cachedAcrossInspectors(t *testing.T) {
	t.Run("elasticsearch", func(t *testing.T) {
		hits := 0
		s := newDialectServer(t, dialectES, `{
			"n1":{"name":"es01","settings":{
				"xpack.security.http.ssl.enabled":"true",
				"network.host":"0.0.0.0"
			}}
		}`, &hits)
		cc := config.Context{URL: s.URL}
		c, err := cluster.New(context.Background(), cc)
		if err != nil {
			t.Fatalf("cluster.New: %v", err)
		}
		_, _ = c.TransportTLS.TransportTLS(context.Background())
		_, _ = c.HTTPTLS.HTTPTLS(context.Background())
		_, _ = c.NodeSettings.NodeSettings(context.Background())
		if hits != 1 {
			t.Errorf("expected 1 /_nodes/settings hit across TransportTLS+HTTPTLS+NodeSettings, got %d", hits)
		}
	})
	t.Run("opensearch", func(t *testing.T) {
		hits := 0
		s := newDialectServer(t, dialectOS, `{
			"n1":{"name":"os01","settings":{
				"plugins.security.ssl.http.enabled":"true",
				"plugins.security.ssl.transport.pemcert_filepath":"/x",
				"network.host":"0.0.0.0"
			}}
		}`, &hits)
		cc := config.Context{URL: s.URL}
		c, err := cluster.New(context.Background(), cc)
		if err != nil {
			t.Fatalf("cluster.New: %v", err)
		}
		_, _ = c.TransportTLS.TransportTLS(context.Background())
		_, _ = c.HTTPTLS.HTTPTLS(context.Background())
		_, _ = c.NodeSettings.NodeSettings(context.Background())
		if hits != 1 {
			t.Errorf("expected 1 /_nodes/settings hit across TransportTLS+HTTPTLS+NodeSettings, got %d", hits)
		}
	})
}
