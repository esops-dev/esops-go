package ops

import (
	"context"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// CacheClearOptions narrows the input to CacheClear. Index is the
// comma-separated pattern (literal or glob) the cluster receives.
//
// Fielddata, Query, Request map to the cluster's per-cache flags.
// Default ("none of the three") sends a vanilla POST whose cluster-
// side default is to clear every cache. Fields narrows the fielddata
// flush to specific field names — only honoured when Fielddata is
// true (the cluster ignores it otherwise; the adapter forwards Fields
// only in that mode).
type CacheClearOptions struct {
	Index             string
	Fielddata         bool
	Query             bool
	Request           bool
	Fields            []string
	IgnoreUnavailable bool
	DryRun            bool
}

// CacheClear is the business-layer entry point for `ops cache-clear`.
// Validation refuses an empty --index outright — a cluster-wide
// "_cache/clear with no path" is a real footgun on busy clusters
// where a fielddata flush evicts the heap in one go.
//
// On a dry run the cluster is not contacted; the result echoes back
// the resolved indices and the cache labels that would have been
// cleared so the operator can verify what they're about to do.
func CacheClear(ctx context.Context, cc client.IndexCacheClearer, opts CacheClearOptions) (types.IndexCacheClearResult, error) {
	idx := strings.TrimSpace(opts.Index)
	if idx == "" {
		return types.IndexCacheClearResult{}, client.UsageError("--index is required (use a literal name or pattern; cluster-wide cache clear is intentionally not supported)")
	}
	if idx == "*" || idx == "_all" {
		return types.IndexCacheClearResult{}, client.UsageError("--index '%s' is too broad; name a pattern that matches the indices whose cache you intend to clear", idx)
	}
	if !opts.Fielddata && len(opts.Fields) > 0 {
		return types.IndexCacheClearResult{}, client.UsageError("--fields requires --fielddata (the cluster only honours --fields when fielddata clearing is on)")
	}
	for _, f := range opts.Fields {
		if strings.TrimSpace(f) == "" {
			return types.IndexCacheClearResult{}, client.UsageError("--fields entries must be non-empty")
		}
	}

	indices := client.SplitCSV(idx)
	caches := resolveCacheLabels(opts.Fielddata, opts.Query, opts.Request)

	if opts.DryRun {
		return types.IndexCacheClearResult{
			Action:  "cache-clear",
			DryRun:  true,
			Indices: indices,
			Caches:  caches,
			Fields:  opts.Fields,
		}, nil
	}

	res, err := cc.ClearCache(ctx, types.IndexCacheClearRequest{
		Indices:           indices,
		Fielddata:         opts.Fielddata,
		Query:             opts.Query,
		Request:           opts.Request,
		Fields:            opts.Fields,
		IgnoreUnavailable: opts.IgnoreUnavailable,
	})
	if err != nil {
		return types.IndexCacheClearResult{}, err
	}
	if res.Action == "" {
		res.Action = "cache-clear"
	}
	if len(res.Indices) == 0 {
		res.Indices = indices
	}
	res.Caches = caches
	res.Fields = opts.Fields
	return res, nil
}

// resolveCacheLabels turns the three per-cache flags into the labels
// surfaced on the result. When none are set, the cluster's default is
// "clear every cache" — we record the synthetic "all" label so a
// captured JSON record stays interpretable without having to know the
// cluster's default expansion.
func resolveCacheLabels(fielddata, query, request bool) []string {
	if !fielddata && !query && !request {
		return []string{"all"}
	}
	out := make([]string, 0, 3)
	if fielddata {
		out = append(out, "fielddata")
	}
	if query {
		out = append(out, "query")
	}
	if request {
		out = append(out, "request")
	}
	return out
}
