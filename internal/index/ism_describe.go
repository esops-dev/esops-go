package index

import (
	"context"
	"sort"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// ISMDescribeOptions is the business-layer input to DescribeISM. ID
// is the operator-supplied policy ID; patterns are refused.
type ISMDescribeOptions struct {
	ID string
}

// ISMDescribeCapabilities is the read-only capability bundle the
// aggregator consumes. Matches the shape used by DescribeILM so the
// two stay symmetric on the surface even though the underlying
// wire shapes diverge.
type ISMDescribeCapabilities struct {
	ISM     client.ISMManager
	Dialect string
}

// DescribeISM is the business-layer entry point for
// `esops index ism describe`. Fetches the policy itself and runs a
// cluster-wide /_plugins/_ism/explain, filtering client-side to
// indices managed by this policy. ISM has no `in_use_by` analogue
// — explain is the only signal the plugin gives for "which indices
// use this policy".
//
// ISM is OpenSearch-only; ErrUnsupported on the ES adapter
// propagates so operators see the dialect mismatch.
func DescribeISM(ctx context.Context, caps ISMDescribeCapabilities, opts ISMDescribeOptions) (types.ISMPolicyDescription, error) {
	policy, err := GetISMPolicy(ctx, caps.ISM, opts.ID)
	if err != nil {
		return types.ISMPolicyDescription{}, err
	}

	desc := types.ISMPolicyDescription{
		ID:              policy.ID,
		Dialect:         caps.Dialect,
		Policy:          policy,
		SectionWarnings: map[string]string{},
	}

	entries, err := caps.ISM.ExplainPolicy(ctx, types.ISMExplainRequest{})
	if err != nil {
		desc.SectionWarnings["managed_indices"] = err.Error()
	} else {
		matched := make([]types.ISMExplainEntry, 0, len(entries))
		for _, e := range entries {
			if e.PolicyID == policy.ID {
				matched = append(matched, e)
			}
		}
		sort.SliceStable(matched, func(i, j int) bool { return matched[i].Index < matched[j].Index })
		desc.ManagedIndices = matched
		desc.ManagedCount = len(matched)
		for _, e := range matched {
			if e.Step == "ERROR" {
				desc.ErrorCount++
				continue
			}
			if info := e.StepInfo; len(info) > 0 {
				// ISM plugins surface failures via step_info.error
				// rather than a dedicated FailedStep field — match
				// the cluster's shape verbatim.
				if _, ok := info["error"]; ok {
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
