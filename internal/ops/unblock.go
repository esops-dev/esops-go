package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// UnblockOptions narrows the input to UnblockIndices. Block defaults
// to "read_only_allow_delete" — the disk-watermark block the cluster
// auto-applies under disk pressure and does not auto-clear when
// pressure subsides. Other block names ("read_only", "write",
// "metadata", "read") are accepted so a single command can clear an
// operator-applied freeze without forcing them to remember the
// underlying _settings shape.
type UnblockOptions struct {
	Index             string
	Block             string
	IgnoreUnavailable bool
	DryRun            bool
}

// allowedUnblockBlocks is the canonical set of block names esops
// supports clearing through `ops unblock`. Anything else maps to the
// generic IndexSettingsUpdater path — but the safe-by-default story
// here is "the names the cluster's docs actually list", so a typo on
// the command line surfaces as a clear validation error rather than a
// silent no-op writing an unknown key the cluster will accept and
// ignore on a future restart.
var allowedUnblockBlocks = map[string]struct{}{
	"read_only_allow_delete": {},
	"read_only":              {},
	"write":                  {},
	"metadata":               {},
	"read":                   {},
}

// UnblockIndices clears the named index block via PUT
// /<indices>/_settings with body {"index":{"blocks":{"<name>":null}}}.
// The cluster honours JSON null as "remove this key" — leaving an
// empty string would keep the key visible in /<indices>/_settings
// forever.
//
// Refuses an empty --index outright. Refuses '*' / '_all' specifically:
// disk-pressure blocks are common cluster-wide, but blanket clearing
// without a pattern is a footgun (the operator might be releasing a
// block they wanted to keep in place on a stale archive index). An
// explicit --index pattern keeps grep-for-intent in shell history.
func UnblockIndices(ctx context.Context, su client.IndexSettingsUpdater, opts UnblockOptions) (types.IndexUnblockResult, error) {
	idx := strings.TrimSpace(opts.Index)
	if idx == "" {
		return types.IndexUnblockResult{}, client.UsageError("--index is required (use a literal name or pattern; '*' to unblock every index is intentionally not supported)")
	}
	if idx == "*" || idx == "_all" {
		return types.IndexUnblockResult{}, client.UsageError("--index '%s' is too broad; name a pattern that matches the indices you intend to unblock", idx)
	}
	block := strings.TrimSpace(opts.Block)
	if block == "" {
		block = "read_only_allow_delete"
	}
	if _, ok := allowedUnblockBlocks[block]; !ok {
		return types.IndexUnblockResult{}, client.UsageError("--block %q is not a known cluster index block (accepted: read_only_allow_delete, read_only, write, metadata, read)", block)
	}

	body, err := json.Marshal(map[string]any{
		"index": map[string]any{
			"blocks": map[string]any{
				block: nil,
			},
		},
	})
	if err != nil {
		return types.IndexUnblockResult{}, fmt.Errorf("encoding unblock body: %w", err)
	}

	indices := client.SplitCSV(idx)
	if opts.DryRun {
		return types.IndexUnblockResult{
			Action:       "unblock",
			DryRun:       true,
			Indices:      indices,
			Block:        block,
			Acknowledged: true,
		}, nil
	}

	res, err := su.PutSettings(ctx, types.IndexSettingsUpdateRequest{
		Indices:           indices,
		Body:              body,
		IgnoreUnavailable: opts.IgnoreUnavailable,
	})
	if err != nil {
		return types.IndexUnblockResult{}, err
	}
	return types.IndexUnblockResult{
		Action:       "unblock",
		Indices:      indices,
		Block:        block,
		Acknowledged: res.Acknowledged,
	}, nil
}
