package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/elastic/go-elasticsearch/v9/esapi"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/internal/clientutil"
	"github.com/esops-dev/esops-go/internal/client/snapshotwire"
	"github.com/esops-dev/esops-go/pkg/types"
)

// Repositories implements client.SnapshotInspector via
// GET /_snapshot[/<repos>]. Empty Repository returns every registered
// repo; non-empty is forwarded as a comma-separated path segment which
// the cluster honours as a multi-name lookup.
func (c *Client) Repositories(ctx context.Context, filter types.SnapshotRepositoryFilter) ([]types.SnapshotRepository, error) {
	gr := c.api.Snapshot.GetRepository
	opts := []func(*esapi.SnapshotGetRepositoryRequest){gr.WithContext(ctx)}
	if names := esopsclient.SplitCSV(filter.Repository); len(names) > 0 {
		opts = append(opts, gr.WithRepository(names...))
	}

	resp, err := c.api.Snapshot.GetRepository(opts...)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("snapshot get_repository", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var wire map[string]snapshotwire.RepoEntry
	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "snapshot get_repository")
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding snapshot get_repository response"), err)
	}
	return snapshotwire.RepositoriesFromWire(wire), nil
}

// Snapshots implements client.SnapshotInspector via the buffered fan-out
// path. ES 8+ accepts a comma-separated repository list in the request
// path so a multi-repo lookup can collapse to a single round-trip on
// recent clusters; older clusters fall back to per-repo requests.
//
// Per-repo errors are surfaced as a wrapped error rather than swallowed:
// hiding a broken repo behind a partial fleet view is strictly worse
// than failing loud. Callers who want partial-with-errors semantics use
// SnapshotsStream instead.
func (c *Client) Snapshots(ctx context.Context, filter types.SnapshotFilter) ([]types.Snapshot, error) {
	repos, err := c.resolveRepoNames(ctx, filter.Repository)
	if err != nil {
		return nil, err
	}
	if len(repos) == 0 {
		return []types.Snapshot{}, nil
	}

	pattern := filter.Snapshot
	if pattern == "" {
		pattern = "*"
	}

	out := make([]types.Snapshot, 0)
	if len(repos) > 1 {
		entries, ok, err := c.snapshotsMultiRepo(ctx, repos, pattern, filter)
		if err != nil {
			return nil, err
		}
		if ok {
			for repo, list := range entries {
				for _, e := range list {
					out = append(out, snapshotwire.SnapshotFromWire(repo, e))
				}
			}
			return out, nil
		}
		// Fall through to per-repo path: cluster echoed entries without
		// repository tags (pre-ES 8.1) — issue per-repo requests so the
		// caller's repo→snapshots mapping is correct.
	}

	for _, repo := range repos {
		entries, err := c.snapshotsForRepo(ctx, repo, pattern, filter)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			out = append(out, snapshotwire.SnapshotFromWire(repo, e))
		}
	}
	return out, nil
}

