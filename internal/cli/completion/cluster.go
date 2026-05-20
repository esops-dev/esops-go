// Cluster-backed Suggesters. Each one connects to the configured
// context, asks the cluster for a list, and returns the names —
// honouring the global --complete-timeout deadline and the
// --complete-cache TTL.
//
// Failure mode: any error (config missing, cluster unreachable, auth
// denied, deadline exceeded, JSON decode failure) collapses to zero
// suggestions. Completion must never write diagnostics into the
// operator's shell.
package completion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/cluster"
	"github.com/esops-dev/esops-go/pkg/config"
	"github.com/esops-dev/esops-go/pkg/types"
)

// Defaults for the completion-only global flags. Documented in the
// roadmap entry and surfaced on the flag definitions in
// internal/cli/root.go.
const (
	DefaultCompleteTimeout = 500 * time.Millisecond
	DefaultCompleteCacheTTL = 30 * time.Second
)

// SuggestContexts emits every context name from the loaded config.
// Pure local lookup — never reaches a cluster, so it bypasses the
// timeout/cache machinery.
func SuggestContexts(_ context.Context, cmd *cli.Command) []string {
	cfg, _, err := config.LoadDefault(cmd.String("config"))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(cfg.Contexts))
	for name := range cfg.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SuggestIndices emits index names from the configured cluster.
// Hidden / system / data-stream-backing indices (`.ds-*`, dot-
// prefixed) are filtered out — completing a tab into `.ds-logs-…`
// is rarely what the operator wanted.
func SuggestIndices(ctx context.Context, cmd *cli.Command) []string {
	return clusterSuggest(ctx, cmd, "indices", func(ctx context.Context, c *client.Client) ([]string, error) {
		rows, err := c.Indices.IndicesNamesStatus(ctx, types.IndexFilter{})
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(rows))
		for _, r := range rows {
			if strings.HasPrefix(r.Name, ".") {
				continue
			}
			names = append(names, r.Name)
		}
		return names, nil
	})
}

// SuggestRepositories emits registered snapshot repository names.
func SuggestRepositories(ctx context.Context, cmd *cli.Command) []string {
	return clusterSuggest(ctx, cmd, "repositories", func(ctx context.Context, c *client.Client) ([]string, error) {
		repos, err := c.Snapshot.Repositories(ctx, types.SnapshotRepositoryFilter{})
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(repos))
		for _, r := range repos {
			names = append(names, r.Name)
		}
		return names, nil
	})
}

// SuggestSnapshots emits snapshot names from the repository the
// operator has already named on the command line via --repository.
// Without a repository the cluster's snapshot listing fans out across
// every registered repo, which can be slow and noisy for completion;
// we return no suggestions instead.
func SuggestSnapshots(ctx context.Context, cmd *cli.Command) []string {
	repo := cmd.String("repository")
	if repo == "" {
		return nil
	}
	return clusterSuggest(ctx, cmd, "snapshots:"+repo, func(ctx context.Context, c *client.Client) ([]string, error) {
		snaps, err := c.Snapshot.Snapshots(ctx, types.SnapshotFilter{
			Repository: repo,
			Slim:       true, // bandwidth-cheap envelope is plenty for names.
		})
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(snaps))
		for _, s := range snaps {
			names = append(names, s.Name)
		}
		return names, nil
	})
}

// SuggestSnapshotSpecs emits fully-qualified `repo/snap` candidates
// for `snapshot describe`, which takes one combined positional. The
// underlying capability fans out across every registered repository
// in one call (`SnapshotFilter{}` with no Repository set); the bash
// completion templates then filter the returned list against the
// operator's partial word via `compgen -W`, so typing `myrepo/<TAB>`
// narrows to the snapshots in `myrepo` for free.
func SuggestSnapshotSpecs(ctx context.Context, cmd *cli.Command) []string {
	return clusterSuggest(ctx, cmd, "snapshot-specs", func(ctx context.Context, c *client.Client) ([]string, error) {
		snaps, err := c.Snapshot.Snapshots(ctx, types.SnapshotFilter{Slim: true})
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(snaps))
		for _, s := range snaps {
			out = append(out, s.Repository+"/"+s.Name)
		}
		return out, nil
	})
}

// SuggestPipelines emits configured ingest pipeline IDs.
func SuggestPipelines(ctx context.Context, cmd *cli.Command) []string {
	return clusterSuggest(ctx, cmd, "pipelines", func(ctx context.Context, c *client.Client) ([]string, error) {
		pipes, err := c.IngestPipeline.ListPipelines(ctx, nil)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(pipes))
		for _, p := range pipes {
			names = append(names, p.Name)
		}
		return names, nil
	})
}

