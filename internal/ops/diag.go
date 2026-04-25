package ops

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
	"github.com/dj-wasabi/esops-go/internal/version"
)

// defaultDiagConcurrency caps the number of in-flight collectors when
// req.Concurrency is unset. 4 is empirically a good default: enough
// parallelism to overlap network round-trips, low enough to not look
// like a synthetic load on a struggling cluster.
const defaultDiagConcurrency = 4

// diagCollector describes one endpoint the diag bundle captures. Dialect
// is set when the endpoint is product-specific (ILM on ES, ISM on OS);
// empty means "try on both". Ext defaults to .json — the one text-only
// endpoint (/_nodes/hot_threads) overrides it.
type diagCollector struct {
	Name    string
	Path    string
	Dialect types.Dialect
	Ext     string
}

// baseCollectors is the default bundle content — what an on-call will
// share with support for any cluster triage. Ordered roughly "cluster →
// nodes → shards → features"; the tarball entry order matches this, so
// a `tar -tf` listing reads from broad to narrow.
var baseCollectors = []diagCollector{
	{Name: "cluster_info", Path: "/"},
	{Name: "cluster_health", Path: "/_cluster/health?level=indices"},
	{Name: "cluster_state", Path: "/_cluster/state/_all"},
	{Name: "cluster_stats", Path: "/_cluster/stats"},
	{Name: "cluster_settings", Path: "/_cluster/settings?include_defaults=true&flat_settings=true"},
	{Name: "cluster_pending_tasks", Path: "/_cluster/pending_tasks"},
	// /_cluster/allocation/explain returns 400 when nothing is unassigned.
	// That is expected and worth capturing: "healthy, nothing to explain"
	// is the signal. The collector records the body either way.
	{Name: "cluster_allocation_explain", Path: "/_cluster/allocation/explain"},
	{Name: "nodes_info", Path: "/_nodes"},
	{Name: "nodes_stats", Path: "/_nodes/stats"},
	{Name: "nodes_hot_threads", Path: "/_nodes/hot_threads", Ext: ".txt"},
	{Name: "tasks", Path: "/_tasks?detailed=true"},
	{Name: "cat_allocation", Path: "/_cat/allocation?format=json&bytes=b"},
	{Name: "cat_shards", Path: "/_cat/shards?format=json&bytes=b"},
	{Name: "cat_indices", Path: "/_cat/indices?format=json&bytes=b&expand_wildcards=open,closed,hidden"},
	{Name: "cat_recovery", Path: "/_cat/recovery?format=json&bytes=b&active_only=true"},
	{Name: "snapshot_repositories", Path: "/_snapshot"},
	// Templates: explaining "why does this index have this mapping?"
	// almost always lands on a template. Composable templates (8.x+)
	// reference component templates; capturing both keeps the chain
	// reconstructible from the bundle alone. The legacy /_template
	// endpoint covers 7.x clusters that haven't migrated.
	{Name: "index_templates", Path: "/_index_template"},
	{Name: "component_templates", Path: "/_component_template"},
	{Name: "legacy_templates", Path: "/_template"},
	// Cross-cluster setups: when the symptom is "remote search is
	// slow" or "reindex from remote stalled", the first thing support
	// asks for is what /_remote/info reports. 404s on standalone
	// clusters with no remotes configured — recorded as such.
	{Name: "remote_info", Path: "/_remote/info"},
	// Dialect-specific lifecycle endpoints. The "wrong" dialect 404s
	// (ILM on OS, ISM on ES); we filter before calling so the manifest
	// only lists entries the collector actually attempted.
	{Name: "ilm_policies", Path: "/_ilm/policy", Dialect: types.DialectElasticsearch},
	{Name: "ilm_status", Path: "/_ilm/status", Dialect: types.DialectElasticsearch},
	{Name: "ism_policies", Path: "/_plugins/_ism/policies", Dialect: types.DialectOpenSearch},
}

