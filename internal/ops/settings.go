package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// SettingsScope picks a /_cluster/settings scope on the wire. Empty
// is a synonym for "transient" — the safer default for `ops settings
// set` (a cluster restart drops the value, so a typo doesn't survive
// an outage).
type SettingsScope string

const (
	ScopeTransient  SettingsScope = "transient"
	ScopePersistent SettingsScope = "persistent"
)

// SettingsGetOptions narrows the input to GetClusterSettings. Names
// applies a flat-key filter client-side (the cluster's own filter is
// nested-only, which makes "tell me about cluster.routing.* keys"
// awkward); empty Names returns every key.
type SettingsGetOptions struct {
	Names           []string
	IncludeDefaults bool
}

// GetClusterSettings calls the cluster-settings full inspector and
// applies the operator's name filter client-side.
func GetClusterSettings(ctx context.Context, gi client.ClusterSettingsFullInspector, opts SettingsGetOptions) (types.ClusterSettingsScopedView, error) {
	full, err := gi.GetClusterSettingsFull(ctx, opts.IncludeDefaults)
	if err != nil {
		return types.ClusterSettingsScopedView{}, err
	}
	view := types.ClusterSettingsScopedView{
		Persistent:      filterByPrefixes(full.Persistent, opts.Names),
		Transient:       filterByPrefixes(full.Transient, opts.Names),
		IncludeDefaults: opts.IncludeDefaults,
	}
	if opts.IncludeDefaults {
		view.Defaults = filterByPrefixes(full.Defaults, opts.Names)
	}
	return view, nil
}

// DiffClusterSettings reads /_cluster/settings and returns the keys
// whose persistent and transient values disagree (including keys set
// in only one scope). Rows are sorted by key for stable output.
//
// Defaults are never considered: the diff is operator-set state vs
// operator-set state. A key set persistently and not transiently is
// drift; a key set persistently and matching the cluster's default
// transiently (because transient was never written) is not.
func DiffClusterSettings(ctx context.Context, gi client.ClusterSettingsFullInspector) (types.ClusterSettingsDiff, error) {
	full, err := gi.GetClusterSettingsFull(ctx, false)
	if err != nil {
		return types.ClusterSettingsDiff{}, err
	}
	keys := make(map[string]struct{}, len(full.Persistent)+len(full.Transient))
	for k := range full.Persistent {
		keys[k] = struct{}{}
	}
	for k := range full.Transient {
		keys[k] = struct{}{}
	}
	rows := make([]types.ClusterSettingsDiffRow, 0, len(keys))
	for k := range keys {
		p, hasP := full.Persistent[k]
		t, hasT := full.Transient[k]
		if hasP && hasT && fmt.Sprintf("%v", p) == fmt.Sprintf("%v", t) {
			continue
		}
		rows = append(rows, types.ClusterSettingsDiffRow{Key: k, Persistent: p, Transient: t})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	return types.ClusterSettingsDiff{Rows: rows}, nil
}

// SettingsSetOptions are the input to UpdateClusterSettings. Either
// Body or KeyValues must be set; Scope picks the persistent/transient
// target.
type SettingsSetOptions struct {
	Scope     SettingsScope
	KeyValues map[string]string
	Body      []byte
	DryRun    bool
}

// UpdateClusterSettings is the business-layer entry point for `ops
// settings set`. Validation rejects an empty input outright (the
// cluster's empty-body PUT is acknowledged=true but a no-op, exactly
// the kind of footgun the read-vs-write split exists to prevent).
//
// On a successful live PUT the cluster echoes back the resulting
// persistent and transient maps; we forward those verbatim so the
// renderer can show the post-update state without a follow-up GET.
func UpdateClusterSettings(ctx context.Context, su client.ClusterSettingsPutter, opts SettingsSetOptions) (types.ClusterSettingsPutResult, error) {
	scope := opts.Scope
	if scope == "" {
		scope = ScopeTransient
	}
	if scope != ScopeTransient && scope != ScopePersistent {
		return types.ClusterSettingsPutResult{}, client.UsageError("--scope must be 'transient' or 'persistent' (got %q)", scope)
	}

	values, err := buildClusterSettingsValues(opts.Body, opts.KeyValues)
	if err != nil {
		return types.ClusterSettingsPutResult{}, err
	}

	req := types.ClusterSettingsPutRequest{}
	switch scope {
	case ScopePersistent:
		req.Persistent = values
	default:
		req.Transient = values
	}

	if opts.DryRun {
		// Echo back what would have been sent so the operator can
		// verify scope + values before flipping --dry-run off.
		return types.ClusterSettingsPutResult{
			Acknowledged: true,
			Persistent:   req.Persistent,
			Transient:    req.Transient,
		}, nil
	}
	return su.PutClusterSettings(ctx, req)
}

// buildClusterSettingsValues mirrors index.buildSettingsBody's contract
// but unwraps the cluster-settings shape: the cluster's body is one
// scope mapping flat dotted-keys to values, no surrounding "index"
// envelope. Either Body (JSON object) or KeyValues (--set k=v pairs)
// must be set, but not both.
func buildClusterSettingsValues(raw []byte, kv map[string]string) (map[string]any, error) {
	hasBody := len(raw) > 0
	hasKV := len(kv) > 0
	switch {
	case hasBody && hasKV:
		return nil, client.UsageError("--body / --from-file and --set are mutually exclusive (pick one)")
	case !hasBody && !hasKV:
		return nil, client.UsageError("provide settings via --set key=value, --body JSON, or --from-file PATH")
	case hasBody:
		var probe map[string]any
		if err := json.Unmarshal(raw, &probe); err != nil {
			return nil, client.UsageError("settings body is not valid JSON object: %v", err)
		}
		// Reject the {"persistent":{...}} envelope that ES/OS' raw
		// /_cluster/settings PUT expects — `--scope` already picks the
		// scope, so an inline persistent/transient key is a footgun
		// that would flip the operator's intended scope on the wire.
		for _, banned := range []string{"persistent", "transient"} {
			if _, present := probe[banned]; present {
				return nil, client.UsageError("--body must be the bare key/value object (no surrounding %q wrapper); pick scope via --scope", banned)
			}
		}
		return probe, nil
	default:
		out := make(map[string]any, len(kv))
		for k, v := range kv {
			key := strings.TrimSpace(k)
			if key == "" {
				return nil, client.UsageError("--set key is empty")
			}
			out[key] = v
		}
		return out, nil
	}
}

// filterByPrefixes keeps only the keys whose dotted-path begins with
// any of the given prefixes. An empty filter list returns the input
// map unchanged. The match is prefix-with-boundary so passing
// "cluster.routing" doesn't accidentally pull in "cluster.routinghash".
func filterByPrefixes(in map[string]any, prefixes []string) map[string]any {
	if len(in) == 0 {
		return in
	}
	if len(prefixes) == 0 {
		return in
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		for _, p := range prefixes {
			if k == p || strings.HasPrefix(k, p+".") {
				out[k] = v
				break
			}
		}
	}
	return out
}
