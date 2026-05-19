package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// dataStreamWire mirrors one entry under the top-level
// {"data_streams":[...]} returned by GET /_data_stream[/<names>] on
// both Elasticsearch and OpenSearch. Field set is the union — fields
// the cluster does not emit decode as zero, and the adapter surfaces
// only what's populated.
//
// ILMPolicy is ES-only; Lifecycle is the ES 8.11+ data-stream
// lifecycle (DSL) block. Indices is ordered by generation as the
// cluster returned it.
type dataStreamWire struct {
	Name               string                `json:"name"`
	Status             string                `json:"status"`
	Generation         int64                 `json:"generation"`
	Template           string                `json:"template"`
	ILMPolicy          string                `json:"ilm_policy"`
	Lifecycle          map[string]any        `json:"lifecycle"`
	Hidden             bool                  `json:"hidden"`
	System             bool                  `json:"system"`
	Replicated         bool                  `json:"replicated"`
	AllowCustomRouting bool                  `json:"allow_custom_routing"`
	Indices            []dataStreamIndexWire `json:"indices"`
}

// dataStreamIndexWire mirrors one entry in data_streams[].indices.
// PreferILM and ManagedBy are ES-only fields; both products emit
// IndexName and IndexUUID.
type dataStreamIndexWire struct {
	IndexName string `json:"index_name"`
	IndexUUID string `json:"index_uuid"`
	PreferILM *bool  `json:"prefer_ilm,omitempty"`
	ManagedBy string `json:"managed_by,omitempty"`
}

// dataStreamListWire is the top-level envelope. Both dialects return
// the same shape on GET /_data_stream and GET /_data_stream/<names>.
type dataStreamListWire struct {
	DataStreams []dataStreamWire `json:"data_streams"`
}

// DataStreams implements client.DataStreamInspector via
// GET /_data_stream[/<names>]. The endpoint shape is identical to OS;
// the adapter goes through the raw transport (rather than the typed
// IndicesGetDataStream helper) so the response body can be wrapped in
// a BoundedReader — a cluster with hundreds of streams × multi-
// generation backing-index chains is a realistic shape, and an
// unbounded io.ReadAll on a misbehaving cluster could peg the CLI's
// memory.
//
// A 404 against the bare endpoint normalises to an empty slice — some
// older builds 404 a cluster with no streams configured rather than
// returning {"data_streams":[]}. A 404 against an explicit name list
// is genuinely not-found.
func (c *Client) DataStreams(ctx context.Context, filter types.DataStreamFilter) ([]types.DataStream, error) {
	path := dataStreamPath(filter.Names)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.Perform(req)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, esopsclient.ErrAuth
	case http.StatusForbidden:
		return nil, esopsclient.ErrForbidden
	case http.StatusNotFound:
		if len(filter.Names) == 0 {
			return []types.DataStream{}, nil
		}
		return nil, fmt.Errorf("%w: data stream(s) %s", esopsclient.ErrNotFound, strings.Join(filter.Names, ","))
	}
	if err := classifyResponse("get_data_stream", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "get_data_stream")
	var wire dataStreamListWire
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding get_data_stream response"), err)
	}
	out := make([]types.DataStream, 0, len(wire.DataStreams))
	for _, w := range wire.DataStreams {
		out = append(out, dataStreamFromWire(w))
	}
	return out, nil
}

// GetDataStream implements client.DataStreamInspector for the single-
// name lookup. Returns ErrNotFound (wrapped) when the cluster 404s.
func (c *Client) GetDataStream(ctx context.Context, name string) (types.DataStream, error) {
	rows, err := c.DataStreams(ctx, types.DataStreamFilter{Names: []string{name}})
	if err != nil {
		return types.DataStream{}, err
	}
	for _, d := range rows {
		if d.Name == name {
			return d, nil
		}
	}
	return types.DataStream{}, fmt.Errorf("%w: data stream %q", esopsclient.ErrNotFound, name)
}

