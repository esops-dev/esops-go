package opensearch

import (
	"bytes"
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

// snapshotCreateResponseWire mirrors the PUT /_snapshot/<repo>/<snap>
// response on OpenSearch — same envelope as Elasticsearch. Two shapes
// share one decoder: when wait_for_completion=true the cluster returns
// `{"snapshot": {<entry>}}`; when false it returns `{"accepted": true}`.
type snapshotCreateResponseWire struct {
	Accepted bool               `json:"accepted"`
	Snapshot *snapshotEntryWire `json:"snapshot"`
}

// CreateSnapshot implements client.SnapshotCreator via
// PUT /_snapshot/<repo>/<snapshot>. Goes through raw HTTP (rather than
// the typed osapi.SnapshotCreate) because the typed client's request
// shape forces the body through a struct that doesn't carry the
// `metadata` block — round-tripping a free-form metadata map is
// straightforward over HTTP.
func (c *Client) CreateSnapshot(ctx context.Context, req types.SnapshotCreateRequest) (types.SnapshotCreateResult, error) {
	body, err := buildSnapshotCreateBody(req)
	if err != nil {
		return types.SnapshotCreateResult{}, err
	}

	path := fmt.Sprintf("/_snapshot/%s/%s", url.PathEscape(req.Repository), url.PathEscape(req.Snapshot))
	if req.WaitForCompletion {
		path += "?wait_for_completion=true"
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, path, bytes.NewReader(body))
	if err != nil {
		return types.SnapshotCreateResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.SnapshotCreateResult{}, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(esopsclient.NewBoundedReader(resp.Body, 64*1024, "snapshot create error"))
		return types.SnapshotCreateResult{}, esopsclient.ClassifyStatus("snapshot create repo="+req.Repository+" snapshot="+req.Snapshot, resp.StatusCode, errBody)
	}

	var wire snapshotCreateResponseWire
	bodyR := esopsclient.NewBoundedReader(resp.Body, esopsclient.DefaultMaxResponseBytes, "snapshot create")
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return types.SnapshotCreateResult{}, errors.Join(errors.New("decoding snapshot create response"), err)
	}
	return snapshotCreateResultFromWire(req, wire), nil
}

// snapshotVerifyResponseWire mirrors POST /_snapshot/<repo>/_verify on
// OpenSearch. The cluster returns a `nodes` object keyed by node id; each
// value carries a `name` field. Same shape as Elasticsearch.
type snapshotVerifyResponseWire struct {
	Nodes map[string]struct {
		Name string `json:"name"`
	} `json:"nodes"`
}

// VerifyRepository implements client.SnapshotVerifier via
// POST /_snapshot/<repo>/_verify.
func (c *Client) VerifyRepository(ctx context.Context, req types.SnapshotVerifyRequest) (types.SnapshotVerifyResult, error) {
	path := fmt.Sprintf("/_snapshot/%s/_verify", url.PathEscape(req.Repository))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, path, nil)
	if err != nil {
		return types.SnapshotVerifyResult{}, err
	}

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.SnapshotVerifyResult{}, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(esopsclient.NewBoundedReader(resp.Body, 64*1024, "snapshot verify error"))
		return types.SnapshotVerifyResult{}, esopsclient.ClassifyStatus("snapshot verify repo="+req.Repository, resp.StatusCode, errBody)
	}

	var wire snapshotVerifyResponseWire
	bodyR := esopsclient.NewBoundedReader(resp.Body, esopsclient.DefaultMaxResponseBytes, "snapshot verify")
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return types.SnapshotVerifyResult{}, errors.Join(errors.New("decoding snapshot verify response"), err)
	}
	return snapshotVerifyResultFromWire(req.Repository, wire), nil
}

