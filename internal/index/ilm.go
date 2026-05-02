package index

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// ILMListOptions narrows ListILMPolicies. Names is the optional list of
// policy names the cluster filters on (empty returns every policy).
// The cluster honours wildcards in name segments, so the business layer
// does not expand them.
type ILMListOptions struct {
	Names []string
}

// ListILMPolicies is the business-layer entry point for
// `esops index ilm list`. Returns the cluster's view of every (or every-
// matching) lifecycle policy, sorted alphabetically by name so a captured
// stdout is stable across runs.
func ListILMPolicies(ctx context.Context, m client.ILMManager, opts ILMListOptions) ([]types.ILMPolicy, error) {
	rows, err := m.ListPolicies(ctx, opts.Names)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].Name < rows[j].Name
	})
	return rows, nil
}

// GetILMPolicy is the business-layer entry point for
// `esops index ilm get`. Name is required and must be a literal — the
// cluster honours wildcards on this endpoint, but a bare-name lookup is
// the common case and a typo with a glob would silently widen the
// response. Operators wanting wildcard matching pass the names through
// `ilm list --name`.
func GetILMPolicy(ctx context.Context, m client.ILMManager, name string) (types.ILMPolicy, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return types.ILMPolicy{}, client.UsageError("--name is required")
	}
	if client.IsPatternish(n) {
		return types.ILMPolicy{}, client.UsageError("--name must be a literal policy name, not a pattern (got %q); use 'index ilm list --name %s' instead", n, n)
	}
	return m.GetPolicy(ctx, n)
}

// ILMPutOptions are the knobs the CLI layer hands to PutILMPolicy. Name
// is the literal policy name. Body is the raw JSON the operator handed
// in (the cluster expects {"policy":{"phases":{…}}}).
type ILMPutOptions struct {
	Name   string
	Body   []byte
	DryRun bool
}

// PutILMPolicy is the business-layer entry point for
// `esops index ilm put`. Validates Name and Body up-front:
//
//  1. Name is required, literal, no patterns.
//  2. Body parses as a JSON object — bad JSON or a non-object root
//     fails fast so the cluster doesn't have to.
//  3. Body declares a "policy" key — the wire shape the cluster
//     expects. A body without it is a 400 the operator could have
//     prevented client-side; failing here keeps the message specific.
//
// On a dry run, the cluster is not contacted and a synthetic
// Acknowledged=true result is returned echoing Name.
func PutILMPolicy(ctx context.Context, m client.ILMManager, opts ILMPutOptions) (types.ILMPolicyPutResult, error) {
	if err := validateILMName(opts.Name); err != nil {
		return types.ILMPolicyPutResult{}, err
	}
	if err := validateILMBody(opts.Body); err != nil {
		return types.ILMPolicyPutResult{}, err
	}
	if opts.DryRun {
		return types.ILMPolicyPutResult{
			Action:       "ilm-policy-put",
			DryRun:       true,
			Name:         strings.TrimSpace(opts.Name),
			Acknowledged: true,
		}, nil
	}
	res, err := m.PutPolicy(ctx, types.ILMPolicyPutRequest{
		Name: strings.TrimSpace(opts.Name),
		Body: opts.Body,
	})
	if err != nil {
		return types.ILMPolicyPutResult{}, err
	}
	if res.Action == "" {
		res.Action = "ilm-policy-put"
	}
	if res.Name == "" {
		res.Name = strings.TrimSpace(opts.Name)
	}
	return res, nil
}

// ILMDeleteOptions are the knobs the CLI layer hands to DeleteILMPolicy.
// Name is the literal policy name; patterns are not supported on the
// delete path so a typo can't wipe out a category of policies in one
// call.
type ILMDeleteOptions struct {
	Name   string
	DryRun bool
}

// DeleteILMPolicy is the business-layer entry point for
// `esops index ilm delete`. Validation is "literal name only"; the
// cluster's own 4xx surfaces the still-attached case with a useful
// message (the cluster lists every index that still references the
// policy, which is exactly what an operator needs to clean up).
func DeleteILMPolicy(ctx context.Context, m client.ILMManager, opts ILMDeleteOptions) (types.ILMPolicyDeleteResult, error) {
	if err := validateILMName(opts.Name); err != nil {
		return types.ILMPolicyDeleteResult{}, err
	}
	if opts.DryRun {
		return types.ILMPolicyDeleteResult{
			Action:       "ilm-policy-delete",
			DryRun:       true,
			Name:         strings.TrimSpace(opts.Name),
			Acknowledged: true,
		}, nil
	}
	res, err := m.DeletePolicy(ctx, types.ILMPolicyDeleteRequest{
		Name: strings.TrimSpace(opts.Name),
	})
	if err != nil {
		return types.ILMPolicyDeleteResult{}, err
	}
	if res.Action == "" {
		res.Action = "ilm-policy-delete"
	}
	if res.Name == "" {
		res.Name = strings.TrimSpace(opts.Name)
	}
	return res, nil
}

// ILMExplainOptions narrows ExplainILM. Index is the comma-separated
// pattern the cluster accepts; empty means "every index" ("*"). The
// only-errors / only-managed knobs map to the cluster's same-named
// query params.
type ILMExplainOptions struct {
	Index       string
	OnlyErrors  bool
	OnlyManaged bool
}

// ExplainILM is the business-layer entry point for
// `esops index ilm explain`. Returns one entry per index the cluster
// reports, sorted by index name so the rendered table (and a captured
// JSON capture) is stable across runs.
//
// "*" is allowed here even though it is the cluster default — the
// blast-radius rules don't apply because explain is a read-only
// operation. Operators frequently want a cluster-wide "what is stuck?"
// view, which OnlyErrors=true captures at the cluster side.
func ExplainILM(ctx context.Context, m client.ILMManager, opts ILMExplainOptions) ([]types.ILMExplainEntry, error) {
	rows, err := m.ExplainPolicy(ctx, types.ILMExplainRequest{
		Index:       strings.TrimSpace(opts.Index),
		OnlyErrors:  opts.OnlyErrors,
		OnlyManaged: opts.OnlyManaged,
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].Index < rows[j].Index
	})
	return rows, nil
}

// ExplainILMStream is the streaming companion to ExplainILM. No
// client-side sort applies — streaming and sort are mutually
// exclusive. Operators wanting a sorted view use ExplainILM (table /
// json / yaml) which buffers; NDJSON is for the cluster-response-
// order stream.
func ExplainILMStream(ctx context.Context, m client.ILMManager, opts ILMExplainOptions, yield func(types.ILMExplainEntry) error) error {
	return m.ExplainPolicyStream(ctx, types.ILMExplainRequest{
		Index:       strings.TrimSpace(opts.Index),
		OnlyErrors:  opts.OnlyErrors,
		OnlyManaged: opts.OnlyManaged,
	}, yield)
}

func validateILMName(name string) error {
	n := strings.TrimSpace(name)
	if n == "" {
		return client.UsageError("--name is required")
	}
	if client.IsPatternish(n) {
		return client.UsageError("--name must be a literal policy name, not a pattern (got %q)", n)
	}
	return nil
}

func validateILMBody(body []byte) error {
	if len(body) == 0 {
		return client.UsageError("policy body is required (use --from-file PATH or --body JSON)")
	}
	var probe map[string]any
	if err := json.Unmarshal(body, &probe); err != nil {
		return client.UsageError("policy body is not valid JSON: %v", err)
	}
	if _, ok := probe["policy"]; !ok {
		return client.UsageError("policy body must declare a 'policy' object (cluster expects {\"policy\":{\"phases\":{…}}})")
	}
	return nil
}
