package opensearch

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	osapi "github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// nodesInfoBootstrapWire mirrors the slice of /_nodes (info) we read for
// bootstrap-check posture and JVM ergonomics. /_nodes/process carries
// `mlockall` and `max_file_descriptors`; /_nodes/jvm carries
// `mem.using_memory_lock` plus `gc_collectors[]` and
// `input_arguments[]` for the jvm_gc_ergonomics rule. Every metric
// arrives in one response when we ask for the `process,jvm` metric
// pair. The OpenSearch typed NodesInfoResp does not model
// max_file_descriptors on Process, so we parse the wire shape
// ourselves rather than mix typed + raw decode for a single missing
// field.
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
	} `json:"nodes"`
}

// NodeBootstrap implements client.NodeBootstrapInspector. Same shape as
// the ES adapter — /_nodes/process,jvm gives mlockall +
// max_file_descriptors + the JVM-side using_memory_lock cross-check
// in one round trip on every supported version. Either signal proves
// the node honoured bootstrap.memory_lock; we OR them so a stale
// process flag on a known-locked node doesn't get reported as
// "unlocked".
//
// vm.max_map_count is not exposed via /_nodes (it's a kernel reading
// the cluster does not surface back), so MaxMapCount stays zero.
// BootstrapWarnings stays empty here too: bootstrap-check failure
// strings are in cluster startup logs, not /_nodes — for that signal
// the doctor catalog points at /_migration/deprecations on ES, and
// OpenSearch doesn't expose anything analogous.
//
// GCCollectors and JVMInputArguments come from the same response body
// — `jvm.gc_collectors[]` and `jvm.input_arguments[]`. OS 2.x defaults
// to G1GC; the rule that consumes these fields flags CMS (deprecated
// upstream) and manual G1 overrides regardless of dialect.
func (c *Client) NodeBootstrap(ctx context.Context) ([]types.NodeBootstrap, error) {
	body, status, err := c.doSecurityGET(ctx, "/_nodes/process,jvm")
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
		mlock := n.Process.Mlockall || n.JVM.Mem.UsingMemoryLock
		out = append(out, types.NodeBootstrap{
			Name:               n.Name,
			MlockallEnabled:    mlock,
			MaxFileDescriptors: n.Process.MaxFileDescriptors,
			GCCollectors:       n.JVM.GCCollectors,
			JVMInputArguments:  n.JVM.InputArguments,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// GetClusterSettingsFull implements client.ClusterSettingsFullInspector.
// flat_settings=true is always on so the returned maps key on dotted
// paths; includeDefaults flips ?include_defaults to surface the
// cluster's default tree when a drift/awareness rule needs it.
func (c *Client) GetClusterSettingsFull(ctx context.Context, includeDefaults bool) (types.ClusterSettingsFull, error) {
	flat := true
	defaults := includeDefaults
	req := &osapi.ClusterGetSettingsReq{
		Params: osapi.ClusterGetSettingsParams{
			FlatSettings:    &flat,
			IncludeDefaults: &defaults,
		},
	}
	resp, err := c.api.Cluster.GetSettings(ctx, req)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.ClusterSettingsFull{}, classifyResponseErr(err, status)
	}

	persistent, err := unmarshalSettingsScope(resp.Persistent)
	if err != nil {
		return types.ClusterSettingsFull{}, err
	}
	transient, err := unmarshalSettingsScope(resp.Transient)
	if err != nil {
		return types.ClusterSettingsFull{}, err
	}
	defaultsMap, err := unmarshalSettingsScope(resp.Defaults)
	if err != nil {
		return types.ClusterSettingsFull{}, err
	}
	out := types.ClusterSettingsFull{
		Persistent: persistent,
		Transient:  transient,
	}
	// Only populate Defaults when the caller asked for it; an empty
	// map vs. nil is the signal that include_defaults was on but the
	// cluster reported nothing, which never happens in practice but is
	// preserved here in case it ever does.
	if includeDefaults {
		out.Defaults = defaultsMap
	}
	return out, nil
}

// AllocationByNode implements client.AllocationInspector via
// /_cat/allocation. Uses the typed CatAllocationsResp which carries
// per-node shards as a stringly-typed int — the cluster always emits
// "shards" as a JSON string, so the upstream's `,string` tag is
// load-bearing rather than a quirk we work around. Disk fields are
// pointer-to-string because /_cat/allocation legitimately omits them
// for the orphan-shard pseudo-row; we drop that row here so the slice
// is keyed by real nodes only.
func (c *Client) AllocationByNode(ctx context.Context) ([]types.NodeAllocation, error) {
	req := &osapi.CatAllocationReq{
		Params: osapi.CatAllocationParams{
			Bytes: "b",
			H: []string{
				"shards", "disk.indices", "disk.used", "disk.avail",
				"disk.total", "disk.percent", "host", "ip", "node",
			},
		},
	}
	resp, err := c.api.Cat.Allocation(ctx, req)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return nil, classifyResponseErr(err, status)
	}

	out := make([]types.NodeAllocation, 0, len(resp.Allocations))
	for _, r := range resp.Allocations {
		if r.Node == "" || strings.EqualFold(r.Node, "UNASSIGNED") {
			continue
		}
		entry := types.NodeAllocation{
			Name:             r.Node,
			Shards:           r.Shards,
			DiskIndicesBytes: parseInt64(deref(r.DiskIndices)),
			DiskUsedBytes:    parseInt64(deref(r.DiskUsed)),
			DiskAvailBytes:   parseInt64(deref(r.DiskAvail)),
			DiskTotalBytes:   parseInt64(deref(r.DiskTotal)),
			DiskPercent:      intPtrValue(r.DiskPercent),
			Host:             deref(r.Host),
			IP:               deref(r.IP),
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// nodesSettingsWire mirrors the slice of /_nodes/settings consumed by
// every per-node static-settings inspector — TransportTLS, HTTPTLS,
// and NodeSettings. flat_settings=true makes the keys arrive as
// dotted paths; the TLS inspectors read both
// `plugins.security.ssl.{transport,http}.*` keys and the legacy
// `opendistro_security.ssl.{transport,http}.*` aliases (the plugin
// still honours both on rolling upgrades), while NodeSettings hands
// the full per-node map through unchanged for downstream rules to
// inspect any dotted-path key (network.host, node.store.allow_mmap,
// discovery.*).
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
// is /_cluster/settings only, and the security plugin's SSL settings
// don't surface through cluster settings either. The view here is
// operator-only by design; rules that need to flag a weak default
// have to know the dialect's defaults out-of-band.
//
// filter_path stops at `nodes.*.settings` rather than drilling deeper
// into specific keypaths. With flat_settings=true the keys are
// literal dotted strings (not JSON-path segments), so a filter_path
// matching them as paths returns nothing — see the matching ES
// adapter comment.
const nodesSettingsPath = "/_nodes/settings?flat_settings=true&filter_path=" +
	"nodes.*.name,nodes.*.settings"

const nodesSettingsTTLDefault = 2 * time.Second

// cachedNodesSettings issues at most one /_nodes/settings GET per TTL
// window per Client. TransportTLS, HTTPTLS, and NodeSettings all call
// through here, so a doctor running every static-settings rule in
// sequence pays one round trip rather than three. Errors are not
// cached: a transient transport failure should not mask a recovered
// cluster on the next call.
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
	body, status, err := c.doSecurityGET(ctx, nodesSettingsPath)
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

// NodeSettings implements client.NodeSettingsInspector for OS. Hands
// the cached /_nodes/settings snapshot through as a stable
// []NodeSettings slice keyed alphabetically by node name. Settings
// are returned verbatim from the cluster (flat_settings=true) so
// downstream rules read dotted-path keys directly without walking
// nested objects.
//
// Empty cluster (no reachable nodes) returns nil, mirroring the ES
// adapter — rules treat absence as "no signal", not "the cluster
// reported zero nodes".
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

// TransportTLS implements client.TransportTLSInspector for OpenSearch.
// Aggregation is all-or-nothing across the reachable node set, the
// same way the ES adapter does it: Enabled is true only when every
// node reports plugins.security.ssl.transport.enabled, and Verified is
// true only when every node has hostname verification enforced.
//
// OpenSearch's security plugin does not expose a "ssl transport
// enabled" flag in the same shape ES does; transport TLS is on
// whenever the plugin is configured at all. We treat the presence of
// any `plugins.security.ssl.transport.*` keypath as evidence of an
// active transport-TLS config, with the keystore/pemcert keys as the
// strongest signal — operators who set those have transport TLS on
// by construction.
//
// Reads from the same /_nodes/settings snapshot HTTPTLS uses
// (cachedNodesSettings); a doctor running both rules pays one
// round trip rather than two.
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
		enabled := osTransportTLSEnabled(n.Settings)
		// enforce_hostname_verification defaults to true on the OS
		// security plugin; only an explicit "false" disables it.
		// Missing key → assume verified (cluster default).
		verified := osHostnameVerification(n.Settings)
		if !enabled {
			allEnabled = false
		}
		if !verified {
			allVerified = false
		}
	}
	return types.TransportTLS{Enabled: allEnabled, Verified: allEnabled && allVerified}, nil
}

// osTransportTLSEnabled returns true when the node's flat-settings map
// shows any sign of an active transport-TLS configuration. The OS
// security plugin doesn't ship a single "transport.enabled" toggle the
// way ES does; the operator-facing knob is the keystore/pemcert pair.
// We accept either the modern `plugins.security.*` keys or the legacy
// `opendistro_security.*` aliases — the plugin still honours both on
// rolling upgrades.
func osTransportTLSEnabled(m map[string]any) bool {
	if m == nil {
		return false
	}
	signals := []string{
		"plugins.security.ssl.transport.keystore_filepath",
		"plugins.security.ssl.transport.pemcert_filepath",
		"plugins.security.ssl.transport.pemkey_filepath",
		"plugins.security.ssl.transport.truststore_filepath",
		"plugins.security.ssl.transport.pemtrustedcas_filepath",
		"opendistro_security.ssl.transport.keystore_filepath",
		"opendistro_security.ssl.transport.pemcert_filepath",
		"opendistro_security.ssl.transport.pemkey_filepath",
		"opendistro_security.ssl.transport.truststore_filepath",
		"opendistro_security.ssl.transport.pemtrustedcas_filepath",
	}
	for _, k := range signals {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return true
			}
		}
	}
	return false
}

