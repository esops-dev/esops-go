package index

import (
	"context"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// ILMDescribeOptions is the business-layer input to DescribeILM. Name
// is the operator-supplied policy name; patterns are refused.
type ILMDescribeOptions struct {
	Name string
}

// ILMDescribeCapabilities is the read-only capability bundle the
// aggregator consumes. Matches the shape used by every other
// describe so the four aggregators read uniformly. Dialect is echoed
// into the result so structured consumers can tell which cluster
// product answered.
type ILMDescribeCapabilities struct {
	ILM     client.ILMManager
	Dialect string
}

// DescribeILM is the business-layer entry point for
// `esops index ilm describe`. Fetches the policy itself and reads
// /_ilm/explain to surface the per-managed-index phase/action/step
// view alongside the policy body.
//
// Scoping the explain call:
//   - When the policy has `in_use_by.indices` set, explain is
//     narrowed to those names — cheap on busy fleets.
//   - When `in_use_by.indices` is empty but `in_use_by.data_streams`
//     is non-empty (the modern ES pattern where policies attach via
//     templates / data streams rather than directly), explain runs
//     cluster-wide with OnlyManaged=true and filters client-side.
//     This avoids the gap where a policy attached via a data stream
//     would silently report 0 managed indices.
//   - When both lists are empty, explain is skipped entirely.
//
// ILM is Elasticsearch-only; ErrUnsupported on the OS adapter
// propagates so the operator sees the dialect mismatch.
func DescribeILM(ctx context.Context, caps ILMDescribeCapabilities, opts ILMDescribeOptions) (types.ILMPolicyDescription, error) {
	policy, err := GetILMPolicy(ctx, caps.ILM, opts.Name)
	if err != nil {
		return types.ILMPolicyDescription{}, err
	}

	desc := types.ILMPolicyDescription{
		Name:            policy.Name,
		Dialect:         caps.Dialect,
		Policy:          policy,
		SectionWarnings: map[string]string{},
	}

	scope, runExplain := ilmExplainScope(policy.InUseBy)
	if runExplain {
		entries, err := caps.ILM.ExplainPolicy(ctx, types.ILMExplainRequest{
			Index:       scope,
			OnlyManaged: true,
		})
		if err != nil {
			desc.SectionWarnings["managed_indices"] = err.Error()
		} else {
			matched := make([]types.ILMExplainEntry, 0, len(entries))
			for _, e := range entries {
				if e.Policy == policy.Name {
					matched = append(matched, e)
				}
			}
			sort.SliceStable(matched, func(i, j int) bool { return matched[i].Index < matched[j].Index })
			desc.ManagedIndices = matched
			desc.ManagedCount = len(matched)
			for _, e := range matched {
				if e.Step == "ERROR" || e.FailedStep != "" {
					desc.ErrorCount++
				}
			}
		}
	}

	if len(desc.SectionWarnings) == 0 {
		desc.SectionWarnings = nil
	}
	return desc, nil
}

// ilmExplainScope decides whether to run /_ilm/explain at all, and if
// so, what index-list path segment to use. The cluster's `in_use_by`
// block is the authoritative attachment set; we use it to keep the
// explain call as cheap as possible while not missing data-stream
// attachments (the common modern ES pattern).
//
// Returns (indexPath, run). When indexPath is empty and run is true,
// the caller runs a cluster-wide explain — which is the right move
// when the policy attaches via data streams (whose backing-index
// names are not enumerated in in_use_by). When run is false, the
// policy is unattached and explain would just return an empty set.
//
// Why the cluster-wide fallback exists at all: ES's in_use_by
// reports data-stream *names*, not the rolling list of backing
// index names a data stream owns at any given moment. So we cannot
// hand /_ilm/explain a narrow index list when the policy attaches
// only via templates / data streams — the only way to surface the
// per-index phase view is a cluster-wide scan, then a client-side
// filter by policy name. On a fleet with many policies this is the
// slowest path, hence the in_use_by.Indices fast path above.
func ilmExplainScope(u types.ILMInUseBy) (string, bool) {
	if len(u.Indices) > 0 {
		return strings.Join(u.Indices, ","), true
	}
	if len(u.DataStreams) > 0 {
		return "", true
	}
	return "", false
}
