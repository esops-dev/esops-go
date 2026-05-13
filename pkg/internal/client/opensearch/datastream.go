package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// dataStreamWire mirrors one entry under the top-level
// {"data_streams":[...]} returned by GET /_data_stream[/<names>].
// OpenSearch returns the same envelope as Elasticsearch; the ISM
// template binding is OS-specific and surfaces under ism_template
// when one is resolved for the stream.
type dataStreamWire struct {
	Name        string                `json:"name"`
	Status      string                `json:"status"`
	Generation  int64                 `json:"generation"`
	Template    string                `json:"template"`
	ISMTemplate string                `json:"ism_template"`
	Hidden      bool                  `json:"hidden"`
	System      bool                  `json:"system"`
	Replicated  bool                  `json:"replicated"`
	Indices     []dataStreamIndexWire `json:"indices"`
}

type dataStreamIndexWire struct {
	IndexName string `json:"index_name"`
	IndexUUID string `json:"index_uuid"`
}

type dataStreamListWire struct {
	DataStreams []dataStreamWire `json:"data_streams"`
}

// DataStreams implements client.DataStreamInspector via
// GET /_data_stream[/<names>]. The endpoint and envelope are
// identical to ES; the adapter goes through the raw transport so the
// response body can be wrapped in a BoundedReader (multi-generation
// chains × hundreds of streams is a realistic shape).
//
// A 404 against the bare endpoint normalises to an empty slice — some
// older OS builds 404 a cluster with no streams configured. A 404
// against an explicit name list is genuinely not-found.
func (c *Client) DataStreams(ctx context.Context, filter types.DataStreamFilter) ([]types.DataStream, error) {
	path := dataStreamPath(filter.Names)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.Client.Perform(req)
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
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, clientutil.ClassifyStatus("get_data_stream", resp.StatusCode, raw)
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
// POST /<name>/_rollover. Body shape is {"conditions":{...}} — same
// envelope IndexRollover uses; both products honour ?dry_run=true.
func (c *Client) RolloverDataStream(ctx context.Context, req types.DataStreamRolloverRequest) (types.DataStreamRolloverResult, error) {
	body, err := buildDataStreamRolloverBody(req.Conditions)
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

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.DataStreamRolloverResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return types.DataStreamRolloverResult{}, errors.Join(errors.New("reading data_stream rollover response"), err)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return types.DataStreamRolloverResult{}, esopsclient.ErrAuth
	case http.StatusForbidden:
		return types.DataStreamRolloverResult{}, esopsclient.ErrForbidden
	}
	if resp.StatusCode >= 400 {
		return types.DataStreamRolloverResult{}, clientutil.ClassifyStatus("data_stream rollover", resp.StatusCode, respBody)
	}

	var wire struct {
		Acknowledged       bool            `json:"acknowledged"`
		ShardsAcknowledged bool            `json:"shards_acknowledged"`
		OldIndex           string          `json:"old_index"`
		NewIndex           string          `json:"new_index"`
		RolledOver         bool            `json:"rolled_over"`
		DryRun             bool            `json:"dry_run"`
		Conditions         map[string]bool `json:"conditions"`
	}
	if err := json.Unmarshal(respBody, &wire); err != nil {
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
// POST /_data_stream/_modify. OS added the endpoint in 2.x with the
// same wire shape as ES.
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

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.DataStreamModifyResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return types.DataStreamModifyResult{}, errors.Join(errors.New("reading data_stream modify response"), err)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return types.DataStreamModifyResult{}, esopsclient.ErrAuth
	case http.StatusForbidden:
		return types.DataStreamModifyResult{}, esopsclient.ErrForbidden
	}
	if resp.StatusCode >= 400 {
		return types.DataStreamModifyResult{}, clientutil.ClassifyStatus("data_stream modify", resp.StatusCode, respBody)
	}

	var wire struct {
		Acknowledged bool `json:"acknowledged"`
	}
	if err := json.Unmarshal(respBody, &wire); err != nil {
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
// and all of its backing indices in one shot.
func (c *Client) DeleteDataStream(ctx context.Context, req types.DataStreamDeleteRequest) (types.DataStreamDeleteResult, error) {
	if len(req.Names) == 0 {
		return types.DataStreamDeleteResult{}, esopsclient.UsageError("DeleteDataStream requires at least one name")
	}
	path := "/_data_stream/" + strings.Join(req.Names, ",")
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return types.DataStreamDeleteResult{}, err
	}
	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.DataStreamDeleteResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return types.DataStreamDeleteResult{}, errors.Join(errors.New("reading delete_data_stream response"), err)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return types.DataStreamDeleteResult{}, esopsclient.ErrAuth
	case http.StatusForbidden:
		return types.DataStreamDeleteResult{}, esopsclient.ErrForbidden
	}
	if resp.StatusCode >= 400 {
		return types.DataStreamDeleteResult{}, clientutil.ClassifyStatus("delete_data_stream", resp.StatusCode, respBody)
	}

	var wire struct {
		Acknowledged bool `json:"acknowledged"`
	}
	if err := json.Unmarshal(respBody, &wire); err != nil {
		return types.DataStreamDeleteResult{}, errors.Join(errors.New("decoding delete_data_stream response"), err)
	}
	return types.DataStreamDeleteResult{
		Action:       "data_stream-delete",
		Names:        req.Names,
		Acknowledged: wire.Acknowledged,
	}, nil
}

// dataStreamPath builds the GET /_data_stream path for the given
// name list. Mirrors the ES helper so the two adapters stay
// trivially comparable.
func dataStreamPath(names []string) string {
	if len(names) == 0 {
		return "/_data_stream"
	}
	return "/_data_stream/" + strings.Join(names, ",")
}

// buildDataStreamRolloverBody serialises a conditions block into the
// {"conditions":{...}} envelope. Mirrors the ES helper rather than
// importing across packages.
func buildDataStreamRolloverBody(c types.IndexRolloverConditions) ([]byte, error) {
	conditions := make(map[string]any)
	if c.MaxAge != "" {
		conditions["max_age"] = c.MaxAge
	}
	if c.MaxDocs > 0 {
		conditions["max_docs"] = c.MaxDocs
	}
	if c.MaxSize != "" {
		conditions["max_size"] = c.MaxSize
	}
	if c.MaxPrimarySize != "" {
		conditions["max_primary_shard_size"] = c.MaxPrimarySize
	}
	return json.Marshal(map[string]any{"conditions": conditions})
}

// buildDataStreamModifyBody serialises typed actions into the
// {"actions": [...]} envelope. Mirrors the ES helper.
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

// dataStreamFromWire flattens the wire entry to the neutral shape and
// derives the table-rendering helpers from the indices slice.
func dataStreamFromWire(w dataStreamWire) types.DataStream {
	backing := make([]types.DataStreamBackingIndex, 0, len(w.Indices))
	for _, ix := range w.Indices {
		backing = append(backing, types.DataStreamBackingIndex{
			Name: ix.IndexName,
			UUID: ix.IndexUUID,
		})
	}
	d := types.DataStream{
		Name:        w.Name,
		Status:      w.Status,
		Generation:  w.Generation,
		Template:    w.Template,
		ISMTemplate: w.ISMTemplate,
		Hidden:      w.Hidden,
		System:      w.System,
		Replicated:  w.Replicated,
		Indices:     backing,
		IndexCount:  len(backing),
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
