package opensearch

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

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/client/snapshotwire"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// snapshotPath builds a /_snapshot URL safely. Each prefix segment is
// URL-escaped so a repo or snapshot name with regrettable characters
// (`/` and `%` show up in the wild on FS-backed repos when an operator
// pastes a path component into a name) doesn't break the request.
// extra is the trailing path segment (e.g. "_verify", "_restore",
// "_analyze") and is *not* escaped — it is always a literal cluster
// keyword. Query params are passed via url.Values rather than `path +=
// "?..."` concatenation so adding a second param doesn't introduce the
// classic `&` vs `?` foot-gun.
func snapshotPath(extra string, q url.Values, segments ...string) string {
	var b strings.Builder
	b.WriteString("/_snapshot")
	for _, s := range segments {
		if s == "" {
			continue
		}
		b.WriteByte('/')
		b.WriteString(url.PathEscape(s))
	}
	if extra != "" {
		b.WriteByte('/')
		b.WriteString(extra)
	}
	if encoded := q.Encode(); encoded != "" {
		b.WriteByte('?')
		b.WriteString(encoded)
	}
	return b.String()
}

// snapshotPathRaw mirrors snapshotPath but with a raw final path segment
// — used for the snapshot pattern, which can legitimately include `*`,
// `?`, and `,` and which the cluster expects to see verbatim. Comma-
// joined repo lists fall in the same bucket; both ES and OS treat
// commas in the path as the multi-name separator and would 400 on the
// %2C-encoded form.
func snapshotPathRaw(extra string, q url.Values, prefixSegments []string, raw string) string {
	var b strings.Builder
	b.WriteString("/_snapshot")
	for _, s := range prefixSegments {
		if s == "" {
			continue
		}
		b.WriteByte('/')
		b.WriteString(url.PathEscape(s))
	}
	if raw != "" {
		b.WriteByte('/')
		b.WriteString(raw)
	}
	if extra != "" {
		b.WriteByte('/')
		b.WriteString(extra)
	}
	if encoded := q.Encode(); encoded != "" {
		b.WriteByte('?')
		b.WriteString(encoded)
	}
	return b.String()
}

// Repositories implements client.SnapshotInspector via
// GET /_snapshot[/<repos>]. Empty Repository returns every registered
// repo. Uses raw HTTP rather than the typed SnapshotRepository.Get
// because the upstream's typed Settings is map[string]string, which
// silently drops nested plugin settings (e.g. s3 endpoint overrides).
func (c *Client) Repositories(ctx context.Context, filter types.SnapshotRepositoryFilter) ([]types.SnapshotRepository, error) {
	var path string
	if names := esopsclient.SplitCSV(filter.Repository); len(names) > 0 {
		path = snapshotPathRaw("", nil, nil, strings.Join(escapeNames(names), ","))
	} else {
		path = snapshotPath("", nil)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.Client.Perform(req)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(clientutil.NewBoundedReader(resp.Body, 64*1024, "snapshot get_repository error"))
		return nil, clientutil.ClassifyStatus("snapshot get_repository", resp.StatusCode, body)
	}

	var wire map[string]snapshotwire.RepoEntry
	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "snapshot get_repository")
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding snapshot get_repository response"), err)
	}
	return snapshotwire.RepositoriesFromWire(wire), nil
}

// Snapshots implements client.SnapshotInspector. OpenSearch's
// /_snapshot/<repo>/<pattern> endpoint requires a single literal repo
// name in the path — comma-separated repo names are not honoured (unlike
// ES 8+) — so we always fan out client-side. An empty/wildcard
// Repository resolves to "every registered repo" first, then GET per
// repo.
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

// SnapshotsStream implements client.SnapshotInspector. Same shape as the
// ES adapter: bounded fan-out across repos, serialised yield, per-repo
// errors reported and skipped rather than fatal.
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

	const fanOutConcurrency = 4

	var mu sync.Mutex

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

func (c *Client) snapshotsForRepo(ctx context.Context, repo, pattern string, filter types.SnapshotFilter) ([]snapshotwire.Entry, error) {
	q := url.Values{}
	if filter.IgnoreUnavailable {
		q.Set("ignore_unavailable", "true")
	}
	if filter.Slim {
		q.Set("verbose", "false")
	}
	path := snapshotPathRaw("", q, []string{repo}, pattern)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.Client.Perform(req)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(clientutil.NewBoundedReader(resp.Body, 64*1024, "snapshot get error"))
		return nil, clientutil.ClassifyStatus("snapshot get repo="+repo, resp.StatusCode, body)
	}

	var wire snapshotwire.EntriesResponse
	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "snapshot get")
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding snapshot get response"), err)
	}
	return wire.Snapshots, nil
}