// SnapshotsStream implements client.SnapshotInspector. Resolves the
// repository set, then fans the per-repo requests out concurrently with
// a small worker pool — bandwidth-bound work against an S3-backed
// repository easily takes 200-500ms each, so 4-way parallelism turns a
// 20-repo fleet from 5s to 1s without overwhelming the cluster's master
// node. Yields go through a serialised callback so the caller's writer
// (NDJSON encoder) doesn't need its own mutex.
//
// Per-repo errors are reported via repoErr and iteration continues; one
// S3 outage doesn't blank the rest of the fleet. The returned error is
// non-nil only on a fatal failure (initial repo resolution, context
// cancellation).
func (c *Client) SnapshotsStream(ctx context.Context, filter types.SnapshotFilter, yield func(types.Snapshot) error, repoErr func(repo string, err error)) error {
	repos, err := c.resolveRepoNames(ctx, filter.Repository)
	if err != nil {
		return err
	}
	if len(repos) == 0 {
		return nil
	}

	pattern := filter.Snapshot
	if pattern == "" {
		pattern = "*"
	}

	// Bound the parallelism to a small constant rather than scaling with
	// repo count: 4 concurrent S3 reads is plenty to mask latency, more
	// would just hammer the cluster's master node. Single repo skips the
	// worker pool entirely.
	const fanOutConcurrency = 4

	var (
		mu sync.Mutex // serialises yield and repoErr
	)

	tasks := make([]func(context.Context) error, 0, len(repos))
	for _, repo := range repos {
		repo := repo
		tasks = append(tasks, func(ctx context.Context) error {
			entries, err := c.snapshotsForRepo(ctx, repo, pattern, filter)
			if err != nil {
				if repoErr != nil {
					mu.Lock()
					repoErr(repo, err)
					mu.Unlock()
				}
				// Don't propagate — per-repo errors are reported, not
				// fatal, so the fleet view continues.
				return nil
			}
			for _, e := range entries {
				snap := snapshotwire.SnapshotFromWire(repo, e)
				mu.Lock()
				err := yield(snap)
				mu.Unlock()
				if err != nil {
					return err
				}
			}
			return nil
		})
	}
	return clientutil.RunBounded(ctx, fanOutConcurrency, tasks)
}

// resolveRepoNames returns the literal list of repositories to query.
// Empty input → list every registered repo. Wildcard ("*") behaves the
// same. A comma-separated literal list is forwarded as-is; we trust the
// cluster to 404 on a typo rather than pre-validating client-side and
// drifting from cluster behaviour.
func (c *Client) resolveRepoNames(ctx context.Context, repository string) ([]string, error) {
	if r := repository; r != "" && r != "*" {
		return esopsclient.SplitCSV(r), nil
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

// snapshotsMultiRepo issues a single GET against the comma-joined repo
// list and returns the per-repo split when the cluster (ES 8.1+) tags
// each entry with its repository. ok=false means the cluster responded
// with the older shape (no per-entry repo tag); the caller should fall
// back to per-repo path requests rather than guess.
func (c *Client) snapshotsMultiRepo(ctx context.Context, repos []string, pattern string, filter types.SnapshotFilter) (map[string][]snapshotwire.Entry, bool, error) {
	type taggedEntry struct {
		snapshotwire.Entry
		Repository string `json:"repository"`
	}
	type taggedResponse struct {
		Snapshots []taggedEntry `json:"snapshots"`
	}

	sg := c.api.Snapshot.Get
	opts := snapshotsGetOptions(sg, ctx, filter)
	resp, err := c.api.Snapshot.Get(joinNames(repos), esopsclient.SplitCSV(pattern), opts...)
	if err != nil {
		return nil, false, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("snapshot get repos="+joinNames(repos), resp.StatusCode, resp.Body); err != nil {
		return nil, false, err
	}

	var tagged taggedResponse
	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "snapshot get")
	if err := json.NewDecoder(body).Decode(&tagged); err != nil {
		return nil, false, errors.Join(errors.New("decoding snapshot get response"), err)
	}

	if len(tagged.Snapshots) == 0 {
		return map[string][]snapshotwire.Entry{}, true, nil
	}
	for _, t := range tagged.Snapshots {
		if t.Repository == "" {
			return nil, false, nil
		}
	}
	out := make(map[string][]snapshotwire.Entry, len(repos))
	for _, t := range tagged.Snapshots {
		out[t.Repository] = append(out[t.Repository], t.Entry)
	}
	return out, true, nil
}

func (c *Client) snapshotsForRepo(ctx context.Context, repo, pattern string, filter types.SnapshotFilter) ([]snapshotwire.Entry, error) {
	sg := c.api.Snapshot.Get
	opts := snapshotsGetOptions(sg, ctx, filter)
	resp, err := c.api.Snapshot.Get(repo, esopsclient.SplitCSV(pattern), opts...)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("snapshot get repo="+repo, resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var wire snapshotwire.EntriesResponse
	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "snapshot get")
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding snapshot get response"), err)
	}
	return wire.Snapshots, nil
}

// snapshotsGetOptions builds the upstream client's options slice from a
// SnapshotFilter. Pulled out so the multi-repo and per-repo code paths
// stay aligned on which knobs land where.
func snapshotsGetOptions(sg esapi.SnapshotGet, ctx context.Context, filter types.SnapshotFilter) []func(*esapi.SnapshotGetRequest) {
	opts := []func(*esapi.SnapshotGetRequest){sg.WithContext(ctx)}
	if filter.IgnoreUnavailable {
		opts = append(opts, sg.WithIgnoreUnavailable(true))
	}
	if filter.Slim {
		opts = append(opts, sg.WithVerbose(false))
	}
	return opts
}

// joinNames builds a comma-separated path segment without the
// `,`-from-strings.Join trip-up. ES (and OS) treat commas as the
// multi-name separator and reject `%2C`-encoded forms.
func joinNames(repos []string) string {
	if len(repos) == 0 {
		return ""
	}
	out := repos[0]
	for _, r := range repos[1:] {
		out += "," + r
	}
	return out
}

// Recovery implements client.RecoveryInspector via
// GET /<indices>/_recovery. Goes through Perform rather than the typed
// client because the upstream's typed Indices.Recovery decodes into a
// shape that doesn't carry the `source` block (snapshot repo/name/
// index) we need for cross-restore disambiguation.
func (c *Client) Recovery(ctx context.Context, req types.RecoveryRequest) (types.RecoveryReport, error) {
	q := url.Values{}
	if req.ActiveOnly {
		q.Set("active_only", "true")
	}
	indices := strings.TrimSpace(req.Indices)
	if indices == "" {
		indices = "_all"
	}
	path := "/" + url.PathEscape(indices) + "/_recovery"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return types.RecoveryReport{}, err
	}
	resp, err := c.api.Perform(httpReq)
	if err != nil {
		return types.RecoveryReport{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == 404 {
		// No matching index has any recovery state — for ?active_only
		// that means "everything is settled". Surface as an empty,
		// AllDone report so the polling caller stops immediately.
		return types.RecoveryReport{AllDone: true, Indices: []types.IndexRecovery{}}, nil
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(clientutil.NewBoundedReader(resp.Body, 64*1024, "recovery error"))
		return types.RecoveryReport{}, clientutil.ClassifyStatus("recovery indices="+indices, resp.StatusCode, body)
	}

	var wire snapshotwire.RecoveryEnvelope
	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "recovery")
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return types.RecoveryReport{}, errors.Join(errors.New("decoding recovery response"), err)
	}
	return snapshotwire.RecoveryReportFromWire(wire), nil
}

