package opensearch

import (
	"encoding/json"
	"testing"
)

// Mirrors the elasticsearch transform_test pattern: exercise the
// per-dialect decoders directly, the wire shape is the contract.

func TestOsTransformFromWire_pullsScalarFields(t *testing.T) {
	raw := `{
		"_id":"users-pivot",
		"_seq_no":12,
		"_primary_term":1,
		"transform":{
			"description":"users pivot",
			"source_index":"users-*",
			"target_index":"users-pivot",
			"continuous":true,
			"updated_at":1700000000000
		}
	}`
	var entry osTransformEntryWire
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := osTransformFromWire(entry)

	if got.Name != "users-pivot" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.Source != "users-*" {
		t.Errorf("Source = %q", got.Source)
	}
	if got.Dest != "users-pivot" {
		t.Errorf("Dest = %q", got.Dest)
	}
	if !got.Continuous {
		t.Errorf("Continuous = false, want true")
	}
	// OS surfaces `updated_at` (last modified), not a separate create
	// timestamp — that lands on UpdatedAtMillis, not CreatedAtMillis.
	if got.UpdatedAtMillis != 1_700_000_000_000 {
		t.Errorf("UpdatedAtMillis = %d, want 1_700_000_000_000", got.UpdatedAtMillis)
	}
	if got.CreatedAtMillis != 0 {
		t.Errorf("CreatedAtMillis = %d, want 0 (OS doesn't surface create time)", got.CreatedAtMillis)
	}
}

func TestOsTransformFromWire_handlesListSourceIndex(t *testing.T) {
	// Some OS versions emit source_index as a list rather than a
	// string. The adapter flattens deterministically.
	raw := `{
		"_id":"audit-rollup",
		"transform":{
			"source_index":["audit-eu","audit-us"],
			"target_index":"audit-rollup",
			"continuous":false
		}
	}`
	var entry osTransformEntryWire
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := osTransformFromWire(entry)

	if got.Source != "audit-eu,audit-us" {
		t.Errorf("Source = %q, want audit-eu,audit-us", got.Source)
	}
	if got.Continuous {
		t.Errorf("Continuous = true on one-shot transform")
	}
}

func TestOsTransformStatsFromWire_flattensMetadata(t *testing.T) {
	raw := `{
		"metadata_id":"abc",
		"transform_metadata":{
			"last_updated_at":1700000000000,
			"status":"started",
			"failure_reason":"",
			"stats":{
				"pages_processed":50,
				"documents_processed":5000,
				"documents_indexed":4800,
				"index_time_in_millis":900,
				"search_time_in_millis":1100,
				"index_failures":2,
				"search_failures":1
			}
		}
	}`
	var entry osTransformExplainEntry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := osTransformStatsFromWire("audit-rollup", entry)

	if got.Name != "audit-rollup" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.State != "started" {
		t.Errorf("State = %q", got.State)
	}
	if got.DocsProcessed != 5000 || got.DocsIndexed != 4800 {
		t.Errorf("docs = (%d,%d)", got.DocsProcessed, got.DocsIndexed)
	}
	if got.IndexFailures != 2 || got.SearchFailures != 1 {
		t.Errorf("failures = (%d,%d)", got.IndexFailures, got.SearchFailures)
	}
	if got.CheckpointLastTimeMillis != 1_700_000_000_000 {
		t.Errorf("CheckpointLastTimeMillis = %d", got.CheckpointLastTimeMillis)
	}
}
