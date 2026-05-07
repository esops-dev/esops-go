package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// defaultRestoreConventionIndex is the index esops-doctor's runbooks
// write to when an operator exercises a verified restore. The
// capability accepts an override per-call so a deployment that uses a
// different naming scheme can wire its own; the default keeps the
// "out of the box" path documented in one place.
const defaultRestoreConventionIndex = ".esops-doctor-restore-tests"

// restoreTaskWire mirrors the slice of GET /_tasks?actions=cluster:admin/snapshot/restore*
// the capability reads. The cluster reports timing in epoch milliseconds
// and the per-restore description as a free-text string the cluster
// composes from "snapshot=<repo>:<name>"; we parse it best-effort and
// fall through to empty fields when the format ever drifts.
type restoreTaskWire struct {
	Action          string `json:"action"`
	Description     string `json:"description"`
	StartTimeMillis int64  `json:"start_time_in_millis"`
	// running_time_in_nanos lets us compute completedAt for in-flight
	// tasks the cluster has yet to mark "completed" — we surface
	// CompletedAt only when the task has actually finished.
	RunningTimeNanos int64 `json:"running_time_in_nanos"`
	Cancelled        bool  `json:"cancelled"`
}

// RestoreHistory implements client.SnapshotRestoreHistoryInspector. The
// two sources are read independently — operators tune which path they
// trust per environment:
//
//   - task_history is best-effort. The cluster only retains task entries
//     for as long as its task index keeps them, so a quiet cluster
//     reports nothing rather than "never restored". Treat an empty
//     slice as "no evidence in the lookback window".
//   - convention_index reads a metadata index esops-doctor's runbooks
//     write to. Deliberately uses the same /_search read window the
//     DocumentInspector capability uses, but gathers the structured
//     fields (snapshot_id/repository/started_at/state/indices) for the
//     RestoreEvent shape rather than returning raw documents.
//
// Sort order is StartedAt descending so the freshest event is first —
// the rule consumer typically wants "the most recent verified restore"
// not "every restore ever recorded".
func (c *Client) RestoreHistory(ctx context.Context, req types.SnapshotRestoreHistoryRequest) ([]types.RestoreEvent, error) {
	if !req.IncludeTaskHistory && !req.IncludeConventionIndex {
		return nil, esopsclient.UsageError(
			"restore history: at least one of IncludeTaskHistory or IncludeConventionIndex must be set",
		)
	}

	out := make([]types.RestoreEvent, 0)

	if req.IncludeTaskHistory {
		taskEvents, err := c.fetchRestoreTaskHistory(ctx, req.Lookback)
		if err != nil {
			return nil, err
		}
		out = append(out, taskEvents...)
	}

	if req.IncludeConventionIndex {
		idx := req.ConventionIndex
		if idx == "" {
			idx = defaultRestoreConventionIndex
		}
		convEvents, err := c.fetchRestoreConventionEvents(ctx, idx)
		if err != nil {
			return nil, err
		}
		out = append(out, convEvents...)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

func (c *Client) fetchRestoreTaskHistory(ctx context.Context, lookback time.Duration) ([]types.RestoreEvent, error) {
	tl := c.api.Tasks.List
	resp, err := c.api.Tasks.List(
		tl.WithContext(ctx),
		tl.WithActions("cluster:admin/snapshot/restore*"),
		tl.WithDetailed(true),
	)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("tasks list (restore history)", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var wire struct {
		Nodes map[string]struct {
			Tasks map[string]restoreTaskWire `json:"tasks"`
		} `json:"nodes"`
	}
	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "tasks list (restore history)")
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding tasks list (restore history) response"), err)
	}

	cutoff := time.Time{}
	if lookback > 0 {
		cutoff = time.Now().Add(-lookback)
	}
	out := make([]types.RestoreEvent, 0)
	for _, node := range wire.Nodes {
		for _, t := range node.Tasks {
			// Defence-in-depth: the cluster is supposed to honour our
			// `actions=cluster:admin/snapshot/restore*` filter, but a
			// future cluster build that loosens the match (or a proxy
			// that strips the query param) would leak unrelated tasks
			// into the result. Filter client-side as well.
			if !strings.HasPrefix(t.Action, "cluster:admin/snapshot/restore") {
				continue
			}
			started := time.UnixMilli(t.StartTimeMillis)
			if !cutoff.IsZero() && started.Before(cutoff) {
				continue
			}
			repo, snap := parseRestoreDescription(t.Description)
			ev := types.RestoreEvent{
				SnapshotID: snap,
				Repository: repo,
				StartedAt:  started,
				State:      "in_progress",
				Source:     "task_history",
			}
			// running_time_in_nanos is the cluster's per-task elapsed
			// counter — it advances while the task is alive. The task
			// API does not surface a separate "is the task done" flag
			// (apart from cancelled), so by the time a /_tasks read
			// returns the entry, the task is still running. We mark
			// CompletedAt = StartedAt + RunningTime when the cluster
			// reports a non-zero running time so a downstream renderer
			// has a stable end-of-window timestamp; the rule consumer
			// distinguishes "completed" from "still running" via State,
			// not the presence of CompletedAt.
			if t.RunningTimeNanos > 0 {
				completed := started.Add(time.Duration(t.RunningTimeNanos) * time.Nanosecond)
				ev.CompletedAt = &completed
			}
			if t.Cancelled {
				ev.State = "failed"
			}
			out = append(out, ev)
		}
	}
	return out, nil
}

// parseRestoreDescription extracts the "snapshot=<repo>:<name>" hint
// the cluster bakes into the task description. Best-effort: an empty
// or unparseable description returns empty strings rather than an
// error — the SnapshotID is still surfaced through the convention
// index path when an operator records the restore there.
func parseRestoreDescription(desc string) (repo, snapshot string) {
	const key = "snapshot="
	idx := strings.Index(desc, key)
	if idx < 0 {
		return "", ""
	}
	rest := desc[idx+len(key):]
	end := strings.IndexAny(rest, " ,]")
	if end >= 0 {
		rest = rest[:end]
	}
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", parts[0]
}

// restoreEventDocWire mirrors the document shape esops-doctor's
// runbooks write into the convention index. Liberal on field names so
// older runbooks with different field naming still parse — a missing
// field falls back to the zero value rather than an error.
type restoreEventDocWire struct {
	SnapshotID  string    `json:"snapshot_id"`
	Repository  string    `json:"repository"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	State       string    `json:"state"`
	Indices     []string  `json:"indices"`
}

func (c *Client) fetchRestoreConventionEvents(ctx context.Context, index string) ([]types.RestoreEvent, error) {
	body, err := json.Marshal(map[string]any{
		"size":             1000,
		"track_total_hits": false,
		"sort":             []map[string]any{{"started_at": map[string]any{"order": "desc"}}},
		"query":            map[string]any{"match_all": map[string]any{}},
	})
	if err != nil {
		return nil, err
	}
	path := "/" + url.PathEscape(index) + "/_search?ignore_unavailable=true&allow_no_indices=true"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.api.Perform(httpReq)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 404 with "no such index" is *expected*: a fresh deployment without a
	// recorded restore yet is the most common case. Surface it as an empty
	// slice rather than an error so the rule treats "no evidence" as the
	// data, not a failure.
	if resp.StatusCode == http.StatusNotFound {
		return []types.RestoreEvent{}, nil
	}
	if err := classifyResponse("restore-history convention search", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	bodyR := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "restore-history convention search")
	var wire struct {
		Hits struct {
			Hits []struct {
				Source restoreEventDocWire `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding restore-history convention search response"), err)
	}
	out := make([]types.RestoreEvent, 0, len(wire.Hits.Hits))
	for _, h := range wire.Hits.Hits {
		ev := types.RestoreEvent{
			SnapshotID: h.Source.SnapshotID,
			Repository: h.Source.Repository,
			StartedAt:  h.Source.StartedAt,
			State:      h.Source.State,
			Indices:    h.Source.Indices,
			Source:     "convention_index",
		}
		if !h.Source.CompletedAt.IsZero() {
			c := h.Source.CompletedAt
			ev.CompletedAt = &c
		}
		out = append(out, ev)
	}
	return out, nil
}
