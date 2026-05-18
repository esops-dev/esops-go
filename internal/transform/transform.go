// Package transform holds the business logic behind the
// `esops transform ...` command tree. Like its sibling packages, it
// depends only on the small capability interfaces from pkg/client —
// never on a specific dialect's adapter — so the same code path
// covers Elasticsearch and OpenSearch.
package transform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// DefaultMaxTransforms is the blast-radius cap for pattern-based
// `transform delete` resolves. Same shape as the data-stream cap,
// lower than the index cap because a transform represents an ongoing
// materialisation pipeline — losing more than ten in one shot is
// almost always a misclick.
const DefaultMaxTransforms = 10

// nowFn lets tests fix the wall-clock used for CheckpointAgeMillis
// derivation. The default is time.Now in UTC.
var nowFn = func() time.Time { return time.Now().UTC() }

// ListOptions narrows a `transform list` call. Names is a repeatable
// filter forwarded to the inspector; empty Names returns every
// configured transform. SkipStats suppresses the per-row stats join
// — set by callers that only need definitions and don't want the
// extra round-trip (notably the deletion pre-flight, which only
// resolves ids).
type ListOptions struct {
	Names     []string
	SkipStats bool
}

// List returns the cluster's transforms sorted by name for
// deterministic output, with state / docs-processed / checkpoint age
// joined from /_stats — the same operational columns an SRE expects
// from `kubectl get pods`. The join is best-effort: a stats failure
// logs at debug level and leaves the per-row stats fields zero rather
// than failing the whole list. An operator running `transform list`
// against a partially-degraded cluster should still see what
// transforms exist.
func List(ctx context.Context, c client.TransformInspector, opts ListOptions) ([]types.Transform, error) {
	rows, err := c.ListTransforms(ctx, opts.Names)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	if !opts.SkipStats && len(rows) > 0 {
		enrichListWithStats(ctx, c, rows)
	}
	return rows, nil
}

// enrichListWithStats fetches stats for the listed transforms in one
// call (or one parallel fan-out on OS) and copies the operationally
// relevant fields onto each row. Best-effort: any error short-circuits
// the enrichment without surfacing to the caller, mirroring how the
// data-stream list enriches per-backing /_cat/indices stats.
func enrichListWithStats(ctx context.Context, c client.TransformInspector, rows []types.Transform) {
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	stats, err := c.ListTransformStats(ctx, names)
	if err != nil {
		// Stats are a bonus column on the list view, not the primary
		// signal. Don't fail the whole call — leave stats fields zero
		// and let the renderer show `-`.
		return
	}
	byName := make(map[string]types.TransformStats, len(stats))
	for _, s := range stats {
		byName[s.Name] = s
	}
	now := nowFn().UnixMilli()
	for i := range rows {
		s, ok := byName[rows[i].Name]
		if !ok {
			continue
		}
		rows[i].State = s.State
		rows[i].DocsProcessed = s.DocsProcessed
		rows[i].Failures = s.IndexFailures + s.SearchFailures
		rows[i].CheckpointLastTimeMillis = s.CheckpointLastTimeMillis
		if s.CheckpointLastTimeMillis > 0 {
			age := now - s.CheckpointLastTimeMillis
			if age < 0 {
				age = 0
			}
			rows[i].CheckpointAgeMillis = age
		}
	}
}

// Get fetches one transform by literal id. Patterns are refused
// up-front — `transform get` is the single-id verb.
func Get(ctx context.Context, c client.TransformInspector, name string) (types.Transform, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return types.Transform{}, client.UsageError("--name is required")
	}
	if client.IsPatternish(name) {
		return types.Transform{}, client.UsageError("--name must be a literal transform id, not a pattern (got %q)", name)
	}
	return c.GetTransform(ctx, name)
}

// Stats fetches runtime stats for one transform and derives
// CheckpointAgeMillis (now - last) so renderers don't have to do
// arithmetic against time.Now.
func Stats(ctx context.Context, c client.TransformInspector, name string) (types.TransformStats, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return types.TransformStats{}, client.UsageError("--name is required")
	}
	if client.IsPatternish(name) {
		return types.TransformStats{}, client.UsageError("--name must be a literal transform id, not a pattern (got %q)", name)
	}
	stats, err := c.StatsTransform(ctx, name)
	if err != nil {
		return types.TransformStats{}, err
	}
	if stats.CheckpointLastTimeMillis > 0 {
		ageMs := nowFn().UnixMilli() - stats.CheckpointLastTimeMillis
		if ageMs < 0 {
			ageMs = 0
		}
		stats.CheckpointAgeMillis = ageMs
	}
	return stats, nil
}

// PreviewOptions are the knobs the CLI hands to Preview.
type PreviewOptions struct {
	Body []byte
}