// SuggestDataStreams emits configured data-stream names.
func SuggestDataStreams(ctx context.Context, cmd *cli.Command) []string {
	return clusterSuggest(ctx, cmd, "datastreams", func(ctx context.Context, c *client.Client) ([]string, error) {
		streams, err := c.DataStream.DataStreams(ctx, types.DataStreamFilter{})
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(streams))
		for _, s := range streams {
			names = append(names, s.Name)
		}
		return names, nil
	})
}

// SuggestTransforms emits configured transform ids.
func SuggestTransforms(ctx context.Context, cmd *cli.Command) []string {
	return clusterSuggest(ctx, cmd, "transforms", func(ctx context.Context, c *client.Client) ([]string, error) {
		rows, err := c.Transform.ListTransforms(ctx, nil)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(rows))
		for _, r := range rows {
			names = append(names, r.Name)
		}
		return names, nil
	})
}

// SuggestILMPolicies emits Elasticsearch ILM policy names. On an
// OpenSearch cluster the underlying capability returns ErrUnsupported
// and we collapse to zero suggestions.
func SuggestILMPolicies(ctx context.Context, cmd *cli.Command) []string {
	return clusterSuggest(ctx, cmd, "ilm-policies", func(ctx context.Context, c *client.Client) ([]string, error) {
		policies, err := c.ILM.ListPolicies(ctx, nil)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(policies))
		for _, p := range policies {
			names = append(names, p.Name)
		}
		return names, nil
	})
}

// SuggestISMPolicies emits OpenSearch ISM policy IDs. ES contexts
// short-circuit via ErrUnsupported and produce zero suggestions.
func SuggestISMPolicies(ctx context.Context, cmd *cli.Command) []string {
	return clusterSuggest(ctx, cmd, "ism-policies", func(ctx context.Context, c *client.Client) ([]string, error) {
		policies, err := c.ISM.ListPolicies(ctx, nil)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(policies))
		for _, p := range policies {
			names = append(names, p.ID)
		}
		return names, nil
	})
}

// SuggestSnapshotPolicies emits SLM (ES) / SM (OS) policy names.
func SuggestSnapshotPolicies(ctx context.Context, cmd *cli.Command) []string {
	return clusterSuggest(ctx, cmd, "snapshot-policies", func(ctx context.Context, c *client.Client) ([]string, error) {
		policies, err := c.SnapshotPolicyRead.ListSnapshotPolicies(ctx, nil)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(policies))
		for _, p := range policies {
			names = append(names, p.Name)
		}
		return names, nil
	})
}

// SuggestAliases emits alias names configured on the cluster.
func SuggestAliases(ctx context.Context, cmd *cli.Command) []string {
	return clusterSuggest(ctx, cmd, "aliases", func(ctx context.Context, c *client.Client) ([]string, error) {
		aliases, err := c.AliasInspect.Aliases(ctx, types.AliasFilter{})
		if err != nil {
			return nil, err
		}
		seen := make(map[string]struct{}, len(aliases))
		for _, a := range aliases {
			seen[a.Alias] = struct{}{}
		}
		names := make([]string, 0, len(seen))
		for n := range seen {
			names = append(names, n)
		}
		sort.Strings(names)
		return names, nil
	})
}

// SuggestNodes emits node names from /_cat/nodes. Used by `ops drain
// --node`, `ops shards --node`, `ops hot-threads --node`, and the
// node-filter flags on `ops tasks` / `ops nodes`.
func SuggestNodes(ctx context.Context, cmd *cli.Command) []string {
	return clusterSuggest(ctx, cmd, "nodes", func(ctx context.Context, c *client.Client) ([]string, error) {
		nodes, err := c.Nodes.Nodes(ctx)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(nodes))
		for _, n := range nodes {
			names = append(names, n.Name)
		}
		return names, nil
	})
}

// SuggestIndexTemplates emits configured index/component template names.
func SuggestIndexTemplates(ctx context.Context, cmd *cli.Command) []string {
	return clusterSuggest(ctx, cmd, "index-templates", func(ctx context.Context, c *client.Client) ([]string, error) {
		tmpls, err := c.IndexTemplateGet.GetTemplates(ctx, nil)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(tmpls))
		for _, t := range tmpls {
			names = append(names, t.Name)
		}
		return names, nil
	})
}

// clusterSuggest is the common path for cluster-backed Suggesters:
// honour the completion deadline, consult the cache, otherwise build a
// connected client (bypassing the destructive-op confirmation in
// cmdsetup.Prepare since completion is read-only), run the per-shape
// fetch, and cache the result. Any error collapses to nil suggestions.
//
// kind is the cache-key suffix and disambiguates payloads for the same
// context (e.g. "indices" vs "repositories"). It must not vary on
// per-invocation transient state — only the cluster shape it
// represents — or cached entries will drift.
func clusterSuggest(ctx context.Context, cmd *cli.Command, kind string, fetch func(context.Context, *client.Client) ([]string, error)) []string {
	contextName, cc, ok := resolveContextForCompletion(cmd)
	if !ok {
		return nil
	}

	cacheTTL := cacheTTLFor(cmd)
	if cached, ok := readCache(contextName, kind, cacheTTL); ok {
		return cached
	}

	timeout := timeoutFor(cmd)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c, err := cluster.New(ctx, cc)
	if err != nil {
		return nil
	}
	names, err := fetch(ctx, c)
	if err != nil {
		return nil
	}
	sort.Strings(names)
	writeCache(contextName, kind, names, cacheTTL)
	return names
}

