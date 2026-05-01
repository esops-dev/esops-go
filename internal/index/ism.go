package index

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// ISMListOptions narrows ListISMPolicies. IDs is the optional list of
// policy ids the plugin filters on (empty returns every policy).
type ISMListOptions struct {
	IDs []string
}

// ListISMPolicies is the business-layer entry point for
// `esops index ism list`. Returns the plugin's view of every (or every-
// matching) policy, sorted alphabetically by id so the captured stdout
// is stable across runs.
func ListISMPolicies(ctx context.Context, m client.ISMManager, opts ISMListOptions) ([]types.ISMPolicy, error) {
	rows, err := m.ListPolicies(ctx, opts.IDs)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].ID < rows[j].ID
	})
	return rows, nil
}

// GetISMPolicy is the business-layer entry point for
// `esops index ism get`. ID is required and must be a literal — the
// plugin honours wildcards on this endpoint, but a typo with a glob
// would silently widen the response. Operators wanting wildcard matching
// pass the ids through `ism list --id`.
func GetISMPolicy(ctx context.Context, m client.ISMManager, id string) (types.ISMPolicy, error) {
	n := strings.TrimSpace(id)
	if n == "" {
		return types.ISMPolicy{}, client.UsageError("--id is required")
	}
	if client.IsPatternish(n) {
		return types.ISMPolicy{}, client.UsageError("--id must be a literal policy id, not a pattern (got %q); use 'index ism list --id %s' instead", n, n)
	}
	return m.GetPolicy(ctx, n)
}

// ISMPutOptions are the knobs the CLI layer hands to PutISMPolicy. ID
// is the literal policy id. Body is the raw JSON the operator handed
// in (the plugin expects {"policy":{"description":"…","default_state":
// "…","states":[…]}}). SeqNo/PrimaryTerm map to the plugin's optimistic-
// concurrency knobs; both zero means "create-or-overwrite".
type ISMPutOptions struct {
	ID          string
	Body        []byte
	SeqNo       int64
	PrimaryTerm int64
	DryRun      bool
}

// PutISMPolicy is the business-layer entry point for
// `esops index ism put`. Validates ID and Body up-front:
//
//  1. ID is required, literal, no patterns.
//  2. Body parses as a JSON object — bad JSON or a non-object root
//     fails fast so the cluster doesn't have to.
//  3. Body declares a "policy" object — the plugin's wire shape.
//  4. SeqNo/PrimaryTerm are either both zero or both > 0 — passing one
//     without the other is a misuse the plugin would 400 on with a
//     terse "missing required parameter".
//
// On a dry run, the cluster is not contacted and a synthetic result
// echoes ID (SeqNo/PrimaryTerm are not synthesised — they only have
// meaning post-write).
func PutISMPolicy(ctx context.Context, m client.ISMManager, opts ISMPutOptions) (types.ISMPolicyPutResult, error) {
	if err := validateISMID(opts.ID); err != nil {
		return types.ISMPolicyPutResult{}, err
	}
	if err := validateISMBody(opts.Body); err != nil {
		return types.ISMPolicyPutResult{}, err
	}
	if (opts.SeqNo > 0) != (opts.PrimaryTerm > 0) {
		return types.ISMPolicyPutResult{}, client.UsageError("--if-seq-no and --if-primary-term must be set together (got seq_no=%d primary_term=%d)", opts.SeqNo, opts.PrimaryTerm)
	}
	if opts.DryRun {
		return types.ISMPolicyPutResult{
			Action: "ism-policy-put",
			DryRun: true,
			ID:     strings.TrimSpace(opts.ID),
		}, nil
	}
	res, err := m.PutPolicy(ctx, types.ISMPolicyPutRequest{
		ID:          strings.TrimSpace(opts.ID),
		Body:        opts.Body,
		SeqNo:       opts.SeqNo,
		PrimaryTerm: opts.PrimaryTerm,
	})
	if err != nil {
		return types.ISMPolicyPutResult{}, err
	}
	if res.Action == "" {
		res.Action = "ism-policy-put"
	}
	if res.ID == "" {
		res.ID = strings.TrimSpace(opts.ID)
	}
	return res, nil
}

