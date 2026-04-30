package index

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
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
		return nil, fmt.Errorf("--index is required (use a literal name or pattern; '*' to fetch every index is intentionally not supported)")
	}
	for _, n := range opts.Names {
		if strings.TrimSpace(n) == "" {
			return nil, fmt.Errorf("--name entries must be non-empty")
		}
	}

	rows, err := gi.GetSettings(ctx, types.IndexSettingsGetRequest{
		Index:             opts.Index,
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
}

// UpdateSettings is the business-layer entry point for `esops index
// settings set`. Validation rejects a missing --index, requires
// exactly one of Body or KeyValues, and parses Body as a JSON object
// up-front so a malformed --from-file is caught before the cluster
// sees the request.
func UpdateSettings(ctx context.Context, su client.IndexSettingsUpdater, opts SettingsUpdateOptions) (types.IndexSettingsUpdateResult, error) {
	if strings.TrimSpace(opts.Index) == "" {
		return types.IndexSettingsUpdateResult{}, fmt.Errorf("--index is required")
	}
	if isPatternish(opts.Index) && !strings.ContainsAny(opts.Index, "?,") {
		// '*' alone is the all-indices form — refused outright. A
		// pattern with a glob (e.g. "logs-*") is allowed since that's
		// the standard rotation use case.
		if strings.TrimSpace(opts.Index) == "*" || strings.TrimSpace(opts.Index) == "_all" {
			return types.IndexSettingsUpdateResult{}, fmt.Errorf("--index '%s' is too broad; name a pattern that matches the indices you intend to mutate", opts.Index)
		}
	}

	body, err := buildSettingsBody(opts.Body, opts.KeyValues)
	if err != nil {
		return types.IndexSettingsUpdateResult{}, err
	}

	indices := splitAndTrim(opts.Index)
	if opts.DryRun {
		return types.IndexSettingsUpdateResult{
			Action:       "settings-update",
			DryRun:       true,
			Indices:      indices,
			Acknowledged: true,
			Body:         string(body),
		}, nil
	}

	res, err := su.PutSettings(ctx, types.IndexSettingsUpdateRequest{
		Index:             opts.Index,
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
		return nil, fmt.Errorf("--body / --from-file and --set are mutually exclusive (pick one)")
	case !hasBody && !hasKV:
		return nil, fmt.Errorf("provide settings via --set key=value, --body JSON, or --from-file PATH")
	case hasBody:
		var probe any
		if err := json.Unmarshal(raw, &probe); err != nil {
			return nil, fmt.Errorf("settings body is not valid JSON: %w", err)
		}
		if _, ok := probe.(map[string]any); !ok {
			return nil, fmt.Errorf("settings body must be a JSON object, not a %T", probe)
		}
		return raw, nil
	default:
		index := make(map[string]any)
		for k, v := range kv {
			key := strings.TrimSpace(k)
			if key == "" {
				return nil, fmt.Errorf("--set key is empty")
			}
			key = strings.TrimPrefix(key, "index.")
			index[key] = v
		}
		return json.Marshal(map[string]any{"index": index})
	}
}

// splitAndTrim parses a comma-separated index list, trims each entry,
// and drops empties. Used on the dry-run path so the echoed Indices
// slice mirrors what the cluster would have seen on a live call.
func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