// resolveContextForCompletion loads the config and resolves the active
// context, applying the same CLI overrides cmdsetup.Prepare does but
// without running CheckProtection — completion is read-only and must
// never refuse a prod context. Returns (name, ctx, ok); ok=false on
// any failure (config missing, no context selected, etc.).
func resolveContextForCompletion(cmd *cli.Command) (string, config.Context, bool) {
	cfg, _, err := config.LoadDefault(cmd.String("config"))
	if err != nil {
		return "", config.Context{}, false
	}
	name, cc, err := cfg.ResolveContext(cmd.String("context"))
	if err != nil {
		return "", config.Context{}, false
	}
	cmdsetup.ApplyContextOverrides(&cc, cmd)
	return name, cc, true
}

// timeoutFor reads the global --complete-timeout. Falls back to
// DefaultCompleteTimeout when the flag is undefined on the command
// (e.g. in unit tests with a stub root) so the suggester still has a
// deadline.
func timeoutFor(cmd *cli.Command) time.Duration {
	if d := cmd.Duration("complete-timeout"); d > 0 {
		return d
	}
	return DefaultCompleteTimeout
}

// cacheTTLFor reads the global --complete-cache. The flag is defined
// with Value: DefaultCompleteCacheTTL, so an unset flag returns the
// default; an operator-set 0 disables caching for the invocation. The
// fallback covers stub-root unit tests where the flag isn't defined.
func cacheTTLFor(cmd *cli.Command) time.Duration {
	if d := cmd.Duration("complete-cache"); d > 0 {
		return d
	}
	if cmd.IsSet("complete-cache") {
		// Operator explicitly disabled caching by passing 0.
		return 0
	}
	return DefaultCompleteCacheTTL
}

// CacheDir is the exported view of cacheDir for callers outside this
// package (notably `esops config cache-clear`, which needs the path
// without re-implementing the OS dispatch logic). Returns "" on the
// same conditions cacheDir does — no usable directory.
func CacheDir() string { return cacheDir() }

// cacheDir returns $XDG_CACHE_HOME/esops/complete (Linux convention),
// $HOME/Library/Caches/esops/complete on macOS, and an OS-appropriate
// fallback elsewhere. Returns "" if no usable directory can be found —
// the caller treats that as "no caching for this invocation" so a
// stranger setup degrades to live-fetch-every-time instead of failing.
func cacheDir() string {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "esops", "complete")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Caches", "esops", "complete")
	case "windows":
		if appData := os.Getenv("LOCALAPPDATA"); appData != "" {
			return filepath.Join(appData, "esops", "complete")
		}
		return filepath.Join(home, ".esops", "cache", "complete")
	default:
		return filepath.Join(home, ".cache", "esops", "complete")
	}
}

// cacheKey hashes the (context, kind) pair so the on-disk filename
// stays predictable but never reveals secrets or path-troublesome
// characters from the context name.
func cacheKey(contextName, kind string) string {
	h := sha256.Sum256([]byte(contextName + "\x00" + kind))
	return hex.EncodeToString(h[:8]) + "-" + sanitize(kind)
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// readCache returns the cached suggestion list if it exists and is
// within ttl; (nil, false) otherwise. ttl <= 0 disables caching.
func readCache(contextName, kind string, ttl time.Duration) ([]string, bool) {
	if ttl <= 0 {
		return nil, false
	}
	dir := cacheDir()
	if dir == "" {
		return nil, false
	}
	path := filepath.Join(dir, cacheKey(contextName, kind))
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	if time.Since(info.ModTime()) > ttl {
		return nil, false
	}
	// #nosec G304 -- path is composed from a hash of (context, kind); not attacker-controlled.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	if len(data) == 0 {
		return nil, true // empty list is a legitimate cached value
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	return lines, true
}

// writeCache atomically replaces the cache file for (contextName,
// kind). Uses os.CreateTemp + rename so two completions firing in
// parallel terminals never corrupt each other's mid-write tmp file —
// each gets a unique tmp name, and only the last rename wins. Errors
// are silent by design (completion never surfaces diagnostics).
func writeCache(contextName, kind string, names []string, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	dir := cacheDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, cacheKey(contextName, kind))
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	body := strings.Join(names, "\n")
	if len(names) > 0 {
		body += "\n"
	}
	if _, err := tmp.Write([]byte(body)); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
	}
}