// ISMDeleteOptions are the knobs the CLI layer hands to DeleteISMPolicy.
// ID is the literal policy id; patterns are not supported on the delete
// path so a typo can't wipe out a category of policies in one call.
type ISMDeleteOptions struct {
	ID     string
	DryRun bool
}

// DeleteISMPolicy is the business-layer entry point for
// `esops index ism delete`. Validation is "literal id only"; the
// plugin's own 4xx surfaces the still-attached case with a useful
// message.
func DeleteISMPolicy(ctx context.Context, m client.ISMManager, opts ISMDeleteOptions) (types.ISMPolicyDeleteResult, error) {
	if err := validateISMID(opts.ID); err != nil {
		return types.ISMPolicyDeleteResult{}, err
	}
	if opts.DryRun {
		return types.ISMPolicyDeleteResult{
			Action: "ism-policy-delete",
			DryRun: true,
			ID:     strings.TrimSpace(opts.ID),
			OK:     true,
		}, nil
	}
	res, err := m.DeletePolicy(ctx, types.ISMPolicyDeleteRequest{
		ID: strings.TrimSpace(opts.ID),
	})
	if err != nil {
		return types.ISMPolicyDeleteResult{}, err
	}
	if res.Action == "" {
		res.Action = "ism-policy-delete"
	}
	if res.ID == "" {
		res.ID = strings.TrimSpace(opts.ID)
	}
	return res, nil
}

// ISMExplainOptions narrows ExplainISM. Index is the comma-separated
// pattern the plugin accepts; empty hits the no-index form (every
// managed index). ShowPolicy maps to ?show_policy=true — the plugin
// embeds each managed index's full policy body in the response, which
// can be megabytes on a large fleet.
type ISMExplainOptions struct {
	Index      string
	ShowPolicy bool
}

// ExplainISM is the business-layer entry point for
// `esops index ism explain`. Returns one entry per index the plugin
// reports, sorted by index name so a captured stdout is stable across
// runs.
func ExplainISM(ctx context.Context, m client.ISMManager, opts ISMExplainOptions) ([]types.ISMExplainEntry, error) {
	rows, err := m.ExplainPolicy(ctx, types.ISMExplainRequest{
		Index:      strings.TrimSpace(opts.Index),
		ShowPolicy: opts.ShowPolicy,
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].Index < rows[j].Index
	})
	return rows, nil
}

// ExplainISMStream is the streaming companion to ExplainISM. See the
// ILM equivalent for the streaming-vs-sort contract.
func ExplainISMStream(ctx context.Context, m client.ISMManager, opts ISMExplainOptions, yield func(types.ISMExplainEntry) error) error {
	return m.ExplainPolicyStream(ctx, types.ISMExplainRequest{
		Index:      strings.TrimSpace(opts.Index),
		ShowPolicy: opts.ShowPolicy,
	}, yield)
}

func validateISMID(id string) error {
	n := strings.TrimSpace(id)
	if n == "" {
		return client.UsageError("--id is required")
	}
	if client.IsPatternish(n) {
		return client.UsageError("--id must be a literal policy id, not a pattern (got %q)", n)
	}
	return nil
}

func validateISMBody(body []byte) error {
	if len(body) == 0 {
		return client.UsageError("policy body is required (use --from-file PATH or --body JSON)")
	}
	var probe map[string]any
	if err := json.Unmarshal(body, &probe); err != nil {
		return client.UsageError("policy body is not valid JSON: %v", err)
	}
	if _, ok := probe["policy"]; !ok {
		return client.UsageError("policy body must declare a 'policy' object (plugin expects {\"policy\":{\"description\":\"…\",\"default_state\":\"…\",\"states\":[…]}})")
	}
	return nil
}
