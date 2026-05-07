// Package reindexwire holds the wire shapes and translation helpers the
// Elasticsearch and OpenSearch adapters share for the /_reindex,
// /<index>/_count, and /_cluster/settings (reindex.remote.whitelist)
// surfaces. Both products expose identical envelopes for these endpoints;
// only the upstream-client API surface and the path-level routing
// differ. Centralising the wire shape here keeps the per-adapter code
// limited to "build the request, parse the response with these helpers"
// and removes a real divergence risk — there used to be byte-for-byte
// duplicates of every helper here in both adapters and a future
// cluster-envelope change would have to be applied twice.
package reindexwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/esops-dev/esops-go/pkg/types"
)

// CountWire mirrors GET /<index>/_count. Both ES and OS report the same
// envelope; the adapter is the one piece of dialect-specific glue.
type CountWire struct {
	Count  int64 `json:"count"`
	Shards struct {
		Total      int `json:"total"`
		Successful int `json:"successful"`
		Failed     int `json:"failed"`
		Skipped    int `json:"skipped"`
	} `json:"_shards"`
}

// ToCountResult lifts a decoded CountWire into the neutral domain type.
func (c CountWire) ToCountResult() types.CountResult {
	return types.CountResult{
		Count: c.Count,
		Shards: types.CountShardsInfo{
			Total:      c.Shards.Total,
			Successful: c.Shards.Successful,
			Failed:     c.Shards.Failed,
			Skipped:    c.Shards.Skipped,
		},
	}
}

// SubmitWire mirrors the cluster's response to a deferred /_reindex
// submission (?wait_for_completion=false). Both ES and OS return
// {"task": "<node>:<id>"} — one shape covers both adapters.
type SubmitWire struct {
	Task string `json:"task"`
}

// TaskWire mirrors GET /_tasks/<id> when the task is a reindex. Most
// fields live under task.status; completed/cancellable/cancelled live on
// the outer object. The failures block is decoded as raw messages — the
// cluster's failure records vary across versions and dialects, and we
// render a one-line summary per entry rather than re-typing the full
// payload.
type TaskWire struct {
	Completed bool `json:"completed"`
	Task      struct {
		Node               string     `json:"node"`
		ID                 int64      `json:"id"`
		Action             string     `json:"action"`
		Description        string     `json:"description"`
		StartTimeInMillis  int64      `json:"start_time_in_millis"`
		RunningTimeInNanos int64      `json:"running_time_in_nanos"`
		Cancellable        bool       `json:"cancellable"`
		Cancelled          bool       `json:"cancelled"`
		Status             StatusWire `json:"status"`
	} `json:"task"`
	Response *struct {
		Failures []json.RawMessage `json:"failures"`
	} `json:"response"`
	Error *struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	} `json:"error"`
}

// StatusWire is the per-status block both /_tasks/<id> and the inline
// completed-task response return.
type StatusWire struct {
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
	ThrottledMillis   int64   `json:"throttled_millis"`
	RequestsPerSecond float64 `json:"requests_per_second"`
}

// ToTaskStatus lifts a decoded TaskWire into the neutral domain type,
// merging any cluster-level error into the failures slice so a saved
// JSON record stays self-contained.
func (w TaskWire) ToTaskStatus(taskID string) types.ReindexTaskStatus {
	out := types.ReindexTaskStatus{
		TaskID:            taskID,
		Completed:         w.Completed,
		Total:             w.Task.Status.Total,
		Created:           w.Task.Status.Created,
		Updated:           w.Task.Status.Updated,
		Deleted:           w.Task.Status.Deleted,
		Batches:           w.Task.Status.Batches,
		VersionConflicts:  w.Task.Status.VersionConflicts,
		Noops:             w.Task.Status.Noops,
		BulkRetries:       w.Task.Status.Retries.Bulk,
		SearchRetries:     w.Task.Status.Retries.Search,
		ThrottledMillis:   w.Task.Status.ThrottledMillis,
		RequestsPerSecond: w.Task.Status.RequestsPerSecond,
		StartTimeMillis:   w.Task.StartTimeInMillis,
		RunningTimeNanos:  w.Task.RunningTimeInNanos,
		Cancellable:       w.Task.Cancellable,
		Cancelled:         w.Task.Cancelled,
	}
	if w.Response != nil {
		out.Failures = FormatFailures(w.Response.Failures)
	}
	if w.Error != nil && (w.Error.Type != "" || w.Error.Reason != "") {
		out.Failures = append(out.Failures, fmt.Sprintf("task error: %s: %s", w.Error.Type, w.Error.Reason))
	}
	return out
}