// buildSnapshotCreateBody renders the request body. Mirrors the ES
// adapter's helper (the body shape is identical between dialects);
// duplicated rather than shared because the wire struct is package-private.
func buildSnapshotCreateBody(req types.SnapshotCreateRequest) ([]byte, error) {
	body := make(map[string]any)
	if indices := strings.TrimSpace(req.Indices); indices != "" {
		body["indices"] = indices
	}
	if req.IncludeGlobalState != nil {
		body["include_global_state"] = *req.IncludeGlobalState
	}
	if req.Partial != nil {
		body["partial"] = *req.Partial
	}
	if req.IgnoreUnavailable != nil {
		body["ignore_unavailable"] = *req.IgnoreUnavailable
	}
	if len(req.Metadata) > 0 {
		body["metadata"] = req.Metadata
	}
	return json.Marshal(body)
}

func snapshotCreateResultFromWire(req types.SnapshotCreateRequest, wire snapshotCreateResponseWire) types.SnapshotCreateResult {
	out := types.SnapshotCreateResult{
		Action:            "create",
		Repository:        req.Repository,
		Name:              req.Snapshot,
		WaitForCompletion: req.WaitForCompletion,
		Accepted:          wire.Accepted,
	}
	switch {
	case wire.Snapshot != nil:
		out.Snapshot = snapshotFromWire(req.Repository, *wire.Snapshot)
		out.Accepted = true
	case req.WaitForCompletion:
		out.Snapshot = types.Snapshot{Repository: req.Repository, Name: req.Snapshot}
	default:
		out.Snapshot = types.Snapshot{Repository: req.Repository, Name: req.Snapshot, State: "STARTED"}
	}
	return out
}

func snapshotVerifyResultFromWire(repo string, wire snapshotVerifyResponseWire) types.SnapshotVerifyResult {
	ids := make([]string, 0, len(wire.Nodes))
	for id := range wire.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		names = append(names, wire.Nodes[id].Name)
	}
	return types.SnapshotVerifyResult{
		Action:        "verify",
		Repository:    repo,
		VerifiedNodes: ids,
		Names:         names,
	}
}

// snapshotRestoreResponseWire mirrors POST /_snapshot/<repo>/<snap>/_restore
// on OpenSearch — same envelope as Elasticsearch.
type snapshotRestoreResponseWire struct {
	Accepted bool `json:"accepted"`
	Snapshot *struct {
		Snapshot string   `json:"snapshot"`
		Indices  []string `json:"indices"`
		Shards   struct {
			Total      int `json:"total"`
			Successful int `json:"successful"`
			Failed     int `json:"failed"`
		} `json:"shards"`
	} `json:"snapshot"`
}

// RestoreSnapshot implements client.SnapshotRestorer via
// POST /_snapshot/<repo>/<snap>/_restore. Goes through raw HTTP for the same
// reason CreateSnapshot does — the typed osapi shape doesn't carry the full
// rename/index_settings block round-trip cleanly.
func (c *Client) RestoreSnapshot(ctx context.Context, req types.SnapshotRestoreRequest) (types.SnapshotRestoreResult, error) {
	body, err := buildSnapshotRestoreBody(req)
	if err != nil {
		return types.SnapshotRestoreResult{}, err
	}

	path := fmt.Sprintf("/_snapshot/%s/%s/_restore", url.PathEscape(req.Repository), url.PathEscape(req.Snapshot))
	if req.WaitForCompletion {
		path += "?wait_for_completion=true"
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return types.SnapshotRestoreResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.SnapshotRestoreResult{}, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(esopsclient.NewBoundedReader(resp.Body, 64*1024, "snapshot restore error"))
		return types.SnapshotRestoreResult{}, esopsclient.ClassifyStatus("snapshot restore repo="+req.Repository+" snapshot="+req.Snapshot, resp.StatusCode, errBody)
	}

	var wire snapshotRestoreResponseWire
	bodyR := esopsclient.NewBoundedReader(resp.Body, esopsclient.DefaultMaxResponseBytes, "snapshot restore")
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return types.SnapshotRestoreResult{}, errors.Join(errors.New("decoding snapshot restore response"), err)
	}
	return snapshotRestoreResultFromWire(req, wire), nil
}

