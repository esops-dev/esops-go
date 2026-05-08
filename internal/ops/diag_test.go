package ops_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeDiagnoser returns canned responses keyed by path. Unknown paths
// return 404 so the collector's "record and move on" behaviour is
// exercised without having to stub every endpoint in every test.
//
// `mu` guards `calls` because Diag dispatches collectors concurrently
// (default concurrency 4). Without the lock `go test -race` flags the
// append. `responses` is read-only after construction so it doesn't
// need protection.
type fakeDiagnoser struct {
	responses map[string]fakeResp
	mu        sync.Mutex
	calls     []string
}

type fakeResp struct {
	body   []byte
	status int
	err    error
}

func (f *fakeDiagnoser) DoGET(_ context.Context, path string) ([]byte, int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, path)
	f.mu.Unlock()
	if r, ok := f.responses[path]; ok {
		return r.body, r.status, r.err
	}
	return []byte(`{"error":"not_found"}`), 404, nil
}

// callsSnapshot returns a copy of the recorded paths so test assertions
// iterate without holding the lock during reads.
func (f *fakeDiagnoser) callsSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func esClient(diag client.ClusterDiagnoser) *client.Client {
	return &client.Client{
		Info: types.ClusterInfo{
			Dialect:     types.DialectElasticsearch,
			ClusterName: "esops-dev-es",
			Version:     "9.3.3",
		},
		Diag: diag,
	}
}

func osClient(diag client.ClusterDiagnoser) *client.Client {
	return &client.Client{
		Info: types.ClusterInfo{
			Dialect:      types.DialectOpenSearch,
			ClusterName:  "esops-dev-os",
			Version:      "3.6.0",
			Distribution: "opensearch",
		},
		Diag: diag,
	}
}

// readBundle un-tars a diag bundle into a name → bytes map and asserts
// that manifest.json is present (every test needs that invariant;
// duplicating the check at each call site adds noise). Tests that need
// the parsed manifest pull it from Diag's return value — readBundle's
// job is the file inventory.
func readBundle(t *testing.T, buf *bytes.Buffer) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(buf)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)

	files := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("tar read %q: %v", hdr.Name, err)
		}
		files[hdr.Name] = data
	}
	if _, ok := files["manifest.json"]; !ok {
		t.Fatal("manifest.json missing from bundle")
	}
	return files
}

func TestDiag_writesManifestAndArtifacts(t *testing.T) {
	fake := &fakeDiagnoser{responses: map[string]fakeResp{
		"/":                              {body: []byte(`{"cluster":"ok"}`), status: 200},
		"/_cluster/health?level=indices": {body: []byte(`{"status":"green"}`), status: 200},
		"/_nodes/hot_threads":            {body: []byte("Hot threads at 2026-04-24\n"), status: 200},
	}}
	var buf bytes.Buffer
	manifest, err := ops.Diag(context.Background(), esClient(fake), types.DiagRequest{}, &buf)
	if err != nil {
		t.Fatalf("Diag: %v", err)
	}
	if manifest.Tool != "esops" {
		t.Errorf("manifest.Tool = %q, want esops", manifest.Tool)
	}
	if manifest.Cluster.Dialect != "elasticsearch" {
		t.Errorf("manifest.Cluster.Dialect = %q, want elasticsearch", manifest.Cluster.Dialect)
	}
	if manifest.Cluster.Name != "esops-dev-es" {
		t.Errorf("manifest.Cluster.Name = %q, want esops-dev-es", manifest.Cluster.Name)
	}

	files := readBundle(t, &buf)
	if _, ok := files["cluster_info.json"]; !ok {
		t.Errorf("bundle missing cluster_info.json (files: %v)", keys(files))
	}
	if got, ok := files["cluster_health.json"]; !ok || string(got) != `{"status":"green"}` {
		t.Errorf("cluster_health.json = %q (ok=%v), want {\"status\":\"green\"}", string(got), ok)
	}
	if _, ok := files["nodes_hot_threads.txt"]; !ok {
		t.Errorf("bundle missing nodes_hot_threads.txt (files: %v)", keys(files))
	}
}

