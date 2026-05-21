package index

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/internal/onerror"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// SettingsGetOptions are the knobs the CLI layer hands to GetSettings.
// Index is forwarded to the cluster as the path segment; Names is the
// optional list of dotted setting keys to filter by (the cluster
// honours these as path segments). IncludeDefaults flips on the
// cluster's defaults block so an audit can tell "configured" from
// "inherited".
type SettingsGetOptions struct {
	Index             string
	Names             []string
	IncludeDefaults   bool
	FlatSettings      bool
	IgnoreUnavailable bool
}

// GetSettings is the business-layer entry point for `esops index
// settings get`. Index is required — the cluster's no-index form
// returns every index in the cluster, which on a busy prod cluster is
// a multi-megabyte response that is almost never what an operator
// asked for. Forcing --index keeps the foot-gun out of shell history.
//
// Results are sorted by index name so a captured stdout (saved to a
// ticket, or diffed across runs) is stable across invocations.
func GetSettings(ctx context.Context, gi client.IndexSettingsInspector, opts SettingsGetOptions) ([]types.IndexSettings, error) {
	if strings.TrimSpace(opts.Index) == "" {
		return nil, client.UsageError("--index is required (use a literal name or pattern; '*' to fetch every index is intentionally not supported)")
	}
	for _, n := range opts.Names {
		if strings.TrimSpace(n) == "" {
			return nil, client.UsageError("--name entries must be non-empty")
		}
	}

	rows, err := gi.GetSettings(ctx, types.IndexSettingsGetRequest{
		Indices:           client.SplitCSV(opts.Index),
		Names:             opts.Names,
		IncludeDefaults:   opts.IncludeDefaults,
		FlatSettings:      opts.FlatSettings,
		IgnoreUnavailable: opts.IgnoreUnavailable,
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].Index < rows[j].Index
	})
	return rows, nil
}

// GetSettingsStream is the streaming companion to GetSettings: invokes
// yield per (index, settings, defaults) triple as the cluster's
// top-level object envelope is decoded. No client-side sort happens —
// streaming and sort are mutually exclusive.
func GetSettingsStream(ctx context.Context, gi client.IndexSettingsInspector, opts SettingsGetOptions, yield func(types.IndexSettings) error) error {
	if strings.TrimSpace(opts.Index) == "" {
		return client.UsageError("--index is required (use a literal name or pattern; '*' to fetch every index is intentionally not supported)")
	}
	for _, n := range opts.Names {
		if strings.TrimSpace(n) == "" {
			return client.UsageError("--name entries must be non-empty")
		}
	}
	return gi.GetSettingsStream(ctx, types.IndexSettingsGetRequest{
		Indices:           client.SplitCSV(opts.Index),
		Names:             opts.Names,
		IncludeDefaults:   opts.IncludeDefaults,
		FlatSettings:      opts.FlatSettings,
		IgnoreUnavailable: opts.IgnoreUnavailable,
	}, yield)
}

// SettingsUpdateOptions are the knobs the CLI layer hands to
// UpdateSettings. Index is the comma-separated pattern (literal or
// glob) the cluster will receive. Either Body or KeyValues must be
// set, but not both:
//
//   - Body is a raw JSON object the operator handed in (e.g. via
//     --from-file). Forwarded verbatim; the cluster validates field
//     names and outer shape.
//   - KeyValues is the dotted-key/string-value map built from one or
//     more --set key=value flags. The business layer wraps it in the
//     cluster's expected {"index":{...}} envelope.
//
// PreserveExisting maps to the cluster's ?preserve_existing=true.
// IgnoreUnavailable maps to ?ignore_unavailable=true. DryRun mirrors
// the global flag; on a dry run the cluster is not contacted and the
// resulting body is echoed back so the operator can verify what would
// have been sent.
type SettingsUpdateOptions struct {
	Index             string
	Body              []byte
	KeyValues         map[string]string
	PreserveExisting  bool
	IgnoreUnavailable bool
	DryRun            bool
	// OnError is the operator's --on-error selection. The default
	// (abort) issues a single bulk PutSettings; "skip" dispatches one
	// PUT per resolved index so one rejected setting on a single index
	// doesn't take the whole sweep down. "rollback" is rejected by the
	// CLI layer (settings rollback needs prior-values capture, which
	// is tracked as a follow-up).
	OnError types.OnErrorPolicy
}

