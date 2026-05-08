package cluster_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/cluster"
	"github.com/esops-dev/esops-go/pkg/config"
	"github.com/esops-dev/esops-go/pkg/types"
)

// TestFieldCaps_dialectContract proves both adapters fold an
// identical /_field_caps wire shape into the same neutral slice. The
// flatten step (per-type entries → one row per field, types union,
// any-searchable/any-aggregatable, dedup-sorted indices for
// heterogeneous fields) is duplicated between adapters and a future
// drift would land here, not in production.
//
// The test lives in pkg/cluster (where both adapters are wired through
// the factory) so a dialect-probe + factory regression is also caught
// at the same time — the path downstream consumers actually take.
func TestFieldCaps_dialectContract(t *testing.T) {
	const fieldCapsBody = `{
		"indices": ["idx-a","idx-b","idx-c"],
		"fields": {
			"user": {
				"keyword": {"type":"keyword","searchable":true,"aggregatable":true,"indices":["idx-a","idx-b"]},
				"text":    {"type":"text","searchable":true,"aggregatable":false,"indices":["idx-c"]}
			},
			"_id": {
				"_id": {"type":"_id","searchable":true,"aggregatable":false,"metadata_field":true}
			},
			"price": {
				"long": {"type":"long","searchable":true,"aggregatable":true}
			}
		}
	}`
	want := []types.FieldCaps{
		{Field: "_id", Types: []string{"_id"}, Searchable: true, MetadataField: true},
		{Field: "price", Types: []string{"long"}, Searchable: true, Aggregatable: true},
		{Field: "user", Types: []string{"keyword", "text"}, Searchable: true, Aggregatable: true, Indices: []string{"idx-a", "idx-b", "idx-c"}},
	}
	wantMatchedIndices := []string{"idx-a", "idx-b", "idx-c"}

	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			c := newFieldCapsClient(t, dialect, fieldCapsBody)
			got, err := c.FieldCaps.FieldCaps(context.Background(), types.FieldCapsRequest{Index: "idx-*"})
			if err != nil {
				t.Fatalf("FieldCaps: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("flattened slice mismatch\n got: %#v\nwant: %#v", got, want)
			}
			summary, err := c.FieldCaps.FieldCapsSummary(context.Background(), types.FieldCapsRequest{Index: "idx-*"})
			if err != nil {
				t.Fatalf("FieldCapsSummary: %v", err)
			}
			if !reflect.DeepEqual(summary.MatchedIndices, wantMatchedIndices) {
				t.Errorf("MatchedIndices = %#v, want %#v", summary.MatchedIndices, wantMatchedIndices)
			}
			if !reflect.DeepEqual(summary.Fields, want) {
				t.Errorf("Summary.Fields mismatch\n got: %#v\nwant: %#v", summary.Fields, want)
			}
		})
	}
}

// TestCardinality_dialectContract proves both adapters send the same
// cardinality-agg body and decode the same response shape. The
// build-body and decode-value steps live duplicated in each adapter —
// a future cluster spec change (e.g. precision_threshold rename) must
// fail here.
func TestCardinality_dialectContract(t *testing.T) {
	const cardinalityBody = `{"aggregations":{"c":{"value":17}}}`

	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			capture := &bodyCapture{}
			c := newCardinalityClient(t, dialect, cardinalityBody, capture)
			got, err := c.FieldCardinality.Cardinality(context.Background(), types.FieldCardinalityRequest{
				Indices:   "logs-2024",
				Fields:    []string{"user.id"},
				Precision: 5000,
			})
			if err != nil {
				t.Fatalf("Cardinality: %v", err)
			}
			if len(got) != 1 || got[0].Cardinality != 17 || got[0].Precision != "approximate" {
				t.Errorf("got = %+v, want one row with Cardinality=17 Precision=approximate", got)
			}

			var body map[string]any
			if err := json.Unmarshal(capture.last, &body); err != nil {
				t.Fatalf("body not JSON: %v\nraw=%s", err, capture.last)
			}
			aggs, _ := body["aggs"].(map[string]any)
			cAgg, _ := aggs["c"].(map[string]any)
			card, _ := cAgg["cardinality"].(map[string]any)
			if card["field"] != "user.id" {
				t.Errorf("aggs.c.cardinality.field = %v, want user.id", card["field"])
			}
			if v, _ := card["precision_threshold"].(float64); int(v) != 5000 {
				t.Errorf("precision_threshold = %v, want 5000", card["precision_threshold"])
			}
			if v, _ := body["size"].(float64); int(v) != 0 {
				t.Errorf("size = %v, want 0", body["size"])
			}
			if v, _ := body["track_total_hits"].(bool); v {
				t.Errorf("track_total_hits = true, want false")
			}
		})
	}
}

