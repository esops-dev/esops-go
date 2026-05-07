package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	osapi "github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// defaultRestoreConventionIndex matches the ES adapter's default — the
// convention-based index esops-doctor's runbooks write to when an
// operator exercises a verified restore. Same name on both adapters so
// a single runbook works against either dialect.
const defaultRestoreConventionIndex = ".esops-doctor-restore-tests"

// RestoreHistory implements client.SnapshotRestoreHistoryInspector for
// OpenSearch. The two-source split mirrors the ES adapter exactly: see
// the matching elasticsearch.Client.RestoreHistory for the rationale —
// the implementations are deliberately kept symmetrical so a contract
// test against both dialects exercises the same surface.
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
	detailed := true
	params := osapi.TasksListParams{
		Actions:  []string{"cluster:admin/snapshot/restore*"},
		Detailed: &detailed,
	}
	resp, err := c.api.Tasks.List(ctx, &osapi.TasksListReq{Params: params})
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return nil, classifyResponseErr(err, status)
	}

	cutoff := time.Time{}
	if lookback > 0 {
		cutoff = time.Now().Add(-lookback)
	}
	out := make([]types.RestoreEvent, 0)
	for _, node := range resp.Nodes {
		for _, t := range node.Tasks {
			// Defence-in-depth: see the matching ES adapter for the
			// rationale. The cluster's actions= filter is supposed to
			// be authoritative, but a future cluster build that
			// loosens the match (or a proxy that strips the param)
			// would leak unrelated tasks.
			if !strings.HasPrefix(t.Action, "cluster:admin/snapshot/restore") {
				continue
			}
			started := time.UnixMilli(t.StartTimeInMillis)
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
			// Same CompletedAt convention as the ES adapter:
			// StartedAt + RunningTimeInNanos when the cluster reports
			// non-zero elapsed time. Distinguishing "still running"
			// from "completed" goes through State, not CompletedAt.
			if t.RunningTimeInNanos > 0 {
				completed := started.Add(time.Duration(t.RunningTimeInNanos) * time.Nanosecond)
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

// parseRestoreDescription extracts "snapshot=<repo>:<name>" from the
// cluster's task description. Best-effort — the cluster's wording has
// evolved across versions and an unparseable description leaves the
// SnapshotID/Repository empty rather than failing the whole call.
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

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return []types.RestoreEvent{}, nil
	}
	respBody, err := io.ReadAll(clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "restore-history convention search"))
	if err != nil {
		return nil, errors.Join(errors.New("reading restore-history convention search response"), err)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, esopsclient.ErrAuth
	case http.StatusForbidden:
		return nil, esopsclient.ErrForbidden
	}
	if resp.StatusCode >= 400 {
		return nil, clientutil.ClassifyStatus("restore-history convention search", resp.StatusCode, respBody)
	}

	var wire struct {
		Hits struct {
			Hits []struct {
				Source restoreEventDocWire `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(respBody, &wire); err != nil {
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
