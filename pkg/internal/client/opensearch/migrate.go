package opensearch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"context"

	osapi "github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/client/reindexwire"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

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

	indices := resp.GetIndices()
	out := make([]types.IndexMapping, 0, len(indices))
	for name, entry := range indices {
		var mb reindexwire.MappingsBlock
		if len(entry.Mappings) > 0 {
			if err := json.Unmarshal(entry.Mappings, &mb); err != nil {
				return nil, errors.Join(errors.New("decoding mappings block for "+name), err)
			}
		}
		out = append(out, types.IndexMapping{
			Name:       name,
			Properties: mb.Properties,
			Meta:       mb.Meta,
			Dynamic:    reindexwire.DecodeDynamic(mb.Dynamic),
		})
	}
	return out, nil
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
		Persistent: reindexwire.ParseRemoteWhitelist(persistent),
		Transient:  reindexwire.ParseRemoteWhitelist(transient),
		Defaults:   reindexwire.ParseRemoteWhitelist(defaults),
	}, nil
}

// Count implements client.DocCounter via POST /<index>/_count. The OS
// typed Indices.Count helper drops the cluster's `_shards` block from
// its response struct, so the adapter falls back to the same path the
// ES adapter uses (Stream on the underlying transport) and decodes the
// envelope directly. Sharing the wire shape with the ES side keeps a
// captured CountResult identical across dialects.
func (c *Client) Count(ctx context.Context, req types.CountRequest) (types.CountResult, error) {
	params := osapi.IndicesCountParams{}
	if req.IgnoreUnavailable {
		v := true
		params.IgnoreUnavailable = &v
	}
	osReq := &osapi.IndicesCountReq{
		Indices: req.Indices,
		Params:  params,
	}
	httpReq, err := osReq.GetRequest(http.MethodPost)
	if err != nil {
		return types.CountResult{}, fmt.Errorf("opensearch count: build request: %w", err)
	}
	httpReq = httpReq.WithContext(ctx)

	resp, err := c.api.Client.Stream(httpReq)
	if err != nil {
		return types.CountResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return types.CountResult{}, fmt.Errorf("opensearch count: read body: %w", err)
	}
	if err := clientutil.ClassifyStatus("count", resp.StatusCode, body); err != nil {
		return types.CountResult{}, err
	}

	var wire reindexwire.CountWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return types.CountResult{}, errors.Join(errors.New("decoding count response"), err)
	}
	return wire.ToCountResult(), nil
}

// SubmitReindex implements client.Reindexer via POST /_reindex with
// ?wait_for_completion=false. Body shape is shared with the ES adapter
// via reindexwire.BuildReindexBody — both dialects accept the same
// /_reindex envelope, only the upstream client call differs.
func (c *Client) SubmitReindex(ctx context.Context, req types.ReindexSubmitRequest) (types.ReindexSubmit, error) {
	if strings.TrimSpace(req.SourceIndex) == "" || strings.TrimSpace(req.TargetIndex) == "" {
		return types.ReindexSubmit{}, esopsclient.UsageError("reindex: source_index and target_index are required")
	}
	body, err := reindexwire.BuildReindexBody(req)
	if err != nil {
		if errors.Is(err, reindexwire.ErrSchemeRequired) {
			return types.ReindexSubmit{}, esopsclient.UsageError("reindex: %v", err)
		}
		return types.ReindexSubmit{}, err
	}

	waitFalse := false
	params := osapi.ReindexParams{
		WaitForCompletion: &waitFalse,
	}
	if rps, ok := reindexwire.NormaliseRPS(req.RequestsPerSecond); ok {
		v := rps
		params.RequestsPerSecond = &v
	}
	if req.Slices != "" {
		params.Slices = req.Slices
	}
	if req.WaitForActiveShards != "" {
		params.WaitForActiveShards = req.WaitForActiveShards
	}

	osReq := osapi.ReindexReq{
		Body:   bytes.NewReader(body),
		Params: params,
	}
	resp, err := c.api.Reindex(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.ReindexSubmit{}, classifyResponseErr(err, status)
	}
	return types.ReindexSubmit{
		Action:      "migrate-reindex",
		TaskID:      resp.Task,
		SourceIndex: req.SourceIndex,
		TargetIndex: req.TargetIndex,
	}, nil
}

// ReindexTaskStatus implements client.Reindexer via GET /_tasks/<id>.
// Uses the underlying transport directly because the typed Tasks.Get
// response on opensearch-go doesn't decode the reindex status block —
// it stops at the runtime metrics common to every task action.
func (c *Client) ReindexTaskStatus(ctx context.Context, taskID string) (types.ReindexTaskStatus, error) {
	if strings.TrimSpace(taskID) == "" {
		return types.ReindexTaskStatus{}, esopsclient.UsageError("reindex task status: task_id is required")
	}
	osReq := osapi.TasksGetReq{TaskID: taskID}
	httpReq, err := osReq.GetRequest(http.MethodGet)
	if err != nil {
		return types.ReindexTaskStatus{}, fmt.Errorf("opensearch reindex task status: build request: %w", err)
	}
	httpReq = httpReq.WithContext(ctx)

	resp, err := c.api.Client.Stream(httpReq)
	if err != nil {
		return types.ReindexTaskStatus{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return types.ReindexTaskStatus{}, fmt.Errorf("opensearch reindex task status: read body: %w", err)
	}
	if err := clientutil.ClassifyStatus("reindex task status", resp.StatusCode, body); err != nil {
		return types.ReindexTaskStatus{}, err
	}

	var wire reindexwire.TaskWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return types.ReindexTaskStatus{}, errors.Join(errors.New("decoding reindex task status response"), err)
	}
	return wire.ToTaskStatus(taskID), nil
}
