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
	"time"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// AuditLogInspector wires the OS side of the audit-logging-enabled and
// recent-audit-log-warnings rules. AuditConfig reads
// /_plugins/_security/audit/config (the security plugin's audit
// settings endpoint); AuditWarnings runs a small _search across the
// audit-log indices (the security plugin defaults to internal-
// opensearch storage with `audit_log` and `audit-log-YYYY.MM.dd` index
// names — both are accepted to cover deployment variation).
//
// Only metadata fields leave the adapter — request bodies and document
// IDs recorded in the OS audit log stay inside the cluster, by
// capability contract.

const (
	// OpenSearch 2.x+ exposes the audit config under
	// /_plugins/_security/api/audit (not /_plugins/_security/audit/config
	// — that path 400s with "no handler found" on every release we
	// support). The capability uses this canonical path; if a future
	// build moves it, ErrUnsupported is the right surface for the
	// audit-logging-enabled rule to translate to ErrProbeNotApplicable.
	osAuditConfigPath = "/_plugins/_security/api/audit"
	// OpenSearch defaults to security-auditlog-YYYY.MM.dd; older
	// distributions wrote audit-log-YYYY.MM.dd or a fixed audit_log
	// index. Cover all three with a comma-separated path so a deployment
	// running on any supported variant lands the right rows; the
	// allow_no_indices and ignore_unavailable params keep the missing
	// patterns from being a hard error.
	osAuditLogIndexPattern    = "security-auditlog-*,audit-log-*,audit_log"
	auditWarningsDefaultLimit = 100
	auditWarningsMaxLimit     = 1000
)

// osAuditConfigWire mirrors the response from
// /_plugins/_security/audit/config. The OS security plugin has shipped
// two response shapes:
//
//   - Newer plugins wrap settings under `config` (with `audit` and
//     `compliance` sub-blocks).
//   - Older plugins return the audit block flat at the top level.
//
// Both forms are accepted: we look for the audit sub-block under
// `config.audit` first and fall back to the top-level fields. The
// `compliance` block is intentionally not surfaced — its keys overlap
// names with the audit block (enabled, etc.) and the rule cares about
// the audit-trail config, not compliance-mode tagging.
type osAuditConfigWire struct {
	Config *struct {
		Enabled *bool             `json:"enabled"`
		Audit   *osAuditConfigAud `json:"audit"`
	} `json:"config"`

	Enabled *bool             `json:"enabled"`
	Audit   *osAuditConfigAud `json:"audit"`
}

type osAuditConfigAud struct {
	EnableREST                  *bool    `json:"enable_rest"`
	EnableTransport             *bool    `json:"enable_transport"`
	DisabledRESTCategories      []string `json:"disabled_rest_categories"`
	DisabledTransportCategories []string `json:"disabled_transport_categories"`
	IgnoreUsers                 []string `json:"ignore_users"`
}

// AuditConfig implements client.AuditLogInspector. A 404 from the
// security plugin is the "no audit endpoint registered" case (security
// plugin not installed, or the build dropped /audit/config) — surfaced
// as ErrUnsupported so esops-doctor's translation to
// ErrProbeNotApplicable keeps working unchanged.
func (c *Client) AuditConfig(ctx context.Context) (types.AuditConfig, error) {
	body, status, err := c.doSecurityGET(ctx, osAuditConfigPath)
	if err != nil {
		return types.AuditConfig{}, err
	}
	switch status {
	case http.StatusUnauthorized:
		return types.AuditConfig{}, esopsclient.ErrAuth
	case http.StatusForbidden:
		return types.AuditConfig{}, esopsclient.ErrForbidden
	case http.StatusNotFound:
		return types.AuditConfig{}, fmt.Errorf("%w: /_plugins/_security/audit/config not exposed by this cluster build", esopsclient.ErrUnsupported)
	}
	if status >= 400 {
		return types.AuditConfig{}, clientutil.ClassifyStatus("audit config", status, body)
	}

	var wire osAuditConfigWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return types.AuditConfig{}, errors.Join(errors.New("decoding audit config response"), err)
	}
	return projectOSAuditConfig(wire), nil
}

// projectOSAuditConfig folds the wrapped/unwrapped envelopes into one
// AuditConfig. Outputs is derived from the rest/transport enable flags:
// when either side is on, the plugin's default sink is the
// internal-opensearch index, which is what we report as "index".
func projectOSAuditConfig(w osAuditConfigWire) types.AuditConfig {
	enabled := false
	var audit *osAuditConfigAud

	switch {
	case w.Config != nil:
		if w.Config.Enabled != nil {
			enabled = *w.Config.Enabled
		}
		audit = w.Config.Audit
	default:
		if w.Enabled != nil {
			enabled = *w.Enabled
		}
		audit = w.Audit
	}

	out := types.AuditConfig{Enabled: enabled}
	if audit == nil {
		return out
	}

	excluded := append([]string{}, audit.DisabledRESTCategories...)
	excluded = append(excluded, audit.DisabledTransportCategories...)
	if len(excluded) > 0 {
		sort.Strings(excluded)
		out.EventsExclude = clientutil.UniqueStrings(excluded)
	}

	if len(audit.IgnoreUsers) > 0 {
		users := append([]string{}, audit.IgnoreUsers...)
		sort.Strings(users)
		out.IgnoreUsers = clientutil.UniqueStrings(users)
	}

	// OS does not maintain an explicit `events_include` list — the
	// model is "everything is recorded except disabled categories" —
	// so EventsInclude stays empty, which the rule should read as
	// "the cluster default include set" rather than "nothing
	// recorded".

	if enabled {
		// Default sink for the security plugin is the internal
		// OpenSearch index when audit is enabled. log4j sinks are
		// configurable but not exposed here — the rule only needs to
		// know an audit trail exists.
		restOn := audit.EnableREST == nil || *audit.EnableREST
		transportOn := audit.EnableTransport == nil || *audit.EnableTransport
		if restOn || transportOn {
			out.Outputs = []string{"index"}
		}
	}

	return out
}