func TestDiag_skipsDialectMismatchedCollectors(t *testing.T) {
	fake := &fakeDiagnoser{}
	var buf bytes.Buffer
	_, err := ops.Diag(context.Background(), osClient(fake), types.DiagRequest{}, &buf)
	if err != nil {
		t.Fatalf("Diag: %v", err)
	}
	calls := fake.callsSnapshot()
	for _, p := range calls {
		if strings.HasPrefix(p, "/_ilm/") {
			t.Errorf("ILM endpoint %q hit on OpenSearch cluster", p)
		}
	}
	var ismHit bool
	for _, p := range calls {
		if strings.HasPrefix(p, "/_plugins/_ism/") {
			ismHit = true
		}
	}
	if !ismHit {
		t.Errorf("ISM endpoint was not hit on OpenSearch cluster — collector did not plan it")
	}
}

func TestDiag_manifestOmitsConnectionDetails(t *testing.T) {
	// The redaction contract: the bundle never contains the cluster URL
	// or credentials. The only place those could appear is the manifest
	// (response bodies come from the cluster, which doesn't know our URL).
	// Round-tripping the manifest through JSON and grepping for the
	// sensitive substrings is the airtight check against regressions
	// where someone adds a "connection" field to DiagManifest.
	fake := &fakeDiagnoser{responses: map[string]fakeResp{
		"/": {body: []byte(`{}`), status: 200},
	}}
	var buf bytes.Buffer
	manifest, err := ops.Diag(context.Background(), esClient(fake), types.DiagRequest{}, &buf)
	if err != nil {
		t.Fatalf("Diag: %v", err)
	}
	enc, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	for _, forbidden := range []string{"password", "Password", "Authorization", "https://", "http://"} {
		if bytes.Contains(enc, []byte(forbidden)) {
			t.Errorf("manifest contains forbidden substring %q: %s", forbidden, enc)
		}
	}
}

func TestDiag_recordsNon2xxWithoutAbort(t *testing.T) {
	// 404 on a dialect-specific endpoint is expected data. The collector
	// must keep running and record the cluster's body verbatim so support
	// tickets can see "yes, we asked — here's what the cluster said".
	fake := &fakeDiagnoser{responses: map[string]fakeResp{
		"/": {body: []byte(`{}`), status: 200},
		// Every other endpoint falls through to the default 404. The
		// bundle should still write all artifacts and complete cleanly.
	}}
	var buf bytes.Buffer
	manifest, err := ops.Diag(context.Background(), esClient(fake), types.DiagRequest{}, &buf)
	if err != nil {
		t.Fatalf("Diag: %v", err)
	}
	var has404, has200 bool
	for _, a := range manifest.Artifacts {
		if a.Status == 404 {
			has404 = true
		}
		if a.Status == 200 {
			has200 = true
		}
	}
	if !has404 || !has200 {
		t.Errorf("expected a mix of statuses in manifest, got has200=%v has404=%v", has200, has404)
	}
}

func TestDiag_abortsOnAuthFailure(t *testing.T) {
	// Auth errors against any endpoint mean the whole bundle is
	// untrustworthy (same credentials apply to every request) — stop
	// now rather than produce a half-empty tarball and leave the operator
	// to notice something's wrong.
	fake := &fakeDiagnoser{responses: map[string]fakeResp{
		"/": {body: []byte(`{"error":"unauthorized"}`), status: 401, err: client.ErrAuth},
	}}
	var buf bytes.Buffer
	_, err := ops.Diag(context.Background(), esClient(fake), types.DiagRequest{}, &buf)
	if !errors.Is(err, client.ErrAuth) {
		t.Errorf("err = %v, want ErrAuth", err)
	}
}

func TestDiag_respectsOptionalFlags(t *testing.T) {
	fake := &fakeDiagnoser{}
	var buf bytes.Buffer
	_, err := ops.Diag(context.Background(), esClient(fake),
		types.DiagRequest{
			IncludeMappings:    true,
			IncludeSlowlog:     true,
			IncludeIdleThreads: true,
		}, &buf)
	if err != nil {
		t.Fatalf("Diag: %v", err)
	}
	calls := fake.callsSnapshot()
	var sawMappings, sawSlowlog, sawIdle bool
	for _, p := range calls {
		if strings.HasPrefix(p, "/_all/_mapping") {
			sawMappings = true
		}
		if strings.Contains(p, "slowlog") {
			sawSlowlog = true
		}
		if strings.Contains(p, "ignore_idle_threads=false") {
			sawIdle = true
		}
	}
	if !sawMappings {
		t.Errorf("--include-mappings did not trigger /_all/_mapping (calls: %v)", calls)
	}
	if !sawSlowlog {
		t.Errorf("--include-slowlog did not trigger slowlog settings call (calls: %v)", calls)
	}
	if !sawIdle {
		t.Errorf("--include-idle-threads did not trigger hot_threads?ignore_idle_threads=false (calls: %v)", calls)
	}
}