// SnapshotStatus implements client.SnapshotInspector via
// GET /_snapshot/<repo>/<snap>/_status. The cluster wraps the single
// snapshot in a `snapshots` array; we surface the first entry. A 404
// translates to MissingFromCluster=true so the wait/resume machinery
// can tell "still pending" apart from "the cluster forgot".
func (c *Client) SnapshotStatus(ctx context.Context, req types.SnapshotStatusRequest) (types.SnapshotStatus, error) {
	ss := c.api.Snapshot.Status
	opts := []func(*esapi.SnapshotStatusRequest){
		ss.WithContext(ctx),
		ss.WithRepository(req.Repository),
		ss.WithSnapshot(req.Snapshot),
	}

	resp, err := c.api.Snapshot.Status(opts...)
	if err != nil {
		return types.SnapshotStatus{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == 404 {
		return types.SnapshotStatus{
			Repository: req.Repository, Snapshot: req.Snapshot,
			MissingFromCluster: true,
		}, nil
	}
	if err := classifyResponse("snapshot status repo="+req.Repository+" snapshot="+req.Snapshot, resp.StatusCode, resp.Body); err != nil {
		return types.SnapshotStatus{}, err
	}

	var wire snapshotwire.StatusResponse
	bodyR := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "snapshot status")
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return types.SnapshotStatus{}, errors.Join(errors.New("decoding snapshot status response"), err)
	}
	if len(wire.Snapshots) == 0 {
		return types.SnapshotStatus{
			Repository: req.Repository, Snapshot: req.Snapshot,
			MissingFromCluster: true,
		}, nil
	}
	return snapshotwire.StatusFromWire(req.Repository, req.Snapshot, wire.Snapshots[0]), nil
}

