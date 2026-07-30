package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// transformListPageSize is the per-page size on
// GET /_plugins/_transform — OS defaults to size=10, so without an
// explicit ?size= a cluster with more than ten transforms would
// silently truncate. 100 matches the ES adapter's batch size.
const transformListPageSize = 100

// transformStatsConcurrency caps the parallel fan-out for the
// per-id _explain calls behind ListTransformStats. OS has no bulk
// explain endpoint; a small bounded pool keeps the request rate
// sane on a cluster with hundreds of transforms.
const transformStatsConcurrency = 8

// osTransformEntryWire mirrors one entry under the
// {"transforms":[...]} envelope GET /_plugins/_transform returns, and
// the per-id GET response /_plugins/_transform/<id>.
type osTransformEntryWire struct {
	ID        string         `json:"_id"`
	SeqNo     int64          `json:"_seq_no"`
	PrimTerm  int64          `json:"_primary_term"`
	Transform map[string]any `json:"transform"`
}

// osTransformListWire is the top-level envelope OS returns for the
// list endpoint.
type osTransformListWire struct {
	TotalTransforms int                    `json:"total_transforms"`
	Transforms      []osTransformEntryWire `json:"transforms"`
}

// osTransformExplainEntry is the per-id payload that OS reports under
// GET /_plugins/_transform/<id>/_explain. The cluster keys the
// envelope by transform id; we decode one entry and surface
// metadata.* counters onto the neutral TransformStats shape.
type osTransformExplainEntry struct {
	MetadataID string              `json:"metadata_id"`
	Metadata   osTransformMetadata `json:"transform_metadata"`
}

type osTransformMetadata struct {
	TransformMetadataID string                   `json:"transform_metadata_id"`
	LastUpdatedAt       int64                    `json:"last_updated_at"`
	Status              string                   `json:"status"`
	FailureReason       string                   `json:"failure_reason"`
	Stats               osTransformMetadataStats `json:"stats"`
}

type osTransformMetadataStats struct {
	PagesProcessed     int64 `json:"pages_processed"`
	DocumentsProcessed int64 `json:"documents_processed"`
	DocumentsIndexed   int64 `json:"documents_indexed"`
	IndexTimeMS        int64 `json:"index_time_in_millis"`
	SearchTimeMS       int64 `json:"search_time_in_millis"`
	IndexFailures      int64 `json:"index_failures"`
	SearchFailures     int64 `json:"search_failures"`
}

