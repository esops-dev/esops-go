package opensearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	esopsclient "github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// snapshotRepoEntryWire mirrors a single value in the
// GET /_snapshot[/<repos>] response. Settings is decoded as map[string]any
// because the upstream typed client's map[string]string can't carry nested
// objects from plugins like s3 ("settings.repositories.url" hashes) — going
// through raw HTTP keeps the cluster's full shape.
type snapshotRepoEntryWire struct {
	Type     string         `json:"type"`
	Settings map[string]any `json:"settings"`
}

// snapshotEntryWire is the per-snapshot row inside the `snapshots` array.
// Same field set as the ES adapter's wire because both products share the
// shape — the divergence is at the repository plugin layer (which we
// surface verbatim via Settings) not at the snapshot envelope.
type snapshotEntryWire struct {
	Snapshot           string            `json:"snapshot"`
	UUID               string            `json:"uuid"`
	State              string            `json:"state"`
	Indices            []string          `json:"indices"`
	IncludeGlobalState bool              `json:"include_global_state"`
	StartTimeMillis    int64             `json:"start_time_in_millis"`
	EndTimeMillis      int64             `json:"end_time_in_millis"`
	DurationMillis     int64             `json:"duration_in_millis"`
	Version            string            `json:"version"`
	Failures           []json.RawMessage `json:"failures"`
	Shards             struct {
		Total      int `json:"total"`
		Successful int `json:"successful"`
		Failed     int `json:"failed"`
	} `json:"shards"`
}

type snapshotsResponseWire struct {
	Snapshots []snapshotEntryWire `json:"snapshots"`
}

// Repositories implements client.SnapshotInspector via
// GET /_snapshot[/<repos>]. Empty Repository returns every registered
// repo. Uses raw HTTP rather than the typed SnapshotRepository.Get because
// the upstream's typed Settings is map[string]string, which silently
// drops nested plugin settings (e.g. s3 endpoint overrides).
func (c *Client) Repositories(ctx context.Context, filter types.SnapshotRepositoryFilter) ([]types.SnapshotRepository, error) {
	path := "/_snapshot"
	if names := esopsclient.SplitCSV(filter.Repository); len(names) > 0 {
		path += "/" + strings.Join(names, ",")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.Client.Perform(req)
	if err != nil {
		return nil, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(esopsclient.NewBoundedReader(resp.Body, 64*1024, "snapshot get_repository error"))
		return nil, esopsclient.ClassifyStatus("snapshot get_repository", resp.StatusCode, body)
	}

	var wire map[string]snapshotRepoEntryWire
	body := esopsclient.NewBoundedReader(resp.Body, esopsclient.DefaultMaxResponseBytes, "snapshot get_repository")
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding snapshot get_repository response"), err)
	}

	out := make([]types.SnapshotRepository, 0, len(wire))
	for name, r := range wire {
		out = append(out, types.SnapshotRepository{
			Name:     name,
			Type:     r.Type,
			Settings: r.Settings,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Snapshots implements client.SnapshotInspector. OpenSearch's
// /_snapshot/<repo>/<pattern> endpoint requires a single literal repo
// name — comma-separated repo names are not honoured (unlike ES 8+) — so
// we always fan out client-side. An empty/wildcard Repository resolves
// to "every registered repo" first, then GET per repo. The fan-out is
// the same shape an operator would write by hand; folding it behind one
// capability call is the value-add.
func (c *Client) Snapshots(ctx context.Context, filter types.SnapshotFilter) ([]types.Snapshot, error) {
	repos, err := c.resolveRepoNames(ctx, filter.Repository)
	if err != nil {
		return nil, err
	}

	pattern := filter.Snapshot
	if pattern == "" {
		pattern = "*"
	}

	out := make([]types.Snapshot, 0)
	for _, repo := range repos {
		entries, err := c.snapshotsForRepo(ctx, repo, pattern, filter.IgnoreUnavailable)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			out = append(out, snapshotFromWire(repo, e))
		}
	}
	return out, nil
}

func (c *Client) resolveRepoNames(ctx context.Context, repository string) ([]string, error) {
	repository = strings.TrimSpace(repository)
	if repository != "" && repository != "*" {
		return esopsclient.SplitCSV(repository), nil
	}
	repos, err := c.Repositories(ctx, types.SnapshotRepositoryFilter{})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		names = append(names, r.Name)
	}
	return names, nil
}

func (c *Client) snapshotsForRepo(ctx context.Context, repo, pattern string, ignoreUnavailable bool) ([]snapshotEntryWire, error) {
	path := fmt.Sprintf("/_snapshot/%s/%s", url.PathEscape(repo), pattern)
	if ignoreUnavailable {
		path += "?ignore_unavailable=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.Client.Perform(req)
	if err != nil {
		return nil, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(esopsclient.NewBoundedReader(resp.Body, 64*1024, "snapshot get error"))
		return nil, esopsclient.ClassifyStatus("snapshot get repo="+repo, resp.StatusCode, body)
	}

	var wire snapshotsResponseWire
	body := esopsclient.NewBoundedReader(resp.Body, esopsclient.DefaultMaxResponseBytes, "snapshot get")
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding snapshot get response"), err)
	}
	return wire.Snapshots, nil
}

// snapshotFromWire mirrors the ES adapter's helper. Kept as a private
// package-level function (rather than shared via internal/types) because
// the wire struct is package-private — the conversion is mechanical and
// duplicating one ten-line function across two packages beats exporting
// either type.
func snapshotFromWire(repo string, e snapshotEntryWire) types.Snapshot {
	out := types.Snapshot{
		Repository:         repo,
		Name:               e.Snapshot,
		UUID:               e.UUID,
		State:              e.State,
		Indices:            e.Indices,
		IncludeGlobalState: e.IncludeGlobalState,
		StartTimeMillis:    e.StartTimeMillis,
		EndTimeMillis:      e.EndTimeMillis,
		DurationMillis:     e.DurationMillis,
		ShardsTotal:        e.Shards.Total,
		ShardsSuccessful:   e.Shards.Successful,
		ShardsFailed:       e.Shards.Failed,
		Version:            e.Version,
	}
	if len(e.Failures) > 0 {
		failures := make([]string, 0, len(e.Failures))
		for _, raw := range e.Failures {
			failures = append(failures, string(raw))
		}
		out.Failures = failures
	}
	return out
}