// Preview forwards the operator-supplied transform body to the
// cluster's preview endpoint and returns a bounded sample of
// materialised rows.
func Preview(ctx context.Context, c client.TransformInspector, opts PreviewOptions) (types.TransformPreviewResult, error) {
	if len(bytes.TrimSpace(opts.Body)) == 0 {
		return types.TransformPreviewResult{}, client.UsageError("transform body is required (--file or stdin)")
	}
	return c.PreviewTransform(ctx, types.TransformPreviewRequest{Body: opts.Body})
}

// ApplyOptions are the knobs the CLI hands to Apply.
//
// ConfirmOverwrite is invoked exactly when the apply would replace an
// existing definition (existence probe hit and body diff non-empty).
// The CLI wires this to cmdsetup.CheckProtection so a create against
// a prod-protected context proceeds without --i-know-this-is-prod
// while an overwrite still requires it — matches the story's
// "overwriting apply counts as destructive" wording.
// Nil callback is allowed for tests; means "no overwrite gate".
//
// Dialect is the connected cluster's product (Elasticsearch or
// OpenSearch). It drives the body-shape sanity check so an operator
// pointing at an OS context with an ES-shaped JSON body fails fast
// with a clear message rather than a cluster 400. Empty means "skip
// the check" (tests, or older callers).
type ApplyOptions struct {
	Name             string
	Body             []byte
	DryRun           bool
	ConfirmOverwrite func() error
	Dialect          types.Dialect
}

// Apply creates or updates a transform from the operator-supplied
// body.
//
// Flow:
//  1. Validates name (literal, non-empty) and body (well-formed JSON).
//  2. Sanity-checks the body shape against the connected dialect so
//     an OS-formatted body against an ES context fails fast with a
//     clear hint rather than a cluster 400.
//  3. Probes the existing transform by id. On 404, treats this as
//     a create; otherwise an update.
//  4. Diffs the submitted body against the existing Definition
//     after canonical-JSON re-encode. Match returns Unchanged=true
//     without touching the cluster.
//  5. On overwrite path (existed, body differs, not dry-run), calls
//     ConfirmOverwrite — the CLI wires this to the protection-tier
//     check so an overwrite against a prod context requires
//     --i-know-this-is-prod while a create does not. Matches the
//     story's "overwriting apply counts as destructive" wording.
//  6. On --dry-run, short-circuits after the diff.
//  7. Otherwise calls the manager.
func Apply(ctx context.Context, inspector client.TransformInspector, manager client.TransformManager, opts ApplyOptions) (types.TransformApplyResult, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return types.TransformApplyResult{}, client.UsageError("--name is required")
	}
	if client.IsPatternish(name) {
		return types.TransformApplyResult{}, client.UsageError("--name must be a literal transform id, not a pattern (got %q)", name)
	}
	if len(bytes.TrimSpace(opts.Body)) == 0 {
		return types.TransformApplyResult{}, client.UsageError("transform body is required (--file or stdin)")
	}
	var submitted map[string]any
	if err := json.Unmarshal(opts.Body, &submitted); err != nil {
		return types.TransformApplyResult{}, fmt.Errorf("%w: transform body is not valid JSON: %v", client.ErrUsage, err)
	}
	if err := validateBodyShape(submitted, opts.Dialect); err != nil {
		return types.TransformApplyResult{}, err
	}

	existing, err := inspector.GetTransform(ctx, name)
	exists := err == nil
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		return types.TransformApplyResult{}, err
	}

	if exists && transformBodiesEqual(submitted, existing.Definition) {
		return types.TransformApplyResult{
			Action:       "transform-apply",
			Name:         name,
			Unchanged:    true,
			DryRun:       opts.DryRun,
			Acknowledged: true,
		}, nil
	}

	if opts.DryRun {
		return types.TransformApplyResult{
			Action:       "transform-apply",
			Name:         name,
			Created:      !exists,
			Updated:      exists,
			DryRun:       true,
			Acknowledged: true,
		}, nil
	}

	// Overwriting an existing definition is the destructive path the
	// protection tier guards. Gate it now, after we know it's a real
	// overwrite — a clean create skips the gate.
	if exists && opts.ConfirmOverwrite != nil {
		if err := opts.ConfirmOverwrite(); err != nil {
			return types.TransformApplyResult{}, err
		}
	}

	result, err := manager.ApplyTransform(ctx, types.TransformApplyRequest{
		Name:   name,
		Body:   opts.Body,
		Update: exists,
	})
	if err != nil {
		return types.TransformApplyResult{}, err
	}
	result.Created = !exists
	result.Updated = exists
	return result, nil
}

