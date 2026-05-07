package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	osapi "github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"github.com/esops-dev/esops-go/pkg/types"
)

// PutClusterSettings implements client.ClusterSettingsPutter against
// /_cluster/settings on OpenSearch. The OS typed wrapper takes a Body
// io.Reader and a flat_settings query param; the body shape is the
// same {"persistent":{...},"transient":{...}} envelope ES accepts, so
// we share the request encoding with the ES adapter.
//
// An empty Persistent map (and likewise Transient) is omitted from the
// body so the cluster does not record an empty-scope no-op write.
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

	flat := true
	osReq := osapi.ClusterPutSettingsReq{
		Body: bytes.NewReader(raw),
		Params: osapi.ClusterPutSettingsParams{
			FlatSettings: &flat,
		},
	}
	resp, err := c.api.Cluster.PutSettings(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.ClusterSettingsPutResult{}, classifyResponseErr(err, status)
	}

	persistent, err := unmarshalSettingsScope(resp.Persistent)
	if err != nil {
		return types.ClusterSettingsPutResult{}, err
	}
	transient, err := unmarshalSettingsScope(resp.Transient)
	if err != nil {
		return types.ClusterSettingsPutResult{}, err
	}
	out := types.ClusterSettingsPutResult{
		Acknowledged: resp.Acknowledged,
	}
	if len(persistent) > 0 {
		out.Persistent = persistent
	}
	if len(transient) > 0 {
		out.Transient = transient
	}
	return out, nil
}

// ClearCache implements client.IndexCacheClearer via POST
// /<indices>/_cache/clear on OpenSearch. The OS typed wrapper exposes
// per-cache *bool toggles plus a Fields list; we forward the request
// shape unchanged so the cluster's own default (clear every cache when
// no per-cache flag is set) is what an operator who passed neither
// --fielddata nor --query nor --request gets.
//
// The OS typed response carries the {_shards:{...}} envelope directly —
// no second JSON decode needed, and the failures slice is dropped on
// the floor here because the result type only surfaces the count
// (Failed). A future renderer can pull the failures detail by going
// through the diag bundle path if it becomes useful.
func (c *Client) ClearCache(ctx context.Context, req types.IndexCacheClearRequest) (types.IndexCacheClearResult, error) {
	osReq := &osapi.IndicesClearCacheReq{
		Indices: req.Indices,
		Params:  osapi.IndicesClearCacheParams{},
	}
	if req.Fielddata {
		t := true
		osReq.Params.Fielddata = &t
		if len(req.Fields) > 0 {
			osReq.Params.Fields = req.Fields
		}
	}
	if req.Query {
		t := true
		osReq.Params.Query = &t
	}
	if req.Request {
		t := true
		osReq.Params.Request = &t
	}
	if req.IgnoreUnavailable {
		t := true
		osReq.Params.IgnoreUnavailable = &t
	}

	resp, err := c.api.Indices.ClearCache(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.IndexCacheClearResult{}, classifyResponseErr(err, status)
	}
	if resp == nil {
		return types.IndexCacheClearResult{}, errors.New("opensearch clear_cache: empty response")
	}
	return types.IndexCacheClearResult{
		Action:     "cache-clear",
		Indices:    req.Indices,
		Total:      resp.Shards.Total,
		Successful: resp.Shards.Successful,
		Failed:     resp.Shards.Failed,
	}, nil
}
