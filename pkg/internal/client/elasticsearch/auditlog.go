package elasticsearch

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
	"time"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// AuditLogInspector wires the ES side of the audit-logging-enabled and
// recent-audit-log-warnings rules. AuditConfig reads
// /_cluster/settings filtered to xpack.security.audit.* and resolves
// the effective values across transient → persistent → defaults
// precedence (the same precedence the cluster itself applies, surfaced
// here so the rule sees what an operator request would). AuditWarnings
// runs a small _search against the .security_audit_log-* index pattern
// and returns metadata-only rows — request bodies and document IDs
// recorded in the audit log are off-limits, by capability contract.

const (
	esAuditLogIndexPattern    = ".security_audit_log-*"
	auditWarningsDefaultLimit = 100
	auditWarningsMaxLimit     = 1000
)

// esAuditSettingsWire mirrors the filter_path-narrowed
// /_cluster/settings response. flat_settings is *not* requested (the
// filter_path syntax only matches the nested form), so the audit block
// arrives nested under xpack.security.audit. Each scope is decoded
// independently so transient → persistent → defaults precedence can be
// resolved without losing the source-of-truth info.
type esAuditSettingsWire struct {
	Persistent esAuditSettingsScope `json:"persistent"`
	Transient  esAuditSettingsScope `json:"transient"`
	Defaults   esAuditSettingsScope `json:"defaults"`
}

type esAuditSettingsScope struct {
	Xpack struct {
		Security struct {
			Audit esAuditSettingsBlock `json:"audit"`
		} `json:"security"`
	} `json:"xpack"`
}

// esAuditSettingsBlock is the audit subtree. Enabled is a *string
// because /_cluster/settings reports booleans as quoted strings ("true"
// / "false") on most ES versions; pointer-typed so a missing key
// (filter_path matched nothing in this scope) is distinct from the
// empty string. Outputs is included for older ES that still honoured
// `xpack.security.audit.outputs` — newer ES dropped it but the field
// is harmless on the wire if absent.
type esAuditSettingsBlock struct {
	Enabled *string  `json:"enabled"`
	Outputs []string `json:"outputs"`
	Logfile struct {
		Events struct {
			Include       []string                              `json:"include"`
			Exclude       []string                              `json:"exclude"`
			IgnoreFilters map[string]esAuditIgnoreFilterEntry   `json:"ignore_filters"`
		} `json:"events"`
	} `json:"logfile"`
}

type esAuditIgnoreFilterEntry struct {
	Users []string `json:"users"`
}

// AuditConfig implements client.AuditLogInspector. The cluster is the
// source of truth: when no audit config is set anywhere, the response
// body is `{}` (filter_path matched nothing) and we report Enabled=false
// with empty filter slices — the same shape rules see when audit is
// genuinely off.
func (c *Client) AuditConfig(ctx context.Context) (types.AuditConfig, error) {
	body, status, err := c.doRawGET(ctx, "/_cluster/settings?include_defaults=true&filter_path=*.xpack.security.audit.*")
	if err != nil {
		return types.AuditConfig{}, err
	}
	switch status {
	case http.StatusUnauthorized:
		return types.AuditConfig{}, esopsclient.ErrAuth
	case http.StatusForbidden:
		return types.AuditConfig{}, esopsclient.ErrForbidden
	}
	if status >= 400 {
		return types.AuditConfig{}, clientutil.ClassifyStatus("audit config", status, body)
	}

	var wire esAuditSettingsWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return types.AuditConfig{}, errors.Join(errors.New("decoding audit settings response"), err)
	}
	return mergeESAuditSettings(wire), nil
}