// TestRestoreHistory_dialectContract proves both adapters parse the
// same task-history wire shape and convention-index document shape
// into identical RestoreEvent slices. parseRestoreDescription is
// duplicated, as is the convention-doc decoder; a drift in either
// fails here.
func TestRestoreHistory_dialectContract(t *testing.T) {
	tasksBody := `{
		"nodes": {
			"node-a": {
				"tasks": {
					"node-a:1": {
						"action": "cluster:admin/snapshot/restore",
						"description": "snapshot=fs:nightly-2024-01 indices[logs-*]",
						"start_time_in_millis": 1700000000000,
						"running_time_in_nanos": 1500000000,
						"cancelled": false
					}
				}
			}
		}
	}`
	conventionBody := `{"hits":{"hits":[
		{"_source":{"snapshot_id":"nightly-2024-02","repository":"fs","started_at":"2024-01-02T00:00:00Z","completed_at":"2024-01-02T00:30:00Z","state":"success","indices":["a","b"]}}
	]}}`

	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			c := newRestoreHistoryClient(t, dialect, tasksBody, conventionBody)
			got, err := c.RestoreHistory.RestoreHistory(context.Background(), types.SnapshotRestoreHistoryRequest{
				IncludeTaskHistory:     true,
				IncludeConventionIndex: true,
			})
			if err != nil {
				t.Fatalf("RestoreHistory: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("len(got) = %d, want 2", len(got))
			}
			// Sort order is StartedAt desc; nightly-2024-02
			// (2024-01-02) is newer than nightly-2024-01 (2023-11-14
			// from epoch ms), so the convention_index event lands
			// first.
			if got[0].SnapshotID != "nightly-2024-02" || got[0].Source != "convention_index" {
				t.Errorf("got[0] = %+v, want nightly-2024-02/convention_index first", got[0])
			}
			if got[0].CompletedAt == nil || !got[0].CompletedAt.Equal(time.Date(2024, 1, 2, 0, 30, 0, 0, time.UTC)) {
				t.Errorf("got[0].CompletedAt = %v, want 2024-01-02T00:30:00Z", got[0].CompletedAt)
			}
			if got[1].SnapshotID != "nightly-2024-01" || got[1].Source != "task_history" {
				t.Errorf("got[1] = %+v, want nightly-2024-01/task_history second", got[1])
			}
			if got[1].Repository != "fs" {
				t.Errorf("got[1].Repository = %q, want fs (parsed from description)", got[1].Repository)
			}
			// running_time_in_nanos was 1.5s; the adapter must populate
			// CompletedAt = StartedAt + RunningTime so a finished task
			// surfaces a non-nil completion time even when the cluster
			// does not echo a separate "completed_at" field.
			if got[1].CompletedAt == nil {
				t.Errorf("got[1].CompletedAt = nil; expected StartedAt + RunningTime")
			}
		})
	}
}

// TestRemoteClusters_dialectContract proves both adapters fold the
// shared /_remote/info envelope into the same neutral slice. Both
// dialects expose the same wire shape; the merge (proxy-mode socket
// count → NumNodesConnected, sniff-mode seeds → SeedHosts) is
// duplicated between adapters and a future drift would land here.
func TestRemoteClusters_dialectContract(t *testing.T) {
	const remoteBody = `{
		"sniff-cluster": {
			"mode": "sniff",
			"connected": true,
			"num_nodes_connected": 3,
			"seeds": ["host1:9300","host2:9300"],
			"skip_unavailable": false
		},
		"proxy-cluster": {
			"mode": "proxy",
			"connected": false,
			"num_proxy_sockets_connected": 0,
			"skip_unavailable": true
		}
	}`
	want := []types.RemoteCluster{
		{Name: "proxy-cluster", Mode: "proxy", Connected: false, NumNodesConnected: 0, SkipUnavailable: true},
		{Name: "sniff-cluster", Mode: "sniff", Connected: true, NumNodesConnected: 3, SeedHosts: []string{"host1:9300", "host2:9300"}, SkipUnavailable: false},
	}
	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			c := newRemoteClustersClient(t, dialect, remoteBody)
			got, err := c.RemoteClusters.RemoteClusters(context.Background())
			if err != nil {
				t.Fatalf("RemoteClusters: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("slice mismatch\n got: %#v\nwant: %#v", got, want)
			}
		})
	}
}

