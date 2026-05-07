package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/elastic/go-elasticsearch/v9/esapi"

	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// clusterSettingsPutWire mirrors the PUT /_cluster/settings response
// envelope. The cluster echoes the post-update persistent and
// transient maps so the operator sees the resulting state without a
// follow-up GET.
type clusterSettingsPutWire struct {
	Acknowledged bool           `json:"acknowledged"`
	Persistent   map[string]any `json:"persistent"`
	Transient    map[string]any `json:"transient"`
}

// PutClusterSettings implements client.ClusterSettingsPutter. The body
// is the minimal envelope the cluster needs: one or both scopes, each
// a flat map of dotted keys to values. flat_settings=true is sent so
// the cluster accepts dotted paths verbatim — no nested-object dance
// from the consumer's side.
//
// An empty Persistent map (and likewise Transient) is omitted from the
// body so the cluster does not record an empty-scope no-op write —
// keeps /_cluster/settings free of empty {"persistent":{}} entries
// that other observability paths might surface as "the operator
// cleared persistent settings".
func (c *Client) PutClusterSettings(ctx context.Context, req types.ClusterSettingsPutRequest) (types.ClusterSettingsPutResult, error) {
	body := map[string]any{}
	if len(req.Persistent) > 0 {
		body["persistent"] = req.Persistent
	}
	if len(req.Transient) > 0 {
		body["transient"] = req.Transient
	}
	if len(body) == 0 {
		return types.ClusterSettingsPutResult{}, fmt.Errorf("PutClusterSettings: at least one scope must carry a setting")
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return types.ClusterSettingsPutResult{}, fmt.Errorf("encoding cluster put_settings body: %w", err)
	}

	ps := c.api.Cluster.PutSettings
	resp, err := c.api.Cluster.PutSettings(
		bytes.NewReader(raw),
		ps.WithContext(ctx),
		ps.WithFlatSettings(true),
	)
	if err != nil {
		return types.ClusterSettingsPutResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cluster put_settings (full)", resp.StatusCode, resp.Body); err != nil {
		return types.ClusterSettingsPutResult{}, err
	}

	var wire clusterSettingsPutWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.ClusterSettingsPutResult{}, errors.Join(errors.New("decoding cluster put_settings response"), err)
	}
	return types.ClusterSettingsPutResult{
		Acknowledged: wire.Acknowledged,
		Persistent:   wire.Persistent,
		Transient:    wire.Transient,
	}, nil
}

// ClearCache implements client.IndexCacheClearer via POST
// /<indices>/_cache/clear. The upstream client's IndicesClearCache
// accepts per-cache toggles and an optional Fields list for the
// fielddata-narrowing case; we forward the request unchanged so the
// cluster's own default (clear every cache when no per-cache flag is
// set) is what an operator who passed neither --fielddata nor --query
// nor --request gets.
func (c *Client) ClearCache(ctx context.Context, req types.IndexCacheClearRequest) (types.IndexCacheClearResult, error) {
	cc := c.api.Indices.ClearCache
	opts := []func(*esapi.IndicesClearCacheRequest){cc.WithContext(ctx)}
	if len(req.Indices) > 0 {
		opts = append(opts, cc.WithIndex(req.Indices...))
	}
	if req.Fielddata {
		opts = append(opts, cc.WithFielddata(true))
		if len(req.Fields) > 0 {
			opts = append(opts, cc.WithFields(req.Fields...))
		}
	}
	if req.Query {
		opts = append(opts, cc.WithQuery(true))
	}
	if req.Request {
		opts = append(opts, cc.WithRequest(true))
	}
	if req.IgnoreUnavailable {
		opts = append(opts, cc.WithIgnoreUnavailable(true))
	}

	resp, err := c.api.Indices.ClearCache(opts...)
	if err != nil {
		return types.IndexCacheClearResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("indices clear_cache", resp.StatusCode, resp.Body); err != nil {
		return types.IndexCacheClearResult{}, err
	}

	var wire struct {
		Shards struct {
			Total      int `json:"total"`
			Successful int `json:"successful"`
			Failed     int `json:"failed"`
		} `json:"_shards"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.IndexCacheClearResult{}, errors.Join(errors.New("decoding indices clear_cache response"), err)
	}
	return types.IndexCacheClearResult{
		Action:     "cache-clear",
		Indices:    req.Indices,
		Total:      wire.Shards.Total,
		Successful: wire.Shards.Successful,
		Failed:     wire.Shards.Failed,
	}, nil
}