// ListTransforms implements client.TransformInspector via
// GET /_plugins/_transform. OS paginates with size=10 by default —
// we explicitly request transformListPageSize per page and loop
// until total_transforms is exhausted so clusters with more than
// ten transforms aren't silently truncated.
//
// When names are given, OS does not honour a comma-joined id list at
// the path segment, so multi-name lookups fan out per-id.
func (c *Client) ListTransforms(ctx context.Context, names []string) ([]types.Transform, error) {
	if len(names) == 0 {
		return c.listTransformsPaginated(ctx)
	}
	out := make([]types.Transform, 0, len(names))
	for _, n := range names {
		t, err := c.GetTransform(ctx, n)
		if err != nil {
			// OS doesn't have a multi-id partial mode. Drop a single
			// 404 silently — `list --name a,b` should not blow up on
			// one missing id — but surface auth/transport errors.
			if errors.Is(err, esopsclient.ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (c *Client) listTransformsPaginated(ctx context.Context) ([]types.Transform, error) {
	var out []types.Transform
	from := 0
	for {
		batch, total, err := c.fetchTransformsPage(ctx, from, transformListPageSize)
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

func (c *Client) fetchTransformsPage(ctx context.Context, from, size int) ([]types.Transform, int, error) {
	path := fmt.Sprintf("/_plugins/_transform?from=%d&size=%d", from, size)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.api.Client.Stream(req)
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
		return []types.Transform{}, 0, nil
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, 0, clientutil.ClassifyStatus("transform get_transform", resp.StatusCode, raw)
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "transform get_transform")
	var wire osTransformListWire
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, 0, errors.Join(errors.New("decoding transform get_transform response"), err)
	}
	out := make([]types.Transform, 0, len(wire.Transforms))
	for _, e := range wire.Transforms {
		out = append(out, osTransformFromWire(e))
	}
	return out, wire.TotalTransforms, nil
}

// GetTransform implements client.TransformInspector via
// GET /_plugins/_transform/<id>.
func (c *Client) GetTransform(ctx context.Context, name string) (types.Transform, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/_plugins/_transform/"+name, nil)
	if err != nil {
		return types.Transform{}, err
	}
	resp, err := c.api.Client.Stream(req)
	if err != nil {
		return types.Transform{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return types.Transform{}, esopsclient.ErrAuth
	case http.StatusForbidden:
		return types.Transform{}, esopsclient.ErrForbidden
	case http.StatusNotFound:
		return types.Transform{}, fmt.Errorf("%w: transform %q", esopsclient.ErrNotFound, name)
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return types.Transform{}, clientutil.ClassifyStatus("transform get_transform", resp.StatusCode, raw)
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "transform get_transform")
	var entry osTransformEntryWire
	if err := json.NewDecoder(body).Decode(&entry); err != nil {
		return types.Transform{}, errors.Join(errors.New("decoding transform get_transform response"), err)
	}
	if entry.ID == "" {
		entry.ID = name
	}
	return osTransformFromWire(entry), nil
}

// StatsTransform implements client.TransformInspector via
// GET /_plugins/_transform/<id>/_explain.
func (c *Client) StatsTransform(ctx context.Context, name string) (types.TransformStats, error) {
	return c.fetchOSTransformStats(ctx, name)
}

// ListTransformStats fans out per-id _explain calls — OS has no bulk
// explain endpoint. The fan-out is bounded by transformStatsConcurrency
// so a cluster with hundreds of transforms doesn't get a thundering
// herd of explain requests.
func (c *Client) ListTransformStats(ctx context.Context, names []string) ([]types.TransformStats, error) {
	if len(names) == 0 {
		rows, err := c.listTransformsPaginated(ctx)
		if err != nil {
			return nil, err
		}
		names = make([]string, 0, len(rows))
		for _, r := range rows {
			names = append(names, r.Name)
		}
	}
	if len(names) == 0 {
		return []types.TransformStats{}, nil
	}

	// RunBounded captures the first task error itself and cancels the
	// shared context so peer tasks exit early — we don't need a
	// parallel firstErr tracker on top of it.
	out := make([]types.TransformStats, len(names))
	tasks := make([]func(context.Context) error, len(names))
	for i, n := range names {
		idx, name := i, n
		tasks[idx] = func(ctx context.Context) error {
			s, err := c.fetchOSTransformStats(ctx, name)
			if err != nil {
				// A fresh apply with no run yet has no metadata; the
				// _explain endpoint 404s. Treat that as "stats absent
				// for this id" rather than failing the whole list.
				if errors.Is(err, esopsclient.ErrNotFound) {
					out[idx] = types.TransformStats{Name: name}
					return nil
				}
				return err
			}
			out[idx] = s
			return nil
		}
	}
	if err := clientutil.RunBounded(ctx, transformStatsConcurrency, tasks); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) fetchOSTransformStats(ctx context.Context, name string) (types.TransformStats, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/_plugins/_transform/"+name+"/_explain", nil)
	if err != nil {
		return types.TransformStats{}, err
	}
	resp, err := c.api.Client.Stream(req)
	if err != nil {
		return types.TransformStats{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return types.TransformStats{}, esopsclient.ErrAuth
	case http.StatusForbidden:
		return types.TransformStats{}, esopsclient.ErrForbidden
	case http.StatusNotFound:
		return types.TransformStats{}, fmt.Errorf("%w: transform %q", esopsclient.ErrNotFound, name)
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return types.TransformStats{}, clientutil.ClassifyStatus("transform explain_transform", resp.StatusCode, raw)
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "transform explain_transform")
	var wire map[string]osTransformExplainEntry
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return types.TransformStats{}, errors.Join(errors.New("decoding transform explain_transform response"), err)
	}
	entry, ok := wire[name]
	if !ok {
		return types.TransformStats{}, fmt.Errorf("%w: transform %q", esopsclient.ErrNotFound, name)
	}
	return osTransformStatsFromWire(name, entry), nil
}

// PreviewTransform implements client.TransformInspector via
// POST /_plugins/_transform/_preview.
func (c *Client) PreviewTransform(ctx context.Context, req types.TransformPreviewRequest) (types.TransformPreviewResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "/_plugins/_transform/_preview", bytes.NewReader(req.Body))
	if err != nil {
		return types.TransformPreviewResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.api.Client.Stream(httpReq)
	if err != nil {
		return types.TransformPreviewResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return types.TransformPreviewResult{}, clientutil.ClassifyStatus("transform preview_transform", resp.StatusCode, raw)
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "transform preview_transform")
	var wire struct {
		Documents []map[string]any `json:"documents"`
	}
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return types.TransformPreviewResult{}, errors.Join(errors.New("decoding transform preview_transform response"), err)
	}
	return types.TransformPreviewResult{Documents: wire.Documents}, nil
}

// ApplyTransform implements client.TransformManager via
// PUT /_plugins/_transform/<id>. OS expects a {"transform": {...}}
// envelope; we forward the body verbatim. Update is recorded on the
// result, but OS uses the same PUT path for both create and update —
// the cluster decides based on whether the id already exists.
func (c *Client) ApplyTransform(ctx context.Context, req types.TransformApplyRequest) (types.TransformApplyResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, "/_plugins/_transform/"+req.Name, bytes.NewReader(req.Body))
	if err != nil {
		return types.TransformApplyResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.api.Client.Stream(httpReq)
	if err != nil {
		return types.TransformApplyResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return types.TransformApplyResult{}, clientutil.ClassifyStatus("transform put_transform", resp.StatusCode, raw)
	}

	return types.TransformApplyResult{
		Action:       "transform-apply",
		Name:         req.Name,
		Created:      !req.Update,
		Updated:      req.Update,
		Acknowledged: true,
	}, nil
}

// DeleteTransform implements client.TransformManager via
// DELETE /_plugins/_transform/<id>. ForceStop=true issues a stop
// first, then the delete — OS has no `?force=true` parameter on the
// delete endpoint, unlike ES.
//
// The cluster's "transform is running" rejection is wrapped with a
// hint pointing at --force-stop, mirroring the ES adapter so the CLI
// surface is consistent.
func (c *Client) DeleteTransform(ctx context.Context, req types.TransformDeleteRequest) (types.TransformDeleteResult, error) {
	if req.ForceStop {
		if _, err := c.StopTransform(ctx, types.TransformStopRequest{Name: req.Name}); err != nil && !errors.Is(err, esopsclient.ErrNotFound) {
			return types.TransformDeleteResult{}, fmt.Errorf("stopping transform before delete: %w", err)
		}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, "/_plugins/_transform/"+req.Name, nil)
	if err != nil {
		return types.TransformDeleteResult{}, err
	}
	resp, err := c.api.Client.Stream(httpReq)
	if err != nil {
		return types.TransformDeleteResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return types.TransformDeleteResult{}, fmt.Errorf("%w: transform %q", esopsclient.ErrNotFound, req.Name)
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		err := clientutil.ClassifyStatus("transform delete_transform", resp.StatusCode, raw)
		return types.TransformDeleteResult{}, wrapRunningTransformError(req.Name, err)
	}
	return types.TransformDeleteResult{
		Action:       "transform-delete",
		Name:         req.Name,
		Stopped:      req.ForceStop,
		Acknowledged: true,
	}, nil
}

// StartTransform implements client.TransformManager via
// POST /_plugins/_transform/<id>/_start.
func (c *Client) StartTransform(ctx context.Context, req types.TransformStartRequest) (types.TransformStartResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "/_plugins/_transform/"+req.Name+"/_start", nil)
	if err != nil {
		return types.TransformStartResult{}, err
	}
	resp, err := c.api.Client.Stream(httpReq)
	if err != nil {
		return types.TransformStartResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return types.TransformStartResult{}, fmt.Errorf("%w: transform %q", esopsclient.ErrNotFound, req.Name)
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return types.TransformStartResult{}, clientutil.ClassifyStatus("transform start_transform", resp.StatusCode, raw)
	}
	return types.TransformStartResult{
		Action:       "transform-start",
		Name:         req.Name,
		Acknowledged: true,
	}, nil
}

// StopTransform implements client.TransformManager via
// POST /_plugins/_transform/<id>/_stop. OS does not surface a
// per-checkpoint stop knob on this endpoint today; WaitForCheckpoint
// is preserved on the result for symmetry with the ES adapter but
// the cluster always halts at the next safe page boundary. The CLI
// layer logs a warning when an operator passes the flag against an
// OS context so the missing guarantee is loud, not hidden.
func (c *Client) StopTransform(ctx context.Context, req types.TransformStopRequest) (types.TransformStopResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "/_plugins/_transform/"+req.Name+"/_stop", nil)
	if err != nil {
		return types.TransformStopResult{}, err
	}
	resp, err := c.api.Client.Stream(httpReq)
	if err != nil {
		return types.TransformStopResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return types.TransformStopResult{}, fmt.Errorf("%w: transform %q", esopsclient.ErrNotFound, req.Name)
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return types.TransformStopResult{}, clientutil.ClassifyStatus("transform stop_transform", resp.StatusCode, raw)
	}
	return types.TransformStopResult{
		Action:            "transform-stop",
		Name:              req.Name,
		WaitForCheckpoint: req.WaitForCheckpoint,
		Acknowledged:      true,
	}, nil
}

// wrapRunningTransformError wraps a cluster delete error so an
// operator hitting the "transform is running" rejection sees the
// --force-stop hint without recognising the raw OS message.
// Non-matching errors pass through untouched.
func wrapRunningTransformError(name string, err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "transform is running") || strings.Contains(msg, "task is running") || strings.Contains(msg, "cannot be deleted") {
		return fmt.Errorf("transform %q is running; rerun with --force-stop to stop it first (underlying: %w)", name, err)
	}
	return err
}

// osTransformFromWire flattens the OS entry onto the neutral
// Transform shape. The cluster's `transform` body is preserved on
// Definition; Source/Dest/Continuous/Description are pulled out for
// renderers that don't want to walk the map.
//
// UpdatedAtMillis carries the cluster's `updated_at`; OS does not
// surface a separate create timestamp, so CreatedAtMillis stays zero
// here (ES is the only adapter that populates it).
func osTransformFromWire(e osTransformEntryWire) types.Transform {
	t := types.Transform{
		Name:       e.ID,
		Definition: e.Transform,
	}
	if desc, ok := e.Transform["description"].(string); ok {
		t.Description = desc
	}
	if src, ok := e.Transform["source_index"].(string); ok {
		t.Source = src
	}
	if dst, ok := e.Transform["target_index"].(string); ok {
		t.Dest = dst
	}
	if cont, ok := e.Transform["continuous"].(bool); ok {
		t.Continuous = cont
	}
	switch v := e.Transform["updated_at"].(type) {
	case float64:
		t.UpdatedAtMillis = int64(v)
	case int64:
		t.UpdatedAtMillis = v
	}
	if t.Source == "" {
		if arr, ok := e.Transform["source_index"].([]any); ok {
			parts := make([]string, 0, len(arr))
			for _, v := range arr {
				if s, ok := v.(string); ok {
					parts = append(parts, s)
				}
			}
			sort.Strings(parts)
			t.Source = strings.Join(parts, ",")
		}
	}
	return t
}

// osTransformStatsFromWire flattens the OS explain entry onto the
// neutral TransformStats shape. CheckpointAgeMillis is left zero —
// the business layer fills it from CheckpointLastTimeMillis so the
// adapter stays deterministic.
func osTransformStatsFromWire(name string, e osTransformExplainEntry) types.TransformStats {
	return types.TransformStats{
		Name:                     name,
		State:                    e.Metadata.Status,
		DocsProcessed:            e.Metadata.Stats.DocumentsProcessed,
		DocsIndexed:              e.Metadata.Stats.DocumentsIndexed,
		PagesProcessed:           e.Metadata.Stats.PagesProcessed,
		SearchTimeMillis:         e.Metadata.Stats.SearchTimeMS,
		IndexTimeMillis:          e.Metadata.Stats.IndexTimeMS,
		IndexFailures:            e.Metadata.Stats.IndexFailures,
		SearchFailures:           e.Metadata.Stats.SearchFailures,
		CheckpointLastTimeMillis: e.Metadata.LastUpdatedAt,
		Reason:                   e.Metadata.FailureReason,
	}
}

// Compile-time guarantees that this Client satisfies the transform
// capability surface.
var (
	_ esopsclient.TransformInspector = (*Client)(nil)
	_ esopsclient.TransformManager   = (*Client)(nil)
)