// osAuditSearchHit captures the fields esops surfaces from each audit
// log entry. Only metadata leaves the adapter — request bodies and
// document IDs remain inside the cluster.
type osAuditSearchHit struct {
	Source json.RawMessage `json:"_source"`
}

type osAuditSearchResponse struct {
	Hits struct {
		Hits []osAuditSearchHit `json:"hits"`
	} `json:"hits"`
}

// AuditWarnings implements client.AuditLogInspector. A 404 across the
// resolved index set (audit just enabled, no entries yet) lands as an
// empty slice — the same "nothing to report" shape the ES adapter
// returns. ignore_unavailable=true and allow_no_indices=true are
// always set so a partial pattern match is not a hard error.
func (c *Client) AuditWarnings(ctx context.Context, req types.AuditWarningsRequest) ([]types.AuditWarning, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = auditWarningsDefaultLimit
	}
	if limit > auditWarningsMaxLimit {
		limit = auditWarningsMaxLimit
	}

	body := buildOSAuditSearchBody(req.Since, limit)
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding audit warnings request: %w", err)
	}

	path := "/" + osAuditLogIndexPattern + "/_search?ignore_unavailable=true&allow_no_indices=true&track_total_hits=false&expand_wildcards=open,hidden" +
		"&_source_includes=" + strings.Join([]string{
		"@timestamp", "audit_utc_timestamp",
		"audit_request_layer", "audit_category",
	}, ",")

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.api.Client.Stream(httpReq)
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

	respBody, err := readBoundedBody(resp.Body, "audit warnings")
	if err != nil {
		return nil, errors.Join(errors.New("reading audit warnings response"), err)
	}
	if resp.StatusCode >= 400 {
		return nil, clientutil.ClassifyStatus("audit warnings", resp.StatusCode, respBody)
	}

	var wire osAuditSearchResponse
	if err := json.Unmarshal(respBody, &wire); err != nil {
		return nil, errors.Join(errors.New("decoding audit warnings response"), err)
	}

	out := make([]types.AuditWarning, 0, len(wire.Hits.Hits))
	for _, h := range wire.Hits.Hits {
		if w, ok := projectOSAuditWarning(h.Source); ok {
			out = append(out, w)
		}
	}
	return out, nil
}

// readBoundedBody reads the response body through the bounded reader so
// a hostile cluster response cannot trick the adapter into buffering an
// unbounded body.
func readBoundedBody(r io.Reader, op string) ([]byte, error) {
	br := clientutil.NewBoundedReader(r, clientutil.DefaultMaxResponseBytes, op)
	var buf bytes.Buffer
	_, err := buf.ReadFrom(br)
	return buf.Bytes(), err
}

// buildOSAuditSearchBody assembles the _search body. Match-all when
// Since is zero so the cluster returns the most recent rows up to size;
// a range filter when Since is set. Sort on @timestamp desc — every
// supported OS audit-log version writes that field.
func buildOSAuditSearchBody(since time.Time, limit int) map[string]any {
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

// projectOSAuditWarning maps an OS audit-log _source to the neutral
// AuditWarning. Returns (zero, false) when the entry has no usable
// timestamp — a row we can't place in time is not surfaceable as a
// "recent warning". Falls back from @timestamp to audit_utc_timestamp
// because older security-plugin builds wrote only the latter.
func projectOSAuditWarning(source json.RawMessage) (types.AuditWarning, bool) {
	if len(source) == 0 {
		return types.AuditWarning{}, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(source, &raw); err != nil {
		return types.AuditWarning{}, false
	}

	tsStr := firstStringField(raw, "@timestamp", "audit_utc_timestamp")
	ts, ok := parseAuditTimestamp(tsStr)
	if !ok {
		return types.AuditWarning{}, false
	}

	layer := firstStringField(raw, "audit_request_layer")
	category := firstStringField(raw, "audit_category")

	return types.AuditWarning{
		Timestamp: ts,
		Layer:     normaliseAuditTag(layer),
		Type:      normaliseAuditTag(category),
		Count:     1,
	}, true
}

// firstStringField walks keys in order and returns the first one
// decodable as a non-empty string. Mirrors the ES adapter's helper —
// duplicated rather than shared because the two adapter packages are
// peer-internal with no shared dependency they can cross.
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

// normaliseAuditTag lowercases the layer/type tag so consumer matches
// stay stable — OS reports REST/TRANSPORT and FAILED_LOGIN/etc. in
// upper-case, ES uses lowercase.
func normaliseAuditTag(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// parseAuditTimestamp accepts both shapes the OS audit log emits:
// RFC3339-with-nanos on @timestamp, and the older "yyyy-MM-dd HH:mm:ss"
// form that audit_utc_timestamp can carry.
func parseAuditTimestamp(s string) (time.Time, bool) {
	if s = strings.TrimSpace(s); s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
