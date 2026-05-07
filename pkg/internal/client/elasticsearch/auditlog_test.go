package elasticsearch

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestMergeESAuditSettings_emptyScopesProducesDisabled(t *testing.T) {
	got := mergeESAuditSettings(esAuditSettingsWire{})
	if got.Enabled {
		t.Errorf("Enabled = true, want false on empty input")
	}
	if len(got.Outputs) != 0 || len(got.EventsInclude) != 0 || len(got.EventsExclude) != 0 || len(got.IgnoreUsers) != 0 {
		t.Errorf("expected empty filter slices, got %+v", got)
	}
}

func TestMergeESAuditSettings_scopePrecedenceTransientFirst(t *testing.T) {
	tStr := "true"
	fStr := "false"
	in := esAuditSettingsWire{}
	in.Transient.Xpack.Security.Audit = esAuditSettingsBlock{Enabled: &tStr}
	in.Persistent.Xpack.Security.Audit = esAuditSettingsBlock{Enabled: &fStr}
	in.Defaults.Xpack.Security.Audit = esAuditSettingsBlock{Enabled: &fStr}

	got := mergeESAuditSettings(in)
	if !got.Enabled {
		t.Errorf("Enabled = false, want true (transient should win)")
	}
}

func TestMergeESAuditSettings_includeExcludeFromPersistentWhenTransientEmpty(t *testing.T) {
	tStr := "true"
	in := esAuditSettingsWire{}
	in.Transient.Xpack.Security.Audit = esAuditSettingsBlock{Enabled: &tStr}
	in.Persistent.Xpack.Security.Audit.Logfile.Events.Include = []string{"access_granted", "authentication_failed"}
	in.Persistent.Xpack.Security.Audit.Logfile.Events.Exclude = []string{"connection_granted"}

	got := mergeESAuditSettings(in)
	if !reflect.DeepEqual(got.EventsInclude, []string{"access_granted", "authentication_failed"}) {
		t.Errorf("EventsInclude = %v, want [access_granted authentication_failed]", got.EventsInclude)
	}
	if !reflect.DeepEqual(got.EventsExclude, []string{"connection_granted"}) {
		t.Errorf("EventsExclude = %v, want [connection_granted]", got.EventsExclude)
	}
}

func TestMergeESAuditSettings_ignoreUsersFlattenedAcrossFilters(t *testing.T) {
	tStr := "true"
	in := esAuditSettingsWire{}
	in.Persistent.Xpack.Security.Audit = esAuditSettingsBlock{Enabled: &tStr}
	in.Persistent.Xpack.Security.Audit.Logfile.Events.IgnoreFilters = map[string]esAuditIgnoreFilterEntry{
		"healthchecks": {Users: []string{"kibana_system", "monitoring"}},
		"reporting":    {Users: []string{"reporting_user", "kibana_system"}},
	}

	got := mergeESAuditSettings(in)
	want := []string{"kibana_system", "monitoring", "reporting_user"}
	if !reflect.DeepEqual(got.IgnoreUsers, want) {
		t.Errorf("IgnoreUsers = %v, want %v (sorted, deduplicated)", got.IgnoreUsers, want)
	}
}

func TestMergeESAuditSettings_enabledImpliesLogfileSinkWhenOutputsAbsent(t *testing.T) {
	tStr := "true"
	in := esAuditSettingsWire{}
	in.Persistent.Xpack.Security.Audit = esAuditSettingsBlock{Enabled: &tStr}

	got := mergeESAuditSettings(in)
	if !got.Enabled {
		t.Fatalf("Enabled = false, want true")
	}
	if !reflect.DeepEqual(got.Outputs, []string{"logfile"}) {
		t.Errorf("Outputs = %v, want [logfile] (default sink when outputs key absent)", got.Outputs)
	}
}

func TestProjectAuditWarning_ecsDottedKeys(t *testing.T) {
	source := json.RawMessage(`{"@timestamp":"2026-04-01T10:00:00Z","event.action":"authentication_failed","event.type":"rest"}`)
	got, ok := projectAuditWarning(source)
	if !ok {
		t.Fatalf("projectAuditWarning: !ok on dotted-key shape")
	}
	if got.Layer != "rest" {
		t.Errorf("Layer = %q, want %q", got.Layer, "rest")
	}
	if got.Type != "authentication_failed" {
		t.Errorf("Type = %q, want %q", got.Type, "authentication_failed")
	}
	if got.Count != 1 {
		t.Errorf("Count = %d, want 1", got.Count)
	}
	wantTime := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	if !got.Timestamp.Equal(wantTime) {
		t.Errorf("Timestamp = %v, want %v", got.Timestamp, wantTime)
	}
}

func TestProjectAuditWarning_nestedEventObject(t *testing.T) {
	source := json.RawMessage(`{"@timestamp":"2026-04-01T10:00:00Z","event":{"action":"access_denied","type":"transport"}}`)
	got, ok := projectAuditWarning(source)
	if !ok {
		t.Fatalf("projectAuditWarning: !ok on nested-object shape")
	}
	if got.Layer != "transport" {
		t.Errorf("Layer = %q, want %q", got.Layer, "transport")
	}
	if got.Type != "access_denied" {
		t.Errorf("Type = %q, want %q", got.Type, "access_denied")
	}
}