// HTTPTLS implements client.HTTPTLSInspector for OpenSearch. Sibling
// of TransportTLS — the transport layer is node-to-node, the HTTP
// layer is what an external client (esops itself, Dashboards,
// application traffic) connects to, so the two endpoints answer
// independent questions and each gets its own inspector. Reads
// /_nodes/settings?flat_settings=true and aggregates the OS security
// plugin's `plugins.security.ssl.http.*` keys (with the legacy
// `opendistro_security.ssl.http.*` aliases the plugin still honours
// on rolling upgrades) across every reachable node.
//
// Aggregation:
//   - Enabled is true only when every node reports HTTP TLS as on.
//   - ClientAuth is the homogeneous value when nodes agree; "mixed"
//     when they disagree.
//   - Protocols and CipherSuites are the sorted, deduplicated union
//     across nodes (the attacker picks the weakest accepted by any
//     node, so the rule evaluates the union, not the intersection).
//
// PerNode is populated only when nodes disagree on Enabled or
// ClientAuth, or on their Protocols/CipherSuites lists.
//
// Unlike the transport layer (which OS treats as "on whenever the
// plugin is configured"), the HTTP layer has a real `enabled` toggle
// — `plugins.security.ssl.http.enabled` — and its default is false,
// so a missing or false value means an unauthenticated 9200 listener.
// We honour that toggle directly rather than inferring from keystore
// presence; the rule needs to flag exactly the case where the plugin
// is configured for transport but the operator forgot to enable the
// HTTP layer.
//
// Reads from the same /_nodes/settings snapshot TransportTLS uses
// (cachedNodesSettings).
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
		per = append(per, types.NodeTLSPosture{
			Name:         n.Name,
			Enabled:      osHTTPTLSEnabled(n.Settings),
			ClientAuth:   osHTTPClientAuth(n.Settings),
			Protocols:    osReadSliceSetting(n.Settings, "plugins.security.ssl.http.enabled_protocols", "opendistro_security.ssl.http.enabled_protocols"),
			CipherSuites: osReadSliceSetting(n.Settings, "plugins.security.ssl.http.enabled_ciphers", "opendistro_security.ssl.http.enabled_ciphers"),
		})
	}
	return aggregateOSHTTPTLS(per), nil
}

