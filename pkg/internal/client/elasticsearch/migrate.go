package elasticsearch

import (
	"encoding/json"
	"errors"
	"strings"

	"context"

	"github.com/elastic/go-elasticsearch/v9/esapi"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// mappingGetWire mirrors the cluster's GET /<indices>/_mapping envelope.
// The cluster keys the response by index name; each entry has a
// `mappings` block whose `properties` / `_meta` / `dynamic` fields are
// the parts esops looks at. Properties is intentionally left as
// map[string]any — the migrate translator walks the tree and there is
// no value in re-typing every field.
type mappingGetWire map[string]struct {
	Mappings struct {
		Properties map[string]any  `json:"properties"`
		Meta       map[string]any  `json:"_meta"`
		Dynamic    json.RawMessage `json:"dynamic"`
	} `json:"mappings"`
}

// GetMappings implements client.MappingsInspector.
func (c *Client) GetMappings(ctx context.Context, filter types.MappingFilter) ([]types.IndexMapping, error) {
	gm := c.api.Indices.GetMapping
	opts := []func(*esapi.IndicesGetMappingRequest){gm.WithContext(ctx)}
	if filter.Index != "" {
		opts = append(opts, gm.WithIndex(esopsclient.SplitCSV(filter.Index)...))
	}
	if filter.IgnoreUnavailable {
		opts = append(opts, gm.WithIgnoreUnavailable(true))
	}

	resp, err := c.api.Indices.GetMapping(opts...)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("indices get_mapping", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "indices get_mapping")
	var wire mappingGetWire
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding indices get_mapping response"), err)
	}

	out := make([]types.IndexMapping, 0, len(wire))
	for name, entry := range wire {
		out = append(out, types.IndexMapping{
			Name:       name,
			Properties: entry.Mappings.Properties,
			Meta:       entry.Mappings.Meta,
			Dynamic:    decodeDynamic(entry.Mappings.Dynamic),
		})
	}
	return out, nil
}

// decodeDynamic flattens the cluster's `dynamic` field, which arrives as
// either a string ("true", "strict", "false", "runtime") or a JSON
// boolean depending on how it was set. Empty when absent.
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

// RemoteReindexWhitelist implements client.RemoteReindexInspector by
// reading /_cluster/settings?flat_settings=true&include_defaults=true
// and pulling `reindex.remote.whitelist` from each scope. The cluster
// reports the value as either a comma-separated string or a JSON array
// depending on how it was set; both shapes are flattened to a clean
// []string here.
func (c *Client) RemoteReindexWhitelist(ctx context.Context) (types.RemoteReindexWhitelist, error) {
	gs := c.api.Cluster.GetSettings
	resp, err := c.api.Cluster.GetSettings(
		gs.WithContext(ctx),
		gs.WithFlatSettings(true),
		gs.WithIncludeDefaults(true),
	)
	if err != nil {
		return types.RemoteReindexWhitelist{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cluster get_settings (whitelist)", resp.StatusCode, resp.Body); err != nil {
		return types.RemoteReindexWhitelist{}, err
	}

	var wire struct {
		Persistent map[string]any `json:"persistent"`
		Transient  map[string]any `json:"transient"`
		Defaults   map[string]any `json:"defaults"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.RemoteReindexWhitelist{}, errors.Join(errors.New("decoding cluster settings response"), err)
	}
	return types.RemoteReindexWhitelist{
		Persistent: parseRemoteWhitelist(wire.Persistent),
		Transient:  parseRemoteWhitelist(wire.Transient),
		Defaults:   parseRemoteWhitelist(wire.Defaults),
	}, nil
}

// parseRemoteWhitelist extracts `reindex.remote.whitelist` from a flat
// settings map. The cluster serialises the value as a string when set
// via the `host:port,host:port` shorthand, and as a slice when set via
// the JSON-array form. Both are normalised to []string with whitespace
// trimmed; an empty/missing key returns nil.
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

// splitTrim splits "a, b ,c" into []string{"a","b","c"}, dropping
// empty entries. Used by parseRemoteWhitelist where hand-edited
// cluster settings have been seen carrying stray spaces.
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
