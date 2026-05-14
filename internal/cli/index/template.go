package index

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	esopsindex "github.com/esops-dev/esops-go/internal/index"
	"github.com/esops-dev/esops-go/internal/output"
)

// templateNameSuggesters drives tab-completion for the --name flag on
// every `index template *` subcommand. Hoisted because the same map
// is wired on four call sites.
var templateNameSuggesters = map[string]completion.Suggester{
	"name": completion.SuggestIndexTemplates,
}

// templateCommand is the parent for `esops index template
// <list|get|put|delete>`. list and get are read-only; put and delete
// mutate cluster state and trigger the protection-tier check.
//
// The subcommand surface targets the v2 (composable) /_index_template
// endpoints; the legacy /_template surface is deliberately out of
// scope (both products consider it deprecated).
func templateCommand() *cli.Command {
	return &cli.Command{
		Name:  "template",
		Usage: "Manage v2 index templates (composable templates)",
		Commands: []*cli.Command{
			templateListCommand(),
			templateGetCommand(),
			templatePutCommand(),
			templateDeleteCommand(),
		},
	}
}

func templateListCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List index templates",
		Description: `List v2 index templates via GET /_index_template. The default view
shows name, priority, version, and the index_patterns the template
matches; -o wide adds composed_of and a data-stream flag.

Read-only and safe to run against a prod-protected context at any
time. --name takes one or more template names or patterns ("logs-*")
to narrow the response.`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{
				Name:  "name",
				Usage: "Limit to specific template name(s) or pattern(s); repeatable",
			},
		},
		Action:        runTemplateList,
		ShellComplete: completion.ForFlags(templateNameSuggesters),
	}
}

func runTemplateList(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	rows, err := esopsindex.ListTemplates(ctx, s.Client.IndexTemplateGet, esopsindex.TemplateListOptions{
		Names: cmd.StringSlice("name"),
	})
	if err != nil {
		return err
	}

	s.Log("index.template.list", "rows", len(rows))
	return output.IndexTemplates(os.Stdout, rows, s.Format)
}

func templateGetCommand() *cli.Command {
	return &cli.Command{
		Name:  "get",
		Usage: "Get a single index template",
		Description: `Get one v2 index template by name via GET /_index_template/<name>.
--name must be a literal template name (no patterns); for pattern
matching, use 'index template list --name'.

In table mode, the default view is a one-line header and the index
patterns the template matches; -o wide adds the full template body
(settings/mappings/aliases) as indented JSON. JSON/YAML emit the
neutral IndexTemplate shape directly.

Read-only and safe to run against a prod-protected context at any
time.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Template name (literal)",
			},
		},
		Action:        runTemplateGet,
		ShellComplete: completion.ForFlags(templateNameSuggesters),
	}
}

func runTemplateGet(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	tpl, err := esopsindex.GetTemplate(ctx, s.Client.IndexTemplateGet, cmd.String("name"))
	if err != nil {
		return err
	}

	s.Log("index.template.get", "name", tpl.Name)
	return output.IndexTemplate(os.Stdout, tpl, s.Format)
}

func templatePutCommand() *cli.Command {
	return &cli.Command{
		Name:  "put",
		Usage: "Create or update an index template",
		Description: `Create or update a v2 index template via PUT /_index_template/<name>.
The body must declare 'index_patterns' — without it the template
never matches an index, so esops refuses up-front rather than letting
the cluster reject a request that's already in its audit log.

Body input is exactly one of:
  --body JSON          Literal JSON object on the command line
  --from-file PATH     Read the JSON object from a file

Mutates cluster state. The cluster has no native dry-run for
/_index_template, so --dry-run is a client-side "validate + report"
path. Against a prod-protected context, --i-know-this-is-prod is
required.

--create maps to ?create=true: when set, the cluster refuses with a
400 if a template with the same name already exists. The safe default
for first-time installs in a CI pipeline.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Template name (literal, no patterns)",
			},
			&cli.StringFlag{
				Name:  "body",
				Usage: "Raw JSON object (template definition)",
			},
			&cli.StringFlag{
				Name:  "from-file",
				Usage: "Path to a file containing the JSON template body",
			},
			&cli.BoolFlag{
				Name:  "create",
				Usage: "Forward as ?create=true (refuse if a template with this name already exists)",
			},
		},
		Action:        runTemplatePut,
		ShellComplete: completion.ForFlags(templateNameSuggesters),
	}
}

func runTemplatePut(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	body, err := resolveBody(cmd.String("body"), cmd.String("from-file"))
	if err != nil {
		return err
	}

	opts := esopsindex.TemplatePutOptions{
		Name:   cmd.String("name"),
		Body:   body,
		Create: cmd.Bool("create"),
		DryRun: cmd.Bool("dry-run"),
	}
	result, err := esopsindex.PutTemplate(ctx, s.Client.IndexTemplateSet, opts)
	if err != nil {
		return err
	}

	s.Log("index.template.put",
		"name", result.Name,
		"create", opts.Create,
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.IndexTemplatePut(os.Stdout, result, s.Format)
}

func templateDeleteCommand() *cli.Command {
	return &cli.Command{
		Name:  "delete",
		Usage: "Delete an index template",
		Description: `Delete a v2 index template by name via
DELETE /_index_template/<name>. The cluster's own 404 surfaces a
missing template with a useful message — esops does not pre-check
existence so the destructive surface stays minimal.

Mutates cluster state. The cluster has no native dry-run for the
delete endpoint; --dry-run is a client-side "validate + report"
path. Against a prod-protected context, --i-know-this-is-prod is
required.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Template name (literal, no patterns)",
			},
		},
		Action:        runTemplateDelete,
		ShellComplete: completion.ForFlags(templateNameSuggesters),
	}
}

func runTemplateDelete(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopsindex.TemplateDeleteOptions{
		Name:   cmd.String("name"),
		DryRun: cmd.Bool("dry-run"),
	}
	result, err := esopsindex.DeleteTemplate(ctx, s.Client.IndexTemplateSet, opts)
	if err != nil {
		return err
	}

	s.Log("index.template.delete",
		"name", result.Name,
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.IndexTemplateDelete(os.Stdout, result, s.Format)
}
