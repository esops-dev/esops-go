package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/elastic/go-elasticsearch/v9/esapi"

	esopsclient "github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// snapshotRepoEntryWire mirrors a single value in the
// GET /_snapshot[/<repos>] response. The cluster keys the envelope by
// repository name; we decode the value side and surface Name from the
// key. Settings is left as a free-form map because each repository plugin
// (fs, s3, azure, gcs) ships its own set of keys — surfacing the cluster's
// shape verbatim avoids a translation table that drifts with new plugin
// versions.
type snapshotRepoEntryWire struct {
	Type     string         `json:"type"`
	Settings map[string]any `json:"settings"`
}

// snapshotEntryWire mirrors a single object inside the `snapshots` array
// of GET /_snapshot/<repo>/<pattern>. Both ES and OS share these fields;
// product-specific extras (slm policy on ES, remote_store_index_shallow_copy
// on OS) are intentionally not modeled here — `--output json/yaml`
// consumers who need them can fall back to the cluster's raw API.
type snapshotEntryWire struct {
	Snapshot           string   `json:"snapshot"`
	UUID               string   `json:"uuid"`
	State              string   `json:"state"`
	Indices            []string `json:"indices"`
	IncludeGlobalState bool     `json:"include_global_state"`
	StartTimeMillis    int64    `json:"start_time_in_millis"`
	EndTimeMillis      int64    `json:"end_time_in_millis"`
	DurationMillis     int64    `json:"duration_in_millis"`
	Version            string   `json:"version"`
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
// repo; non-empty is forwarded as a comma-separated path segment, which
// the cluster honours as a multi-name lookup.
func (c *Client) Repositories(ctx context.Context, filter types.SnapshotRepositoryFilter) ([]types.SnapshotRepository, error) {
	gr := c.api.Snapshot.GetRepository
	opts := []func(*esapi.SnapshotGetRepositoryRequest){gr.WithContext(ctx)}
	if names := esopsclient.SplitCSV(filter.Repository); len(names) > 0 {
		opts = append(opts, gr.WithRepository(names...))
	}

	resp, err := c.api.Snapshot.GetRepository(opts...)
	if err != nil {
		return nil, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("snapshot get_repository", resp.StatusCode, resp.Body); err != nil {
		return nil, err
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
	// Map iteration is unordered; sort for deterministic output so a
	// captured JSON record matches across re-runs.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Snapshots implements client.SnapshotInspector via
// GET /_snapshot/<repo>/<pattern>. The cluster requires a literal repo
// name in the path, so an empty/wildcard Repository fans out: list
// repositories first, then GET per repo. Same fan-out a hand-rolled curl
// loop would do — kept behind one capability call so callers see one
// flat snapshot list regardless of how many repos answered.
//
// Per-repo errors (a repo whose backing storage is unreachable returns
// 500) are surfaced with the repo name in the wrapped message rather
// than swallowed: a partial fleet view that hides a broken repo is
// strictly worse than failing loud.
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

// resolveRepoNames returns the literal list of repositories to query.
// Empty input → list every registered repo. Wildcard ("*") behaves the
// same. A comma-separated literal list is forwarded as-is; we trust the
// cluster to 404 on a typo rather than pre-validating client-side and
// drifting from cluster behaviour.
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
	sg := c.api.Snapshot.Get
	opts := []func(*esapi.SnapshotGetRequest){sg.WithContext(ctx)}
	if ignoreUnavailable {
		opts = append(opts, sg.WithIgnoreUnavailable(true))
	}
	resp, err := c.api.Snapshot.Get(repo, esopsclient.SplitCSV(pattern), opts...)
	if err != nil {
		return nil, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("snapshot get repo="+repo, resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var wire snapshotsResponseWire
	body := esopsclient.NewBoundedReader(resp.Body, esopsclient.DefaultMaxResponseBytes, "snapshot get")
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding snapshot get response"), err)
	}
	return wire.Snapshots, nil
}

// snapshotCreateResponseWire mirrors the PUT /_snapshot/<repo>/<snap>
// response. Two shapes share one decoder: when wait_for_completion=true
// the cluster returns `{"snapshot": {<entry>}}`; when false it returns
// `{"accepted": true}`. Both fields are tagged so a single decode
// captures whichever the cluster sent.
type snapshotCreateResponseWire struct {
	Accepted bool               `json:"accepted"`
	Snapshot *snapshotEntryWire `json:"snapshot"`
}

// CreateSnapshot implements client.SnapshotCreator via
// PUT /_snapshot/<repo>/<snapshot>. The body shape is the same on both
// dialects, so the same decoder serves the OS adapter via the shared
// helper below.
func (c *Client) CreateSnapshot(ctx context.Context, req types.SnapshotCreateRequest) (types.SnapshotCreateResult, error) {
	body, err := buildSnapshotCreateBody(req)
	if err != nil {
		return types.SnapshotCreateResult{}, err
	}

	cr := c.api.Snapshot.Create
	opts := []func(*esapi.SnapshotCreateRequest){
		cr.WithContext(ctx),
		cr.WithBody(bytes.NewReader(body)),
	}
	if req.WaitForCompletion {
		opts = append(opts, cr.WithWaitForCompletion(true))
	}

	resp, err := c.api.Snapshot.Create(req.Repository, req.Snapshot, opts...)
	if err != nil {
		return types.SnapshotCreateResult{}, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("snapshot create repo="+req.Repository+" snapshot="+req.Snapshot, resp.StatusCode, resp.Body); err != nil {
		return types.SnapshotCreateResult{}, err
	}

	var wire snapshotCreateResponseWire
	bodyR := esopsclient.NewBoundedReader(resp.Body, esopsclient.DefaultMaxResponseBytes, "snapshot create")
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return types.SnapshotCreateResult{}, errors.Join(errors.New("decoding snapshot create response"), err)
	}

	return snapshotCreateResultFromWire(req, wire), nil
}

// snapshotVerifyResponseWire mirrors POST /_snapshot/<repo>/_verify. The
// cluster returns a `nodes` object keyed by node id; each value is an
// object with a `name` field. Same shape on ES and OS — the OS adapter
// decodes into the same struct via the shared helper below.
type snapshotVerifyResponseWire struct {
	Nodes map[string]struct {
		Name string `json:"name"`
	} `json:"nodes"`
}

// VerifyRepository implements client.SnapshotVerifier via
// POST /_snapshot/<repo>/_verify.
func (c *Client) VerifyRepository(ctx context.Context, req types.SnapshotVerifyRequest) (types.SnapshotVerifyResult, error) {
	vr := c.api.Snapshot.VerifyRepository
	opts := []func(*esapi.SnapshotVerifyRepositoryRequest){vr.WithContext(ctx)}

	resp, err := c.api.Snapshot.VerifyRepository(req.Repository, opts...)
	if err != nil {
		return types.SnapshotVerifyResult{}, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("snapshot verify repo="+req.Repository, resp.StatusCode, resp.Body); err != nil {
		return types.SnapshotVerifyResult{}, err
	}

	var wire snapshotVerifyResponseWire
	bodyR := esopsclient.NewBoundedReader(resp.Body, esopsclient.DefaultMaxResponseBytes, "snapshot verify")
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return types.SnapshotVerifyResult{}, errors.Join(errors.New("decoding snapshot verify response"), err)
	}
	return snapshotVerifyResultFromWire(req.Repository, wire), nil
}

// buildSnapshotCreateBody renders the request body the cluster expects.
// Empty fields are omitted entirely so the cluster falls back to its own
// defaults — encoding `"indices": ""` would over-specify the request and
// risks drift if a future cluster version interprets the empty pattern
// differently from the omitted field.
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

// snapshotCreateResultFromWire builds the neutral result. When
// wait_for_completion was true the cluster returns the full snapshot
// entry, so we route it through the same conversion helper used by
// `snapshot list`. When false (the default), the cluster only returns
// `accepted: true` and the caller polls list to learn the state.
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
		// The cluster returns the snapshot block only after the
		// operation finished; treat that as acknowledgement so callers
		// who synced on `Accepted` see a consistent signal across both
		// wait modes.
		out.Accepted = true
	case req.WaitForCompletion:
		// Defensive — the cluster always populates `snapshot` when
		// wait_for_completion=true, but if a future version changes that
		// we still surface the operator's intent on the captured record.
		out.Snapshot = types.Snapshot{Repository: req.Repository, Name: req.Snapshot}
	default:
		// Async path: surface a placeholder state so a captured JSON
		// record describes the queued operation without the operator
		// having to remember "no snapshot block = STARTED".
		out.Snapshot = types.Snapshot{Repository: req.Repository, Name: req.Snapshot, State: "STARTED"}
	}
	return out
}

// snapshotVerifyResultFromWire flattens the cluster's `nodes:{<id>:{name}}`
// map into two parallel slices sorted by node id for deterministic output.
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

// snapshotFromWire converts the ES wire shape into the neutral type.
// Failures are surfaced as raw JSON strings so the cluster's per-failure
// reasons (which carry the operator's most useful signal) survive the
// translation without a per-version decoder. Used by both ES and OS
// adapters since the wire shape for a snapshot entry is identical.
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
