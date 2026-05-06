package elasticsearch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

// countWire mirrors GET /<index>/_count. Both ES and OS report the same
// envelope; the adapter is the one piece of dialect-specific glue.
type countWire struct {
	Count  int64 `json:"count"`
	Shards struct {
		Total      int `json:"total"`
		Successful int `json:"successful"`
		Failed     int `json:"failed"`
		Skipped    int `json:"skipped"`
	} `json:"_shards"`
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

	var wire countWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
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

// reindexSubmitWire mirrors the cluster's response to a deferred
// /_reindex submission (?wait_for_completion=false). Both ES and OS
// return {"task": "<node>:<id>"} — one shape covers both adapters.
type reindexSubmitWire struct {
	Task string `json:"task"`
}

// SubmitReindex implements client.Reindexer via POST /_reindex with
// ?wait_for_completion=false. The body shape is the standard remote
// reindex envelope:
//
//	{
//	  "source": {
//	    "remote": {"host": "https://es-src:9200", ...},
//	    "index":  "logs-*",
//	    ...
//	  },
//	  "dest":   {"index": "logs-target"},
//	  "conflicts": "abort"|"proceed",
//	  "size": <max-docs>
//	}
//
// SourceHeaders is forwarded as the body's `source.remote.headers` map
// (the cluster picks them up there and sends them on the remote fetch).
func (c *Client) SubmitReindex(ctx context.Context, req types.ReindexSubmitRequest) (types.ReindexSubmit, error) {
	if strings.TrimSpace(req.SourceIndex) == "" || strings.TrimSpace(req.TargetIndex) == "" {
		return types.ReindexSubmit{}, esopsclient.UsageError("reindex: source_index and target_index are required")
	}

	body, err := buildReindexBody(req)
	if err != nil {
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
	if rps, ok := normaliseRPS(req.RequestsPerSecond); ok {
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
	var wire reindexSubmitWire
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

// buildReindexBody renders the JSON the cluster's /_reindex endpoint
// expects. The remote.host scheme is checked here — the cluster rejects
// scheme-less hosts at request time, but a local check yields a clearer
// error than the cluster's "[validation_exception] failed to parse
// setting [host]".
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
			// Headers must be a map[string]string in the body; copy
			// to keep us from passing the operator's mutable map
			// down into the wire layer.
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
// the upstream client's *int parameter. -1 = unbounded (the cluster's
// own sentinel); 0 = unspecified (fall through to cluster default and
// don't pass the param at all). Positive values are truncated to the
// nearest integer — sub-1-rps reindex throttling is too tight to be
// useful in practice and the upstream library won't accept a float.
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

// reindexTaskWire mirrors the GET /_tasks/<id> envelope when the task
// is a reindex. Most fields live under `task.status`; `completed` and
// the wrapping action/cancellable flags live on the outer object. The
// `failures` block is decoded as raw messages — the cluster's failure
// records vary across versions and dialects, and we render a one-line
// summary per entry rather than re-typing the full payload.
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
	// While the task is still running the cluster returns the same
	// status block under task.status; once completed it ALSO surfaces
	// it under "response" with the `failures` populated. We honour
	// both shapes and copy whichever set is non-empty into the typed
	// view.
	Error *struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	} `json:"error"`
}

type reindexStatusWire struct {
	Total            int64   `json:"total"`
	Created          int64   `json:"created"`
	Updated          int64   `json:"updated"`
	Deleted          int64   `json:"deleted"`
	Batches          int64   `json:"batches"`
	VersionConflicts int64   `json:"version_conflicts"`
	Noops            int64   `json:"noops"`
	Retries          struct {
		Bulk   int64 `json:"bulk"`
		Search int64 `json:"search"`
	} `json:"retries"`
	ThrottledMillis    int64   `json:"throttled_millis"`
	RequestsPerSecond  float64 `json:"requests_per_second"`
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
	var wire reindexTaskWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
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
		// A task-level error (cluster failed to schedule, source
		// validation refused) shows up as a top-level "error" block;
		// surface it as a synthetic failure entry so a saved JSON
		// record still tells the operator what went wrong.
		out.Failures = append(out.Failures, fmt.Sprintf("task error: %s: %s", wire.Error.Type, wire.Error.Reason))
	}
	return out, nil
}

// formatReindexFailures renders the cluster's failure records into a
// flat []string. The cluster's per-failure shape is one of:
//
//	{ "index": "...", "id": "...", "type": "...", "cause": {"type":"...","reason":"..."}, "status": N }
//	{ "shard": N, "index": "...", "node": "...", "reason": {"type":"...","reason":"..."} }
//
// We pluck index/id/cause where present and emit a one-line "<index>[<id>]:
// <type>: <reason>" string per entry; unknown shapes fall back to a
// clipped raw JSON snippet so a future cluster version's variant doesn't
// silently disappear from the report.
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

// clipFailureSnippet renders an unrecognised failure record as a
// bounded one-line JSON snippet — the same shape as the generic 4xx
// snippet helper, kept local so a future cluster envelope change
// doesn't silently turn into a silent drop.
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

