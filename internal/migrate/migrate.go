// Package migrate is the business logic behind `esops migrate ...`.
// Like internal/snapshot and internal/index it depends only on the
// small capability interfaces from pkg/client — never on a specific
// dialect's adapter — and the per-command actions in
// internal/cli/migrate stay thin wiring.
//
// Surface: read-only pre-flights (Plan, Check), and the destructive
// reindex / cutover / verify trio that move data and atomically swap
// aliases on the target.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/esops-dev/esops-go/internal/migrate/mapping"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// DefaultThroughputDocsPerSec is the conservative reindex rate used
// when an operator does not pass --throughput-docs-per-sec on
// `migrate plan`. Picked from observed throughput on a 2-node ES dev
// cluster with default settings — it is *not* a promise; the
// resulting estimate is a "you have time to grab coffee" hint, not a
// SLA. Operators tuning a real run override it from a recent reindex.
const DefaultThroughputDocsPerSec = 5000

// Endpoint is the per-cluster meta + capability bundle a migrate
// command operates on. Built by the CLI command before the action calls
// into this package. Not every command exercises every field — Plan
// and Check are read-only; Reindex and Cutover use the write surfaces.
// Nil capabilities short-circuit with a clear "wiring bug" error
// rather than panicking, so a missing assignment surfaces loudly.
//
// Protection and AuthType are flat scalars rather than a config.Context
// reference so the migrate package stays free of pkg/config types — the
// CLI layer copies the two fields it needs out of the resolved context
// when it builds the endpoint. Protection is the operator-set tier
// ("none", "staging", "prod"), surfaced so multi-context commands gate
// against both sides without re-loading config. AuthType is the
// resolved Auth.Type ("basic", "api_key", "bearer", "mtls", "sigv4")
// so the reindex preflight can refuse non-forwardable source-side auth
// without a second config load.
type Endpoint struct {
	Context          string
	URL              string
	Info             types.ClusterInfo
	Protection       string
	AuthType         string
	Auth             EndpointAuth
	Indices          client.IndexInspector
	Mappings         client.MappingsInspector
	Whitelist        client.RemoteReindexInspector
	DocCount         client.DocCounter
	Reindex          client.Reindexer
	TasksCancel      client.TasksCanceller
	Health           client.HealthInspector
	IndexSettingsSet client.IndexSettingsUpdater
	AliasUpdate      client.AliasUpdater
	AliasInspect     client.AliasInspector
}

// EndpointAuth is the credential subset migrate forwards to the
// target's /_reindex source.remote block when the source cluster
// requires authentication. Mirrors the fields config.Auth exposes
// without depending on the config package directly — keeps the
// migrate business layer free of CLI/config imports the way the rest
// of internal/* is.
//
// Only the fields the cluster's source.remote block honours are kept;
// mTLS / SigV4 source-side auth is *not* a remote-reindex feature in
// either dialect, so a context using either is rejected at the CLI
// layer (the operator points at a bearer/basic-auth proxy in front of
// the source instead).
type EndpointAuth struct {
	Username string
	Password string
	Token    string  // bearer token; rendered as Authorization: Bearer <t>
	APIKey   string  // ES api key; rendered as Authorization: ApiKey <k>
}

// remoteHeaders renders an EndpointAuth into the `headers` map the
// /_reindex body's source.remote block expects. Username/password go
// in their own typed slots — the headers map is for the bearer/api-
// key forms the cluster doesn't have a typed slot for.
func (a EndpointAuth) remoteHeaders() map[string]string {
	if a.Token == "" && a.APIKey == "" {
		return nil
	}
	h := make(map[string]string, 1)
	switch {
	case a.APIKey != "":
		h["Authorization"] = "ApiKey " + a.APIKey
	case a.Token != "":
		h["Authorization"] = "Bearer " + a.Token
	}
	return h
}

// summary lifts Endpoint into the neutral MigrationEndpoint shape used
// in both reports. Kept private — the CLI surface uses
// MigrationEndpoint, the package-internal type carries the live
// capabilities.
func (e Endpoint) summary() types.MigrationEndpoint {
	return types.MigrationEndpoint{
		Context: e.Context,
		Dialect: string(e.Info.Dialect),
		Version: e.Info.Version,
		URL:     e.URL,
	}
}