// optionalCollectors gate the expensive or policy-sensitive captures.
// Slow-log *settings* (not the file itself — that's not exposed via the
// HTTP API) are small but document what queries the cluster would log;
// full mappings can be hundreds of MB on large clusters; the idle-thread
// capture produces noise by default (the cluster hides them for a
// reason) but is the right next step when busy-only didn't explain the
// symptom — parked threads in a contended lock look "idle" to the
// default filter.
var optionalCollectors = map[string]diagCollector{
	"slowlog":       {Name: "index_slowlog_settings", Path: "/_all/_settings?filter_path=**.settings.index.search.slowlog.**,**.settings.index.indexing.slowlog.**,**.defaults.index.search.slowlog.**,**.defaults.index.indexing.slowlog.**&include_defaults=true&flat_settings=true"},
	"mappings":      {Name: "index_mappings", Path: "/_all/_mapping"},
	"idle_threads":  {Name: "nodes_hot_threads_with_idle", Path: "/_nodes/hot_threads?ignore_idle_threads=false", Ext: ".txt"},
}

// invalidFilenameChars strips anything that would make a tarball member
// awkward on a foreign filesystem. `?` and `&` from our query strings
// are the common offenders; `:` breaks Windows. We keep the result
// readable rather than hashing the path — support tickets inspect these
// filenames by eye.
var invalidFilenameChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// Diag assembles a diagnostic bundle for the active cluster and writes
// it as a gzipped tarball to out. Returns the manifest it produced so
// callers can log artifact counts without re-reading the file.
//
// Each collector's response body is streamed directly into the tarball
// as the collector finishes, then released — only artifact metadata
// (name/path/status/bytes) accumulates across the run. Without this,
// `--include-mappings` against a 10k-index cluster would hold hundreds
// of MB of mapping JSON in RAM alongside every other artifact's body
// before the tarball was assembled.
//
// manifest.json is written as the final entry rather than the first.
// Tar is sequential anyway — `tar -xOf bundle manifest.json` finds the
// member regardless of position — and writing it last lets the manifest
// reflect every artifact that actually streamed without holding bodies
// in memory to compute lengths up-front.
//
// The collector makes best-effort progress: a transport error on one
// endpoint records an Error on that artifact and keeps going. Auth
// errors (ErrAuth/ErrForbidden) abort the bundle because re-running with
// the right credentials is cheaper than producing a half-empty one and
// leaving the operator to notice.
func Diag(ctx context.Context, c *client.Client, req types.DiagRequest, out io.Writer) (types.DiagManifest, error) {
	if c == nil || c.Diag == nil {
		return types.DiagManifest{}, errors.New("diag: cluster client is not configured for raw GETs")
	}

	plan := planCollectors(c.Info.Dialect, req)
	concurrency := req.Concurrency
	if concurrency <= 0 {
		concurrency = defaultDiagConcurrency
	}
	if concurrency > len(plan) {
		concurrency = len(plan)
	}

	gz := gzip.NewWriter(out)
	defer func() { _ = gz.Close() }()
	tw := tar.NewWriter(gz)
	defer func() { _ = tw.Close() }()

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Workers fetch in parallel; a single goroutine (the loop below)
	// drains and writes to the tar so we don't have to make tar.Writer
	// concurrency-safe. Manifest order stays canonical (plan order)
	// regardless of completion order; tarball entry order is by
	// completion, which the manifest documents.
	type collected struct {
		idx    int
		art    types.DiagArtifact
		body   []byte
	}
	results := make(chan collected, concurrency)

	var (
		fatalOnce sync.Once
		fatalErr  error
		sem       = make(chan struct{}, concurrency)
		wg        sync.WaitGroup
	)
	wg.Add(len(plan))
	for i, col := range plan {
		go func(i int, col diagCollector) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-cctx.Done():
				return
			}
			defer func() { <-sem }()

			body, status, err := c.Diag.DoGET(cctx, col.Path)
			if errors.Is(err, client.ErrAuth) || errors.Is(err, client.ErrForbidden) {
				fatalOnce.Do(func() {
					fatalErr = err
					cancel()
				})
				return
			}
			art := types.DiagArtifact{
				Name:   col.Name,
				Path:   col.Path,
				File:   artifactFileName(col),
				Status: status,
				Bytes:  len(body),
			}
			// Hash whatever body we captured — including non-2xx
			// responses (a 404's "{"error":"not_found"}" body still
			// lands in the tarball; recording its hash lets a verifier
			// confirm the recorded status came from this exact body).
			// Skipped only when there's no body at all, which happens
			// on transport-level failures.
			if len(body) > 0 {
				sum := sha256.Sum256(body)
				art.SHA256 = hex.EncodeToString(sum[:])
			}
			if err != nil {
				art.Error = err.Error()
			}
			select {
			case results <- collected{idx: i, art: art, body: body}:
			case <-cctx.Done():
			}
		}(i, col)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	artifacts := make([]types.DiagArtifact, len(plan))
	seen := make([]bool, len(plan))
	for r := range results {
		artifacts[r.idx] = r.art
		seen[r.idx] = true
		// Skip the tar entry only when there's literally nothing to
		// record — no body AND no HTTP response (transport-level
		// failure). The manifest still names the attempt with its error.
		if len(r.body) > 0 || r.art.Status != 0 {
			if werr := writeTarEntry(tw, r.art.File, r.body); werr != nil {
				cancel()
				return types.DiagManifest{}, werr
			}
		}
		// r.body is no longer referenced — eligible for GC before
		// the next result arrives.
	}

	if fatalErr != nil {
		return types.DiagManifest{}, fatalErr
	}

	// Compact: cancelled-before-fired tasks left zero-valued slots
	// (they shouldn't happen on the success path — `seen` will be all
	// true — but be explicit so any future cancellation source can't
	// leak empty rows into the manifest).
	finalArtifacts := make([]types.DiagArtifact, 0, len(plan))
	for i, a := range artifacts {
		if seen[i] {
			finalArtifacts = append(finalArtifacts, a)
		}
	}

	manifest := types.DiagManifest{
		Tool:      "esops",
		Version:   version.Version,
		Commit:    version.Commit,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Cluster: types.DiagClusterSummary{
			Name:    c.Info.ClusterName,
			Dialect: string(c.Info.Dialect),
			Version: c.Info.Version,
		},
		Artifacts: finalArtifacts,
	}

	mBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return manifest, fmt.Errorf("encoding manifest: %w", err)
	}
	// Hash the rendered bytes — what the tarball will hold and what a
	// verifier will re-hash. Computed AFTER marshaling because the
	// manifest's content (each artifact's SHA256, Bytes, etc.) is what
	// the digest must commit to. Surfaced via manifest.SHA256 (json:"-")
	// so the CLI can print it on stderr; never embedded inside the
	// manifest itself.
	manifestSum := sha256.Sum256(mBytes)
	manifest.SHA256 = hex.EncodeToString(manifestSum[:])
	if err := writeTarEntry(tw, "manifest.json", mBytes); err != nil {
		return manifest, err
	}

	if err := tw.Close(); err != nil {
		return manifest, err
	}
	if err := gz.Close(); err != nil {
		return manifest, err
	}
	return manifest, nil
}