// CreateSnapshot implements client.SnapshotCreator via
// PUT /_snapshot/<repo>/<snapshot>. The body shape is the same on both
// dialects, so the same wire helpers serve the OS adapter.
func (c *Client) CreateSnapshot(ctx context.Context, req types.SnapshotCreateRequest) (types.SnapshotCreateResult, error) {
	body, err := snapshotwire.BuildCreateBody(req)
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
	if req.MasterTimeout > 0 {
		opts = append(opts, cr.WithMasterTimeout(req.MasterTimeout))
	}

	resp, err := c.api.Snapshot.Create(req.Repository, req.Snapshot, opts...)
	if err != nil {
		return types.SnapshotCreateResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("snapshot create repo="+req.Repository+" snapshot="+req.Snapshot, resp.StatusCode, resp.Body); err != nil {
		return types.SnapshotCreateResult{}, err
	}

	var wire snapshotwire.CreateResponse
	bodyR := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "snapshot create")
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return types.SnapshotCreateResult{}, errors.Join(errors.New("decoding snapshot create response"), err)
	}
	return snapshotwire.CreateResultFromWire(req, wire), nil
}

// VerifyRepository implements client.SnapshotVerifier via
// POST /_snapshot/<repo>/_verify.
func (c *Client) VerifyRepository(ctx context.Context, req types.SnapshotVerifyRequest) (types.SnapshotVerifyResult, error) {
	vr := c.api.Snapshot.VerifyRepository
	opts := []func(*esapi.SnapshotVerifyRepositoryRequest){vr.WithContext(ctx)}

	resp, err := c.api.Snapshot.VerifyRepository(req.Repository, opts...)
	if err != nil {
		return types.SnapshotVerifyResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("snapshot verify repo="+req.Repository, resp.StatusCode, resp.Body); err != nil {
		return types.SnapshotVerifyResult{}, err
	}

	var wire snapshotwire.VerifyResponse
	bodyR := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "snapshot verify")
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return types.SnapshotVerifyResult{}, errors.Join(errors.New("decoding snapshot verify response"), err)
	}
	return snapshotwire.VerifyResultFromWire(req.Repository, wire), nil
}

// AnalyzeRepository implements client.SnapshotVerifier via
// POST /_snapshot/<repo>/_analyze. The upstream typed client doesn't
// expose this endpoint, so we go through the low-level Perform path —
// the request is small (no body, query params only) and the response
// is straightforward to decode via the shared wire shape.
func (c *Client) AnalyzeRepository(ctx context.Context, req types.SnapshotAnalyzeRequest) (types.SnapshotAnalyzeResult, error) {
	q := buildAnalyzeQuery(req)
	path := "/_snapshot/" + url.PathEscape(req.Repository) + "/_analyze"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, path, nil)
	if err != nil {
		return types.SnapshotAnalyzeResult{}, err
	}
	resp, err := c.api.Perform(httpReq)
	if err != nil {
		return types.SnapshotAnalyzeResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == 404 || resp.StatusCode == 405 {
		return types.SnapshotAnalyzeResult{}, errors.Join(esopsclient.ErrUnsupported, errors.New("repository _analyze is not available on this cluster (ES 7.4+ required)"))
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(clientutil.NewBoundedReader(resp.Body, 64*1024, "snapshot analyze error"))
		return types.SnapshotAnalyzeResult{}, clientutil.ClassifyStatus("snapshot analyze repo="+req.Repository, resp.StatusCode, body)
	}

	var wire snapshotwire.AnalyzeResponse
	bodyR := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "snapshot analyze")
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return types.SnapshotAnalyzeResult{}, errors.Join(errors.New("decoding snapshot analyze response"), err)
	}
	return types.SnapshotAnalyzeResult{
		Action:            "analyze",
		Repository:        req.Repository,
		CoordinatingNode:  wire.Coordinating.NodeName,
		BlobCount:         wire.BlobCount,
		ConcurrencyLevels: wire.ConcurrencyLevels,
		ReadNodeCount:     wire.ReadNodeCount,
		EarlyReadNodes:    wire.EarlyReadNodes,
		Status:            wire.Status,
	}, nil
}