// Recovery implements client.RecoveryInspector via
// GET /<indices>/_recovery. Same shape as Elasticsearch; the OS typed
// client doesn't expose the endpoint cleanly so we go through Perform.
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
	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.RecoveryReport{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == 404 {
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
// GET /_snapshot/<repo>/<snap>/_status. A 404 translates to
// MissingFromCluster=true so callers can distinguish "still pending"
// from "the cluster forgot".
func (c *Client) SnapshotStatus(ctx context.Context, req types.SnapshotStatusRequest) (types.SnapshotStatus, error) {
	path := snapshotPath("_status", nil, req.Repository, req.Snapshot)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return types.SnapshotStatus{}, err
	}

	resp, err := c.api.Client.Perform(httpReq)
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
	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(clientutil.NewBoundedReader(resp.Body, 64*1024, "snapshot status error"))
		return types.SnapshotStatus{}, clientutil.ClassifyStatus("snapshot status repo="+req.Repository+" snapshot="+req.Snapshot, resp.StatusCode, errBody)
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
// PUT /_snapshot/<repo>/<snapshot>. Goes through raw HTTP (rather than
// the typed osapi.SnapshotCreate) because the typed client's request
// shape forces the body through a struct that doesn't carry the
// `metadata` block — round-tripping a free-form metadata map is
// straightforward over HTTP.
func (c *Client) CreateSnapshot(ctx context.Context, req types.SnapshotCreateRequest) (types.SnapshotCreateResult, error) {
	body, err := snapshotwire.BuildCreateBody(req)
	if err != nil {
		return types.SnapshotCreateResult{}, err
	}

	q := url.Values{}
	if req.WaitForCompletion {
		q.Set("wait_for_completion", "true")
	}
	if req.MasterTimeout > 0 {
		q.Set("master_timeout", req.MasterTimeout.String())
	}
	path := snapshotPath("", q, req.Repository, req.Snapshot)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, path, bytes.NewReader(body))
	if err != nil {
		return types.SnapshotCreateResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.SnapshotCreateResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(clientutil.NewBoundedReader(resp.Body, 64*1024, "snapshot create error"))
		return types.SnapshotCreateResult{}, clientutil.ClassifyStatus("snapshot create repo="+req.Repository+" snapshot="+req.Snapshot, resp.StatusCode, errBody)
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
	path := snapshotPath("_verify", nil, req.Repository)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, path, nil)
	if err != nil {
		return types.SnapshotVerifyResult{}, err
	}

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.SnapshotVerifyResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(clientutil.NewBoundedReader(resp.Body, 64*1024, "snapshot verify error"))
		return types.SnapshotVerifyResult{}, clientutil.ClassifyStatus("snapshot verify repo="+req.Repository, resp.StatusCode, errBody)
	}

	var wire snapshotwire.VerifyResponse
	bodyR := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "snapshot verify")
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return types.SnapshotVerifyResult{}, errors.Join(errors.New("decoding snapshot verify response"), err)
	}
	return snapshotwire.VerifyResultFromWire(req.Repository, wire), nil
}