// RolloverDataStream implements client.DataStreamManager via
// POST /<name>/_rollover. Body shape is the same {"conditions":{...}}
// envelope IndexRollover uses; an empty conditions map is the
// unconditional form (the cluster always rolls over). The business
// layer guards against unintentional unconditional calls; this
// adapter just serialises whatever the request describes.
func (c *Client) RolloverDataStream(ctx context.Context, req types.DataStreamRolloverRequest) (types.DataStreamRolloverResult, error) {
	body, err := buildRolloverBody(req.Conditions)
	if err != nil {
		return types.DataStreamRolloverResult{}, err
	}
	path := "/" + req.Name + "/_rollover"
	q := []string{}
	if req.DryRun {
		q = append(q, "dry_run=true")
	}
	if req.WaitForActiveShards != "" {
		q = append(q, "wait_for_active_shards="+req.WaitForActiveShards)
	}
	if len(q) > 0 {
		path += "?" + strings.Join(q, "&")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return types.DataStreamRolloverResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.api.Perform(httpReq)
	if err != nil {
		return types.DataStreamRolloverResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("data_stream rollover", resp.StatusCode, resp.Body); err != nil {
		return types.DataStreamRolloverResult{}, err
	}

	var wire rolloverWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.DataStreamRolloverResult{}, errors.Join(errors.New("decoding data_stream rollover response"), err)
	}
	return types.DataStreamRolloverResult{
		DataStream:         req.Name,
		OldIndex:           wire.OldIndex,
		NewIndex:           wire.NewIndex,
		RolledOver:         wire.RolledOver,
		DryRun:             wire.DryRun || req.DryRun,
		Acknowledged:       wire.Acknowledged,
		ShardsAcknowledged: wire.ShardsAcknowledged,
		ConditionsMet:      wire.Conditions,
	}, nil
}

// ModifyDataStream implements client.DataStreamManager via
// POST /_data_stream/_modify. Both dialects accept a
// {"actions": [{"<verb>": {...}}, ...]} body where <verb> is
// `add_backing_index` or `remove_backing_index`. Cluster returns a
// single acknowledged flag; Actions echoes what the operator
// submitted for the captured-JSON case.
func (c *Client) ModifyDataStream(ctx context.Context, req types.DataStreamModifyRequest) (types.DataStreamModifyResult, error) {
	if req.DryRun {
		return types.DataStreamModifyResult{
			Action:       "data_stream-modify",
			DryRun:       true,
			Acknowledged: true,
			Actions:      req.Actions,
		}, nil
	}
	body, err := buildDataStreamModifyBody(req.Actions)
	if err != nil {
		return types.DataStreamModifyResult{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "/_data_stream/_modify", bytes.NewReader(body))
	if err != nil {
		return types.DataStreamModifyResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.api.Perform(httpReq)
	if err != nil {
		return types.DataStreamModifyResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("data_stream modify", resp.StatusCode, resp.Body); err != nil {
		return types.DataStreamModifyResult{}, err
	}

	var wire struct {
		Acknowledged bool `json:"acknowledged"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.DataStreamModifyResult{}, errors.Join(errors.New("decoding data_stream modify response"), err)
	}
	return types.DataStreamModifyResult{
		Action:       "data_stream-modify",
		Acknowledged: wire.Acknowledged,
		Actions:      req.Actions,
	}, nil
}

// DeleteDataStream implements client.DataStreamManager via
// DELETE /_data_stream/<names>. The cluster removes the stream object
// **and** all of its backing indices; the business layer enforces the
// blast-radius cap and the CLI requires --allow-data-loss, so this
// method assumes the caller has cleared both gates already.
func (c *Client) DeleteDataStream(ctx context.Context, req types.DataStreamDeleteRequest) (types.DataStreamDeleteResult, error) {
	if len(req.Names) == 0 {
		return types.DataStreamDeleteResult{}, esopsclient.UsageError("DeleteDataStream requires at least one name")
	}
	path := "/_data_stream/" + strings.Join(req.Names, ",")
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return types.DataStreamDeleteResult{}, err
	}
	resp, err := c.api.Perform(httpReq)
	if err != nil {
		return types.DataStreamDeleteResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("delete_data_stream", resp.StatusCode, resp.Body); err != nil {
		return types.DataStreamDeleteResult{}, err
	}

	var wire struct {
		Acknowledged bool `json:"acknowledged"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.DataStreamDeleteResult{}, errors.Join(errors.New("decoding delete_data_stream response"), err)
	}
	return types.DataStreamDeleteResult{
		Action:       "data_stream-delete",
		Names:        req.Names,
		Acknowledged: wire.Acknowledged,
	}, nil
}

// dataStreamPath builds the GET /_data_stream path for the given
// name list. Both dialects honour patterns at the path segment.
func dataStreamPath(names []string) string {
	if len(names) == 0 {
		return "/_data_stream"
	}
	return "/_data_stream/" + strings.Join(names, ",")
}

// dataStreamStatsPath builds the GET /_data_stream[/<names>]/_stats
// path. Empty names selects every stream; both dialects honour
// patterns at the path segment.
func dataStreamStatsPath(names []string) string {
	if len(names) == 0 {
		return "/_data_stream/_stats"
	}
	return "/_data_stream/" + strings.Join(names, ",") + "/_stats"
}

// dataStreamStatsWire mirrors the GET /_data_stream/_stats response
// envelope. Both ES and OS use the same shape — top-level aggregates
// plus a per-stream array.
type dataStreamStatsWire struct {
	DataStreamCount     int                       `json:"data_stream_count"`
	BackingIndices      int                       `json:"backing_indices"`
	TotalStoreSizeBytes int64                     `json:"total_store_size_bytes"`
	DataStreams         []dataStreamStatsRowWire  `json:"data_streams"`
}

type dataStreamStatsRowWire struct {
	DataStream       string `json:"data_stream"`
	BackingIndices   int    `json:"backing_indices"`
	StoreSizeBytes   int64  `json:"store_size_bytes"`
	MaximumTimestamp int64  `json:"maximum_timestamp"`
}

// DataStreamStats implements client.DataStreamInspector via
// GET /_data_stream[/<names>]/_stats. Cheap aggregate compared to
// listing + enriching from /_cat/indices — the cluster sums sizes
// across backing indices server-side.
//
// A 404 against the bare endpoint normalises to an empty report (the
// same forgiving behaviour DataStreams uses); a 404 against an
// explicit name list is genuinely not-found.
func (c *Client) DataStreamStats(ctx context.Context, req types.DataStreamStatsRequest) (types.DataStreamStatsReport, error) {
	path := dataStreamStatsPath(req.Names)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return types.DataStreamStatsReport{}, err
	}
	resp, err := c.api.Perform(httpReq)
	if err != nil {
		return types.DataStreamStatsReport{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return types.DataStreamStatsReport{}, esopsclient.ErrAuth
	case http.StatusForbidden:
		return types.DataStreamStatsReport{}, esopsclient.ErrForbidden
	case http.StatusNotFound:
		if len(req.Names) == 0 {
			return types.DataStreamStatsReport{}, nil
		}
		return types.DataStreamStatsReport{}, fmt.Errorf("%w: data stream(s) %s", esopsclient.ErrNotFound, strings.Join(req.Names, ","))
	}
	if err := classifyResponse("data_stream_stats", resp.StatusCode, resp.Body); err != nil {
		return types.DataStreamStatsReport{}, err
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "data_stream_stats")
	var wire dataStreamStatsWire
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return types.DataStreamStatsReport{}, errors.Join(errors.New("decoding data_stream_stats response"), err)
	}
	rows := make([]types.DataStreamStats, 0, len(wire.DataStreams))
	for _, r := range wire.DataStreams {
		rows = append(rows, types.DataStreamStats{
			DataStream:             r.DataStream,
			BackingIndicesCount:    r.BackingIndices,
			StoreBytes:             r.StoreSizeBytes,
			MaximumTimestampMillis: r.MaximumTimestamp,
		})
	}
	return types.DataStreamStatsReport{
		DataStreamCount:     wire.DataStreamCount,
		BackingIndicesCount: wire.BackingIndices,
		TotalStoreBytes:     wire.TotalStoreSizeBytes,
		DataStreams:         rows,
	}, nil
}

// buildDataStreamModifyBody serialises typed actions into the
// cluster's wire envelope: {"actions": [{"<verb>": {...}}, ...]}.
// Kept as a per-package helper because OS may diverge here in a
// future version.
func buildDataStreamModifyBody(actions []types.DataStreamModifyEntry) ([]byte, error) {
	if len(actions) == 0 {
		return nil, esopsclient.UsageError("at least one action is required")
	}
	wire := make([]map[string]any, 0, len(actions))
	for _, a := range actions {
		switch a.Action {
		case types.DataStreamModifyAdd, types.DataStreamModifyRemove:
		default:
			return nil, esopsclient.UsageError("unknown modify action %q (expected: add_backing_index, remove_backing_index)", string(a.Action))
		}
		wire = append(wire, map[string]any{
			string(a.Action): map[string]any{
				"data_stream": a.DataStream,
				"index":       a.Index,
			},
		})
	}
	return json.Marshal(map[string]any{"actions": wire})
}

// dataStreamFromWire flattens the wire entry to the neutral shape.
// Derives the table-rendering helpers (IndexCount, BackingIndexOldest/
// Newest, WriteIndex) from the indices slice so the renderer doesn't
// have to walk it twice.
func dataStreamFromWire(w dataStreamWire) types.DataStream {
	backing := make([]types.DataStreamBackingIndex, 0, len(w.Indices))
	for _, ix := range w.Indices {
		backing = append(backing, types.DataStreamBackingIndex{
			Name:      ix.IndexName,
			UUID:      ix.IndexUUID,
			PreferILM: ix.PreferILM,
			ManagedBy: ix.ManagedBy,
		})
	}
	d := types.DataStream{
		Name:               w.Name,
		Status:             w.Status,
		Generation:         w.Generation,
		Template:           w.Template,
		ILMPolicy:          w.ILMPolicy,
		Lifecycle:          w.Lifecycle,
		Hidden:             w.Hidden,
		System:             w.System,
		Replicated:         w.Replicated,
		AllowCustomRouting: w.AllowCustomRouting,
		Indices:            backing,
		IndexCount:         len(backing),
	}
	if len(backing) > 0 {
		d.BackingIndexOldest = backing[0].Name
		d.BackingIndexNewest = backing[len(backing)-1].Name
		d.WriteIndex = backing[len(backing)-1].Name
	}
	return d
}

// Compile-time guarantees that this Client satisfies the data-stream
// capability surface.
var (
	_ esopsclient.DataStreamInspector = (*Client)(nil)
	_ esopsclient.DataStreamManager   = (*Client)(nil)
)