// osHTTPTLSEnabled reads `plugins.security.ssl.http.enabled` (and the
// legacy alias). The plugin's default is false — the operator must
// explicitly turn HTTP TLS on, even when transport TLS is configured
// — so a missing key is "off", which is the exact case the rule has
// to flag.
func osHTTPTLSEnabled(m map[string]any) bool {
	if m == nil {
		return false
	}
	for _, k := range []string{
		"plugins.security.ssl.http.enabled",
		"opendistro_security.ssl.http.enabled",
	} {
		raw, ok := m[k]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case bool:
			return v
		case string:
			b, err := strconv.ParseBool(v)
			if err == nil {
				return b
			}
		}
	}
	return false
}

// osHTTPClientAuth reads `plugins.security.ssl.http.clientauth_mode`
// (and the legacy alias) and normalises the cluster's value to the
// shared "required" / "optional" / "none" vocabulary. The plugin
// emits "REQUIRE" / "OPTIONAL" / "NONE"; we lowercase and map
// "require" → "required" so a CEL rule comparing the ES and OS sides
// uses one set of strings. Empty when the operator did not set the
// key.
func osHTTPClientAuth(m map[string]any) string {
	if m == nil {
		return ""
	}
	var raw string
	for _, k := range []string{
		"plugins.security.ssl.http.clientauth_mode",
		"opendistro_security.ssl.http.clientauth_mode",
	} {
		v, ok := m[k]
		if !ok {
			continue
		}
		if s, ok := v.(string); ok {
			raw = s
			break
		}
	}
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "require":
		return "required"
	case "optional", "none", "required":
		return mode
	default:
		return mode
	}
}