func TestProjectAuditWarning_legacySnakeCase(t *testing.T) {
	source := json.RawMessage(`{"timestamp":"2026-04-01T10:00:00Z","event_type":"authentication_failed","origin_type":"rest"}`)
	got, ok := projectAuditWarning(source)
	if !ok {
		t.Fatalf("projectAuditWarning: !ok on legacy snake-case shape")
	}
	if got.Layer != "rest" {
		t.Errorf("Layer = %q, want %q", got.Layer, "rest")
	}
	if got.Type != "authentication_failed" {
		t.Errorf("Type = %q, want %q", got.Type, "authentication_failed")
	}
}

func TestProjectAuditWarning_uppercaseLowercased(t *testing.T) {
	source := json.RawMessage(`{"@timestamp":"2026-04-01T10:00:00Z","event.action":"AUTH_FAILED","event.type":"REST"}`)
	got, ok := projectAuditWarning(source)
	if !ok {
		t.Fatalf("projectAuditWarning: !ok")
	}
	if got.Layer != "rest" {
		t.Errorf("Layer = %q, want %q", got.Layer, "rest")
	}
	if got.Type != "auth_failed" {
		t.Errorf("Type = %q, want %q", got.Type, "auth_failed")
	}
}

func TestProjectAuditWarning_noTimestampDropsRow(t *testing.T) {
	source := json.RawMessage(`{"event.action":"authentication_failed"}`)
	if _, ok := projectAuditWarning(source); ok {
		t.Errorf("expected !ok when timestamp is missing")
	}
}

// The capability promises document content stays off-limits: request
// bodies, document IDs, header dumps, and arbitrary _source fields
// should never reach the AuditWarning. The wire layer enforces this
// via _source_includes; the projection is the second line of
// defence — even if a future cluster build leaks an unexpected field
// past the include list, the projection only reads the four metadata
// keys it knows about, so nothing else can ride out.
func TestProjectAuditWarning_dropsRequestBodyAndIDFields(t *testing.T) {
	source := json.RawMessage(`{
		"@timestamp":"2026-04-01T10:00:00Z",
		"event.action":"authentication_failed",
		"event.type":"rest",
		"_id":"secret-doc-id",
		"request.body":"{\"password\":\"hunter2\"}",
		"audit_request_body":"...",
		"request.headers":{"Authorization":"Bearer XYZ"}
	}`)
	got, ok := projectAuditWarning(source)
	if !ok {
		t.Fatalf("projectAuditWarning: !ok")
	}
	// Round-trip the result and assert no surprise field made it
	// through. Every key on the marshalled form must be one of the
	// four metadata fields the type promises.
	allowed := map[string]bool{
		"timestamp": true, "layer": true, "type": true, "count": true,
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for k := range keys {
		if !allowed[k] {
			t.Errorf("AuditWarning leaked %q field — expected only timestamp/layer/type/count", k)
		}
	}
}

func TestParseAuditTimestamp_acceptsMultipleShapes(t *testing.T) {
	cases := []struct {
		in       string
		want     time.Time
		wantOK   bool
		wantNote string
	}{
		{in: "", wantOK: false},
		{in: "2026-04-01T10:00:00Z", want: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC), wantOK: true},
		{in: "2026-04-01T10:00:00.123456789Z", want: time.Date(2026, 4, 1, 10, 0, 0, 123456789, time.UTC), wantOK: true},
		{in: "not-a-date", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := parseAuditTimestamp(tc.in)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && !got.Equal(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuildAuditSearchBody_includesRangeFilterWhenSinceSet(t *testing.T) {
	since := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	body := buildAuditSearchBody(since, 50)

	if got, want := body["size"].(int), 50; got != want {
		t.Errorf("size = %d, want %d", got, want)
	}
	q, ok := body["query"].(map[string]any)
	if !ok {
		t.Fatalf("query missing or wrong shape: %T", body["query"])
	}
	rng, ok := q["range"].(map[string]any)
	if !ok {
		t.Fatalf("range missing or wrong shape: %T", q["range"])
	}
	ts, ok := rng["@timestamp"].(map[string]any)
	if !ok {
		t.Fatalf("@timestamp missing or wrong shape: %T", rng["@timestamp"])
	}
	gte, _ := ts["gte"].(string)
	if gte != since.UTC().Format(time.RFC3339Nano) {
		t.Errorf("gte = %q, want %q", gte, since.UTC().Format(time.RFC3339Nano))
	}
}

func TestBuildAuditSearchBody_matchAllWhenSinceZero(t *testing.T) {
	body := buildAuditSearchBody(time.Time{}, 10)
	q, ok := body["query"].(map[string]any)
	if !ok {
		t.Fatalf("query missing or wrong shape: %T", body["query"])
	}
	if _, ok := q["match_all"]; !ok {
		t.Errorf("expected match_all when Since is zero, got %v", q)
	}
}
