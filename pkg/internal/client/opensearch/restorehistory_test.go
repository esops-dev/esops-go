package opensearch_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// restoreTransport replays canned responses for /_tasks vs /_search.
// The OS client does not issue a product probe, so unlike the ES test
// transport we do not special-case the root path.
type restoreTransport struct {
	tasksBody      string
	tasksStatus    int
	conventionBody string
	conventionStat int

	gotConventionPath string
	gotConventionQ    url.Values
}

func (t *restoreTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasPrefix(req.URL.Path, "/_tasks") {
		status := t.tasksStatus
		if status == 0 {
			status = 200
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(t.tasksBody)),
			Request:    req,
		}, nil
	}
	t.gotConventionPath = req.URL.Path
	t.gotConventionQ = req.URL.Query()
	status := t.conventionStat
	if status == 0 {
		status = 200
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(t.conventionBody)),
		Request:    req,
	}, nil
}

func TestRestoreHistory_bothSourcesFalseIsUsageError(t *testing.T) {
	tr := &restoreTransport{}
	c := newProbedClient(t, tr)
	_, err := c.RestoreHistory(context.Background(), types.SnapshotRestoreHistoryRequest{})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Errorf("expected ErrUsage when both sources off, got %v", err)
	}
}

func TestRestoreHistory_taskHistoryParsesDescription(t *testing.T) {
	tr := &restoreTransport{
		tasksBody: `{
			"nodes": {
				"node-a": {
					"tasks": {
						"node-a:1": {
							"action": "cluster:admin/snapshot/restore",
							"description": "snapshot=fs:nightly-2024-01 indices[logs-*]",
							"start_time_in_millis": 1700000000000,
							"running_time_in_nanos": 1234,
							"cancelled": false
						}
					}
				}
			}
		}`,
	}
	c := newProbedClient(t, tr)
	got, err := c.RestoreHistory(context.Background(), types.SnapshotRestoreHistoryRequest{
		IncludeTaskHistory: true,
	})
	if err != nil {
		t.Fatalf("RestoreHistory: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].SnapshotID != "nightly-2024-01" {
		t.Errorf("SnapshotID = %q, want nightly-2024-01", got[0].SnapshotID)
	}
	if got[0].Repository != "fs" {
		t.Errorf("Repository = %q, want fs", got[0].Repository)
	}
	if got[0].Source != "task_history" {
		t.Errorf("Source = %q, want task_history", got[0].Source)
	}
	if !got[0].StartedAt.Equal(time.UnixMilli(1700000000000)) {
		t.Errorf("StartedAt = %v, want 2023-11-14 epoch", got[0].StartedAt)
	}
}

func TestRestoreHistory_taskHistoryPopulatesCompletedAt(t *testing.T) {
	tr := &restoreTransport{
		tasksBody: `{
			"nodes": {"node-a": {"tasks": {
				"node-a:1": {
					"action": "cluster:admin/snapshot/restore",
					"description": "snapshot=fs:s1",
					"start_time_in_millis": 1700000000000,
					"running_time_in_nanos": 1500000000
				}
			}}}
		}`,
	}
	c := newProbedClient(t, tr)
	got, err := c.RestoreHistory(context.Background(), types.SnapshotRestoreHistoryRequest{IncludeTaskHistory: true})
	if err != nil {
		t.Fatalf("RestoreHistory: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].CompletedAt == nil {
		t.Fatalf("CompletedAt = nil, want non-nil for non-zero running time")
	}
	wantCompleted := time.UnixMilli(1700000000000).Add(1500 * time.Millisecond)
	if !got[0].CompletedAt.Equal(wantCompleted) {
		t.Errorf("CompletedAt = %v, want %v", got[0].CompletedAt, wantCompleted)
	}
}

func TestRestoreHistory_taskHistorySkipsForeignActions(t *testing.T) {
	tr := &restoreTransport{
		tasksBody: `{
			"nodes": {"node-a": {"tasks": {
				"node-a:1": {"action":"cluster:admin/snapshot/restore","description":"snapshot=fs:wanted","start_time_in_millis":1700000000000},
				"node-a:2": {"action":"indices:data/write/bulk","description":"unrelated","start_time_in_millis":1700000000000}
			}}}
		}`,
	}
	c := newProbedClient(t, tr)
	got, err := c.RestoreHistory(context.Background(), types.SnapshotRestoreHistoryRequest{IncludeTaskHistory: true})
	if err != nil {
		t.Fatalf("RestoreHistory: %v", err)
	}
	if len(got) != 1 || got[0].SnapshotID != "wanted" {
		t.Errorf("got = %+v, want exactly the restore task", got)
	}
}

func TestRestoreHistory_conventionIndex404IsEmptySlice(t *testing.T) {
	tr := &restoreTransport{
		conventionStat: 404,
		conventionBody: `{"error":{"type":"index_not_found_exception","reason":"no such index"},"status":404}`,
	}
	c := newProbedClient(t, tr)
	got, err := c.RestoreHistory(context.Background(), types.SnapshotRestoreHistoryRequest{
		IncludeConventionIndex: true,
	})
	if err != nil {
		t.Fatalf("RestoreHistory: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("got = %v, want empty slice", got)
	}
}

func TestRestoreHistory_conventionIndexUsesDefaultName(t *testing.T) {
	tr := &restoreTransport{conventionBody: `{"hits":{"hits":[]}}`}
	c := newProbedClient(t, tr)
	if _, err := c.RestoreHistory(context.Background(), types.SnapshotRestoreHistoryRequest{
		IncludeConventionIndex: true,
	}); err != nil {
		t.Fatalf("RestoreHistory: %v", err)
	}
	want := "/.esops-doctor-restore-tests/_search"
	if !strings.HasPrefix(tr.gotConventionPath, want) {
		t.Errorf("path = %q, want prefix %q", tr.gotConventionPath, want)
	}
}

func TestRestoreHistory_conventionEventsAreSortedDescending(t *testing.T) {
	tr := &restoreTransport{
		conventionBody: `{"hits":{"hits":[
			{"_source":{"snapshot_id":"nightly-old","repository":"fs","started_at":"2024-01-01T00:00:00Z","completed_at":"2024-01-01T00:30:00Z","state":"success","indices":["a"]}},
			{"_source":{"snapshot_id":"nightly-new","repository":"fs","started_at":"2024-01-02T00:00:00Z","completed_at":"2024-01-02T00:30:00Z","state":"success","indices":["a"]}}
		]}}`,
	}
	c := newProbedClient(t, tr)
	got, err := c.RestoreHistory(context.Background(), types.SnapshotRestoreHistoryRequest{
		IncludeConventionIndex: true,
	})
	if err != nil {
		t.Fatalf("RestoreHistory: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].SnapshotID != "nightly-new" {
		t.Errorf("got[0].SnapshotID = %q, want nightly-new", got[0].SnapshotID)
	}
	if got[0].Source != "convention_index" {
		t.Errorf("got[0].Source = %q, want convention_index", got[0].Source)
	}
	if got[0].CompletedAt == nil {
		t.Errorf("CompletedAt = nil; expected non-nil")
	}
}