// osReadSliceSetting pulls the first present flat-key list value from
// the candidate keys. With flat_settings=true the cluster keeps list
// values as JSON arrays under the dotted path; non-string elements are
// skipped so a malformed setting can't crash the audit. Returned
// slices are sorted so callers can compare per-node lists by equality
// without taking on the cluster's emission order.
func osReadSliceSetting(m map[string]any, keys ...string) []string {
	if m == nil {
		return nil
	}
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		arr, ok := raw.([]any)
		if !ok {
			continue
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
	return nil
}

// aggregateOSHTTPTLS collapses the per-node slice into the aggregate
// HTTPTLSPosture. Mirrors the ES adapter's aggregator — kept here as
// a private duplicate to avoid an adapter-to-adapter import. The
// shape is dialect-independent; the only thing each adapter owns is
// the per-node mapping that produced the input slice.
func aggregateOSHTTPTLS(per []types.NodeTLSPosture) types.HTTPTLSPosture {
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
				!osStringSliceEqual(prev.Protocols, n.Protocols) ||
				!osStringSliceEqual(prev.CipherSuites, n.CipherSuites) {
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
	out.Protocols = osSortedKeys(protoSet)
	out.CipherSuites = osSortedKeys(cipherSet)
	if disagree {
		out.PerNode = per
	}
	return out
}

func osStringSliceEqual(a, b []string) bool {
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

func osSortedKeys(m map[string]struct{}) []string {
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

// osHostnameVerification reads
// `plugins.security.ssl.transport.enforce_hostname_verification` (and
// the legacy alias) and returns true unless the operator explicitly
// turned it off. The plugin's default is true, so a missing key is a
// verified node, not an unverified one.
func osHostnameVerification(m map[string]any) bool {
	keys := []string{
		"plugins.security.ssl.transport.enforce_hostname_verification",
		"opendistro_security.ssl.transport.enforce_hostname_verification",
	}
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case bool:
			return v
		case string:
			b, err := strconv.ParseBool(v)
			if err == nil {
				return b
			}
		}
	}
	return true
}
