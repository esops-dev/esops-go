package elasticsearch_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// remoteTransport replays canned responses for /_remote/info,
// /_ccr/stats, and /_ccr/auto_follow. The probe is served the same
// way the other tests do (the typed ES client issues a product check
// before any real call).
type remoteTransport struct {
	probeServed atomic.Bool

	remoteBody   string
	remoteStatus int

	ccrBody   string
	ccrStatus int

	autoFollowBody   string
	autoFollowStatus int
}

func (t *remoteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.probeServed.Load() && (req.Method == http.MethodGet || req.Method == http.MethodHead) && (req.URL.Path == "/" || req.URL.Path == "") {
		t.probeServed.Store(true)
		return &http.Response{
			StatusCode: 200,
			Header: http.Header{
				"Content-Type":      []string{"application/json"},
				"X-Elastic-Product": []string{"Elasticsearch"},
			},
			Body:    io.NopCloser(strings.NewReader(`{"version":{"number":"9.0.0","build_flavor":"default"},"tagline":"You Know, for Search"}`)),
			Request: req,
		}, nil
	}
	if strings.HasPrefix(req.URL.Path, "/_remote/info") {
		status := t.remoteStatus
		if status == 0 {
			status = 200
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"X-Elastic-Product": []string{"Elasticsearch"}, "Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(t.remoteBody)),
			Request:    req,
		}, nil
	}
	if strings.HasPrefix(req.URL.Path, "/_ccr/stats") {
		status := t.ccrStatus
		if status == 0 {
			status = 200
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"X-Elastic-Product": []string{"Elasticsearch"}, "Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(t.ccrBody)),
			Request:    req,
		}, nil
	}
	if strings.HasPrefix(req.URL.Path, "/_ccr/auto_follow") {
		status := t.autoFollowStatus
		if status == 0 {
			status = 200
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"X-Elastic-Product": []string{"Elasticsearch"}, "Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(t.autoFollowBody)),
			Request:    req,
		}, nil
	}
	return &http.Response{
		StatusCode: 404,
		Header:     http.Header{"X-Elastic-Product": []string{"Elasticsearch"}, "Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"unhandled path"}`)),
		Request:    req,
	}, nil
}

func TestRemoteClusters_emptyObjectIsEmptySlice(t *testing.T) {
	// The cluster reports `{}` when no remotes are configured. Decoding
	// must surface that as an empty slice rather than a nil/panic — the
	// rule consumer counts on len() == 0 for the "no remotes" outcome.
	tr := &remoteTransport{remoteBody: `{}`}
	c := newProbedClient(t, tr, false)

	got, err := c.RemoteClusters(context.Background())
	if err != nil {
		t.Fatalf("RemoteClusters: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("got = %v, want empty slice", got)
	}
}

func TestRemoteClusters_proxyModeUsesSocketCount(t *testing.T) {
	// Proxy-mode remotes report `num_proxy_sockets_connected`, not
	// `num_nodes_connected`. The capability surfaces the proxy socket
	// count through NumNodesConnected so the rule asks one question
	// across modes — "how many cluster-side connections are live".
	tr := &remoteTransport{remoteBody: `{
		"proxy-cluster": {
			"mode":"proxy",
			"connected":true,
			"num_proxy_sockets_connected":7,
			"skip_unavailable":false
		}
	}`}
	c := newProbedClient(t, tr, false)
	got, err := c.RemoteClusters(context.Background())
	if err != nil {
		t.Fatalf("RemoteClusters: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].NumNodesConnected != 7 {
		t.Errorf("NumNodesConnected = %d, want 7 (proxy socket count)", got[0].NumNodesConnected)
	}
	if got[0].Mode != "proxy" {
		t.Errorf("Mode = %q, want proxy", got[0].Mode)
	}
	if len(got[0].SeedHosts) != 0 {
		t.Errorf("SeedHosts = %v, want empty for proxy mode", got[0].SeedHosts)
	}
}

func TestRemoteClusters_sortedByName(t *testing.T) {
	// Map iteration order is non-deterministic in Go; the capability
	// sorts by Name so a captured slice is stable across runs.
	tr := &remoteTransport{remoteBody: `{
		"zeta": {"mode":"sniff","connected":true,"num_nodes_connected":1,"seeds":["z:9300"]},
		"alpha": {"mode":"sniff","connected":true,"num_nodes_connected":1,"seeds":["a:9300"]},
		"middle": {"mode":"sniff","connected":true,"num_nodes_connected":1,"seeds":["m:9300"]}
	}`}
	c := newProbedClient(t, tr, false)
	got, err := c.RemoteClusters(context.Background())
	if err != nil {
		t.Fatalf("RemoteClusters: %v", err)
	}
	names := []string{got[0].Name, got[1].Name, got[2].Name}
	want := []string{"alpha", "middle", "zeta"}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("name[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestRemoteClusters_forbiddenIsErrForbidden(t *testing.T) {
	tr := &remoteTransport{remoteStatus: 403, remoteBody: `{"error":"forbidden"}`}
	c := newProbedClient(t, tr, false)
	_, err := c.RemoteClusters(context.Background())
	if !errors.Is(err, client.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

func TestRemoteClusters_unauthorizedIsErrAuth(t *testing.T) {
	tr := &remoteTransport{remoteStatus: 401, remoteBody: `{"error":"unauthorized"}`}
	c := newProbedClient(t, tr, false)
	_, err := c.RemoteClusters(context.Background())
	if !errors.Is(err, client.ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

func TestFollowerStats_emptyFollowStatsIsEmptySlice(t *testing.T) {
	// A cluster with CCR licensed but no follower indices configured
	// reports `follow_stats.indices = []`. Empty slice is the right
	// outcome — no error.
	tr := &remoteTransport{ccrBody: `{"follow_stats":{"indices":[]}}`}
	c := newProbedClient(t, tr, false)
	got, err := c.FollowerStats(context.Background())
	if err != nil {
		t.Fatalf("FollowerStats: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("got = %v, want empty slice", got)
	}
}

func TestFollowerStats_aggregatesMaxLagAcrossShards(t *testing.T) {
	// The capability summarises per-shard lag into one row per follower
	// index; the maximum across shards is the right summary because one
	// lagging shard makes the whole index lagged.
	tr := &remoteTransport{ccrBody: `{
		"follow_stats": {
			"indices": [
				{"index":"idx-a","shards":[
					{"remote_cluster":"leader","leader_index":"idx-a-leader","follower_index":"idx-a","operations_written_lag":3},
					{"remote_cluster":"leader","leader_index":"idx-a-leader","follower_index":"idx-a","operations_written_lag":42},
					{"remote_cluster":"leader","leader_index":"idx-a-leader","follower_index":"idx-a","operations_written_lag":1}
				]}
			]
		}
	}`}
	c := newProbedClient(t, tr, false)
	got, err := c.FollowerStats(context.Background())
	if err != nil {
		t.Fatalf("FollowerStats: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].FollowerLagOps != 42 {
		t.Errorf("FollowerLagOps = %d, want 42 (max across shards)", got[0].FollowerLagOps)
	}
	if got[0].LeaderCluster != "leader" {
		t.Errorf("LeaderCluster = %q, want leader", got[0].LeaderCluster)
	}
}

func TestFollowerStats_anyShardPausedMarksIndexPaused(t *testing.T) {
	// The cluster has no per-index pause flag; the capability marks an
	// index paused when any of its shards are paused, mirroring the lag
	// aggregation.
	tr := &remoteTransport{ccrBody: `{
		"follow_stats": {
			"indices": [
				{"index":"idx-a","shards":[
					{"remote_cluster":"l","leader_index":"idx-a","follower_index":"idx-a","paused":false},
					{"remote_cluster":"l","leader_index":"idx-a","follower_index":"idx-a","paused":true}
				]}
			]
		}
	}`}
	c := newProbedClient(t, tr, false)
	got, err := c.FollowerStats(context.Background())
	if err != nil {
		t.Fatalf("FollowerStats: %v", err)
	}
	if len(got) != 1 || !got[0].Paused {
		t.Errorf("got = %#v, want exactly one paused index", got)
	}
}

func TestFollowerStats_aggregatesMaxRetentionLeaseAgeAcrossShards(t *testing.T) {
	// One expired-lease shard makes the whole follower index
	// unrecoverable, so the capability surfaces the maximum
	// retention_lease_age_in_millis across shards into the neutral
	// type's RetentionLeaseAgeMillis. The doctor rule reads that field
	// to detect the "lease about to expire" failure mode.
	tr := &remoteTransport{ccrBody: `{
		"follow_stats": {
			"indices": [
				{"index":"follower-orders","shards":[
					{"remote_cluster":"leader-eu","leader_index":"orders","follower_index":"follower-orders","operations_written_lag":0,"paused":false,"retention_lease_age_in_millis":60000},
					{"remote_cluster":"leader-eu","leader_index":"orders","follower_index":"follower-orders","operations_written_lag":0,"paused":false,"retention_lease_age_in_millis":25200000}
				]}
			]
		}
	}`}
	c := newProbedClient(t, tr, false)
	got, err := c.FollowerStats(context.Background())
	if err != nil {
		t.Fatalf("FollowerStats: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].RetentionLeaseAgeMillis != 25200000 {
		t.Errorf("RetentionLeaseAgeMillis = %d, want 25200000 (max across shards)", got[0].RetentionLeaseAgeMillis)
	}
}

func TestFollowerStats_404IsErrUnsupported(t *testing.T) {
	// /_ccr/stats 404s on basic-licence clusters (CCR requires
	// platinum/enterprise). Translate to ErrUnsupported so a downstream
	// rule auto-skips with a stable reason.
	tr := &remoteTransport{ccrStatus: 404, ccrBody: `{"error":"not licensed"}`}
	c := newProbedClient(t, tr, false)
	_, err := c.FollowerStats(context.Background())
	if !errors.Is(err, client.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestFollowerStats_indicesAreSortedByName(t *testing.T) {
	tr := &remoteTransport{ccrBody: `{
		"follow_stats": {
			"indices": [
				{"index":"zebra","shards":[{"remote_cluster":"l","leader_index":"zebra","follower_index":"zebra"}]},
				{"index":"apple","shards":[{"remote_cluster":"l","leader_index":"apple","follower_index":"apple"}]}
			]
		}
	}`}
	c := newProbedClient(t, tr, false)
	got, err := c.FollowerStats(context.Background())
	if err != nil {
		t.Fatalf("FollowerStats: %v", err)
	}
	if len(got) != 2 || got[0].Index != "apple" || got[1].Index != "zebra" {
		t.Errorf("got = %#v, want sorted by index name", got)
	}
}

func TestAutoFollowPatterns_emptyArrayIsEmptySlice(t *testing.T) {
	tr := &remoteTransport{autoFollowBody: `{"patterns":[]}`}
	c := newProbedClient(t, tr, false)
	got, err := c.AutoFollowPatterns(context.Background())
	if err != nil {
		t.Fatalf("AutoFollowPatterns: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("got = %v, want empty slice", got)
	}
}

func TestAutoFollowPatterns_parsesNestedShape(t *testing.T) {
	// Recent ES wraps each entry as {name, pattern{...}} with `active`
	// inside the inner pattern object. Confirm flattening preserves
	// every operator-relevant field.
	tr := &remoteTransport{autoFollowBody: `{
		"patterns": [
			{
				"name": "eu-logs",
				"pattern": {
					"remote_cluster": "leader-eu",
					"leader_index_patterns": ["logs-eu-*"],
					"follow_index_pattern": "{{leader_index}}-follower",
					"active": true
				}
			}
		]
	}`}
	c := newProbedClient(t, tr, false)
	got, err := c.AutoFollowPatterns(context.Background())
	if err != nil {
		t.Fatalf("AutoFollowPatterns: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	want := types.AutoFollowPattern{
		Name:                "eu-logs",
		RemoteCluster:       "leader-eu",
		LeaderIndexPatterns: []string{"logs-eu-*"},
		FollowIndexPattern:  "{{leader_index}}-follower",
		Active:              true,
	}
	if got[0].Name != want.Name || got[0].RemoteCluster != want.RemoteCluster || got[0].FollowIndexPattern != want.FollowIndexPattern || got[0].Active != want.Active {
		t.Errorf("got = %#v, want %#v", got[0], want)
	}
}

func TestAutoFollowPatterns_acceptsLegacyOuterActive(t *testing.T) {
	// Older 7.x reported `active` on the outer entry rather than
	// inside `pattern`. The decoder must accept either spelling.
	tr := &remoteTransport{autoFollowBody: `{
		"patterns": [
			{
				"name": "legacy-pattern",
				"active": false,
				"pattern": {
					"remote_cluster": "leader",
					"leader_index_patterns": ["x-*"]
				}
			}
		]
	}`}
	c := newProbedClient(t, tr, false)
	got, err := c.AutoFollowPatterns(context.Background())
	if err != nil {
		t.Fatalf("AutoFollowPatterns: %v", err)
	}
	if len(got) != 1 || got[0].Active {
		t.Errorf("got = %#v, want Active=false from legacy outer field", got)
	}
}

func TestAutoFollowPatterns_404IsErrUnsupported(t *testing.T) {
	tr := &remoteTransport{autoFollowStatus: 404, autoFollowBody: `{"error":"not licensed"}`}
	c := newProbedClient(t, tr, false)
	_, err := c.AutoFollowPatterns(context.Background())
	if !errors.Is(err, client.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestAutoFollowPatterns_sortedByName(t *testing.T) {
	tr := &remoteTransport{autoFollowBody: `{
		"patterns": [
			{"name":"zeta","pattern":{"remote_cluster":"l","leader_index_patterns":["z-*"]}},
			{"name":"alpha","pattern":{"remote_cluster":"l","leader_index_patterns":["a-*"]}}
		]
	}`}
	c := newProbedClient(t, tr, false)
	got, err := c.AutoFollowPatterns(context.Background())
	if err != nil {
		t.Fatalf("AutoFollowPatterns: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Errorf("got = %#v, want sorted by name", got)
	}
}
