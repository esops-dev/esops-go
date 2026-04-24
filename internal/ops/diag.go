package ops

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
	"github.com/dj-wasabi/esops-go/internal/version"
)

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
	artifacts := make([]types.DiagArtifact, 0, len(plan))
	for _, col := range plan {
		art := runCollector(ctx, c.Diag, col)
		if errors.Is(art.rawErr, client.ErrAuth) || errors.Is(art.rawErr, client.ErrForbidden) {
			return types.DiagManifest{}, art.rawErr
		}
		artifacts = append(artifacts, art.DiagArtifact)
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
		Artifacts: artifacts,
	}

	if err := writeBundle(out, manifest); err != nil {
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

// runArtifact carries the business-layer error alongside the wire-level
// DiagArtifact so Diag can decide whether to abort (ErrAuth/ErrForbidden)
// without string-matching on manifest.Error.
type runArtifact struct {
	types.DiagArtifact
	rawErr error
}

func runCollector(ctx context.Context, d client.ClusterDiagnoser, col diagCollector) runArtifact {
	body, status, err := d.DoGET(ctx, col.Path)
	art := types.DiagArtifact{
		Name:   col.Name,
		Path:   col.Path,
		File:   artifactFileName(col),
		Status: status,
		Body:   body,
		Bytes:  len(body),
	}
	if err != nil {
		art.Error = err.Error()
	}
	return runArtifact{DiagArtifact: art, rawErr: err}
}

func artifactFileName(col diagCollector) string {
	ext := col.Ext
	if ext == "" {
		ext = ".json"
	}
	return col.Name + ext
}

// writeBundle assembles the gzipped tarball: manifest.json first (so a
// reader can `tar -xOf bundle manifest.json | jq` without unpacking),
// then one file per collected artifact. We skip artifacts whose body is
// empty AND whose status indicates no response (0) — there's nothing to
// write; the manifest already records the attempt with its error.
func writeBundle(out io.Writer, manifest types.DiagManifest) error {
	gz := gzip.NewWriter(out)
	defer func() { _ = gz.Close() }()
	tw := tar.NewWriter(gz)
	defer func() { _ = tw.Close() }()

	mBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding manifest: %w", err)
	}
	if err := writeTarEntry(tw, "manifest.json", mBytes); err != nil {
		return err
	}

	for _, art := range manifest.Artifacts {
		if len(art.Body) == 0 && art.Status == 0 {
			continue
		}
		if err := writeTarEntry(tw, art.File, art.Body); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
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
