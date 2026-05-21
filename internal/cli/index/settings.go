package index

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	esopsindex "github.com/esops-dev/esops-go/internal/index"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// settingsCommand is the parent for `esops index settings <get|set>`.
// get is read-only; set mutates cluster state and triggers the
// protection-tier check.
func settingsCommand() *cli.Command {
	return &cli.Command{
		Name:  "settings",
		Usage: "Get and update index settings",
		Commands: []*cli.Command{
			settingsGetCommand(),
			settingsSetCommand(),
		},
	}
}

func settingsGetCommand() *cli.Command {
	return &cli.Command{
		Name:  "get",
		Usage: "Get index settings",
		Description: `Read settings via GET /<indices>/_settings/<names>. Output is one
row per (index, dotted-key, value) triple in table mode, and the
neutral []IndexSettings shape in JSON/YAML/NDJSON.

Read-only and safe to run against a prod-protected context at any time.

--include-defaults asks the cluster to merge in the built-in default
values the index inherits but doesn't override; useful for "is this
knob actually configured, or am I looking at the default?".

--name takes one or more dotted setting prefixes (e.g.
"index.number_of_replicas") to narrow the response — handy on busy
clusters where a full settings dump per index is multiple kilobytes.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "index",
				Required: true,
				Usage:    "Comma-separated index names or patterns (e.g. 'logs-2024-*,archive-2023')",
			},
			&cli.StringSliceFlag{
				Name:  "name",
				Usage: "Limit to specific setting key prefix(es); repeatable (e.g. 'index.number_of_replicas')",
			},
			&cli.BoolFlag{
				Name:  "include-defaults",
				Usage: "Include built-in default values the index inherits but doesn't override",
			},
			&cli.BoolFlag{
				Name:  "flat-settings",
				Usage: "Forward as ?flat_settings=true (cluster returns dotted keys instead of nested objects)",
			},
			&cli.BoolFlag{
				Name:  "ignore-unavailable",
				Usage: "Treat missing indices as a no-op instead of a 404 error",
			},
		},
		Action: runSettingsGet,
		ShellComplete: completion.ForFlags(map[string]completion.Suggester{
			"index": completion.SuggestIndices,
		}),
	}
}

func runSettingsGet(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	opts := esopsindex.SettingsGetOptions{
		Index:             cmd.String("index"),
		Names:             cmd.StringSlice("name"),
		IncludeDefaults:   cmd.Bool("include-defaults"),
		FlatSettings:      cmd.Bool("flat-settings"),
		IgnoreUnavailable: cmd.Bool("ignore-unavailable"),
	}

	if s.Format == output.FormatNDJSON {
		count := 0
		err := output.IndexSettingsNDJSONStream(os.Stdout, func(yield func(types.IndexSettings) error) error {
			return esopsindex.GetSettingsStream(ctx, s.Client.IndexSettings, opts, func(r types.IndexSettings) error {
				count++
				return yield(r)
			})
		})
		if err != nil {
			return err
		}
		s.Log("index.settings.get", "rows", count, "stream", true)
		return nil
	}

	rows, err := esopsindex.GetSettings(ctx, s.Client.IndexSettings, opts)
	if err != nil {
		return err
	}

	s.Log("index.settings.get", "rows", len(rows))
	return output.IndexSettings(os.Stdout, rows, s.Format)
}

func settingsSetCommand() *cli.Command {
	return &cli.Command{
		Name:  "set",
		Usage: "Update index settings",
		Description: `Update settings via PUT /<indices>/_settings. The cluster's body
shape is {"index":{"<key>":<value>, ...}}; esops accepts three input
forms:

  --set key=value (repeatable)   Build the body from one or more
                                 dotted-key/value pairs. Bare keys
                                 (without "index." prefix) are
                                 auto-prefixed.
  --body JSON                    Pass a literal JSON object. Forwarded
                                 verbatim — the cluster validates the
                                 shape.
  --from-file PATH               Read the JSON object from a file
                                 (e.g. one a CI job rendered).

--set and --body/--from-file are mutually exclusive — pick one.

Mutates cluster state. The cluster has no native dry-run for
/_settings, so --dry-run is a client-side "validate + report" path
that echoes the body that would have been sent. Against a prod-
protected context, --i-know-this-is-prod is required.

The match-everything shortcuts ('*' and '_all') are refused outright;
name a pattern that matches the indices you intend to mutate.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "index",
				Required: true,
				Usage:    "Comma-separated index names or patterns (e.g. 'logs-2024-*'); '*' / '_all' refused",
			},
			&cli.StringSliceFlag{
				Name:  "set",
				Usage: "key=value pair(s) to set; repeatable (e.g. --set number_of_replicas=2)",
			},
			&cli.StringFlag{
				Name:  "body",
				Usage: "Raw JSON object (e.g. '{\"index\":{\"number_of_replicas\":\"2\"}}')",
			},
			&cli.StringFlag{
				Name:  "from-file",
				Usage: "Path to a file containing the JSON body",
			},
			&cli.BoolFlag{
				Name:  "preserve-existing",
				Usage: "Forward as ?preserve_existing=true (cluster keeps current values for any key the body would overwrite)",
			},
			&cli.BoolFlag{
				Name:  "ignore-unavailable",
				Usage: "Treat missing indices as a no-op instead of a 404 error",
			},
		},
		Action: runSettingsSet,
		ShellComplete: completion.ForFlags(map[string]completion.Suggester{
			"index": completion.SuggestIndices,
		}),
	}
}