func TestDiag_optionalsOffByDefault(t *testing.T) {
	fake := &fakeDiagnoser{}
	var buf bytes.Buffer
	_, err := ops.Diag(context.Background(), esClient(fake), types.DiagRequest{}, &buf)
	if err != nil {
		t.Fatalf("Diag: %v", err)
	}
	for _, p := range fake.callsSnapshot() {
		if strings.HasPrefix(p, "/_all/_mapping") {
			t.Errorf("mappings endpoint %q hit without --include-mappings", p)
		}
		if strings.Contains(p, "slowlog") {
			t.Errorf("slowlog endpoint %q hit without --include-slowlog", p)
		}
		if strings.Contains(p, "ignore_idle_threads=false") {
			t.Errorf("idle-threads endpoint %q hit without --include-idle-threads", p)
		}
	}
}

func TestDiag_idleThreadsDistinctFromDefaultHotThreads(t *testing.T) {
	// The base capture (/_nodes/hot_threads, default params = idle hidden)
	// and the opt-in capture must land in separate files, so a reviewer
	// can diff busy-only vs. all-threads side by side. A regression here
	// would overwrite one with the other inside the tarball.
	fake := &fakeDiagnoser{}
	var buf bytes.Buffer
	_, err := ops.Diag(context.Background(), esClient(fake),
		types.DiagRequest{IncludeIdleThreads: true}, &buf)
	if err != nil {
		t.Fatalf("Diag: %v", err)
	}
	files := readBundle(t, &buf)
	if _, ok := files["nodes_hot_threads.txt"]; !ok {
		t.Errorf("busy-only hot_threads artifact missing (files: %v)", keys(files))
	}
	if _, ok := files["nodes_hot_threads_with_idle.txt"]; !ok {
		t.Errorf("idle-inclusive hot_threads artifact missing (files: %v)", keys(files))
	}
}

func TestDiag_errorsWhenClientHasNoDiagCapability(t *testing.T) {
	var buf bytes.Buffer
	_, err := ops.Diag(context.Background(), &client.Client{
		Info: types.ClusterInfo{Dialect: types.DialectElasticsearch},
	}, types.DiagRequest{}, &buf)
	if err == nil {
		t.Fatalf("expected error when client.Diag is nil")
	}
}