// UpdateSettings is the business-layer entry point for `esops index
// settings set`. Validation rejects a missing --index, requires
// exactly one of Body or KeyValues, and parses Body as a JSON object
// up-front so a malformed --from-file is caught before the cluster
// sees the request.
func UpdateSettings(ctx context.Context, su client.IndexSettingsUpdater, opts SettingsUpdateOptions) (types.IndexSettingsUpdateResult, error) {
	if strings.TrimSpace(opts.Index) == "" {
		return types.IndexSettingsUpdateResult{}, client.UsageError("--index is required")
	}
	if client.IsPatternish(opts.Index) && !strings.ContainsAny(opts.Index, "?,") {
		// '*' alone is the all-indices form — refused outright. A
		// pattern with a glob (e.g. "logs-*") is allowed since that's
		// the standard rotation use case.
		if strings.TrimSpace(opts.Index) == "*" || strings.TrimSpace(opts.Index) == "_all" {
			return types.IndexSettingsUpdateResult{}, client.UsageError("--index '%s' is too broad; name a pattern that matches the indices you intend to mutate", opts.Index)
		}
	}

	body, err := buildSettingsBody(opts.Body, opts.KeyValues)
	if err != nil {
		return types.IndexSettingsUpdateResult{}, err
	}

	indices := client.SplitCSV(opts.Index)
	if opts.DryRun {
		return types.IndexSettingsUpdateResult{
			Action:       "settings-update",
			DryRun:       true,
			Indices:      indices,
			Acknowledged: true,
			Body:         string(body),
		}, nil
	}

	if opts.OnError == types.OnErrorSkip {
		return runSettingsSetPerIndex(ctx, su, indices, body, opts)
	}

	res, err := su.PutSettings(ctx, types.IndexSettingsUpdateRequest{
		Indices:           indices,
		Body:              body,
		PreserveExisting:  opts.PreserveExisting,
		IgnoreUnavailable: opts.IgnoreUnavailable,
	})
	if err != nil {
		return types.IndexSettingsUpdateResult{}, err
	}
	if res.Action == "" {
		res.Action = "settings-update"
	}
	if res.Indices == nil {
		res.Indices = indices
	}
	return res, nil
}

// runSettingsSetPerIndex implements --on-error skip for settings
// updates by issuing one PUT per resolved index. Result.Acknowledged
// flips to true when at least one index succeeded; the precise picture
// is in the per-item OnError.Items slice.
func runSettingsSetPerIndex(ctx context.Context, su client.IndexSettingsUpdater, indices []string, body []byte, opts SettingsUpdateOptions) (types.IndexSettingsUpdateResult, error) {
	step := func(ctx context.Context, name string) error {
		_, err := su.PutSettings(ctx, types.IndexSettingsUpdateRequest{
			Indices:           []string{name},
			Body:              body,
			PreserveExisting:  opts.PreserveExisting,
			IgnoreUnavailable: opts.IgnoreUnavailable,
		})
		return err
	}
	sum, _ := onerror.Run(ctx, indices, identityString, step, nil, opts.OnError)
	// Mirror the bulk live path's result shape — Body is only echoed
	// back on dry-run (where the operator is verifying the body that
	// would have been sent). Emitting it here too would diverge from
	// the abort-path output for the same call.
	return types.IndexSettingsUpdateResult{
		Action:       "settings-update",
		Indices:      indices,
		Acknowledged: sum.Succeeded > 0,
		OnError:      &sum,
	}, nil
}