// DeleteSnapshots implements client.SnapshotPruner via
// DELETE /_snapshot/<repo>/<snap[,snap…]>. OpenSearch accepts the same
// comma-separated path segment Elasticsearch does, so one bulk request per
// repository covers any prune plan.
func (c *Client) DeleteSnapshots(ctx context.Context, req types.SnapshotDeleteRequest) (types.SnapshotDeleteResult, error) {
	if len(req.Snapshots) == 0 {
		return types.SnapshotDeleteResult{}, errors.New("snapshot delete: no snapshots specified")
	}

	path := fmt.Sprintf("/_snapshot/%s/%s", url.PathEscape(req.Repository), strings.Join(req.Snapshots, ","))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return types.SnapshotDeleteResult{}, err
	}

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.SnapshotDeleteResult{}, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(esopsclient.NewBoundedReader(resp.Body, 64*1024, "snapshot delete error"))
		return types.SnapshotDeleteResult{}, esopsclient.ClassifyStatus("snapshot delete repo="+req.Repository, resp.StatusCode, errBody)
	}

	var wire struct {
		Acknowledged bool `json:"acknowledged"`
	}
	bodyR := esopsclient.NewBoundedReader(resp.Body, esopsclient.DefaultMaxResponseBytes, "snapshot delete")
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return types.SnapshotDeleteResult{}, errors.Join(errors.New("decoding snapshot delete response"), err)
	}
	return types.SnapshotDeleteResult{
		Action:       "delete",
		Repository:   req.Repository,
		Snapshots:    append([]string(nil), req.Snapshots...),
		Acknowledged: wire.Acknowledged,
	}, nil
}

// buildSnapshotRestoreBody mirrors the ES adapter's helper. Duplicated rather
// than shared because the wire structs are package-private and the function is
// a flat translation.
func buildSnapshotRestoreBody(req types.SnapshotRestoreRequest) ([]byte, error) {
	body := make(map[string]any)
	if indices := strings.TrimSpace(req.Indices); indices != "" {
		body["indices"] = indices
	}
	if req.IncludeGlobalState != nil {
		body["include_global_state"] = *req.IncludeGlobalState
	}
	if req.Partial != nil {
		body["partial"] = *req.Partial
	}
	if req.IgnoreUnavailable != nil {
		body["ignore_unavailable"] = *req.IgnoreUnavailable
	}
	if req.RenamePattern != "" {
		body["rename_pattern"] = req.RenamePattern
	}
	if req.RenameReplacement != "" {
		body["rename_replacement"] = req.RenameReplacement
	}
	if len(req.IndexSettings) > 0 {
		body["index_settings"] = req.IndexSettings
	}
	if len(req.IgnoreIndexSettings) > 0 {
		body["ignore_index_settings"] = req.IgnoreIndexSettings
	}
	return json.Marshal(body)
}

func snapshotRestoreResultFromWire(req types.SnapshotRestoreRequest, wire snapshotRestoreResponseWire) types.SnapshotRestoreResult {
	out := types.SnapshotRestoreResult{
		Action:            "restore",
		Repository:        req.Repository,
		Name:              req.Snapshot,
		WaitForCompletion: req.WaitForCompletion,
		Accepted:          wire.Accepted,
		RenamePattern:     req.RenamePattern,
		RenameReplacement: req.RenameReplacement,
	}
	if wire.Snapshot != nil {
		out.Snapshot = types.SnapshotRestoreInfo{
			Snapshot:         wire.Snapshot.Snapshot,
			Indices:          wire.Snapshot.Indices,
			ShardsTotal:      wire.Snapshot.Shards.Total,
			ShardsSuccessful: wire.Snapshot.Shards.Successful,
			ShardsFailed:     wire.Snapshot.Shards.Failed,
		}
		out.Accepted = true
	} else {
		out.Snapshot = types.SnapshotRestoreInfo{Snapshot: req.Snapshot}
	}
	return out
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