// FormatFailures renders the cluster's failure records into a flat
// []string. Per-failure shape is one of:
//
//	{ "index": "...", "id": "...", "type": "...", "cause": {"type":"...","reason":"..."}, "status": N }
//	{ "shard": N, "index": "...", "node": "...", "reason": {"type":"...","reason":"..."} }
//
// Unknown shapes fall back to a clipped raw JSON snippet so a future
// cluster envelope change doesn't silently disappear from the report.
func FormatFailures(raws []json.RawMessage) []string {
	if len(raws) == 0 {
		return nil
	}
	out := make([]string, 0, len(raws))
	for _, r := range raws {
		out = append(out, formatOneFailure(r))
	}
	return out
}

func formatOneFailure(raw json.RawMessage) string {
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

// MappingsBlock mirrors the cluster's per-index `mappings` payload —
// the same shape on ES and OS. Properties is intentionally left as
// map[string]any: the migrate translator walks the tree, so re-typing
// every field would just create a second representation to keep in sync
// with the cluster's.
type MappingsBlock struct {
	Properties map[string]any  `json:"properties"`
	Meta       map[string]any  `json:"_meta"`
	Dynamic    json.RawMessage `json:"dynamic"`
}

// DecodeDynamic flattens the cluster's `dynamic` field, which arrives as
// either a string ("true", "strict", "false", "runtime") or a JSON
// boolean depending on how it was set. Empty when absent.
func DecodeDynamic(raw json.RawMessage) string {
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

// ParseRemoteWhitelist extracts `reindex.remote.whitelist` from a flat
// settings map. The cluster serialises the value as a string when set
// via the `host:port,host:port` shorthand and as a slice when set via
// the JSON-array form. Both are normalised to []string with whitespace
// trimmed; an empty/missing key returns nil.
func ParseRemoteWhitelist(m map[string]any) []string {
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
		return SplitTrim(v)
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

// SplitTrim splits "a, b ,c" into []string{"a","b","c"}, dropping empty
// entries. Used by ParseRemoteWhitelist where hand-edited cluster
// settings have been seen carrying stray spaces.
func SplitTrim(s string) []string {
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

// BuildReindexBody renders the JSON the cluster's /_reindex endpoint
// expects. The remote.host scheme is checked here — the cluster rejects
// scheme-less hosts at request time, but a local check yields a clearer
// error than the cluster's "[validation_exception] failed to parse
// setting [host]". Returns ErrSchemeRequired when the operator left the
// scheme off; callers wrap with a usage-error sentinel.
func BuildReindexBody(req types.ReindexSubmitRequest) ([]byte, error) {
	source := map[string]any{
		"index": req.SourceIndex,
	}
	if h := strings.TrimSpace(req.SourceRemoteHost); h != "" {
		if !strings.HasPrefix(h, "http://") && !strings.HasPrefix(h, "https://") {
			return nil, fmt.Errorf("%w: %q must include scheme (http:// or https://)", ErrSchemeRequired, h)
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

// ErrSchemeRequired flags a missing scheme on source.remote.host. The
// adapters wrap it with their package-level usage-error sentinel so the
// CLI sees the same exit class as any other operator-input error.
var ErrSchemeRequired = errors.New("source remote host missing scheme")

// NormaliseRPS maps the migrate layer's float64 throughput knob into
// the upstream client's *int parameter. -1 = unbounded; 0 = unspecified
// (don't pass the param at all); positive = explicit cap, truncated to
// the nearest integer — sub-1-rps reindex throttling is too tight to be
// useful in practice and the upstream libraries won't accept a float.
func NormaliseRPS(rps float64) (int, bool) {
	switch {
	case rps < 0:
		return -1, true
	case rps == 0:
		return 0, false
	default:
		return int(rps), true
	}
}
