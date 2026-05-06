package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// nodesInfoBootstrapWire mirrors the slice of /_nodes (info) we read for
// bootstrap-check posture. /_nodes/process carries `mlockall` and
// `max_file_descriptors`; /_nodes/jvm carries `mem.using_memory_lock`
// (the same condition expressed from the JVM's side, which lines up
// with mlockall on a healthy node and is the field newer ES versions
// preserve when bootstrap.memory_lock is honoured). Both metrics arrive
// in one response when we ask for `process,jvm` together.
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
func (c *Client) NodeBootstrap(ctx context.Context) ([]types.NodeBootstrap, error) {
	body, status, err := c.doRawGET(ctx, "/_nodes/process,jvm")
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
		out = append(out, types.NodeBootstrap{
			Name:               n.Name,
			MlockallEnabled:    mlock,
			MaxFileDescriptors: n.Process.MaxFileDescriptors,
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

// nodesInfoTransportTLSWire mirrors the slice of /_nodes/settings we
// inspect for transport-layer TLS. flat_settings=true makes the keys
// arrive as dotted paths; we look for `xpack.security.transport.ssl.*`
// (ES 7+) and `transport.ssl.*` (legacy / pre-xpack rebrand) and merge
// the two so the answer doesn't depend on which configuration style
// the operator picked.
type nodesInfoTransportTLSWire struct {
	Nodes map[string]struct {
		Name     string         `json:"name"`
		Settings map[string]any `json:"settings"`
	} `json:"nodes"`
}

// TransportTLS implements client.TransportTLSInspector for ES. Reads
// /_nodes/settings?flat_settings=true and aggregates the per-node
// transport-TLS state across every reachable node. Enabled is true
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
	body, status, err := c.doRawGET(ctx, "/_nodes/settings?flat_settings=true")
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