func TestDiag_includesCCRAndLicenseAndRemoteSettingsOnES(t *testing.T) {
	// CCR (stats + auto-follow), licence, and configured cluster.remote.*
	// settings are part of the bundle on ES — the support-ticket
	// signal these collectors carry is too valuable to leave to a
	// follow-up `--include-ccr` flag.
	fake := &fakeDiagnoser{responses: map[string]fakeResp{
		"/_ccr/stats":       {body: []byte(`{"follow_stats":{"indices":[]}}`), status: 200},
		"/_ccr/auto_follow": {body: []byte(`{"patterns":[]}`), status: 200},
		"/_license":         {body: []byte(`{"license":{"status":"active","type":"basic"}}`), status: 200},
	}}
	var buf bytes.Buffer
	_, err := ops.Diag(context.Background(), esClient(fake), types.DiagRequest{}, &buf)
	if err != nil {
		t.Fatalf("Diag: %v", err)
	}

	calls := fake.callsSnapshot()
	wantPaths := []string{
		"/_ccr/stats",
		"/_ccr/auto_follow",
		"/_license",
		"/_cluster/settings?include_defaults=true&filter_path=*.cluster.remote.*",
	}
	for _, want := range wantPaths {
		var hit bool
		for _, p := range calls {
			if p == want {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("collector did not call %q (calls: %v)", want, calls)
		}
	}

	files := readBundle(t, &buf)
	for _, want := range []string{"ccr_stats.json", "ccr_auto_follow.json", "license.json", "cluster_remote_settings.json"} {
		if _, ok := files[want]; !ok {
			t.Errorf("bundle missing %q (files: %v)", want, keys(files))
		}
	}
}

func TestDiag_skipsCCRAndLicenseOnOpenSearch(t *testing.T) {
	// The collector plan filters dialect-specific endpoints before
	// firing. Confirm the OS branch never touches /_ccr/* or /_license
	// — those would 404 on every OpenSearch cluster.
	fake := &fakeDiagnoser{}
	var buf bytes.Buffer
	if _, err := ops.Diag(context.Background(), osClient(fake), types.DiagRequest{}, &buf); err != nil {
		t.Fatalf("Diag: %v", err)
	}
	for _, p := range fake.callsSnapshot() {
		if strings.HasPrefix(p, "/_ccr/") || p == "/_license" {
			t.Errorf("ES-only endpoint %q hit on OpenSearch cluster", p)
		}
	}
}

func TestDiag_includesClusterRemoteSettingsOnBothDialects(t *testing.T) {
	// /_cluster/settings?...filter_path=*.cluster.remote.* is shared
	// across dialects: an OpenSearch cluster with a configured remote
	// must surface the same support-ticket signal as ES.
	for _, factory := range map[string]func(client.ClusterDiagnoser) *client.Client{
		"es": esClient,
		"os": osClient,
	} {
		fake := &fakeDiagnoser{}
		var buf bytes.Buffer
		if _, err := ops.Diag(context.Background(), factory(fake), types.DiagRequest{}, &buf); err != nil {
			t.Fatalf("Diag: %v", err)
		}
		var hit bool
		for _, p := range fake.callsSnapshot() {
			if p == "/_cluster/settings?include_defaults=true&filter_path=*.cluster.remote.*" {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("cluster_remote_settings collector did not fire (calls: %v)", fake.callsSnapshot())
		}
	}
}

// TestDiag_includesNodesSettingsAndProcessJVMOnBothDialects pins the
// two collectors that surface the data NodeSettingsInspector and the
// JVM-ergonomics extension on NodeBootstrapInspector consume. The
// support-ticket signal here — "what static node settings did the
// operator set?" and "what GC is each node running with which flags?"
// — is the load-bearing reason these are first-class collectors
// rather than buried in /_nodes; the test guards that nothing demotes
// them back to dialect-conditional captures.
func TestDiag_includesNodesSettingsAndProcessJVMOnBothDialects(t *testing.T) {
	for name, factory := range map[string]func(client.ClusterDiagnoser) *client.Client{
		"es": esClient,
		"os": osClient,
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeDiagnoser{responses: map[string]fakeResp{
				"/_nodes/settings?flat_settings=true": {
					body:   []byte(`{"nodes":{"n1":{"name":"x","settings":{"network.host":"0.0.0.0"}}}}`),
					status: 200,
				},
				"/_nodes/process,jvm": {
					body:   []byte(`{"nodes":{"n1":{"name":"x","jvm":{"gc_collectors":["G1 Young Generation"],"input_arguments":["-XX:+UseG1GC"]}}}}`),
					status: 200,
				},
			}}
			var buf bytes.Buffer
			_, err := ops.Diag(context.Background(), factory(fake), types.DiagRequest{}, &buf)
			if err != nil {
				t.Fatalf("Diag: %v", err)
			}
			calls := fake.callsSnapshot()
			for _, want := range []string{
				"/_nodes/settings?flat_settings=true",
				"/_nodes/process,jvm",
			} {
				var hit bool
				for _, p := range calls {
					if p == want {
						hit = true
						break
					}
				}
				if !hit {
					t.Errorf("collector did not call %q (calls: %v)", want, calls)
				}
			}
			files := readBundle(t, &buf)
			for _, want := range []string{"nodes_settings_flat.json", "nodes_process_jvm.json"} {
				if _, ok := files[want]; !ok {
					t.Errorf("bundle missing %q (files: %v)", want, keys(files))
				}
			}
		})
	}
}

func TestBundleFilename_scrubsClusterName(t *testing.T) {
	ts := time.Date(2026, 4, 24, 12, 34, 56, 0, time.UTC)
	tests := []struct {
		name    string
		cluster string
		want    string
	}{
		{"simple", "prod", "esops-diag-prod-20260424T123456Z.tar.gz"},
		{"slashes", "ops/eu west/1", "esops-diag-ops-eu-west-1-20260424T123456Z.tar.gz"},
		{"empty", "", "esops-diag-cluster-20260424T123456Z.tar.gz"},
		{"punct only", "!!!", "esops-diag-cluster-20260424T123456Z.tar.gz"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ops.BundleFilename(tc.cluster, ts); got != tc.want {
				t.Errorf("BundleFilename(%q) = %q, want %q", tc.cluster, got, tc.want)
			}
		})
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
