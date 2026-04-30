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

// TemplateListOptions narrows ListTemplates. Names is the optional
// list of template names the cluster filters on; empty returns every
// template. The cluster honours wildcards in name segments
// ("logs-*"), so the business layer doesn't need to expand them.
type TemplateListOptions struct {
	Names []string
}

// ListTemplates is the business-layer entry point for
// `esops index template list`. Returns the cluster's view of every
// (or every-matching) v2 index template, sorted alphabetically by
// name so a captured stdout is stable across runs.
func ListTemplates(ctx context.Context, ti client.IndexTemplateInspector, opts TemplateListOptions) ([]types.IndexTemplate, error) {
	rows, err := ti.GetTemplates(ctx, opts.Names)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].Name < rows[j].Name
	})
	return rows, nil
}

// GetTemplate is the business-layer entry point for
// `esops index template get`. Name is required and must be a literal
// — the cluster honours wildcards on this endpoint, but a bare-name
// lookup is the common case and a typo with a glob ("templ-*") would
// silently widen the response. Operators wanting wildcard matching
// pass the names through `template list --name`.
func GetTemplate(ctx context.Context, ti client.IndexTemplateInspector, name string) (types.IndexTemplate, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return types.IndexTemplate{}, fmt.Errorf("--name is required")
	}
	if isPatternish(n) {
		return types.IndexTemplate{}, fmt.Errorf("--name must be a literal template name, not a pattern (got %q); use 'index template list --name %s' instead", n, n)
	}
	rows, err := ti.GetTemplates(ctx, []string{n})
	if err != nil {
		return types.IndexTemplate{}, err
	}
	for _, t := range rows {
		if t.Name == n {
			return t, nil
		}
	}
	return types.IndexTemplate{}, fmt.Errorf("index template %q not found", n)
}

// TemplatePutOptions are the knobs the CLI layer hands to PutTemplate.
// Name is the literal template name. Body is the raw JSON template
// definition (settings / mappings / aliases / index_patterns / etc.) —
// forwarded verbatim, the cluster validates the schema. Create maps
// to ?create=true: when set, the cluster refuses if a template with
// the same name already exists, which is the safe default for first-
// time installs in shell pipelines.
type TemplatePutOptions struct {
	Name   string
	Body   []byte
	Create bool
	DryRun bool
}

// PutTemplate is the business-layer entry point for
// `esops index template put`. Validates Name and Body up-front:
//
//  1. Name is required, literal, no patterns.
//  2. Body parses as a JSON object — bad JSON or a non-object root
//     fails fast so the cluster doesn't have to.
//  3. Body declares index_patterns — a v2 template without it is
//     useless (it never matches anything). The cluster does report a
//     schema error in that case, but failing client-side keeps the
//     message specific and the request out of the cluster's audit log.
//
// On a dry run, the cluster is not contacted and a synthetic
// Acknowledged=true result is returned echoing Name.
func PutTemplate(ctx context.Context, tu client.IndexTemplateUpdater, opts TemplatePutOptions) (types.IndexTemplatePutResult, error) {
	if err := validateTemplateName(opts.Name); err != nil {
		return types.IndexTemplatePutResult{}, err
	}
	if err := validateTemplateBody(opts.Body); err != nil {
		return types.IndexTemplatePutResult{}, err
	}
	if opts.DryRun {
		return types.IndexTemplatePutResult{
			Action:       "template-put",
			DryRun:       true,
			Name:         opts.Name,
			Acknowledged: true,
		}, nil
	}
	res, err := tu.PutTemplate(ctx, types.IndexTemplatePutRequest{
		Name:   strings.TrimSpace(opts.Name),
		Body:   opts.Body,
		Create: opts.Create,
	})
	if err != nil {
		return types.IndexTemplatePutResult{}, err
	}
	if res.Action == "" {
		res.Action = "template-put"
	}
	if res.Name == "" {
		res.Name = strings.TrimSpace(opts.Name)
	}
	return res, nil
}

// TemplateDeleteOptions are the knobs the CLI layer hands to
// DeleteTemplate. Name is the literal template name; patterns are not
// supported on the delete path so a typo can't wipe out a category of
// templates in one call.
type TemplateDeleteOptions struct {
	Name   string
	DryRun bool
}

// DeleteTemplate is the business-layer entry point for
// `esops index template delete`. Validation is "literal name only";
// the cluster's own 404 surfaces the missing-template case with a
// useful message.
func DeleteTemplate(ctx context.Context, tu client.IndexTemplateUpdater, opts TemplateDeleteOptions) (types.IndexTemplateDeleteResult, error) {
	if err := validateTemplateName(opts.Name); err != nil {
		return types.IndexTemplateDeleteResult{}, err
	}
	if opts.DryRun {
		return types.IndexTemplateDeleteResult{
			Action:       "template-delete",
			DryRun:       true,
			Name:         opts.Name,
			Acknowledged: true,
		}, nil
	}
	res, err := tu.DeleteTemplate(ctx, types.IndexTemplateDeleteRequest{
		Name: strings.TrimSpace(opts.Name),
	})
	if err != nil {
		return types.IndexTemplateDeleteResult{}, err
	}
	if res.Action == "" {
		res.Action = "template-delete"
	}
	if res.Name == "" {
		res.Name = strings.TrimSpace(opts.Name)
	}
	return res, nil
}

func validateTemplateName(name string) error {
	n := strings.TrimSpace(name)
	if n == "" {
		return fmt.Errorf("--name is required")
	}
	if isPatternish(n) {
		return fmt.Errorf("--name must be a literal template name, not a pattern (got %q)", n)
	}
	return nil
}

func validateTemplateBody(body []byte) error {
	if len(body) == 0 {
		return fmt.Errorf("template body is required (use --from-file PATH or --body JSON)")
	}
	var probe map[string]any
	if err := json.Unmarshal(body, &probe); err != nil {
		return fmt.Errorf("template body is not valid JSON: %w", err)
	}
	if _, ok := probe["index_patterns"]; !ok {
		return fmt.Errorf("template body must declare 'index_patterns'; without it the template never matches an index")
	}
	return nil
}
