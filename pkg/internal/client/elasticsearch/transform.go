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

// transformListPageSize is the per-request batch size on
// GET /_transform / GET /_transform/_stats. ES caps `size` at 1000;
// 100 is a comfortable balance — small enough that an unhealthy
// cluster doesn't block the response on a giant single page, large
// enough that 99% of real fleets fit in a single page anyway.
const transformListPageSize = 100

// transformWire mirrors one entry under the top-level
// {"transforms":[...]} envelope returned by GET /_transform[/<ids>].
// The cluster reports source/dest as nested objects; the adapter
// flattens them to the neutral types.Transform shape and preserves
// the raw envelope on Definition so `transform get` can pretty-print
// detail the neutral fields don't carry.
type transformWire struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Version     string `json:"version"`
	CreateTime  int64  `json:"create_time"`
	Source      struct {
		Index []string `json:"index"`
	} `json:"source"`
	Dest struct {
		Index string `json:"index"`
	} `json:"dest"`
	Sync map[string]any `json:"sync"`
}

// transformListWire is the top-level envelope GET /_transform returns.
type transformListWire struct {
	Count      int             `json:"count"`
	Transforms []transformWire `json:"transforms"`
}

// transformStatsWire mirrors one entry under the top-level
// {"transforms":[...]} envelope GET /_transform/<id>/_stats returns.
// Field selection covers what TransformStats exposes — the cluster
// reports many more counters under `stats` (e.g. `processing_time_in_ms`,
// `delete_time_in_ms`) that we deliberately drop today; widen the
// struct if a future renderer needs them.
type transformStatsWire struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Stats struct {
		DocumentsProcessed int64 `json:"documents_processed"`
		DocumentsIndexed   int64 `json:"documents_indexed"`
		PagesProcessed     int64 `json:"pages_processed"`
		TriggerCount       int64 `json:"trigger_count"`
		SearchTimeMS       int64 `json:"search_time_in_ms"`
		IndexTimeMS        int64 `json:"index_time_in_ms"`
		IndexFailures      int64 `json:"index_failures"`
		SearchFailures     int64 `json:"search_failures"`
	} `json:"stats"`
	Checkpointing struct {
		Last struct {
			Timestamp int64 `json:"timestamp_millis"`
		} `json:"last"`
	} `json:"checkpointing"`
	Reason string `json:"reason"`
}

// transformStatsListWire is the top-level envelope GET /_transform[/<ids>]/_stats
// returns. Same `count` semantics as the non-stats list endpoint.
type transformStatsListWire struct {
	Count      int                  `json:"count"`
	Transforms []transformStatsWire `json:"transforms"`
}

// ListTransforms implements client.TransformInspector via
// GET /_transform[/<ids>] with proper pagination — ES defaults to
// size=10, so without an explicit ?size= a cluster with more than
// ten transforms would silently truncate the result. We page through
// in batches of transformListPageSize, stopping when the cluster's
// reported `count` is exhausted.
//
// A 404 against the bare endpoint normalises to an empty slice — a
// cluster with no transforms configured 404s on some ES builds. A
// 404 against an explicit id list is genuinely not-found.
func (c *Client) ListTransforms(ctx context.Context, names []string) ([]types.Transform, error) {
	if len(names) > 0 {
		// Explicit id list — the cluster returns just those entries,
		// pagination not needed.
		batch, _, err := c.fetchTransformsPage(ctx, "/_transform/"+strings.Join(names, ","), 0, transformListPageSize, true)
		return batch, err
	}
	return c.paginateTransforms(ctx, "/_transform")
}

func (c *Client) paginateTransforms(ctx context.Context, path string) ([]types.Transform, error) {
	var out []types.Transform
	from := 0
	for {
		batch, total, err := c.fetchTransformsPage(ctx, path, from, transformListPageSize, false)
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if len(batch) == 0 || len(out) >= total {
			break
		}
		from += len(batch)
	}
	if out == nil {
		out = []types.Transform{}
	}
	return out, nil
}