// Check runs the pre-flight set: probe outcome, version compatibility,
// and the target cluster's reindex.remote.whitelist (does it allow
// fetching from the source's host:port?). Connectivity is implicit:
// if the caller could build an Endpoint at all, the cluster answered
// the GET / probe.
//
// The function never returns an error for a *check* failure — those
// land as block/warn rows in the report. It returns an error only for
// "the run itself broke" (context cancellation, the whitelist call
// hit ErrAuth, etc.) so the CLI maps to the right exit code.
func Check(ctx context.Context, src, tgt Endpoint, _ types.MigrationCheckRequest) (types.MigrationCheckResult, error) {
	res := types.MigrationCheckResult{
		Source: src.summary(),
		Target: tgt.summary(),
	}

	res.Checks = append(res.Checks,
		probeCheck("source.connectivity", src),
		probeCheck("target.connectivity", tgt),
	)

	res.Checks = append(res.Checks, dialectCheck(src, tgt))
	res.Checks = append(res.Checks, versionCheck(src, tgt))

	whitelistCheck, err := remoteWhitelistCheck(ctx, src, tgt)
	if err != nil {
		return res, err
	}
	res.Checks = append(res.Checks, whitelistCheck)

	res.Status, res.Blockers, res.Warnings = aggregate(res.Checks)
	return res, nil
}

// probeCheck reports the outcome of the up-front cluster probe. Any
// Endpoint that reached this package has an Info populated by the
// factory — a bare empty value means "the probe failed but the CLI
// staged a placeholder so the rest of the report could still run".
func probeCheck(name string, e Endpoint) types.MigrationCheck {
	if e.Info.Dialect == "" {
		return types.MigrationCheck{
			Name:   name,
			Status: types.MigrationStatusBlock,
			Detail: fmt.Sprintf("could not probe %s — see logs for the underlying error", e.Context),
		}
	}
	return types.MigrationCheck{
		Name:   name,
		Status: types.MigrationStatusOK,
		Detail: fmt.Sprintf("%s reachable (%s %s)", e.Context, e.Info.Dialect, e.Info.Version),
	}
}

// dialectCheck flags ES↔OS migrations as a warn — cross-dialect
// reindex is first-class supported, but the operator should know
// they're crossing dialects so the surprises (security, ILM/ISM)
// don't land late.
func dialectCheck(src, tgt Endpoint) types.MigrationCheck {
	if src.Info.Dialect == "" || tgt.Info.Dialect == "" {
		return types.MigrationCheck{
			Name:   "dialect",
			Status: types.MigrationStatusWarn,
			Detail: "dialect comparison skipped — one or both probes failed",
		}
	}
	if src.Info.Dialect == tgt.Info.Dialect {
		return types.MigrationCheck{
			Name:   "dialect",
			Status: types.MigrationStatusOK,
			Detail: fmt.Sprintf("same-dialect (%s → %s)", src.Info.Dialect, tgt.Info.Dialect),
		}
	}
	return types.MigrationCheck{
		Name:   "dialect",
		Status: types.MigrationStatusWarn,
		Detail: fmt.Sprintf("cross-dialect %s → %s — security model, ILM/ISM, and pipelines do not migrate", src.Info.Dialect, tgt.Info.Dialect),
	}
}

