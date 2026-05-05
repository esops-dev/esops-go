package opensearch

import (
	"testing"

	osapi "github.com/opensearch-project/opensearch-go/v4/opensearchapi"
)

// Mirror the ES adapter's tasks tests: the row-per-task projection
// must preserve the composite "<node>:<id>" map key as ID, forward
// every wire field the renderer reads, and produce a stable sort
// order so saved JSON captures compare equal across runs.

func TestTasksFromListResp_carriesIDAndFields(t *testing.T) {
	in := map[string]osapi.TasksListTask{
		"nodeA:42": {
			Node:               "nodeA",
			Type:               "transport",
			Action:             "indices:data/write/reindex",
			Description:        "reindex from logs-2024 to logs-2025",
			StartTimeInMillis:  1_700_000_000_000,
			RunningTimeInNanos: 12_345_678_900,
			Cancellable:        true,
			ParentTaskID:       "nodeA:1",
		},
	}
	got := tasksFromListResp(in)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	t0 := got[0]
	if t0.ID != "nodeA:42" {
		t.Errorf("ID = %q, want nodeA:42", t0.ID)
	}
	if t0.Action != "indices:data/write/reindex" || !t0.Cancellable {
		t.Errorf("fields not forwarded: %+v", t0)
	}
	if t0.ParentTaskID != "nodeA:1" {
		t.Errorf("ParentTaskID = %q, want nodeA:1", t0.ParentTaskID)
	}
}

func TestTasksFromListResp_sortsStably(t *testing.T) {
	in := map[string]osapi.TasksListTask{
		"nodeC:9": {Action: "search"},
		"nodeA:1": {Action: "indices:data/write/reindex"},
		"nodeB:5": {Action: "indices:admin/forcemerge"},
	}
	got := tasksFromListResp(in)
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

func TestFormatNodeFailure(t *testing.T) {
	cases := []struct {
		name string
		in   osapi.FailuresCause
		want string
	}{
		{"all fields", osapi.FailuresCause{NodeID: "nodeA", Reason: "shard unavailable", Type: "shard_unavailable_exception"}, "nodeA: shard unavailable"},
		{"reason only", osapi.FailuresCause{NodeID: "nodeA", Reason: "boom"}, "nodeA: boom"},
		{"type fallback", osapi.FailuresCause{NodeID: "nodeA", Type: "ResourceNotFoundException"}, "nodeA: ResourceNotFoundException"},
		{"unknown when empty", osapi.FailuresCause{}, "<unknown>"},
		{"unknown prefix without node id", osapi.FailuresCause{Reason: "boom"}, "<unknown>: boom"},
	}
	for _, tc := range cases {
		got := formatNodeFailure(tc.in)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