// TestFollowerStats_ESParsesShards proves the ES adapter aggregates
// per-shard /_ccr/stats rows into one row per follower index, taking
// the maximum lag across shards (one lagging shard makes the index
// lagged) and propagating leader_index/remote_cluster from the first
// shard. The OS-side dialect contract is captured separately by
// TestFollowerStats_OSReturnsUnsupported.
func TestFollowerStats_ESParsesShards(t *testing.T) {
	const ccrBody = `{
		"follow_stats": {
			"indices": [
				{
					"index": "logs-2024.01-follower",
					"shards": [
						{"remote_cluster":"leader","leader_index":"logs-2024.01","follower_index":"logs-2024.01-follower","operations_written_lag":4,"paused":false},
						{"remote_cluster":"leader","leader_index":"logs-2024.01","follower_index":"logs-2024.01-follower","operations_written_lag":12,"paused":true,"read_exceptions":{"type":"x_content_parse_exception","reason":"boom"}}
					]
				}
			]
		}
	}`
	c := newFollowerStatsClient(t, dialectES, ccrBody, http.StatusOK)
	got, err := c.RemoteClusters.FollowerStats(context.Background())
	if err != nil {
		t.Fatalf("FollowerStats: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	want := types.FollowerStats{
		Index:           "logs-2024.01-follower",
		LeaderIndex:     "logs-2024.01",
		LeaderCluster:   "leader",
		FollowerLagOps:  12,
		LastReadFailure: "boom",
		Paused:          true,
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("got = %#v, want %#v", got[0], want)
	}
}

// TestFollowerStats_ESUnlicensedTranslatesToUnsupported proves the
// 404-from-/_ccr/stats path (CCR not licensed) translates into
// ErrUnsupported so a downstream rule auto-skips with a stable reason
// rather than seeing a generic 404.
func TestFollowerStats_ESUnlicensedTranslatesToUnsupported(t *testing.T) {
	c := newFollowerStatsClient(t, dialectES, `{"error":"not licensed"}`, http.StatusNotFound)
	_, err := c.RemoteClusters.FollowerStats(context.Background())
	if !errors.Is(err, client.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// TestFollowerStats_OSReturnsUnsupported proves the OS adapter
// short-circuits FollowerStats with ErrUnsupported — CCR is ES-only.
func TestFollowerStats_OSReturnsUnsupported(t *testing.T) {
	c := newFollowerStatsClient(t, dialectOS, `{}`, http.StatusOK)
	_, err := c.RemoteClusters.FollowerStats(context.Background())
	if !errors.Is(err, client.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// TestLicense_ESParsesActive proves the ES adapter decodes /_license
// envelope into the neutral LicenseStatus shape: the millis form is
// preferred over the ISO form and a missing expiry stays nil (basic
// licence).
func TestLicense_ESParsesActive(t *testing.T) {
	const licenseBody = `{
		"license": {
			"status": "active",
			"uid": "abc-123",
			"type": "platinum",
			"issued_to": "ACME Corp",
			"issuer": "elasticsearch",
			"issue_date_in_millis": 1704067200000,
			"expiry_date_in_millis": 1735689600000,
			"max_nodes": 100
		}
	}`
	c := newLicenseClient(t, dialectES, licenseBody, http.StatusOK)
	got, err := c.License.License(context.Background())
	if err != nil {
		t.Fatalf("License: %v", err)
	}
	if got.Status != "active" || got.Type != "platinum" || got.UID != "abc-123" || got.MaxNodes != 100 {
		t.Errorf("scalar mismatch: got = %#v", got)
	}
	if !got.IssuedAt.Equal(time.UnixMilli(1704067200000).UTC()) {
		t.Errorf("IssuedAt = %v, want 2024-01-01T00:00:00Z", got.IssuedAt)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(time.UnixMilli(1735689600000).UTC()) {
		t.Errorf("ExpiresAt = %v, want 2025-01-01T00:00:00Z", got.ExpiresAt)
	}
}

// TestLicense_ESBasicHasNoExpiry proves a basic licence (cluster
// omits expiry_date_in_millis) lands as ExpiresAt=nil so a rule can
// render "no expiry concern" without a special-case branch.
func TestLicense_ESBasicHasNoExpiry(t *testing.T) {
	const basicBody = `{"license":{"status":"active","type":"basic","uid":"u","issue_date_in_millis":1704067200000}}`
	c := newLicenseClient(t, dialectES, basicBody, http.StatusOK)
	got, err := c.License.License(context.Background())
	if err != nil {
		t.Fatalf("License: %v", err)
	}
	if got.ExpiresAt != nil {
		t.Errorf("ExpiresAt = %v, want nil for basic licence", got.ExpiresAt)
	}
	if got.Type != "basic" || got.Status != "active" {
		t.Errorf("got = %#v, want active/basic", got)
	}
}

// TestLicense_OSReturnsUnsupported proves the OS adapter short-
// circuits License with ErrUnsupported — /_license is ES-only.
func TestLicense_OSReturnsUnsupported(t *testing.T) {
	c := newLicenseClient(t, dialectOS, `{}`, http.StatusOK)
	_, err := c.License.License(context.Background())
	if !errors.Is(err, client.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func newRemoteClustersClient(t *testing.T, dialect, remoteBody string) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeDialectHeaders(w, dialect)
		if strings.HasPrefix(r.URL.Path, "/_remote/info") {
			_, _ = w.Write([]byte(remoteBody))
			return
		}
		if dialect == dialectES {
			_, _ = w.Write([]byte(esRootBody))
		} else {
			_, _ = w.Write([]byte(osRootBody))
		}
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return newProbedContractClient(t, s.URL)
}

func newFollowerStatsClient(t *testing.T, dialect, ccrBody string, ccrStatus int) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeDialectHeaders(w, dialect)
		if strings.HasPrefix(r.URL.Path, "/_ccr/stats") {
			w.WriteHeader(ccrStatus)
			_, _ = w.Write([]byte(ccrBody))
			return
		}
		if dialect == dialectES {
			_, _ = w.Write([]byte(esRootBody))
		} else {
			_, _ = w.Write([]byte(osRootBody))
		}
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return newProbedContractClient(t, s.URL)
}

func newLicenseClient(t *testing.T, dialect, licenseBody string, licenseStatus int) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeDialectHeaders(w, dialect)
		if strings.HasPrefix(r.URL.Path, "/_license") {
			w.WriteHeader(licenseStatus)
			_, _ = w.Write([]byte(licenseBody))
			return
		}
		if dialect == dialectES {
			_, _ = w.Write([]byte(esRootBody))
		} else {
			_, _ = w.Write([]byte(osRootBody))
		}
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return newProbedContractClient(t, s.URL)
}

// bodyCapture records the body of the most recent non-probe request.
// Used by the cardinality contract test to assert both adapters send
// the same wire body.
type bodyCapture struct {
	last []byte
}

func newFieldCapsClient(t *testing.T, dialect, fieldCapsBody string) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeDialectHeaders(w, dialect)
		if strings.HasSuffix(r.URL.Path, "/_field_caps") {
			_, _ = w.Write([]byte(fieldCapsBody))
			return
		}
		if dialect == dialectES {
			_, _ = w.Write([]byte(esRootBody))
		} else {
			_, _ = w.Write([]byte(osRootBody))
		}
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return newProbedContractClient(t, s.URL)
}

func newCardinalityClient(t *testing.T, dialect, searchBody string, capture *bodyCapture) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeDialectHeaders(w, dialect)
		if strings.HasSuffix(r.URL.Path, "/_search") {
			if r.Body != nil {
				body, _ := io.ReadAll(r.Body)
				capture.last = body
			}
			_, _ = w.Write([]byte(searchBody))
			return
		}
		if dialect == dialectES {
			_, _ = w.Write([]byte(esRootBody))
		} else {
			_, _ = w.Write([]byte(osRootBody))
		}
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return newProbedContractClient(t, s.URL)
}

func newRestoreHistoryClient(t *testing.T, dialect, tasksBody, conventionBody string) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeDialectHeaders(w, dialect)
		if strings.HasPrefix(r.URL.Path, "/_tasks") {
			_, _ = w.Write([]byte(tasksBody))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/_search") {
			_, _ = w.Write([]byte(conventionBody))
			return
		}
		if dialect == dialectES {
			_, _ = w.Write([]byte(esRootBody))
		} else {
			_, _ = w.Write([]byte(osRootBody))
		}
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return newProbedContractClient(t, s.URL)
}

func writeDialectHeaders(w http.ResponseWriter, dialect string) {
	w.Header().Set("Content-Type", "application/json")
	if dialect == dialectES {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
	}
}

func newProbedContractClient(t *testing.T, url string) *client.Client {
	t.Helper()
	cc := config.Context{URL: url}
	c, err := cluster.New(context.Background(), cc)
	if err != nil {
		t.Fatalf("cluster.New: %v", err)
	}
	return c
}