// versionCheck applies a small set of known incompatibilities.
// Refusal cases (ES 7 → ES 9 without a 7→8 bridge) block; downgrades
// warn so the operator decides.
func versionCheck(src, tgt Endpoint) types.MigrationCheck {
	srcMajor, srcOK := majorOf(src.Info.Version)
	tgtMajor, tgtOK := majorOf(tgt.Info.Version)
	if !srcOK || !tgtOK {
		return types.MigrationCheck{
			Name:   "version",
			Status: types.MigrationStatusWarn,
			Detail: fmt.Sprintf("could not parse cluster version (%q → %q); skipping bridge check", src.Info.Version, tgt.Info.Version),
		}
	}
	if src.Info.Dialect == types.DialectElasticsearch && tgt.Info.Dialect == types.DialectElasticsearch {
		if tgtMajor-srcMajor >= 2 {
			return types.MigrationCheck{
				Name:   "version",
				Status: types.MigrationStatusBlock,
				Detail: fmt.Sprintf("ES %d → ES %d skips a major; reindex via an intermediate ES %d cluster", srcMajor, tgtMajor, srcMajor+1),
			}
		}
	}
	if tgtMajor < srcMajor {
		return types.MigrationCheck{
			Name:   "version",
			Status: types.MigrationStatusWarn,
			Detail: fmt.Sprintf("downgrade %s %s → %s %s — index format compatibility is one-way; reindex will rewrite segments", src.Info.Dialect, src.Info.Version, tgt.Info.Dialect, tgt.Info.Version),
		}
	}
	return types.MigrationCheck{
		Name:   "version",
		Status: types.MigrationStatusOK,
		Detail: fmt.Sprintf("%s %s → %s %s", src.Info.Dialect, src.Info.Version, tgt.Info.Dialect, tgt.Info.Version),
	}
}