func (c *Client) fetchTransformsPage(ctx context.Context, path string, from, size int, isExplicit bool) ([]types.Transform, int, error) {
	q := fmt.Sprintf("?from=%d&size=%d", from, size)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path+q, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.api.Perform(req)
	if err != nil {
		return nil, 0, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, 0, esopsclient.ErrAuth
	case http.StatusForbidden:
		return nil, 0, esopsclient.ErrForbidden
	case http.StatusNotFound:
		if !isExplicit {
			return []types.Transform{}, 0, nil
		}
		return nil, 0, fmt.Errorf("%w: transform(s) %s", esopsclient.ErrNotFound, strings.TrimPrefix(path, "/_transform/"))
	}
	if err := classifyResponse("transform get_transform", resp.StatusCode, resp.Body); err != nil {
		return nil, 0, err
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "transform get_transform")
	var wire transformListWire
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, 0, errors.Join(errors.New("decoding transform get_transform response"), err)
	}
	out := make([]types.Transform, 0, len(wire.Transforms))
	for _, t := range wire.Transforms {
		out = append(out, esTransformFromWire(t))
	}
	return out, wire.Count, nil
}

// GetTransform implements client.TransformInspector for the
// single-id lookup. Returns ErrNotFound (wrapped) when the cluster
// 404s.
func (c *Client) GetTransform(ctx context.Context, name string) (types.Transform, error) {
	rows, err := c.ListTransforms(ctx, []string{name})
	if err != nil {
		return types.Transform{}, err
	}
	for _, t := range rows {
		if t.Name == name {
			return t, nil
		}
	}
	return types.Transform{}, fmt.Errorf("%w: transform %q", esopsclient.ErrNotFound, name)
}

// StatsTransform implements client.TransformInspector via
// GET /_transform/<id>/_stats. Returns ErrNotFound (wrapped) on a
// 404; the cluster 404s a missing id rather than an empty list.
func (c *Client) StatsTransform(ctx context.Context, name string) (types.TransformStats, error) {
	rows, err := c.ListTransformStats(ctx, []string{name})
	if err != nil {
		return types.TransformStats{}, err
	}
	for _, s := range rows {
		if s.Name == name {
			return s, nil
		}
	}
	return types.TransformStats{}, fmt.Errorf("%w: transform %q", esopsclient.ErrNotFound, name)
}

// ListTransformStats implements client.TransformInspector. Single
// round-trip to GET /_transform[/<ids>]/_stats, paginated the same
// way as ListTransforms — the cluster's default size is 10 here too.
func (c *Client) ListTransformStats(ctx context.Context, names []string) ([]types.TransformStats, error) {
	path := "/_transform/_stats"
	isExplicit := false
	if len(names) > 0 {
		path = "/_transform/" + strings.Join(names, ",") + "/_stats"
		isExplicit = true
	}
	if isExplicit {
		batch, _, err := c.fetchTransformStatsPage(ctx, path, 0, transformListPageSize, true)
		return batch, err
	}
	var out []types.TransformStats
	from := 0
	for {
		batch, total, err := c.fetchTransformStatsPage(ctx, path, from, transformListPageSize, false)
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if len(batch) == 0 || len(out) >= total {
			break
		}
		from += len(batch)
	}
	if out == nil {
		out = []types.TransformStats{}
	}
	return out, nil
}

func (c *Client) fetchTransformStatsPage(ctx context.Context, path string, from, size int, isExplicit bool) ([]types.TransformStats, int, error) {
	q := fmt.Sprintf("?from=%d&size=%d", from, size)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path+q, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.api.Perform(req)
	if err != nil {
		return nil, 0, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, 0, esopsclient.ErrAuth
	case http.StatusForbidden:
		return nil, 0, esopsclient.ErrForbidden
	case http.StatusNotFound:
		if !isExplicit {
			return []types.TransformStats{}, 0, nil
		}
		return nil, 0, fmt.Errorf("%w: transform stats", esopsclient.ErrNotFound)
	}
	if err := classifyResponse("transform get_transform_stats", resp.StatusCode, resp.Body); err != nil {
		return nil, 0, err
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "transform get_transform_stats")
	var wire transformStatsListWire
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, 0, errors.Join(errors.New("decoding transform get_transform_stats response"), err)
	}
	out := make([]types.TransformStats, 0, len(wire.Transforms))
	for _, s := range wire.Transforms {
		out = append(out, esTransformStatsFromWire(s))
	}
	return out, wire.Count, nil
}

