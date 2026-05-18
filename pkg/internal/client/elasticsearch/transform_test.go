package elasticsearch

import (
	"encoding/json"
	"testing"
)

// The transform wire tests exercise the per-dialect decoders directly
// rather than spinning up a httptest server. Matches the apikeys_test
// convention the rest of this package follows — the wire shape is the
// contract, the HTTP machinery sits below it.

func TestEsTransformFromWire_flattensSourceAndDest(t *testing.T) {
	raw := `{
		"id":"users-pivot",
		"description":"users pivot",
		"version":"9.0.0",
		"create_time":1700000000000,
		"source":{"index":["users-eu","users-us"]},
		"dest":{"index":"users-pivot-out"},
		"sync":{"time":{"field":"@timestamp","delay":"60s"}}
	}`
	var w transformWire
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := esTransformFromWire(w)

	if got.Name != "users-pivot" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.Source != "users-eu,users-us" {
		t.Errorf("Source = %q, want users-eu,users-us", got.Source)
	}
	if got.Dest != "users-pivot-out" {
		t.Errorf("Dest = %q", got.Dest)
	}
	if !got.Continuous {
		t.Errorf("Continuous = false, want true (sync block present)")
	}
	if got.Version != "9.0.0" {
		t.Errorf("Version = %q", got.Version)
	}
	if got.CreatedAtMillis != 1_700_000_000_000 {
		t.Errorf("CreatedAtMillis = %d", got.CreatedAtMillis)
	}
}

func TestEsTransformFromWire_oneShotHasNoContinuous(t *testing.T) {
	raw := `{
		"id":"audit-rollup",
		"source":{"index":["audit-*"]},
		"dest":{"index":"audit-rollup"}
	}`
	var w transformWire
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := esTransformFromWire(w)
	if got.Continuous {
		t.Errorf("Continuous = true on one-shot transform; want false")
	}
}

func TestWrapRunningTransformError_recognisesESMessage(t *testing.T) {
	inner := &simpleErr{msg: "Cannot delete transform [x] as the task is running. Stop the task first..."}
	got := wrapRunningTransformError("x", inner)
	if got == inner {
		t.Errorf("err passed through; expected wrap with --force-stop hint")
	}
	if !contains(got.Error(), "--force-stop") {
		t.Errorf("err = %q, want --force-stop hint", got.Error())
	}
	// Underlying error must still be reachable via errors.Is.
	if !errorContains(got, inner.msg) {
		t.Errorf("wrapped err doesn't contain original message: %v", got)
	}
}

func TestWrapRunningTransformError_passesThroughUnrelatedError(t *testing.T) {
	inner := &simpleErr{msg: "something else"}
	got := wrapRunningTransformError("x", inner)
	if got != inner {
		t.Errorf("err = %v, want pass-through %v", got, inner)
	}
}

type simpleErr struct{ msg string }

func (s *simpleErr) Error() string { return s.msg }

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func errorContains(err error, sub string) bool {
	return err != nil && contains(err.Error(), sub)
}

func TestEsTransformStatsFromWire_flattensCounters(t *testing.T) {
	raw := `{
		"id":"users-pivot",
		"state":"started",
		"stats":{
			"documents_processed":12345,
			"documents_indexed":12000,
			"pages_processed":100,
			"trigger_count":4,
			"search_time_in_ms":2000,
			"index_time_in_ms":1500,
			"index_failures":1,
			"search_failures":0
		},
		"checkpointing":{
			"last":{"timestamp_millis":1700000000000}
		},
		"reason":""
	}`
	var w transformStatsWire
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := esTransformStatsFromWire(w)

	if got.State != "started" {
		t.Errorf("State = %q", got.State)
	}
	if got.DocsProcessed != 12345 || got.DocsIndexed != 12000 {
		t.Errorf("docs = (%d,%d)", got.DocsProcessed, got.DocsIndexed)
	}
	if got.PagesProcessed != 100 || got.TriggerCount != 4 {
		t.Errorf("pages/triggers = (%d,%d)", got.PagesProcessed, got.TriggerCount)
	}
	if got.SearchTimeMillis != 2000 || got.IndexTimeMillis != 1500 {
		t.Errorf("times = (%d,%d)", got.SearchTimeMillis, got.IndexTimeMillis)
	}
	if got.IndexFailures != 1 || got.SearchFailures != 0 {
		t.Errorf("failures = (%d,%d)", got.IndexFailures, got.SearchFailures)
	}
	if got.CheckpointLastTimeMillis != 1_700_000_000_000 {
		t.Errorf("CheckpointLastTimeMillis = %d", got.CheckpointLastTimeMillis)
	}
	// CheckpointAgeMillis is derived by the business layer, not by the
	// adapter — must stay zero here.
	if got.CheckpointAgeMillis != 0 {
		t.Errorf("CheckpointAgeMillis = %d, want 0 (business layer derives)", got.CheckpointAgeMillis)
	}
}