func runSettingsSet(ctx context.Context, cmd *cli.Command) error {
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
	kv, err := parseKeyValuePairs(cmd.StringSlice("set"))
	if err != nil {
		return err
	}

	opts := esopsindex.SettingsUpdateOptions{
		Index:             cmd.String("index"),
		Body:              body,
		KeyValues:         kv,
		PreserveExisting:  cmd.Bool("preserve-existing"),
		IgnoreUnavailable: cmd.Bool("ignore-unavailable"),
		DryRun:            cmd.Bool("dry-run"),
	}
	result, err := esopsindex.UpdateSettings(ctx, s.Client.IndexSettingsSet, opts)
	if err != nil {
		return err
	}

	s.Log("index.settings.set",
		"index", opts.Index,
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.IndexSettingsUpdate(os.Stdout, result, s.Format)
}

// resolveBody reads the settings body from --body (literal) or
// --from-file (path). Both empty is fine — the business layer falls
// back to --set. Both set is refused at the CLI layer so the error
// arrives before the cluster is contacted.
func resolveBody(body, fromFile string) ([]byte, error) {
	if body != "" && fromFile != "" {
		return nil, client.UsageError("--body and --from-file are mutually exclusive")
	}
	if fromFile != "" {
		buf, err := os.ReadFile(fromFile) // #nosec G304 -- path is the operator-supplied --from-file flag
		if err != nil {
			return nil, fmt.Errorf("reading --from-file %q: %w", fromFile, err)
		}
		return buf, nil
	}
	if body != "" {
		return []byte(body), nil
	}
	return nil, nil
}

// parseKeyValuePairs splits each "k=v" string into a key/value entry.
// Empty input returns nil so callers can compare against an empty map
// without nil-checking. Values may contain '='; only the first one
// separates key from value. Duplicate keys are rejected outright —
// silent last-write-wins on the command line is a footgun.
func parseKeyValuePairs(in []string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for _, raw := range in {
		eq := -1
		for i, r := range raw {
			if r == '=' {
				eq = i
				break
			}
		}
		if eq <= 0 {
			return nil, client.UsageError("--set entries must be key=value (got %q)", raw)
		}
		key := raw[:eq]
		val := raw[eq+1:]
		if prev, dup := out[key]; dup {
			return nil, client.UsageError("--set %q given twice (values %q and %q); pass each key once", key, prev, val)
		}
		out[key] = val
	}
	return out, nil
}