// BundleFilename is the suggested on-disk name for a diag bundle.
// Scrubbed cluster name + UTC timestamp; safe on macOS/Linux/Windows.
// Callers open the file themselves — the business layer shouldn't
// reach for os.Create — but the name convention lives here so docs and
// logs stay consistent.
func BundleFilename(clusterName string, now time.Time) string {
	name := strings.TrimSpace(clusterName)
	if name == "" {
		name = "cluster"
	}
	name = invalidFilenameChars.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-_.")
	if name == "" {
		name = "cluster"
	}
	return fmt.Sprintf("esops-diag-%s-%s.tar.gz", name, now.UTC().Format("20060102T150405Z"))
}

// planCollectors filters the catalog down to what this cluster supports
// and the opt-ins the user requested. Dialect-specific endpoints on the
// wrong product are dropped here (not at the HTTP layer) so the manifest
// stays honest about what was attempted.
func planCollectors(dialect types.Dialect, req types.DiagRequest) []diagCollector {
	out := make([]diagCollector, 0, len(baseCollectors)+2)
	for _, col := range baseCollectors {
		if col.Dialect != "" && col.Dialect != dialect {
			continue
		}
		out = append(out, col)
	}
	if req.IncludeSlowlog {
		out = append(out, optionalCollectors["slowlog"])
	}
	if req.IncludeMappings {
		out = append(out, optionalCollectors["mappings"])
	}
	if req.IncludeIdleThreads {
		out = append(out, optionalCollectors["idle_threads"])
	}
	return out
}

func artifactFileName(col diagCollector) string {
	ext := col.Ext
	if ext == "" {
		ext = ".json"
	}
	return col.Name + ext
}

func writeTarEntry(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    int64(len(data)),
		ModTime: time.Now().UTC(),
		Format:  tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("writing tar header for %q: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("writing tar body for %q: %w", name, err)
	}
	return nil
}