// validateBodyShape catches the most common operator mistake: pasting
// an OS-shaped body (wrapped in `{"transform": {...}}`) into an ES
// apply, or an ES-shaped body (with top-level `source`/`dest`/`pivot`)
// into an OS apply. The two products' transform bodies aren't
// portable today — translating across dialects requires a full
// mapping table — but we can at least fail with a clear message
// rather than letting the cluster reject with a generic 400.
//
// Empty dialect skips the check (tests / older callers).
func validateBodyShape(body map[string]any, dialect types.Dialect) error {
	if dialect == "" {
		return nil
	}
	_, hasTransform := body["transform"]
	_, hasSource := body["source"]
	_, hasDest := body["dest"]
	_, hasPivot := body["pivot"]
	_, hasGroups := body["groups"]
	_, hasSourceIndex := body["source_index"]
	esShaped := hasSource || hasDest || hasPivot
	osShaped := hasTransform || hasGroups || hasSourceIndex

	switch dialect {
	case types.DialectElasticsearch:
		if osShaped && !esShaped {
			return fmt.Errorf("%w: body looks OpenSearch-shaped (top-level `transform`/`source_index`/`groups`) but the target cluster is Elasticsearch; ES expects top-level `source`/`dest`/`pivot`", client.ErrUsage)
		}
	case types.DialectOpenSearch:
		if esShaped && !osShaped {
			return fmt.Errorf("%w: body looks Elasticsearch-shaped (top-level `source`/`dest`/`pivot`) but the target cluster is OpenSearch; OS expects the definition wrapped in `{\"transform\": {...}}` with `source_index`/`target_index`/`groups`", client.ErrUsage)
		}
	}
	return nil
}

// DeleteOptions are the knobs the CLI hands to Delete. Pattern is the
// operator input (literal id(s) or pattern); MaxTransforms is the
// explicit opt-in past the default 10-transform blast-radius cap.
// ForceStop tells the manager to stop a running transform before
// removing it — without this flag the cluster refuses to delete a
// started one.
type DeleteOptions struct {
	Pattern        string
	MaxTransforms  int
	ForceStop      bool
	DryRun         bool
}

// Delete removes one or more transforms by id or pattern.
//
//  1. Validates the pattern (non-empty; rejects bare "*"/"_all").
//  2. Resolves the pattern via TransformInspector.
//  3. Enforces the blast-radius cap on the resolved set.
//  4. On dry-run, returns the resolved list without contacting the
//     destructive path.
//  5. Otherwise issues one delete per resolved id, threading the
//     ForceStop flag into each.
//
// Empty match against a pattern is a no-op result with
// Acknowledged=true so scripts can treat "nothing to delete" as
// success.
func Delete(ctx context.Context, inspector client.TransformInspector, manager client.TransformManager, opts DeleteOptions) ([]types.TransformDeleteResult, error) {
	pattern := strings.TrimSpace(opts.Pattern)
	if pattern == "" {
		return nil, client.UsageError("--name is required")
	}
	switch pattern {
	case "*", "_all":
		return nil, fmt.Errorf(
			"%w: refusing pattern %q — it would delete every transform; name a narrower pattern",
			client.ErrBlastRadius, pattern)
	}

	resolved, err := resolveTransformNames(ctx, inspector, pattern)
	if err != nil {
		return nil, err
	}

	limit := opts.MaxTransforms
	if limit <= 0 {
		limit = DefaultMaxTransforms
	}
	if len(resolved) > limit {
		return nil, fmt.Errorf(
			"%w: pattern %q resolves to %d transforms (limit %d); pass --max-transforms N to override",
			client.ErrBlastRadius, pattern, len(resolved), limit)
	}

	if len(resolved) == 0 {
		return []types.TransformDeleteResult{}, nil
	}

	out := make([]types.TransformDeleteResult, 0, len(resolved))
	if opts.DryRun {
		for _, n := range resolved {
			out = append(out, types.TransformDeleteResult{
				Action:       "transform-delete",
				Name:         n,
				Stopped:      opts.ForceStop,
				DryRun:       true,
				Acknowledged: true,
			})
		}
		return out, nil
	}

	for _, n := range resolved {
		r, err := manager.DeleteTransform(ctx, types.TransformDeleteRequest{
			Name:      n,
			ForceStop: opts.ForceStop,
		})
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

// StartOptions are the knobs the CLI hands to Start.
type StartOptions struct {
	Name   string
	DryRun bool
}

// Start starts a stopped transform.
func Start(ctx context.Context, c client.TransformManager, opts StartOptions) (types.TransformStartResult, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return types.TransformStartResult{}, client.UsageError("--name is required")
	}
	if client.IsPatternish(name) {
		return types.TransformStartResult{}, client.UsageError("--name must be a literal transform id, not a pattern (got %q)", name)
	}
	if opts.DryRun {
		return types.TransformStartResult{
			Action:       "transform-start",
			Name:         name,
			DryRun:       true,
			Acknowledged: true,
		}, nil
	}
	return c.StartTransform(ctx, types.TransformStartRequest{Name: name})
}

// StopOptions are the knobs the CLI hands to Stop. WaitForCheckpoint
// asks the cluster to let the in-flight checkpoint finish before
// halting — critical for continuous transforms so the destination
// index isn't left mid-batch.
type StopOptions struct {
	Name              string
	WaitForCheckpoint bool
	DryRun            bool
}

// Stop halts a running transform.
func Stop(ctx context.Context, c client.TransformManager, opts StopOptions) (types.TransformStopResult, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return types.TransformStopResult{}, client.UsageError("--name is required")
	}
	if client.IsPatternish(name) {
		return types.TransformStopResult{}, client.UsageError("--name must be a literal transform id, not a pattern (got %q)", name)
	}
	if opts.DryRun {
		return types.TransformStopResult{
			Action:            "transform-stop",
			Name:              name,
			WaitForCheckpoint: opts.WaitForCheckpoint,
			DryRun:            true,
			Acknowledged:      true,
		}, nil
	}
	return c.StopTransform(ctx, types.TransformStopRequest{
		Name:              name,
		WaitForCheckpoint: opts.WaitForCheckpoint,
	})
}

