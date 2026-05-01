package elasticsearch

import (
	"testing"
)

// The wire→domain projection lives behind tasksFromWire. The cluster's
// /_tasks?group_by=none response is keyed by composite "<node>:<id>"
// strings; tests assert the projection preserves the key as ID,
// surfaces every wire field the renderer reads, and sorts the slice
// stably so a saved JSON capture compares equal across runs.

func TestTasksFromWire_carriesIDAndFields(t *testing.T) {
	in := map[string]taskWire{
		"nodeA:42": {
			Node:             "nodeA",
			Type:             "transport",
			Action:           "indices:data/write/reindex",
			Description:      "reindex from logs-2024 to logs-2025",
			StartTimeMillis:  1_700_000_000_000,
			RunningTimeNanos: 12_345_678_900,
			Cancellable:      true,
			Cancelled:        false,
			ParentTaskID:     "nodeA:1",
		},
	}
	got := tasksFromWire(in)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	t0 := got[0]
	if t0.ID != "nodeA:42" {
		t.Errorf("ID = %q, want nodeA:42 (the map key)", t0.ID)
	}
	if t0.Action != "indices:data/write/reindex" || !t0.Cancellable {
		t.Errorf("fields not forwarded: %+v", t0)
	}
	if t0.ParentTaskID != "nodeA:1" {
		t.Errorf("ParentTaskID = %q, want nodeA:1", t0.ParentTaskID)
	}
}

func TestTasksFromWire_sortsStably(t *testing.T) {
	// Three entries with deliberately mixed insertion order — Go map
	// iteration is not deterministic, so the renderer's grep-friendly
	// stable order has to come from the projection step.
	in := map[string]taskWire{
		"nodeC:9": {Action: "search"},
		"nodeA:1": {Action: "indices:data/write/reindex"},
		"nodeB:5": {Action: "indices:admin/forcemerge"},
	}
	got := tasksFromWire(in)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	wantOrder := []string{"nodeA:1", "nodeB:5", "nodeC:9"}
	for i, w := range wantOrder {
		if got[i].ID != w {
			t.Errorf("got[%d].ID = %q, want %q", i, got[i].ID, w)
		}
	}
}

func TestFormatTaskNodeFailure(t *testing.T) {
	cases := []struct {
		name           string
		nodeID, reason string
		typ            string
		want           string
	}{
		{"all fields", "nodeA", "shard unavailable", "shard_unavailable_exception", "nodeA: shard unavailable"},
		{"reason only", "nodeA", "boom", "", "nodeA: boom"},
		{"type fallback when reason empty", "nodeA", "", "ResourceNotFoundException", "nodeA: ResourceNotFoundException"},
		{"unknown when both empty", "", "", "", "<unknown>"},
		{"unknown prefix when node id missing", "", "boom", "", "<unknown>: boom"},
	}
	for _, tc := range cases {
		got := formatTaskNodeFailure(tc.nodeID, tc.reason, tc.typ)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