// mergeESAuditSettings resolves the effective audit config across
// transient → persistent → defaults. Each field independently picks the
// first scope that supplies a value: a partially-overriding operator
// (e.g. enabled=true in persistent but include list only in defaults)
// gets the union the cluster itself would honour, not the strictly-most-
// specific scope's blank fields.
func mergeESAuditSettings(w esAuditSettingsWire) types.AuditConfig {
	scopes := [3]esAuditSettingsBlock{
		w.Transient.Xpack.Security.Audit,
		w.Persistent.Xpack.Security.Audit,
		w.Defaults.Xpack.Security.Audit,
	}

	out := types.AuditConfig{}
	for _, s := range scopes {
		if !out.Enabled && s.Enabled != nil {
			out.Enabled = parseBoolish(*s.Enabled, false)
		}
		if len(out.Outputs) == 0 && len(s.Outputs) > 0 {
			out.Outputs = clientutil.UniqueStrings(append([]string{}, s.Outputs...))
		}
		if len(out.EventsInclude) == 0 && len(s.Logfile.Events.Include) > 0 {
			out.EventsInclude = clientutil.UniqueStrings(append([]string{}, s.Logfile.Events.Include...))
		}
		if len(out.EventsExclude) == 0 && len(s.Logfile.Events.Exclude) > 0 {
			out.EventsExclude = clientutil.UniqueStrings(append([]string{}, s.Logfile.Events.Exclude...))
		}
		if len(out.IgnoreUsers) == 0 && len(s.Logfile.Events.IgnoreFilters) > 0 {
			users := make([]string, 0)
			for _, f := range s.Logfile.Events.IgnoreFilters {
				users = append(users, f.Users...)
			}
			if len(users) > 0 {
				sort.Strings(users)
				out.IgnoreUsers = clientutil.UniqueStrings(users)
			}
		}
	}

	// Older ES versions wrote audit to a logfile by default with no
	// `outputs` key; surface the implied sink so the rule sees a stable
	// "what's catching events" answer instead of an empty slice that
	// could mean either "off" or "default sink".
	if out.Enabled && len(out.Outputs) == 0 {
		out.Outputs = []string{"logfile"}
	}
	return out
}

// esAuditSearchHit captures the fields esops surfaces from each audit
// log entry. ES has shipped multiple shapes across versions — the
// pre-7.0 layout uses snake_case (`event_type`, `origin_type`); the
// 7.x+ ECS-influenced layout uses dotted keys (`event.action`,
// `origin.type`) that arrive on the wire as either nested objects or
// literal dotted-string keys depending on the audit appender's
// configuration. We accept both forms and pick the first non-empty
// match for each field.
type esAuditSearchHit struct {
	Source json.RawMessage `json:"_source"`
}

type esAuditSearchResponse struct {
	Hits struct {
		Hits []esAuditSearchHit `json:"hits"`
	} `json:"hits"`
}

// AuditWarnings implements client.AuditLogInspector. Returns rows in
// the cluster's @timestamp-desc order so the most recent events come
// first. A 404 on the index pattern is the "audit just enabled, no
// entries yet" case — surfaced as an empty slice rather than an error.
func (c *Client) AuditWarnings(ctx context.Context, req types.AuditWarningsRequest) ([]types.AuditWarning, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = auditWarningsDefaultLimit
	}
	if limit > auditWarningsMaxLimit {
		limit = auditWarningsMaxLimit
	}

	body := buildAuditSearchBody(req.Since, limit)
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding audit warnings request: %w", err)
	}

	s := c.api.Search
	resp, err := c.api.Search(
		s.WithContext(ctx),
		s.WithIndex(esAuditLogIndexPattern),
		s.WithBody(bytes.NewReader(raw)),
		s.WithIgnoreUnavailable(true),
		s.WithAllowNoIndices(true),
		s.WithExpandWildcards("open,hidden"),
		s.WithTrackTotalHits(false),
		// Only the metadata fields the capability surfaces leave the
		// adapter — no request bodies, no document IDs, by contract.
		s.WithSourceIncludes("@timestamp", "timestamp", "event.action", "event.type", "event_type", "origin.type", "origin_type", "layer"),
	)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, esopsclient.ErrAuth
	case http.StatusForbidden:
		return nil, esopsclient.ErrForbidden
	}
	if resp.StatusCode >= 400 {
		raw, _ := readBoundedBody(resp.Body, "audit warnings")
		return nil, clientutil.ClassifyStatus("audit warnings", resp.StatusCode, raw)
	}

	bodyR := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "audit warnings")
	var wire esAuditSearchResponse
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding audit warnings response"), err)
	}

	out := make([]types.AuditWarning, 0, len(wire.Hits.Hits))
	for _, h := range wire.Hits.Hits {
		if w, ok := projectAuditWarning(h.Source); ok {
			out = append(out, w)
		}
	}
	return out, nil
}

