package opensearch

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// truthyPtr / falsyPtr help build the *bool fields on the wire types
// without scattering temporary variables across each table case.
func truthyPtr() *bool { v := true; return &v }
func falsyPtr() *bool  { v := false; return &v }

func TestProjectOSAuditConfig_wrappedConfigEnvelope(t *testing.T) {
	wire := osAuditConfigWire{}
	wire.Config = &struct {
		Enabled *bool             `json:"enabled"`
		Audit   *osAuditConfigAud `json:"audit"`
	}{
		Enabled: truthyPtr(),
		Audit: &osAuditConfigAud{
			EnableREST:                  truthyPtr(),
			EnableTransport:             truthyPtr(),
			DisabledRESTCategories:      []string{"AUTHENTICATED", "GRANTED_PRIVILEGES"},
			DisabledTransportCategories: []string{"AUTHENTICATED"},
			IgnoreUsers:                 []string{"kibanaserver"},
		},
	}

	got := projectOSAuditConfig(wire)
	if !got.Enabled {
		t.Fatalf("Enabled = false, want true")
	}
	if !reflect.DeepEqual(got.Outputs, []string{"index"}) {
		t.Errorf("Outputs = %v, want [index]", got.Outputs)
	}
	wantExclude := []string{"AUTHENTICATED", "GRANTED_PRIVILEGES"}
	if !reflect.DeepEqual(got.EventsExclude, wantExclude) {
		t.Errorf("EventsExclude = %v, want %v (sorted, deduplicated across REST+transport)", got.EventsExclude, wantExclude)
	}
	if !reflect.DeepEqual(got.IgnoreUsers, []string{"kibanaserver"}) {
		t.Errorf("IgnoreUsers = %v, want [kibanaserver]", got.IgnoreUsers)
	}
	if len(got.EventsInclude) != 0 {
		t.Errorf("EventsInclude = %v, want empty (OS uses exclude-only model)", got.EventsInclude)
	}
}

func TestProjectOSAuditConfig_unwrappedFlatEnvelope(t *testing.T) {
	wire := osAuditConfigWire{
		Enabled: truthyPtr(),
		Audit: &osAuditConfigAud{
			EnableREST:      truthyPtr(),
			EnableTransport: falsyPtr(),
		},
	}

	got := projectOSAuditConfig(wire)
	if !got.Enabled {
		t.Fatalf("Enabled = false, want true (flat envelope)")
	}
	// REST on, transport off — at least one sink active, default is index.
	if !reflect.DeepEqual(got.Outputs, []string{"index"}) {
		t.Errorf("Outputs = %v, want [index]", got.Outputs)
	}
}

func TestProjectOSAuditConfig_disabledHasNoOutputs(t *testing.T) {
	wire := osAuditConfigWire{Enabled: falsyPtr()}
	got := projectOSAuditConfig(wire)
	if got.Enabled {
		t.Errorf("Enabled = true, want false")
	}
	if len(got.Outputs) != 0 {
		t.Errorf("Outputs = %v, want empty when disabled", got.Outputs)
	}
}

func TestProjectOSAuditConfig_emptyResponseFallsThroughToDisabled(t *testing.T) {
	got := projectOSAuditConfig(osAuditConfigWire{})
	if got.Enabled {
		t.Errorf("Enabled = true on empty wire, want false")
	}
}

func TestProjectOSAuditWarning_basicShape(t *testing.T) {
	source := json.RawMessage(`{"@timestamp":"2026-04-01T10:00:00.000Z","audit_request_layer":"REST","audit_category":"FAILED_LOGIN"}`)
	got, ok := projectOSAuditWarning(source)
	if !ok {
		t.Fatalf("projectOSAuditWarning: !ok")
	}
	if got.Layer != "rest" {
		t.Errorf("Layer = %q, want %q (lowercased)", got.Layer, "rest")
	}
	if got.Type != "failed_login" {
		t.Errorf("Type = %q, want %q (lowercased)", got.Type, "failed_login")
	}
	if got.Count != 1 {
		t.Errorf("Count = %d, want 1", got.Count)
	}
}

func TestProjectOSAuditWarning_fallsBackToAuditUTCTimestamp(t *testing.T) {
	source := json.RawMessage(`{"audit_utc_timestamp":"2026-04-01T10:00:00.000Z","audit_request_layer":"TRANSPORT","audit_category":"AUTHENTICATED"}`)
	got, ok := projectOSAuditWarning(source)
	if !ok {
		t.Fatalf("projectOSAuditWarning: !ok on legacy timestamp field")
	}
	wantTime := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	if !got.Timestamp.Equal(wantTime) {
		t.Errorf("Timestamp = %v, want %v", got.Timestamp, wantTime)
	}
}

func TestProjectOSAuditWarning_dropsRowWithoutTimestamp(t *testing.T) {
	source := json.RawMessage(`{"audit_request_layer":"REST","audit_category":"FAILED_LOGIN"}`)
	if _, ok := projectOSAuditWarning(source); ok {
		t.Errorf("expected !ok when timestamp is missing")
	}
}

func TestBuildOSAuditSearchBody_includesRangeFilterWhenSinceSet(t *testing.T) {
	since := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	body := buildOSAuditSearchBody(since, 25)

	if got, want := body["size"].(int), 25; got != want {
		t.Errorf("size = %d, want %d", got, want)
	}
	q := body["query"].(map[string]any)
	rng, ok := q["range"].(map[string]any)
	if !ok {
		t.Fatalf("range missing or wrong shape: %T", q["range"])
	}
	ts := rng["@timestamp"].(map[string]any)
	if ts["gte"] != since.UTC().Format(time.RFC3339Nano) {
		t.Errorf("gte = %v, want %v", ts["gte"], since.UTC().Format(time.RFC3339Nano))
	}
}

func TestBuildOSAuditSearchBody_matchAllWhenSinceZero(t *testing.T) {
	body := buildOSAuditSearchBody(time.Time{}, 5)
	q := body["query"].(map[string]any)
	if _, ok := q["match_all"]; !ok {
		t.Errorf("expected match_all when Since is zero, got %v", q)
	}
}
