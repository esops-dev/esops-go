package opensearch

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	osapi "github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// nodesInfoBootstrapWire mirrors the slice of /_nodes (info) we read for
// bootstrap-check posture. /_nodes/process carries `mlockall` and
// `max_file_descriptors`; /_nodes/jvm carries `mem.using_memory_lock`.
// Both fields arrive in one response when we ask for the
// `process,jvm` metric pair. The OpenSearch typed NodesInfoResp does
// not model max_file_descriptors on Process, so we parse the wire
// shape ourselves rather than mix typed + raw decode for a single
// missing field.
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

// nodesInfoTransportTLSWire mirrors the slice of /_nodes/settings we
// inspect for transport-layer TLS posture. flat_settings=true makes
// the keys arrive as dotted paths; we look at the OpenSearch security
// plugin's `plugins.security.ssl.transport.*` keys (and the legacy
// `opendistro_security.ssl.transport.*` aliases the plugin still
// honours on rolling upgrades) so the answer covers both pre- and
// post-rebrand clusters.
type nodesInfoTransportTLSWire struct {
	Nodes map[string]struct {
		Name     string         `json:"name"`
		Settings map[string]any `json:"settings"`
	} `json:"nodes"`
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
func (c *Client) TransportTLS(ctx context.Context) (types.TransportTLS, error) {
	body, status, err := c.doSecurityGET(ctx, "/_nodes/settings?flat_settings=true")
	if err != nil {
		return types.TransportTLS{}, err
	}
	if status >= 400 {
		return types.TransportTLS{}, clientutil.ClassifyStatus("nodes settings (transport tls)", status, body)
	}

	var wire nodesInfoTransportTLSWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return types.TransportTLS{}, errors.Join(errors.New("decoding nodes settings (transport tls) response"), err)
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