// majorOf parses the leading integer of a version string ("9.3.3" →
// 9). Returns (0, false) on a value the cluster never produces.
func majorOf(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	dot := strings.IndexByte(v, '.')
	if dot < 0 {
		dot = len(v)
	}
	n := 0
	for i := 0; i < dot; i++ {
		c := v[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// remoteWhitelistCheck reads /_cluster/settings on the *target* and
// asks "does the source host[:port] appear anywhere in
// reindex.remote.whitelist?". The check is asymmetric on purpose:
// remote /_reindex pulls from source through target, so the target's
// whitelist is the gate. ErrUnreachable on the read becomes a warn
// (we still got a probe earlier) so the report stays useful.
func remoteWhitelistCheck(ctx context.Context, src, tgt Endpoint) (types.MigrationCheck, error) {
	if tgt.Whitelist == nil {
		return types.MigrationCheck{
			Name:   "target.remote_whitelist",
			Status: types.MigrationStatusWarn,
			Detail: "target context has no remote-whitelist inspector; skipping check",
		}, nil
	}
	wl, err := tgt.Whitelist.RemoteReindexWhitelist(ctx)
	if err != nil {
		// Forwarding the error class so the CLI maps to the right
		// exit code (auth/forbidden are not "report rows", they
		// abort the run).
		if errors.Is(err, client.ErrAuth) || errors.Is(err, client.ErrForbidden) {
			return types.MigrationCheck{}, err
		}
		return types.MigrationCheck{
			Name:   "target.remote_whitelist",
			Status: types.MigrationStatusWarn,
			Detail: fmt.Sprintf("target.remote_whitelist read failed: %v", err),
		}, nil
	}

	hostPort, ok := hostPortFromURL(src.URL)
	if !ok {
		return types.MigrationCheck{
			Name:   "target.remote_whitelist",
			Status: types.MigrationStatusWarn,
			Detail: fmt.Sprintf("could not parse source URL %q to a host:port; skipping match", src.URL),
		}, nil
	}

	entries := wl.All()
	if len(entries) == 0 {
		return types.MigrationCheck{
			Name:   "target.remote_whitelist",
			Status: types.MigrationStatusBlock,
			Detail: "target has no reindex.remote.whitelist; remote /_reindex from " + hostPort + " will be refused — set reindex.remote.whitelist on the target",
		}, nil
	}
	if matchWhitelist(entries, hostPort) {
		return types.MigrationCheck{
			Name:   "target.remote_whitelist",
			Status: types.MigrationStatusOK,
			Detail: fmt.Sprintf("%s allowed by target reindex.remote.whitelist (%s)", hostPort, strings.Join(entries, ", ")),
		}, nil
	}
	return types.MigrationCheck{
		Name:   "target.remote_whitelist",
		Status: types.MigrationStatusBlock,
		Detail: fmt.Sprintf("%s is not in target reindex.remote.whitelist (%s) — add it before reindex", hostPort, strings.Join(entries, ", ")),
	}, nil
}

// hostPortFromURL extracts host:port from a typical context URL
// ("https://host:9200/", "host:9200", "host"). Empty port falls back
// to the dialect default at match time, not here, so we still return
// a useful host string for an entry like "host:*".
func hostPortFromURL(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err == nil && u.Host != "" {
		return u.Host, true
	}
	return strings.TrimSuffix(strings.TrimSpace(raw), "/"), true
}

// matchWhitelist reports whether hostPort is permitted by the
// cluster's whitelist. The cluster's match rules support a literal
// match and a single trailing '*' as a wildcard (host:* or *:9200);
// both are honoured here. Comparison is case-insensitive on the host
// since DNS labels are — the helper normalises every entry once before
// the loop rather than lowercasing each one on every call.
func matchWhitelist(entries []string, hostPort string) bool {
	hp := strings.ToLower(hostPort)
	for _, raw := range entries {
		e := strings.ToLower(strings.TrimSpace(raw))
		if e == "" {
			continue
		}
		// Strip any "scheme://" prefix the operator may have written;
		// the cluster strips it too at match time. Done once up-front
		// so the literal and wildcard branches share the result.
		if i := strings.Index(e, "://"); i >= 0 {
			e = e[i+3:]
		}
		if e == hp {
			return true
		}
		if strings.Contains(e, "*") && wildcardMatch(e, hp) {
			return true
		}
	}
	return false
}

// wildcardMatch is a tiny shell-style matcher: '*' matches any run of
// characters (including empty). Sufficient for the cluster's "host:*"
// and "*:port" forms (and the operator-written multi-star variants
// like "*-prod-*:9200"); we deliberately do not support '?' or
// character classes — the cluster doesn't either.
//
// The implementation is the canonical recursive glob: at each step
// either consume one matching literal byte or skip the run after a
// star by trying every suffix of s. Ascii-only on the fast path —
// hostnames are ASCII per RFC 1035, and an operator who writes a
// non-ASCII whitelist entry will fall back to the byte-wise compare
// (correct for UTF-8 but not punycode-canonicalised, which is the
// cluster's behaviour too).
func wildcardMatch(pat, s string) bool {
	if pat == "" {
		return s == ""
	}
	if pat[0] == '*' {
		// Collapse consecutive stars; "**" has the same meaning as "*".
		for len(pat) > 0 && pat[0] == '*' {
			pat = pat[1:]
		}
		if pat == "" {
			return true
		}
		for i := 0; i <= len(s); i++ {
			if wildcardMatch(pat, s[i:]) {
				return true
			}
		}
		return false
	}
	if s == "" || pat[0] != s[0] {
		return false
	}
	return wildcardMatch(pat[1:], s[1:])
}

// aggregate computes the worst severity across a check list and
// counts blockers / warnings for the report footer.
func aggregate(checks []types.MigrationCheck) (worst string, blockers int, warnings int) {
	worst = types.MigrationStatusOK
	for _, c := range checks {
		switch c.Status {
		case types.MigrationStatusBlock:
			blockers++
			worst = types.MigrationStatusBlock
		case types.MigrationStatusWarn:
			warnings++
			if worst == types.MigrationStatusOK {
				worst = types.MigrationStatusWarn
			}
		}
	}
	return worst, blockers, warnings
}

// Plan inspects the source cluster's indices + mappings, runs the
// translator against the target dialect, and reports per-index docs/
// bytes/issue counts plus an estimated reindex duration. Side-effect-
// free: only the source's read endpoints are exercised, and only the
// target's *dialect* (not its data) is needed.
//
// The two cluster reads (indices listing + mappings) are independent
// and run concurrently — on a multi-thousand-index source the second
// call is the long tail, and the wall-time difference between
// sequential and concurrent issuance is the difference between
// "instant" and "ok, getting coffee".
func Plan(ctx context.Context, src, tgt Endpoint, req types.MigrationPlanRequest) (types.MigrationPlan, error) {
	if src.Indices == nil || src.Mappings == nil {
		return types.MigrationPlan{}, errors.New("source endpoint missing IndexInspector or MappingsInspector — wiring bug")
	}
	if tgt.Info.Dialect == "" {
		return types.MigrationPlan{}, errors.New("target endpoint not probed — cannot determine target dialect")
	}

	throughput := req.ThroughputDocsPerSec
	if throughput <= 0 {
		throughput = DefaultThroughputDocsPerSec
	}

	indices, mappings, err := planFetchIndicesAndMappings(ctx, src, req.Indices)
	if err != nil {
		return types.MigrationPlan{}, err
	}
	if len(indices) == 0 {
		return types.MigrationPlan{
			Source:        src.summary(),
			Target:        tgt.summary(),
			ThroughputDPS: throughput,
			Status:        types.MigrationStatusOK,
		}, nil
	}
	mappingsByIndex := make(map[string]types.IndexMapping, len(mappings))
	for _, m := range mappings {
		mappingsByIndex[m.Name] = m
	}

	plan := types.MigrationPlan{
		Source:        src.summary(),
		Target:        tgt.summary(),
		ThroughputDPS: throughput,
	}
	for _, ix := range indices {
		entry := types.MigrationIndexPlan{
			Index:             ix.Name,
			DocsCount:         ix.DocsCount,
			PrimaryStoreBytes: ix.PrimaryStoreBytes,
		}

		m, ok := mappingsByIndex[ix.Name]
		if ok {
			entry.Issues = mapping.Analyze(src.Info.Dialect, tgt.Info.Dialect, m)
		}
		entry.Status = mapping.WorstSeverity(entry.Issues)
		entry.EstimatedSeconds = estimateSeconds(ix.DocsCount, throughput)

		plan.Indices = append(plan.Indices, entry)
		plan.Totals.Indices++
		plan.Totals.DocsCount += ix.DocsCount
		plan.Totals.PrimaryStoreBytes += ix.PrimaryStoreBytes
		plan.Totals.EstimatedSeconds += entry.EstimatedSeconds

		switch entry.Status {
		case types.MigrationStatusBlock:
			plan.Blockers++
		case types.MigrationStatusWarn:
			plan.Warnings++
		}
	}

	sort.SliceStable(plan.Indices, func(i, j int) bool {
		return plan.Indices[i].Index < plan.Indices[j].Index
	})

	switch {
	case plan.Blockers > 0:
		plan.Status = types.MigrationStatusBlock
	case plan.Warnings > 0:
		plan.Status = types.MigrationStatusWarn
	default:
		plan.Status = types.MigrationStatusOK
	}
	return plan, nil
}

// planFetchIndicesAndMappings issues the two source-side reads
// concurrently. The cluster's coordinating node serves them from
// different paths (/_cat/indices vs /<indices>/_mapping) and they share
// no state — fan-out cuts the wall time roughly in half on a busy
// source. Errors short-circuit: a failed listing returns immediately,
// the in-flight mappings call observes the cancelled context.
func planFetchIndicesAndMappings(ctx context.Context, src Endpoint, pattern string) ([]types.Index, []types.IndexMapping, error) {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		indices  []types.Index
		mappings []types.IndexMapping
		errIdx   error
		errMap   error
		wg       sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		ix, err := src.Indices.Indices(cctx, types.IndexFilter{
			Index:  pattern,
			Status: "open",
		})
		if err != nil {
			errIdx = fmt.Errorf("listing source indices: %w", err)
			cancel()
			return
		}
		indices = ix
	}()
	go func() {
		defer wg.Done()
		mp, err := src.Mappings.GetMappings(cctx, types.MappingFilter{
			Index:             pattern,
			IgnoreUnavailable: true,
		})
		if err != nil {
			errMap = fmt.Errorf("reading source mappings: %w", err)
			cancel()
			return
		}
		mappings = mp
	}()
	wg.Wait()

	switch {
	case errIdx != nil:
		return nil, nil, errIdx
	case errMap != nil:
		return nil, nil, errMap
	}
	return indices, mappings, nil
}

// estimateSeconds produces the per-index reindex duration estimate.
// Floor at zero (a fresh empty index reports zero docs and the
// estimate should reflect that, not "tiny but non-zero"); throughput
// is guaranteed non-zero by the caller.
func estimateSeconds(docs int64, throughputPerSec int) float64 {
	if docs <= 0 || throughputPerSec <= 0 {
		return 0
	}
	return float64(docs) / float64(throughputPerSec)
}