// PreviewTransform implements client.TransformInspector via
// POST /_transform/_preview. The cluster returns
// {"preview":[...],"generated_dest_index":{...}}; we normalise to the
// neutral {Documents, Mappings} shape.
func (c *Client) PreviewTransform(ctx context.Context, req types.TransformPreviewRequest) (types.TransformPreviewResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "/_transform/_preview", bytes.NewReader(req.Body))
	if err != nil {
		return types.TransformPreviewResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.api.Perform(httpReq)
	if err != nil {
		return types.TransformPreviewResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("transform preview_transform", resp.StatusCode, resp.Body); err != nil {
		return types.TransformPreviewResult{}, err
	}

	var wire struct {
		Preview            []map[string]any `json:"preview"`
		GeneratedDestIndex struct {
			Mappings map[string]any `json:"mappings"`
		} `json:"generated_dest_index"`
	}
	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "transform preview_transform")
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return types.TransformPreviewResult{}, errors.Join(errors.New("decoding transform preview_transform response"), err)
	}
	return types.TransformPreviewResult{
		Documents: wire.Preview,
		Mappings:  wire.GeneratedDestIndex.Mappings,
	}, nil
}

// ApplyTransform implements client.TransformManager. Routing:
//
//   - Create (Update=false): PUT /_transform/<id>. The cluster rejects
//     with 409 if the id already exists — the business layer makes
//     sure that doesn't happen by setting Update on overwrite.
//   - Update (Update=true): POST /_transform/<id>/_update. ES only
//     accepts a *limited* set of fields here (description, frequency,
//     settings, source.query, sync). The cluster rejects anything
//     else with a 400 — we surface that error untranslated rather
//     than guessing at delete+recreate semantics, since recreate
//     would silently destroy the destination index data.
//
// The apply --help text documents this limitation up-front.
func (c *Client) ApplyTransform(ctx context.Context, req types.TransformApplyRequest) (types.TransformApplyResult, error) {
	method := http.MethodPut
	path := "/_transform/" + req.Name
	if req.Update {
		method = http.MethodPost
		path = "/_transform/" + req.Name + "/_update"
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(req.Body))
	if err != nil {
		return types.TransformApplyResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.api.Perform(httpReq)
	if err != nil {
		return types.TransformApplyResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("transform put_transform", resp.StatusCode, resp.Body); err != nil {
		return types.TransformApplyResult{}, err
	}

	var wire struct {
		Acknowledged bool `json:"acknowledged"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.TransformApplyResult{}, errors.Join(errors.New("decoding transform put_transform response"), err)
	}
	return types.TransformApplyResult{
		Action:       "transform-apply",
		Name:         req.Name,
		Created:      !req.Update,
		Updated:      req.Update,
		Acknowledged: wire.Acknowledged || resp.StatusCode < 300,
	}, nil
}

// DeleteTransform implements client.TransformManager via
// DELETE /_transform/<id>. ForceStop=true maps to ?force=true so the
// cluster removes a running transform without a separate stop call.
//
// The cluster's "task is running" rejection is wrapped with a hint
// pointing at --force-stop so the operator doesn't have to know the
// raw ES error format.
func (c *Client) DeleteTransform(ctx context.Context, req types.TransformDeleteRequest) (types.TransformDeleteResult, error) {
	path := "/_transform/" + req.Name
	if req.ForceStop {
		path += "?force=true"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return types.TransformDeleteResult{}, err
	}
	resp, err := c.api.Perform(httpReq)
	if err != nil {
		return types.TransformDeleteResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return types.TransformDeleteResult{}, fmt.Errorf("%w: transform %q", esopsclient.ErrNotFound, req.Name)
	}
	if err := classifyResponse("transform delete_transform", resp.StatusCode, resp.Body); err != nil {
		return types.TransformDeleteResult{}, wrapRunningTransformError(req.Name, err)
	}

	var wire struct {
		Acknowledged bool `json:"acknowledged"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.TransformDeleteResult{}, errors.Join(errors.New("decoding transform delete_transform response"), err)
	}
	return types.TransformDeleteResult{
		Action:       "transform-delete",
		Name:         req.Name,
		Stopped:      req.ForceStop,
		Acknowledged: wire.Acknowledged || resp.StatusCode < 300,
	}, nil
}

// StartTransform implements client.TransformManager via
// POST /_transform/<id>/_start.
func (c *Client) StartTransform(ctx context.Context, req types.TransformStartRequest) (types.TransformStartResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "/_transform/"+req.Name+"/_start", nil)
	if err != nil {
		return types.TransformStartResult{}, err
	}
	resp, err := c.api.Perform(httpReq)
	if err != nil {
		return types.TransformStartResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return types.TransformStartResult{}, fmt.Errorf("%w: transform %q", esopsclient.ErrNotFound, req.Name)
	}
	if err := classifyResponse("transform start_transform", resp.StatusCode, resp.Body); err != nil {
		return types.TransformStartResult{}, err
	}

	var wire struct {
		Acknowledged bool `json:"acknowledged"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.TransformStartResult{}, errors.Join(errors.New("decoding transform start_transform response"), err)
	}
	return types.TransformStartResult{
		Action:       "transform-start",
		Name:         req.Name,
		Acknowledged: wire.Acknowledged || resp.StatusCode < 300,
	}, nil
}

// StopTransform implements client.TransformManager via
// POST /_transform/<id>/_stop. WaitForCheckpoint=true forwards
// ?wait_for_checkpoint=true so the cluster lets the in-flight
// checkpoint complete before halting — important on continuous
// transforms so the destination index isn't left mid-batch.
func (c *Client) StopTransform(ctx context.Context, req types.TransformStopRequest) (types.TransformStopResult, error) {
	path := "/_transform/" + req.Name + "/_stop"
	if req.WaitForCheckpoint {
		path += "?wait_for_checkpoint=true"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, path, nil)
	if err != nil {
		return types.TransformStopResult{}, err
	}
	resp, err := c.api.Perform(httpReq)
	if err != nil {
		return types.TransformStopResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return types.TransformStopResult{}, fmt.Errorf("%w: transform %q", esopsclient.ErrNotFound, req.Name)
	}
	if err := classifyResponse("transform stop_transform", resp.StatusCode, resp.Body); err != nil {
		return types.TransformStopResult{}, err
	}

	var wire struct {
		Acknowledged bool `json:"acknowledged"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.TransformStopResult{}, errors.Join(errors.New("decoding transform stop_transform response"), err)
	}
	return types.TransformStopResult{
		Action:            "transform-stop",
		Name:              req.Name,
		WaitForCheckpoint: req.WaitForCheckpoint,
		Acknowledged:      wire.Acknowledged || resp.StatusCode < 300,
	}, nil
}

// wrapRunningTransformError wraps a cluster delete error so an
// operator hitting the "transform is running" rejection sees the
// --force-stop hint without having to recognise the raw ES message.
// Non-matching errors pass through untouched.
func wrapRunningTransformError(name string, err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "task is running") || strings.Contains(msg, "transform is running") {
		return fmt.Errorf("transform %q is running; rerun with --force-stop to stop it first (underlying: %w)", name, err)
	}
	return err
}

