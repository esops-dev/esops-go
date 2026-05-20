package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// nodesInfoBootstrapWire mirrors the slice of /_nodes (info) we read for
// bootstrap-check posture and JVM ergonomics. /_nodes/process carries
// `mlockall` and `max_file_descriptors`; /_nodes/jvm carries
// `mem.using_memory_lock` (the same condition expressed from the
// JVM's side, which lines up with mlockall on a healthy node and is
// the field newer ES versions preserve when bootstrap.memory_lock is
// honoured) plus `gc_collectors[]` and `input_arguments[]` for the
// jvm_gc_ergonomics rule. Every metric arrives in one response when
// we ask for `process,jvm` together.
type nodesInfoBootstrapWire struct {
	Nodes map[string]struct {
		Name    string `json:"name"`
		Process struct {
			Mlockall           bool  `json:"mlockall"`
			MaxFileDescriptors int64 `json:"max_file_descriptors"`
		} `json:"process"`
		JVM struct {
			Mem struct {
				UsingMemoryLock bool `json:"using_memory_lock"`
			} `json:"mem"`
			GCCollectors   []string `json:"gc_collectors"`
			InputArguments []string `json:"input_arguments"`
		} `json:"jvm"`
		Plugins []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"plugins"`
	} `json:"nodes"`
}

// NodeBootstrap implements client.NodeBootstrapInspector. The cluster
// reports the same bootstrap-check shape on /_nodes/process and
// /_nodes/jvm whether the operator hit a 7.x, 8.x, or 9.x build, so a
// single GET /_nodes/process,jvm covers every supported version. The
// adapter prefers the JVM-side using_memory_lock when it disagrees
// with process.mlockall — newer builds report the JVM view consistently
// while the process flag occasionally lags startup.
//
// vm.max_map_count is not exposed on /_nodes endpoints (it's a kernel
// reading the cluster does not surface back through the API), so
// MaxMapCount stays zero. BootstrapWarnings stays empty here too: the
// "bootstrap checks failed" lines live in cluster startup logs, not
// /_nodes — doctor's deprecation rule consumes /_migration/deprecations
// for that signal instead. A zero numeric / empty slice is "the
// cluster did not tell us", not "the kernel says zero".
//
// GCCollectors and JVMInputArguments come from the same response body:
// `jvm.gc_collectors[]` (e.g. ["G1 Young Generation","G1 Old
// Generation"] on a default G1 cluster, ["ParNew","ConcurrentMarkSweep"]
// on a CMS cluster) and `jvm.input_arguments[]` (every JVM CLI flag
// the running process sees). Doctor's jvm_gc_ergonomics rule reads
// both — the collector list to flag CMS, the argument list to flag
// manual overrides of G1 heuristics. The cluster always emits these
// fields when /_nodes/jvm is requested, so a missing slice means
// "this node did not report it" rather than "the JVM has no GC".
func (c *Client) NodeBootstrap(ctx context.Context) ([]types.NodeBootstrap, error) {
	// /_nodes/process,jvm,plugins folds in the plugin manifest in the
	// same RTT — the plugins block adds ~150 bytes per plugin per node,
	// negligible against the JVM input-arguments slice that already
	// dominates this response.
	body, status, err := c.doRawGET(ctx, "/_nodes/process,jvm,plugins")
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, clientutil.ClassifyStatus("nodes bootstrap", status, body)
	}

	var wire nodesInfoBootstrapWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, errors.Join(errors.New("decoding nodes info bootstrap response"), err)
	}

	out := make([]types.NodeBootstrap, 0, len(wire.Nodes))
	for _, n := range wire.Nodes {
		// Either signal proves the node honoured bootstrap.memory_lock.
		// Newer ES versions populate the JVM-side flag more reliably; a
		// false on one side and true on the other is "honoured" — the
		// cluster could only report true if the syscall succeeded.
		mlock := n.Process.Mlockall || n.JVM.Mem.UsingMemoryLock
		plugins := make([]types.NodePlugin, 0, len(n.Plugins))
		for _, p := range n.Plugins {
			plugins = append(plugins, types.NodePlugin{Name: p.Name, Version: p.Version})
		}
		sort.Slice(plugins, func(i, j int) bool { return plugins[i].Name < plugins[j].Name })
		out = append(out, types.NodeBootstrap{
			Name:               n.Name,
			MlockallEnabled:    mlock,
			MaxFileDescriptors: n.Process.MaxFileDescriptors,
			GCCollectors:       n.JVM.GCCollectors,
			JVMInputArguments:  n.JVM.InputArguments,
			Plugins:            plugins,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// clusterSettingsFullWire mirrors the /_cluster/settings response with
// flat_settings=true. Each scope arrives as a flat map of dotted-path
// keys to cluster-reported values; we hand it through unchanged so the
// consumer can reach for any key without forcing a per-key decoder.
type clusterSettingsFullWire struct {
	Persistent map[string]any `json:"persistent"`
	Transient  map[string]any `json:"transient"`
	Defaults   map[string]any `json:"defaults"`
}

// GetClusterSettingsFull implements client.ClusterSettingsFullInspector.
// flat_settings=true is always on so consumers see dotted-path keys
// directly; include_defaults flips the cluster's ?include_defaults flag.
//
// Empty scopes (the most common case on a fresh cluster) come back as
// nil maps from the JSON decoder; we leave them nil rather than
// allocating an empty map — consumers should treat "missing key"
// identically in either shape.
func (c *Client) GetClusterSettingsFull(ctx context.Context, includeDefaults bool) (types.ClusterSettingsFull, error) {
	gs := c.api.Cluster.GetSettings
	resp, err := c.api.Cluster.GetSettings(
		gs.WithContext(ctx),
		gs.WithFlatSettings(true),
		gs.WithIncludeDefaults(includeDefaults),
	)
	if err != nil {
		return types.ClusterSettingsFull{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cluster get_settings (full)", resp.StatusCode, resp.Body); err != nil {
		return types.ClusterSettingsFull{}, err
	}

	var wire clusterSettingsFullWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.ClusterSettingsFull{}, errors.Join(errors.New("decoding cluster settings (full) response"), err)
	}
	return types.ClusterSettingsFull{
		Persistent: wire.Persistent,
		Transient:  wire.Transient,
		Defaults:   wire.Defaults,
	}, nil
}

// catAllocationRow mirrors a /_cat/allocation row. Numeric fields are
// strings on the wire (the cluster prepends a unit suffix when bytes=
// is omitted); we request bytes=b so the values are bare integers and
// parse them as such. Node is "UNASSIGNED" on the synthetic row the
// cluster emits for orphan shards — we drop it because it has no
// matching name and a hygiene rule keyed by node name would skip past
// it anyway.
type catAllocationRow struct {
	Shards      string `json:"shards"`
	DiskIndices string `json:"disk.indices"`
	DiskUsed    string `json:"disk.used"`
	DiskAvail   string `json:"disk.avail"`
	DiskTotal   string `json:"disk.total"`
	DiskPercent string `json:"disk.percent"`
	Host        string `json:"host"`
	IP          string `json:"ip"`
	Node        string `json:"node"`
}

// AllocationByNode implements client.AllocationInspector via
// /_cat/allocation. Stable iteration order is alphabetic by node name
// — operators read the table by name and the cluster doesn't promise
// any particular order for /_cat output.
func (c *Client) AllocationByNode(ctx context.Context) ([]types.NodeAllocation, error) {
	ca := c.api.Cat.Allocation
	resp, err := c.api.Cat.Allocation(
		ca.WithContext(ctx),
		ca.WithFormat("json"),
		ca.WithBytes("b"),
		ca.WithH(
			"shards", "disk.indices", "disk.used", "disk.avail",
			"disk.total", "disk.percent", "host", "ip", "node",
		),
	)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cat allocation", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var rows []catAllocationRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, errors.Join(errors.New("decoding cat allocation response"), err)
	}

	out := make([]types.NodeAllocation, 0, len(rows))
	for _, r := range rows {
		if r.Node == "" || strings.EqualFold(r.Node, "UNASSIGNED") {
			// Cluster's orphan-shard pseudo-row carries no node name;
			// drop it so the slice is keyed by real nodes only.
			continue
		}
		out = append(out, types.NodeAllocation{
			Name:             r.Node,
			Shards:           parseInt(r.Shards),
			DiskIndicesBytes: parseInt64(r.DiskIndices),
			DiskUsedBytes:    parseInt64(r.DiskUsed),
			DiskAvailBytes:   parseInt64(r.DiskAvail),
			DiskTotalBytes:   parseInt64(r.DiskTotal),
			DiskPercent:      parsePercent(r.DiskPercent),
			Host:             r.Host,
			IP:               r.IP,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// nodesSettingsWire mirrors the slice of /_nodes/settings consumed by
// every per-node static-settings inspector — TransportTLS, HTTPTLS,
// and NodeSettings. flat_settings=true makes the keys arrive as
// dotted paths; the TLS inspectors read both
// `xpack.security.{transport,http}.ssl.*` (ES 7+) and the legacy
// `{transport,http}.ssl.*` aliases, while NodeSettings hands the full
// per-node map through unchanged for downstream rules to inspect any
// dotted-path key (network.host, node.store.allow_mmap, discovery.*).
type nodesSettingsWire struct {
	Nodes map[string]struct {
		Name     string         `json:"name"`
		Settings map[string]any `json:"settings"`
	} `json:"nodes"`
}

// nodesSettingsPath is the single read that backs TransportTLS,
// HTTPTLS, and NodeSettings. filter_path keeps the body to
// `nodes.*.name,nodes.*.settings` — small enough that one fetch
// covers every consumer of static node-level settings without paying
// for /_cluster/settings-style include_defaults bloat.
//
// /_nodes/settings does not accept ?include_defaults — that parameter
// is /_cluster/settings only, and the keys these inspectors consume
// are static node-level settings that don't surface through
// /_cluster/settings either. Cluster-default ciphers/protocols are
// JVM-baked and not exposed through any cluster API; rules that need
// to flag a weak default have to know the dialect's defaults
// out-of-band. The view here is operator-only by design.
//
// filter_path stops at `nodes.*.settings` rather than drilling deeper
// into specific keypaths. With flat_settings=true the cluster emits
// keys like "xpack.security.http.ssl.enabled" — the dots are literal
// characters, not JSON-path separators, so a filter_path of
// `nodes.*.settings.xpack.security.http.ssl.*` matches nothing and
// silently drops the keys we came for. Stopping at `settings` keeps
// the per-node settings map intact (still small — only operator-set
// static keys appear without include_defaults) while skipping the
// host/ip/version/roles/attributes the inspectors don't read.
const nodesSettingsPath = "/_nodes/settings?flat_settings=true&filter_path=" +
	"nodes.*.name,nodes.*.settings"

const nodesSettingsTTLDefault = 2 * time.Second

// cachedNodesSettings issues at most one /_nodes/settings GET per TTL
// window per Client. TransportTLS, HTTPTLS, and NodeSettings all call
// through here, so a doctor running every static-settings rule in
// sequence pays one round trip rather than three. Errors are not
// cached: a transient transport failure should not mask a recovered
// cluster on the next call.
//
// The mutex is held across the fetch deliberately. Concurrent first
// callers serialise rather than racing on duplicate requests; in the
// expected single-goroutine doctor flow this is a no-op, and even
// under contention the worst case is a brief wait for the leading
// fetch to finish, which is what we want anyway.
func (c *Client) cachedNodesSettings(ctx context.Context) (nodesSettingsWire, error) {
	c.nodesSettingsMu.Lock()
	defer c.nodesSettingsMu.Unlock()
	ttl := c.nodesSettingsTTL
	if ttl <= 0 {
		ttl = nodesSettingsTTLDefault
	}
	if !c.nodesSettingsTime.IsZero() && time.Since(c.nodesSettingsTime) < ttl {
		return c.nodesSettingsSnap, nil
	}
	body, status, err := c.doRawGET(ctx, nodesSettingsPath)
	if err != nil {
		return nodesSettingsWire{}, err
	}
	if status >= 400 {
		return nodesSettingsWire{}, clientutil.ClassifyStatus("nodes settings", status, body)
	}
	var wire nodesSettingsWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return nodesSettingsWire{}, errors.Join(errors.New("decoding nodes settings response"), err)
	}
	c.nodesSettingsSnap = wire
	c.nodesSettingsTime = time.Now()
	return wire, nil
}

// NodeSettings implements client.NodeSettingsInspector for ES. Hands
// the cached /_nodes/settings snapshot through as a stable []NodeSettings
// slice keyed alphabetically by node name — downstream rules iterate
// per node and read dotted-path keys directly off Settings without
// walking nested objects (flat_settings=true is always on at the
// adapter layer).
//
// Empty cluster (no reachable nodes — observable during a transient
// master loss) returns a nil slice rather than a vacuous empty map;
// rules that count nodes treat absence as "no signal", not "the
// cluster reported zero nodes".
func (c *Client) NodeSettings(ctx context.Context) ([]types.NodeSettings, error) {
	wire, err := c.cachedNodesSettings(ctx)
	if err != nil {
		return nil, err
	}
	if len(wire.Nodes) == 0 {
		return nil, nil
	}
	out := make([]types.NodeSettings, 0, len(wire.Nodes))
	for _, n := range wire.Nodes {
		out = append(out, types.NodeSettings{Name: n.Name, Settings: n.Settings})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// TransportTLS implements client.TransportTLSInspector for ES.
// Aggregates the per-node transport-TLS state across every reachable
// node from the shared /_nodes/settings snapshot. Enabled is true
// only when every node reports it on; one false in the fleet is
// exactly what the rule has to flag, so the aggregation is
// deliberately strict. Same rule for Verified, against
// `xpack.security.transport.ssl.verification_mode` — anything other
// than "none" means the cluster is doing certificate verification.
//
// A cluster with zero nodes (impossible in practice but observable
// during a transient master loss) returns Enabled=false rather than
// vacuously true; the rule expects a populated cluster to make a
// claim.
func (c *Client) TransportTLS(ctx context.Context) (types.TransportTLS, error) {
	wire, err := c.cachedNodesSettings(ctx)
	if err != nil {
		return types.TransportTLS{}, err
	}
	if len(wire.Nodes) == 0 {
		return types.TransportTLS{}, nil
	}

	allEnabled := true
	allVerified := true
	for _, n := range wire.Nodes {
		enabled := readBoolSetting(n.Settings, "xpack.security.transport.ssl.enabled") ||
			readBoolSetting(n.Settings, "transport.ssl.enabled")
		// "verification_mode": "none" → not verified; anything else
		// (full / certificate) means the cluster is checking the peer.
		// Missing key → assume the cluster default ("full") and treat
		// as verified, which mirrors what bootstrap defaults to.
		mode := strings.ToLower(strings.TrimSpace(readStringSetting(n.Settings, "xpack.security.transport.ssl.verification_mode")))
		if mode == "" {
			mode = strings.ToLower(strings.TrimSpace(readStringSetting(n.Settings, "transport.ssl.verification_mode")))
		}
		verified := mode != "none"
		if !enabled {
			allEnabled = false
		}
		if !verified {
			allVerified = false
		}
	}
	return types.TransportTLS{Enabled: allEnabled, Verified: allEnabled && allVerified}, nil
}

// HTTPTLS implements client.HTTPTLSInspector for ES. Reads
// /_nodes/settings?flat_settings=true and aggregates the per-node
// HTTP-layer TLS state across every reachable node. Sibling of
// TransportTLS — the transport layer is node-to-node, the HTTP layer
// is what an external client connects to, so the two endpoints answer
// independent questions and each gets its own inspector.
//
// Aggregation:
//   - Enabled is true only when every node reports HTTP TLS as on. One
//     misconfigured node is exactly what the rule has to flag, so the
//     all-or-nothing rule mirrors TransportTLS.
//   - ClientAuth is the homogeneous value when every node agrees;
//     "mixed" when they disagree (the disagreement is the finding).
//   - Protocols and CipherSuites are the sorted, deduplicated union
//     across nodes. The attacker picks the weakest accepted by any
//     node, so the rule evaluates the union, not the intersection.
//
// PerNode is populated only when nodes disagree on Enabled or
// ClientAuth, or when their Protocols/CipherSuites differ — operators
// reading a uniformly-configured fleet only need the aggregate, and
// inflating the JSON with redundant per-node rows on every audit
// would be noise.
//
// Reads from the same /_nodes/settings snapshot TransportTLS uses
// (cachedNodesSettings); a doctor running both rules pays one
// round trip rather than two. The view is operator-only — cluster-
// default ciphers/protocols are JVM-baked and not exposed through
// any /_nodes or /_cluster API, so a rule that flags a weak default
// has to know the dialect's defaults out-of-band rather than reading
// them off the wire here.
func (c *Client) HTTPTLS(ctx context.Context) (types.HTTPTLSPosture, error) {
	wire, err := c.cachedNodesSettings(ctx)
	if err != nil {
		return types.HTTPTLSPosture{}, err
	}
	if len(wire.Nodes) == 0 {
		return types.HTTPTLSPosture{}, nil
	}

	per := make([]types.NodeTLSPosture, 0, len(wire.Nodes))
	for _, n := range wire.Nodes {
		enabled := readBoolSetting(n.Settings, "xpack.security.http.ssl.enabled") ||
			readBoolSetting(n.Settings, "http.ssl.enabled")
		// client_authentication: "required" | "optional" | "none".
		// Default is "none" (HTTP-layer client auth is opt-in even
		// when SSL is on); we report "" when the operator did not
		// set it so the aggregate can distinguish "explicitly none"
		// from "unset".
		clientAuth := strings.ToLower(strings.TrimSpace(readStringSetting(n.Settings, "xpack.security.http.ssl.client_authentication")))
		if clientAuth == "" {
			clientAuth = strings.ToLower(strings.TrimSpace(readStringSetting(n.Settings, "http.ssl.client_authentication")))
		}
		protocols := readSliceSetting(n.Settings, "xpack.security.http.ssl.supported_protocols")
		if len(protocols) == 0 {
			protocols = readSliceSetting(n.Settings, "http.ssl.supported_protocols")
		}
		ciphers := readSliceSetting(n.Settings, "xpack.security.http.ssl.cipher_suites")
		if len(ciphers) == 0 {
			ciphers = readSliceSetting(n.Settings, "http.ssl.cipher_suites")
		}
		per = append(per, types.NodeTLSPosture{
			Name:         n.Name,
			Enabled:      enabled,
			ClientAuth:   clientAuth,
			Protocols:    protocols,
			CipherSuites: ciphers,
		})
	}
	return aggregateHTTPTLS(per), nil
}

// aggregateHTTPTLS collapses a per-node slice into the cluster-side
// HTTPTLSPosture. Lives outside HTTPTLS so the OS adapter can share
// it: aggregation is dialect-independent, the dialect-specific work
// is in mapping the cluster's flat-settings vocabulary to NodeTLSPosture.
//
// PerNode is populated only when the nodes are not unanimous on
// every field — a uniformly-configured fleet renders a clean
// aggregate without the redundant per-node block.
func aggregateHTTPTLS(per []types.NodeTLSPosture) types.HTTPTLSPosture {
	if len(per) == 0 {
		return types.HTTPTLSPosture{}
	}
	sort.Slice(per, func(i, j int) bool { return per[i].Name < per[j].Name })

	allEnabled := true
	clientAuth := per[0].ClientAuth
	clientAuthMixed := false
	protoSet := map[string]struct{}{}
	cipherSet := map[string]struct{}{}
	disagree := false
	for i, n := range per {
		if !n.Enabled {
			allEnabled = false
		}
		if n.ClientAuth != clientAuth {
			clientAuthMixed = true
		}
		for _, p := range n.Protocols {
			protoSet[p] = struct{}{}
		}
		for _, c := range n.CipherSuites {
			cipherSet[c] = struct{}{}
		}
		if i > 0 {
			prev := per[i-1]
			if prev.Enabled != n.Enabled || prev.ClientAuth != n.ClientAuth ||
				!stringSliceEqual(prev.Protocols, n.Protocols) ||
				!stringSliceEqual(prev.CipherSuites, n.CipherSuites) {
				disagree = true
			}
		}
	}
	out := types.HTTPTLSPosture{Enabled: allEnabled}
	if clientAuthMixed {
		out.ClientAuth = "mixed"
	} else {
		out.ClientAuth = clientAuth
	}
	out.Protocols = sortedKeys(protoSet)
	out.CipherSuites = sortedKeys(cipherSet)
	if disagree {
		out.PerNode = per
	}
	return out
}

// stringSliceEqual reports whether two string slices have the same
// elements in the same order. Used by aggregateHTTPTLS to detect
// per-node disagreement on protocol / cipher lists; the input slices
// arrive sorted from readSliceSetting so order-sensitive comparison
// is safe.
func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sortedKeys returns the map's keys in alphabetic order. Stable
// output is what scripts and golden tests expect; the union order
// across nodes is otherwise nondeterministic.
func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// readSliceSetting pulls a flat-key setting whose value is a list of
// strings. With flat_settings=true the cluster keeps list values as
// JSON arrays under the dotted-path key, so the decoder hands us an
// []any. Non-string elements are skipped — a malformed setting
// shouldn't crash the audit; the aggregate just sees fewer entries.
//
// Returned slices are sorted so callers can compare per-node lists by
// equality without taking on the cluster's emission order.
func readSliceSetting(m map[string]any, key string) []string {
	if m == nil {
		return nil
	}
	raw, ok := m[key]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// readBoolSetting pulls a flat-key setting and tolerates the cluster's
// quirk of reporting bools as either real JSON booleans or the strings
// "true"/"false" depending on which subsystem set the key. Missing
// keys return false — the cluster default for every transport-TLS knob
// we inspect is "off", so absence is a meaningful signal.
func readBoolSetting(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	raw, ok := m[key]
	if !ok {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		b, err := strconv.ParseBool(v)
		if err != nil {
			return false
		}
		return b
	}
	return false
}

// readStringSetting pulls a flat-key setting as a string; missing keys
// or non-string values return "". The cluster reports verification_mode
// as a plain string, so this is sufficient for the keys we inspect.
func readStringSetting(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	raw, ok := m[key]
	if !ok {
		return ""
	}
	if s, ok := raw.(string); ok {
		return s
	}
	return ""
}
