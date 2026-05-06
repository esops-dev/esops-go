package opensearch

import (
	"encoding/json"
	"errors"
	"strings"

	"context"

	osapi "github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// mappingsBlock mirrors the cluster's per-index `mappings` payload —
// the same shape on ES and OS. Properties is intentionally left as
// map[string]any: the migrate translator walks the tree, so re-typing
// every field would just create a second representation to keep in
// sync with the cluster's.
type mappingsBlock struct {
	Properties map[string]any  `json:"properties"`
	Meta       map[string]any  `json:"_meta"`
	Dynamic    json.RawMessage `json:"dynamic"`
}

// GetMappings implements client.MappingsInspector. The upstream typed
// MappingGetReq returns the per-index `mappings` block as a
// json.RawMessage; we decode each one here so the neutral
// IndexMapping shape matches the ES adapter's output.
func (c *Client) GetMappings(ctx context.Context, filter types.MappingFilter) ([]types.IndexMapping, error) {
	req := &osapi.MappingGetReq{}
	if filter.Index != "" {
		req.Indices = esopsclient.SplitCSV(filter.Index)
	}
	if filter.IgnoreUnavailable {
		v := true
		req.Params.IgnoreUnavailable = &v
	}

	resp, err := c.api.Indices.Mapping.Get(ctx, req)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return nil, classifyResponseErr(err, status)
	}

	out := make([]types.IndexMapping, 0, len(resp.Indices))
	for name, entry := range resp.Indices {
		var mb mappingsBlock
		if len(entry.Mappings) > 0 {
			if err := json.Unmarshal(entry.Mappings, &mb); err != nil {
				return nil, errors.Join(errors.New("decoding mappings block for "+name), err)
			}
		}
		out = append(out, types.IndexMapping{
			Name:       name,
			Properties: mb.Properties,
			Meta:       mb.Meta,
			Dynamic:    decodeDynamic(mb.Dynamic),
		})
	}
	return out, nil
}

// decodeDynamic flattens the cluster's `dynamic` field. The cluster
// reports it as either a string ("true", "strict", "false", "runtime")
// or a JSON boolean depending on how it was set. Empty when absent.
func decodeDynamic(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		if b {
			return "true"
		}
		return "false"
	}
	return strings.Trim(string(raw), `"`)
}

// RemoteReindexWhitelist implements client.RemoteReindexInspector. The
// shape of /_cluster/settings is identical on ES and OS when
// flat_settings=true; this method differs from the ES adapter only in
// the upstream-client API surface.
func (c *Client) RemoteReindexWhitelist(ctx context.Context) (types.RemoteReindexWhitelist, error) {
	flat := true
	includeDefaults := true
	req := &osapi.ClusterGetSettingsReq{
		Params: osapi.ClusterGetSettingsParams{
			FlatSettings:    &flat,
			IncludeDefaults: &includeDefaults,
		},
	}
	resp, err := c.api.Cluster.GetSettings(ctx, req)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.RemoteReindexWhitelist{}, classifyResponseErr(err, status)
	}

	persistent, err := unmarshalSettingsScope(resp.Persistent)
	if err != nil {
		return types.RemoteReindexWhitelist{}, err
	}
	transient, err := unmarshalSettingsScope(resp.Transient)
	if err != nil {
		return types.RemoteReindexWhitelist{}, err
	}
	defaults, err := unmarshalSettingsScope(resp.Defaults)
	if err != nil {
		return types.RemoteReindexWhitelist{}, err
	}
	return types.RemoteReindexWhitelist{
		Persistent: parseRemoteWhitelist(persistent),
		Transient:  parseRemoteWhitelist(transient),
		Defaults:   parseRemoteWhitelist(defaults),
	}, nil
}

// parseRemoteWhitelist mirrors the ES helper of the same name: pulls
// `reindex.remote.whitelist` from a flat settings map and normalises
// either the comma-string or JSON-array shape into a clean []string.
func parseRemoteWhitelist(m map[string]any) []string {
	if m == nil {
		return nil
	}
	raw, ok := m["reindex.remote.whitelist"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case string:
		if v == "" {
			return nil
		}
		return splitTrim(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

func splitTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
