package opensearch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"context"

	osapi "github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
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

// Count implements client.DocCounter via POST /<index>/_count. The OS
// typed Indices.Count helper drops the cluster's `_shards` block from
// its response struct, so the adapter falls back to the same path the
// ES adapter uses (Perform on the underlying transport) and decodes the
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
	// The typed helper exists; we use it for the wire trip but parse
	// the body ourselves so the _shards block reaches the operator
	// (the typed response only surfaces Count). Round-tripping through
	// the typed call keeps auth, retry, and SigV4 wrapping consistent
	// with every other OS adapter call.
	httpReq, err := osReq.GetRequest()
	if err != nil {
		return types.CountResult{}, fmt.Errorf("opensearch count: build request: %w", err)
	}
	httpReq = httpReq.WithContext(ctx)

	resp, err := c.api.Client.Perform(httpReq)
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

	var wire struct {
		Count  int64 `json:"count"`
		Shards struct {
			Total      int `json:"total"`
			Successful int `json:"successful"`
			Failed     int `json:"failed"`
			Skipped    int `json:"skipped"`
		} `json:"_shards"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return types.CountResult{}, errors.Join(errors.New("decoding count response"), err)
	}
	return types.CountResult{
		Count: wire.Count,
		Shards: types.CountShardsInfo{
			Total:      wire.Shards.Total,
			Successful: wire.Shards.Successful,
			Failed:     wire.Shards.Failed,
			Skipped:    wire.Shards.Skipped,
		},
	}, nil
}

// SubmitReindex implements client.Reindexer via POST /_reindex with
// ?wait_for_completion=false. Body shape mirrors the ES adapter — both
// dialects accept the same /_reindex envelope, so the wire builder is
// the same code in both adapters.
func (c *Client) SubmitReindex(ctx context.Context, req types.ReindexSubmitRequest) (types.ReindexSubmit, error) {
	if strings.TrimSpace(req.SourceIndex) == "" || strings.TrimSpace(req.TargetIndex) == "" {
		return types.ReindexSubmit{}, esopsclient.UsageError("reindex: source_index and target_index are required")
	}
	body, err := buildReindexBody(req)
	if err != nil {
		return types.ReindexSubmit{}, err
	}

	waitFalse := false
	params := osapi.ReindexParams{
		WaitForCompletion: &waitFalse,
	}
	if rps, ok := normaliseRPS(req.RequestsPerSecond); ok {
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

// buildReindexBody renders the JSON the cluster's /_reindex endpoint
// expects. Shared between the ES and OS adapters: both dialects accept
// the same source.remote / dest envelope, the only difference is which
// upstream client call submits it.
func buildReindexBody(req types.ReindexSubmitRequest) ([]byte, error) {
	source := map[string]any{
		"index": req.SourceIndex,
	}
	if h := strings.TrimSpace(req.SourceRemoteHost); h != "" {
		if !strings.HasPrefix(h, "http://") && !strings.HasPrefix(h, "https://") {
			return nil, esopsclient.UsageError("reindex: source remote host %q must include scheme (http:// or https://)", h)
		}
		remote := map[string]any{"host": h}
		if req.SourceUsername != "" {
			remote["username"] = req.SourceUsername
		}
		if req.SourcePassword != "" {
			remote["password"] = req.SourcePassword
		}
		if len(req.SourceHeaders) > 0 {
			h := make(map[string]string, len(req.SourceHeaders))
			for k, v := range req.SourceHeaders {
				h[k] = v
			}
			remote["headers"] = h
		}
		if req.SourceConnectTimeout > 0 {
			remote["connect_timeout"] = req.SourceConnectTimeout.String()
		}
		if req.SourceSocketTimeout > 0 {
			remote["socket_timeout"] = req.SourceSocketTimeout.String()
		}
		source["remote"] = remote
	}
	body := map[string]any{
		"source": source,
		"dest":   map[string]any{"index": req.TargetIndex},
	}
	if req.Conflicts != "" {
		body["conflicts"] = req.Conflicts
	}
	if req.SizeLimit > 0 {
		body["max_docs"] = req.SizeLimit
	}
	return json.Marshal(body)
}

// normaliseRPS maps the migrate layer's float64 throughput knob into
// the upstream client's *int parameter. -1 = unbounded; 0 = unspecified
// (don't pass the param at all); positive = explicit cap, truncated to
// the nearest integer.
func normaliseRPS(rps float64) (int, bool) {
	switch {
	case rps < 0:
		return -1, true
	case rps == 0:
		return 0, false
	default:
		return int(rps), true
	}
}

// reindexTaskWire mirrors GET /_tasks/<id> when the task is a reindex.
// Same shape on ES and OS but the typed OS TasksGetResp drops the
// reindex-specific status block, so the adapter parses the body
// directly. Failures are kept as raw messages — the cluster's
// per-failure shape varies and we render a one-line summary per entry
// rather than re-typing the full payload.
type reindexTaskWire struct {
	Completed bool `json:"completed"`
	Task      struct {
		Node               string  `json:"node"`
		ID                 int64   `json:"id"`
		Action             string  `json:"action"`
		Description        string  `json:"description"`
		StartTimeInMillis  int64   `json:"start_time_in_millis"`
		RunningTimeInNanos int64   `json:"running_time_in_nanos"`
		Cancellable        bool    `json:"cancellable"`
		Cancelled          bool    `json:"cancelled"`
		Status             reindexStatusWire `json:"status"`
	} `json:"task"`
	Response *struct {
		Failures []json.RawMessage `json:"failures"`
	} `json:"response"`
	Error *struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	} `json:"error"`
}

type reindexStatusWire struct {
	Total            int64 `json:"total"`
	Created          int64 `json:"created"`
	Updated          int64 `json:"updated"`
	Deleted          int64 `json:"deleted"`
	Batches          int64 `json:"batches"`
	VersionConflicts int64 `json:"version_conflicts"`
	Noops            int64 `json:"noops"`
	Retries          struct {
		Bulk   int64 `json:"bulk"`
		Search int64 `json:"search"`
	} `json:"retries"`
	ThrottledMillis    int64   `json:"throttled_millis"`
	RequestsPerSecond  float64 `json:"requests_per_second"`
}

// ReindexTaskStatus implements client.Reindexer via GET /_tasks/<id>.
// Uses the underlying transport directly because the typed
// Tasks.Get response on opensearch-go doesn't decode the reindex
// status block — it stops at the runtime metrics common to every task
// action.
func (c *Client) ReindexTaskStatus(ctx context.Context, taskID string) (types.ReindexTaskStatus, error) {
	if strings.TrimSpace(taskID) == "" {
		return types.ReindexTaskStatus{}, esopsclient.UsageError("reindex task status: task_id is required")
	}
	osReq := osapi.TasksGetReq{TaskID: taskID}
	httpReq, err := osReq.GetRequest()
	if err != nil {
		return types.ReindexTaskStatus{}, fmt.Errorf("opensearch reindex task status: build request: %w", err)
	}
	httpReq = httpReq.WithContext(ctx)

	resp, err := c.api.Client.Perform(httpReq)
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

	var wire reindexTaskWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return types.ReindexTaskStatus{}, errors.Join(errors.New("decoding reindex task status response"), err)
	}

	out := types.ReindexTaskStatus{
		TaskID:            taskID,
		Completed:         wire.Completed,
		Total:             wire.Task.Status.Total,
		Created:           wire.Task.Status.Created,
		Updated:           wire.Task.Status.Updated,
		Deleted:           wire.Task.Status.Deleted,
		Batches:           wire.Task.Status.Batches,
		VersionConflicts:  wire.Task.Status.VersionConflicts,
		Noops:             wire.Task.Status.Noops,
		BulkRetries:       wire.Task.Status.Retries.Bulk,
		SearchRetries:     wire.Task.Status.Retries.Search,
		ThrottledMillis:   wire.Task.Status.ThrottledMillis,
		RequestsPerSecond: wire.Task.Status.RequestsPerSecond,
		StartTimeMillis:   wire.Task.StartTimeInMillis,
		RunningTimeNanos:  wire.Task.RunningTimeInNanos,
		Cancellable:       wire.Task.Cancellable,
		Cancelled:         wire.Task.Cancelled,
	}
	if wire.Response != nil {
		out.Failures = formatReindexFailures(wire.Response.Failures)
	}
	if wire.Error != nil && (wire.Error.Type != "" || wire.Error.Reason != "") {
		out.Failures = append(out.Failures, fmt.Sprintf("task error: %s: %s", wire.Error.Type, wire.Error.Reason))
	}
	return out, nil
}

// formatReindexFailures renders the cluster's failure records into a
// flat []string. Same logic as the ES adapter; the cluster's per-
// failure shape is dialect-stable so the parsing is shared in spirit
// (kept as a copy here rather than promoted to clientutil — the
// failure shape is reindex-specific, not a generic adapter helper).
func formatReindexFailures(raws []json.RawMessage) []string {
	if len(raws) == 0 {
		return nil
	}
	out := make([]string, 0, len(raws))
	for _, r := range raws {
		out = append(out, formatOneReindexFailure(r))
	}
	return out
}

func formatOneReindexFailure(raw json.RawMessage) string {
	var f struct {
		Index  string `json:"index"`
		ID     string `json:"id"`
		Shard  *int   `json:"shard"`
		Status int    `json:"status"`
		Cause  struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"cause"`
		Reason struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"reason"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return clipFailureSnippet(raw)
	}
	prefix := f.Index
	if f.ID != "" {
		prefix = fmt.Sprintf("%s[%s]", f.Index, f.ID)
	} else if f.Shard != nil {
		prefix = fmt.Sprintf("%s[shard %d]", f.Index, *f.Shard)
	}
	cause := f.Cause
	if cause.Type == "" && cause.Reason == "" {
		cause = f.Reason
	}
	switch {
	case cause.Type != "" && cause.Reason != "":
		if prefix == "" {
			return fmt.Sprintf("%s: %s", cause.Type, cause.Reason)
		}
		return fmt.Sprintf("%s: %s: %s", prefix, cause.Type, cause.Reason)
	case cause.Reason != "":
		if prefix == "" {
			return cause.Reason
		}
		return fmt.Sprintf("%s: %s", prefix, cause.Reason)
	case cause.Type != "":
		if prefix == "" {
			return cause.Type
		}
		return fmt.Sprintf("%s: %s", prefix, cause.Type)
	default:
		return clipFailureSnippet(raw)
	}
}

func clipFailureSnippet(raw []byte) string {
	const max = 200
	s := strings.TrimSpace(string(raw))
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	if len(s) > max {
		s = s[:max-1] + "…"
	}
	return s
}