// esTransformFromWire flattens the ES list entry onto the neutral
// Transform shape. The full wire body is re-marshalled onto Definition
// so the renderer can pretty-print it without a second cluster call.
//
// UpdatedAtMillis is left zero — ES doesn't surface a last-modified
// timestamp on the basic GET. CreatedAtMillis carries the cluster's
// `create_time`.
func esTransformFromWire(w transformWire) types.Transform {
	def := map[string]any{
		"id":          w.ID,
		"description": w.Description,
		"version":     w.Version,
		"create_time": w.CreateTime,
		"source":      map[string]any{"index": w.Source.Index},
		"dest":        map[string]any{"index": w.Dest.Index},
	}
	if len(w.Sync) > 0 {
		def["sync"] = w.Sync
	}
	return types.Transform{
		Name:            w.ID,
		Description:     w.Description,
		Source:          strings.Join(w.Source.Index, ","),
		Dest:            w.Dest.Index,
		Continuous:      len(w.Sync) > 0,
		Version:         w.Version,
		CreatedAtMillis: w.CreateTime,
		Definition:      def,
	}
}

// esTransformStatsFromWire flattens the ES stats entry onto the
// neutral TransformStats shape. CheckpointAgeMillis is left zero —
// the business layer fills it from CheckpointLastTimeMillis so the
// adapter stays deterministic.
func esTransformStatsFromWire(w transformStatsWire) types.TransformStats {
	return types.TransformStats{
		Name:                     w.ID,
		State:                    w.State,
		DocsProcessed:            w.Stats.DocumentsProcessed,
		DocsIndexed:              w.Stats.DocumentsIndexed,
		PagesProcessed:           w.Stats.PagesProcessed,
		TriggerCount:             w.Stats.TriggerCount,
		SearchTimeMillis:         w.Stats.SearchTimeMS,
		IndexTimeMillis:          w.Stats.IndexTimeMS,
		IndexFailures:            w.Stats.IndexFailures,
		SearchFailures:           w.Stats.SearchFailures,
		CheckpointLastTimeMillis: w.Checkpointing.Last.Timestamp,
		Reason:                   w.Reason,
	}
}

// Compile-time guarantees that this Client satisfies the transform
// capability surface.
var (
	_ esopsclient.TransformInspector = (*Client)(nil)
	_ esopsclient.TransformManager   = (*Client)(nil)
)
