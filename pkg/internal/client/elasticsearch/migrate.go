package elasticsearch

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"context"

	"github.com/elastic/go-elasticsearch/v9/esapi"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/client/reindexwire"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// mappingGetWire mirrors the cluster's GET /<indices>/_mapping envelope.
// The cluster keys the response by index name; each entry has a
// `mappings` block whose `properties` / `_meta` / `dynamic` fields are
// the parts esops looks at.
type mappingGetWire map[string]struct {
	Mappings reindexwire.MappingsBlock `json:"mappings"`
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
			Dynamic:    reindexwire.DecodeDynamic(entry.Mappings.Dynamic),
		})
	}
	return out, nil
}

// RemoteReindexWhitelist implements client.RemoteReindexInspector by
// reading /_cluster/settings?flat_settings=true&include_defaults=true
// and pulling `reindex.remote.whitelist` from each scope.
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
		Persistent: reindexwire.ParseRemoteWhitelist(wire.Persistent),
		Transient:  reindexwire.ParseRemoteWhitelist(wire.Transient),
		Defaults:   reindexwire.ParseRemoteWhitelist(wire.Defaults),
	}, nil
}

// Count implements client.DocCounter via GET /<index>/_count. Used by
// `migrate verify` and `migrate cutover` for fresh per-index doc counts —
// /_cat/indices' docs.count column lags refreshes by tens of seconds and
// the verify/cutover window is exactly where that lag matters.
func (c *Client) Count(ctx context.Context, req types.CountRequest) (types.CountResult, error) {
	cn := c.api.Count
	opts := []func(*esapi.CountRequest){cn.WithContext(ctx)}
	if len(req.Indices) > 0 {
		opts = append(opts, cn.WithIndex(req.Indices...))
	}
	if req.IgnoreUnavailable {
		opts = append(opts, cn.WithIgnoreUnavailable(true))
	}

	resp, err := c.api.Count(opts...)
	if err != nil {
		return types.CountResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("count", resp.StatusCode, resp.Body); err != nil {
		return types.CountResult{}, err
	}

	var wire reindexwire.CountWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.CountResult{}, errors.Join(errors.New("decoding count response"), err)
	}
	return wire.ToCountResult(), nil
}

// SubmitReindex implements client.Reindexer via POST /_reindex with
// ?wait_for_completion=false. The body shape lives in reindexwire so the
// OS adapter renders the same envelope.
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

	rx := c.api.Reindex
	opts := []func(*esapi.ReindexRequest){
		rx.WithContext(ctx),
		// wait_for_completion=false keeps the call short — cluster
		// returns the task ID immediately and the reindex runs
		// asynchronously. Polling is the business layer's job.
		rx.WithWaitForCompletion(false),
	}
	if rps, ok := reindexwire.NormaliseRPS(req.RequestsPerSecond); ok {
		opts = append(opts, rx.WithRequestsPerSecond(rps))
	}
	if req.Slices != "" {
		opts = append(opts, rx.WithSlices(req.Slices))
	}
	if req.WaitForActiveShards != "" {
		opts = append(opts, rx.WithWaitForActiveShards(req.WaitForActiveShards))
	}

	resp, err := c.api.Reindex(bytes.NewReader(body), opts...)
	if err != nil {
		return types.ReindexSubmit{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("reindex submit", resp.StatusCode, resp.Body); err != nil {
		return types.ReindexSubmit{}, err
	}
	var wire reindexwire.SubmitWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.ReindexSubmit{}, errors.Join(errors.New("decoding reindex submit response"), err)
	}
	return types.ReindexSubmit{
		Action:      "migrate-reindex",
		TaskID:      wire.Task,
		SourceIndex: req.SourceIndex,
		TargetIndex: req.TargetIndex,
	}, nil
}

// ReindexTaskStatus implements client.Reindexer via GET /_tasks/<id>.
// Returns the typed view of the reindex's progress; failures and
// completion are surfaced verbatim.
func (c *Client) ReindexTaskStatus(ctx context.Context, taskID string) (types.ReindexTaskStatus, error) {
	if strings.TrimSpace(taskID) == "" {
		return types.ReindexTaskStatus{}, esopsclient.UsageError("reindex task status: task_id is required")
	}
	tg := c.api.Tasks.Get
	resp, err := c.api.Tasks.Get(taskID, tg.WithContext(ctx))
	if err != nil {
		return types.ReindexTaskStatus{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("reindex task status", resp.StatusCode, resp.Body); err != nil {
		return types.ReindexTaskStatus{}, err
	}
	var wire reindexwire.TaskWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.ReindexTaskStatus{}, errors.Join(errors.New("decoding reindex task status response"), err)
	}
	return wire.ToTaskStatus(taskID), nil
}