// AnalyzeRepository implements client.SnapshotVerifier via
// POST /_snapshot/<repo>/_analyze. Same shape on OS 2.x as on ES 7.4+.
func (c *Client) AnalyzeRepository(ctx context.Context, req types.SnapshotAnalyzeRequest) (types.SnapshotAnalyzeResult, error) {
	q := buildAnalyzeQuery(req)
	path := snapshotPath("_analyze", q, req.Repository)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, path, nil)
	if err != nil {
		return types.SnapshotAnalyzeResult{}, err
	}

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.SnapshotAnalyzeResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == 404 || resp.StatusCode == 405 {
		return types.SnapshotAnalyzeResult{}, errors.Join(esopsclient.ErrUnsupported, errors.New("repository _analyze is not available on this cluster (OS 2.x required)"))
	}
	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(clientutil.NewBoundedReader(resp.Body, 64*1024, "snapshot analyze error"))
		return types.SnapshotAnalyzeResult{}, clientutil.ClassifyStatus("snapshot analyze repo="+req.Repository, resp.StatusCode, errBody)
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
// POST /_snapshot/<repo>/<snap>/_restore. Goes through raw HTTP for the same
// reason CreateSnapshot does — the typed osapi shape doesn't carry the full
// rename/index_settings block round-trip cleanly.
func (c *Client) RestoreSnapshot(ctx context.Context, req types.SnapshotRestoreRequest) (types.SnapshotRestoreResult, error) {
	body, err := snapshotwire.BuildRestoreBody(req)
	if err != nil {
		return types.SnapshotRestoreResult{}, err
	}

	q := url.Values{}
	if req.WaitForCompletion {
		q.Set("wait_for_completion", "true")
	}
	if req.MasterTimeout > 0 {
		q.Set("master_timeout", req.MasterTimeout.String())
	}
	path := snapshotPath("_restore", q, req.Repository, req.Snapshot)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return types.SnapshotRestoreResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.SnapshotRestoreResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(clientutil.NewBoundedReader(resp.Body, 64*1024, "snapshot restore error"))
		return types.SnapshotRestoreResult{}, clientutil.ClassifyStatus("snapshot restore repo="+req.Repository+" snapshot="+req.Snapshot, resp.StatusCode, errBody)
	}

	var wire snapshotwire.RestoreResponse
	bodyR := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "snapshot restore")
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return types.SnapshotRestoreResult{}, errors.Join(errors.New("decoding snapshot restore response"), err)
	}
	return snapshotwire.RestoreResultFromWire(req, wire), nil
}

// DeleteSnapshots implements client.SnapshotPruner via
// DELETE /_snapshot/<repo>/<snap[,snap…]>. OpenSearch accepts the same
// comma-separated path segment Elasticsearch does, so one bulk request per
// repository covers any prune plan.
func (c *Client) DeleteSnapshots(ctx context.Context, req types.SnapshotDeleteRequest) (types.SnapshotDeleteResult, error) {
	if len(req.Snapshots) == 0 {
		return types.SnapshotDeleteResult{}, errors.New("snapshot delete: no snapshots specified")
	}

	path := snapshotPathRaw("", nil, []string{req.Repository}, strings.Join(escapeNames(req.Snapshots), ","))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return types.SnapshotDeleteResult{}, err
	}

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.SnapshotDeleteResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(clientutil.NewBoundedReader(resp.Body, 64*1024, "snapshot delete error"))
		return types.SnapshotDeleteResult{}, clientutil.ClassifyStatus("snapshot delete repo="+req.Repository, resp.StatusCode, errBody)
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
// PUT /_snapshot/<repo>. Body shape is identical on both dialects.
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

	q := url.Values{}
	if req.Verify != nil {
		q.Set("verify", strconv.FormatBool(*req.Verify))
	}
	path := snapshotPath("", q, req.Repository)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, path, bytes.NewReader(raw))
	if err != nil {
		return types.SnapshotRepositoryRegisterResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.SnapshotRepositoryRegisterResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(clientutil.NewBoundedReader(resp.Body, 64*1024, "snapshot register error"))
		return types.SnapshotRepositoryRegisterResult{}, clientutil.ClassifyStatus("snapshot register repo="+req.Repository, resp.StatusCode, errBody)
	}

	var wire snapshotwire.DeleteResponse // {acknowledged:true}
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
// DELETE /_snapshot/<repo>.
func (c *Client) DeregisterRepository(ctx context.Context, req types.SnapshotRepositoryDeregisterRequest) (types.SnapshotRepositoryDeregisterResult, error) {
	path := snapshotPath("", nil, req.Repository)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return types.SnapshotRepositoryDeregisterResult{}, err
	}

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.SnapshotRepositoryDeregisterResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(clientutil.NewBoundedReader(resp.Body, 64*1024, "snapshot deregister error"))
		return types.SnapshotRepositoryDeregisterResult{}, clientutil.ClassifyStatus("snapshot deregister repo="+req.Repository, resp.StatusCode, errBody)
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

// escapeNames URL-escapes each name in a comma-joined list. Commas
// themselves are reserved for the cluster's multi-name separator;
// %-escaping a comma would be parsed as one literal name.
func escapeNames(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = url.PathEscape(n)
	}
	return out
}

// buildAnalyzeQuery renders the cluster's _analyze knobs as query params.
// Empty/zero fields are omitted entirely so the cluster's defaults
// remain the source of truth.
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