// RestoreSnapshot implements client.SnapshotRestorer via
// POST /_snapshot/<repo>/<snap>/_restore.
func (c *Client) RestoreSnapshot(ctx context.Context, req types.SnapshotRestoreRequest) (types.SnapshotRestoreResult, error) {
	body, err := snapshotwire.BuildRestoreBody(req)
	if err != nil {
		return types.SnapshotRestoreResult{}, err
	}

	rs := c.api.Snapshot.Restore
	opts := []func(*esapi.SnapshotRestoreRequest){
		rs.WithContext(ctx),
		rs.WithBody(bytes.NewReader(body)),
	}
	if req.WaitForCompletion {
		opts = append(opts, rs.WithWaitForCompletion(true))
	}
	if req.MasterTimeout > 0 {
		opts = append(opts, rs.WithMasterTimeout(req.MasterTimeout))
	}

	resp, err := c.api.Snapshot.Restore(req.Repository, req.Snapshot, opts...)
	if err != nil {
		return types.SnapshotRestoreResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("snapshot restore repo="+req.Repository+" snapshot="+req.Snapshot, resp.StatusCode, resp.Body); err != nil {
		return types.SnapshotRestoreResult{}, err
	}

	var wire snapshotwire.RestoreResponse
	bodyR := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "snapshot restore")
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return types.SnapshotRestoreResult{}, errors.Join(errors.New("decoding snapshot restore response"), err)
	}
	return snapshotwire.RestoreResultFromWire(req, wire), nil
}

// DeleteSnapshots implements client.SnapshotPruner via
// DELETE /_snapshot/<repo>/<snap[,snap…]>. The cluster returns
// `{acknowledged:true}` on success; per-snapshot failures (missing names) are
// surfaced as a wrapped error from classifyResponse rather than silently
// dropped, so the prune business layer never reports "deleted" for something
// the cluster refused.
func (c *Client) DeleteSnapshots(ctx context.Context, req types.SnapshotDeleteRequest) (types.SnapshotDeleteResult, error) {
	if len(req.Snapshots) == 0 {
		return types.SnapshotDeleteResult{}, errors.New("snapshot delete: no snapshots specified")
	}

	sd := c.api.Snapshot.Delete
	opts := []func(*esapi.SnapshotDeleteRequest){sd.WithContext(ctx)}

	resp, err := c.api.Snapshot.Delete(req.Repository, req.Snapshots, opts...)
	if err != nil {
		return types.SnapshotDeleteResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("snapshot delete repo="+req.Repository, resp.StatusCode, resp.Body); err != nil {
		return types.SnapshotDeleteResult{}, err
	}

	var wire snapshotwire.DeleteResponse
	bodyR := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "snapshot delete")
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

// RegisterRepository implements client.SnapshotRepositoryManager via
// PUT /_snapshot/<repo>. Body shape is identical on both dialects so the
// adapter is a thin translation layer; settings are forwarded verbatim
// to keep each repository plugin's full surface available.
func (c *Client) RegisterRepository(ctx context.Context, req types.SnapshotRepositoryRegisterRequest) (types.SnapshotRepositoryRegisterResult, error) {
	if req.Type == "" {
		return types.SnapshotRepositoryRegisterResult{}, errors.New("snapshot register: --type is required")
	}
	body := map[string]any{"type": req.Type}
	if len(req.Settings) > 0 {
		body["settings"] = req.Settings
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return types.SnapshotRepositoryRegisterResult{}, err
	}

	cr := c.api.Snapshot.CreateRepository
	opts := []func(*esapi.SnapshotCreateRepositoryRequest){
		cr.WithContext(ctx),
	}
	if req.Verify != nil {
		opts = append(opts, cr.WithVerify(*req.Verify))
	}

	resp, err := c.api.Snapshot.CreateRepository(req.Repository, bytes.NewReader(raw), opts...)
	if err != nil {
		return types.SnapshotRepositoryRegisterResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("snapshot register repo="+req.Repository, resp.StatusCode, resp.Body); err != nil {
		return types.SnapshotRepositoryRegisterResult{}, err
	}

	var wire snapshotwire.DeleteResponse // {acknowledged:true} — same envelope
	bodyR := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "snapshot register")
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return types.SnapshotRepositoryRegisterResult{}, errors.Join(errors.New("decoding snapshot register response"), err)
	}
	return types.SnapshotRepositoryRegisterResult{
		Action:       "register",
		Repository:   req.Repository,
		Type:         req.Type,
		Acknowledged: wire.Acknowledged,
	}, nil
}