// resolveTransformNames expands a comma-joined pattern into the set
// of matching transform ids. Literal ids (no glob metacharacter) are
// returned unchanged; patterns are resolved by listing every
// transform and matching client-side, since OS does not honour
// patterns at the path segment.
func resolveTransformNames(ctx context.Context, inspector client.TransformInspector, pattern string) ([]string, error) {
	parts := client.SplitCSV(pattern)
	literals := make([]string, 0, len(parts))
	patternish := make([]string, 0, len(parts))
	for _, p := range parts {
		if client.IsPatternish(p) {
			patternish = append(patternish, p)
		} else {
			literals = append(literals, p)
		}
	}

	if len(patternish) == 0 {
		// Pure-literal path: trust the caller's input and avoid the
		// list round-trip. The DELETE returns 404 if any id is
		// missing — surfaced to the operator one row at a time.
		sort.Strings(literals)
		return uniqueStrings(literals), nil
	}

	rows, err := inspector.ListTransforms(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(rows))
	for _, l := range literals {
		out[l] = struct{}{}
	}
	for _, r := range rows {
		for _, pat := range patternish {
			if matchTransformPattern(pat, r.Name) {
				out[r.Name] = struct{}{}
				break
			}
		}
	}
	names := make([]string, 0, len(out))
	for n := range out {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// matchTransformPattern is the tiny glob matcher we use for
// pattern-driven deletes. The cluster only honours `*` and `?` at
// the path segment for ES list reads (transforms have no first-class
// pattern endpoint); we reproduce the same two-character glob
// dialect client-side so the operator-facing behaviour is identical
// on both products.
func matchTransformPattern(pattern, name string) bool {
	return globMatch(pattern, name)
}

// globMatch implements the simple `*`/`?` glob without backtracking
// blow-ups. Adapted from the canonical iterative pattern matcher;
// `*` matches any subsequence, `?` matches one character.
func globMatch(pattern, name string) bool {
	pi, ni := 0, 0
	star, nbacktrack := -1, 0
	for ni < len(name) {
		switch {
		case pi < len(pattern) && (pattern[pi] == name[ni] || pattern[pi] == '?'):
			pi++
			ni++
		case pi < len(pattern) && pattern[pi] == '*':
			star = pi
			nbacktrack = ni
			pi++
		case star != -1:
			pi = star + 1
			nbacktrack++
			ni = nbacktrack
		default:
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// uniqueStrings returns its input with adjacent duplicates dropped;
// caller must sort first.
func uniqueStrings(in []string) []string {
	if len(in) <= 1 {
		return in
	}
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// transformBodiesEqual reports whether the submitted body matches
// the existing Definition once both sides have been canonicalised
// (same key order, no whitespace). Used to short-circuit a no-op
// apply.
//
// Both sides are passed as map[string]any so this stays a pure
// data comparison — no string fiddling with the original JSON
// bytes. json.Marshal sorts map keys alphabetically, so two maps
// with the same contents but different key orders produce
// identical output.
func transformBodiesEqual(submitted, existing map[string]any) bool {
	if len(existing) == 0 {
		return false
	}
	a, err := json.Marshal(submitted)
	if err != nil {
		return false
	}
	b, err := json.Marshal(existing)
	if err != nil {
		return false
	}
	return bytes.Equal(a, b)
}