// buildSettingsBody resolves Body / KeyValues into the JSON body the
// cluster expects. Exactly one of the inputs must be set:
//
//   - Body alone: parsed to ensure it's a JSON object, then forwarded
//     verbatim (the cluster's schema check is the source of truth).
//   - KeyValues alone: each "index.foo=bar" pair becomes a leaf in the
//     standard {"index":{"foo":"bar"}} envelope. Bare keys with no
//     "index." prefix are auto-prefixed so an operator typing
//     `--set number_of_replicas=2` doesn't have to remember the
//     envelope.
//   - Both set: refused. There is no sensible merge order — picking one
//     would surprise the operator who set the other.
//   - Neither set: refused. The cluster's empty-body PUT is a no-op
//     accepted with acknowledged=true; that's a footgun (operator
//     thinks the change went through), so we fail fast instead.
func buildSettingsBody(raw []byte, kv map[string]string) ([]byte, error) {
	hasBody := len(raw) > 0
	hasKV := len(kv) > 0
	switch {
	case hasBody && hasKV:
		return nil, client.UsageError("--body / --from-file and --set are mutually exclusive (pick one)")
	case !hasBody && !hasKV:
		return nil, client.UsageError("provide settings via --set key=value, --body JSON, or --from-file PATH")
	case hasBody:
		var probe any
		if err := json.Unmarshal(raw, &probe); err != nil {
			return nil, client.UsageError("settings body is not valid JSON: %v", err)
		}
		if _, ok := probe.(map[string]any); !ok {
			return nil, client.UsageError("settings body must be a JSON object, not a %T", probe)
		}
		return raw, nil
	default:
		// The cluster's settings body is hierarchical
		// (`index` → `lifecycle` → `name`), so a user-typed
		// --set index.lifecycle.name=foo must expand into a three-level
		// nested object — emitting {"index":{"lifecycle.name":"foo"}}
		// would only work under ?flat_settings=true and silently no-op
		// otherwise.
		//
		// Bare keys (no "index." prefix) are auto-prefixed first so
		// --set number_of_replicas=2 still produces the standard
		// {"index":{"number_of_replicas":"2"}} shape.
		root := make(map[string]any)
		seen := make(map[string]string, len(kv))
		for k, v := range kv {
			key := strings.TrimSpace(k)
			if key == "" {
				return nil, client.UsageError("--set key is empty")
			}
			if key != "index" && !strings.HasPrefix(key, "index.") {
				key = "index." + key
			}
			if prev, dup := seen[key]; dup {
				return nil, client.UsageError("--set %q resolves to the same effective key as a prior pair (values %q and %q); pass each key once", k, prev, v)
			}
			seen[key] = v
			if err := setNestedString(root, strings.Split(key, "."), v); err != nil {
				return nil, err
			}
		}
		return json.Marshal(root)
	}
}

// setNestedString walks (and creates) a nested map[string]any path and
// stores v at the leaf. Returns an error when a path segment collides
// with a previously-set scalar — e.g. --set index.x=1 followed by
// --set index.x.y=2 is contradictory and would otherwise silently
// overwrite the scalar with a fresh sub-object.
func setNestedString(root map[string]any, path []string, v string) error {
	if len(path) == 0 {
		return fmt.Errorf("internal: empty key path")
	}
	cur := root
	for i, seg := range path[:len(path)-1] {
		next, ok := cur[seg]
		if !ok {
			m := make(map[string]any)
			cur[seg] = m
			cur = m
			continue
		}
		m, isMap := next.(map[string]any)
		if !isMap {
			return client.UsageError("--set key collision: %q is set as both a scalar and a sub-object", strings.Join(path[:i+1], "."))
		}
		cur = m
	}
	leaf := path[len(path)-1]
	if existing, ok := cur[leaf]; ok {
		if _, isMap := existing.(map[string]any); isMap {
			return client.UsageError("--set key collision: %q already has nested keys; cannot overwrite with a scalar", strings.Join(path, "."))
		}
	}
	cur[leaf] = v
	return nil
}