// DeregisterRepository implements client.SnapshotRepositoryManager via
// DELETE /_snapshot/<repo>. The cluster refuses to delete a repository
// that has in-progress snapshots — surfaced verbatim as a wrapped error
// rather than swallowed, since "couldn't deregister because something
// is still using it" is exactly the signal the operator needs.
func (c *Client) DeregisterRepository(ctx context.Context, req types.SnapshotRepositoryDeregisterRequest) (types.SnapshotRepositoryDeregisterResult, error) {
	dr := c.api.Snapshot.DeleteRepository
	opts := []func(*esapi.SnapshotDeleteRepositoryRequest){dr.WithContext(ctx)}

	resp, err := c.api.Snapshot.DeleteRepository([]string{req.Repository}, opts...)
	if err != nil {
		return types.SnapshotRepositoryDeregisterResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("snapshot deregister repo="+req.Repository, resp.StatusCode, resp.Body); err != nil {
		return types.SnapshotRepositoryDeregisterResult{}, err
	}

	var wire snapshotwire.DeleteResponse
	bodyR := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "snapshot deregister")
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return types.SnapshotRepositoryDeregisterResult{}, errors.Join(errors.New("decoding snapshot deregister response"), err)
	}
	return types.SnapshotRepositoryDeregisterResult{
		Action:       "deregister",
		Repository:   req.Repository,
		Acknowledged: wire.Acknowledged,
	}, nil
}

// buildAnalyzeQuery renders the cluster's _analyze knobs as query params.
// Empty/zero fields are omitted entirely so the cluster's defaults
// remain the source of truth — encoding `?blob_count=0` would
// over-specify the request.
func buildAnalyzeQuery(req types.SnapshotAnalyzeRequest) url.Values {
	q := url.Values{}
	if req.BlobCount > 0 {
		q.Set("blob_count", strconv.Itoa(req.BlobCount))
	}
	if req.Concurrency > 0 {
		q.Set("concurrency", strconv.Itoa(req.Concurrency))
	}
	if req.ReadNodeCount > 0 {
		q.Set("read_node_count", strconv.Itoa(req.ReadNodeCount))
	}
	if req.EarlyReadNode > 0 {
		q.Set("early_read_node_count", strconv.Itoa(req.EarlyReadNode))
	}
	if req.MaxBlobSize != "" {
		q.Set("max_blob_size", req.MaxBlobSize)
	}
	if req.MaxTotalSize != "" {
		q.Set("max_total_data_size", req.MaxTotalSize)
	}
	if req.Timeout > 0 {
		q.Set("timeout", req.Timeout.String())
	}
	return q
}