// readBoundedBody reads the response body through the bounded reader so
// an error path can't be tricked into buffering a multi-GB body via a
// hostile cluster response.
func readBoundedBody(r io.Reader, op string) ([]byte, error) {
	br := clientutil.NewBoundedReader(r, clientutil.DefaultMaxResponseBytes, op)
	var buf bytes.Buffer
	_, err := buf.ReadFrom(br)
	return buf.Bytes(), err
}

// buildAuditSearchBody assembles the _search body. Match-all when Since
// is zero so the cluster returns the most recent rows up to size; a
// range filter when Since is set. Sort on @timestamp desc — the field
// is present on every supported ES audit-log version.
func buildAuditSearchBody(since time.Time, limit int) map[string]any {
	body := map[string]any{
		"size": limit,
		"sort": []any{
			map[string]any{"@timestamp": map[string]any{"order": "desc"}},
		},
	}
	if since.IsZero() {
		body["query"] = map[string]any{"match_all": map[string]any{}}
	} else {
		body["query"] = map[string]any{
			"range": map[string]any{
				"@timestamp": map[string]any{
					"gte": since.UTC().Format(time.RFC3339Nano),
				},
			},
		}
	}
	return body
}

// projectAuditWarning maps an audit-log hit's _source to the neutral
// AuditWarning. Returns (zero, false) when the entry has no usable
// timestamp — a row we can't place in time is not surfaceable as a
// "recent warning". The dotted-key fallbacks (event.action vs
// event_type, origin.type vs origin_type) cover the supported ES
// audit-log layout history.
func projectAuditWarning(source json.RawMessage) (types.AuditWarning, bool) {
	if len(source) == 0 {
		return types.AuditWarning{}, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(source, &raw); err != nil {
		return types.AuditWarning{}, false
	}

	tsStr := firstStringField(raw, "@timestamp", "timestamp")
	ts, ok := parseAuditTimestamp(tsStr)
	if !ok {
		return types.AuditWarning{}, false
	}

	layer := firstStringField(raw, "event.type", "origin.type", "origin_type", "layer")
	if layer == "" {
		// 7.x+ writes event.* as a nested object on some appenders.
		if nested, ok := raw["event"]; ok {
			layer = firstStringField(decodeRawObject(nested), "type")
		}
		if layer == "" {
			if nested, ok := raw["origin"]; ok {
				layer = firstStringField(decodeRawObject(nested), "type")
			}
		}
	}

	action := firstStringField(raw, "event.action", "event_type")
	if action == "" {
		if nested, ok := raw["event"]; ok {
			action = firstStringField(decodeRawObject(nested), "action")
		}
	}

	return types.AuditWarning{
		Timestamp: ts,
		Layer:     normaliseAuditTag(layer),
		Type:      normaliseAuditTag(action),
		Count:     1,
	}, true
}

// firstStringField walks keys in order and returns the first one
// decodable as a non-empty string.
func firstStringField(m map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok || len(raw) == 0 {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil && s != "" {
			return s
		}
	}
	return ""
}

// decodeRawObject decodes an object node into the same map shape
// firstStringField walks. Returns an empty map (not nil) so callers can
// pass the result to firstStringField without a nil check.
func decodeRawObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return map[string]json.RawMessage{}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]json.RawMessage{}
	}
	return m
}

// normaliseAuditTag lowercases the layer/type tag so consumer matches
// stay stable across dialects (OS reports "REST" upper-case, ES
// lowercase). Trims whitespace too — older audit-log formatters
// occasionally pad values, and a downstream rule comparing tags should
// not have to defend against that.
func normaliseAuditTag(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// parseAuditTimestamp accepts both the ES audit-log timestamp shapes:
// the ECS-style RFC3339Nano on @timestamp and the older
// "2006-01-02T15:04:05.999Z" / "2006-01-02 15:04:05,999" forms some
// audit appenders still emit. Returns (zero, false) on a miss so the
// caller can drop the entry rather than surface a wrong instant.
func parseAuditTimestamp(s string) (time.Time, bool) {
	if s = strings.TrimSpace(s); s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05,000",
		"2006-01-02T15:04:05.000-07:00",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
