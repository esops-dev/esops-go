package ops

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	esopsops "github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/client"
)

// settingsCommand is the parent for `esops ops settings <get|set|diff>`.
// get and diff are read-only; set mutates cluster state and triggers
// the protection-tier check.
func settingsCommand() *cli.Command {
	return &cli.Command{
		Name:  "settings",
		Usage: "Get, update, and diff cluster-level settings",
		Commands: []*cli.Command{
			settingsGetCommand(),
			settingsSetCommand(),
			settingsDiffCommand(),
		},
	}
}

func settingsGetCommand() *cli.Command {
	return &cli.Command{
		Name:  "get",
		Usage: "Get cluster-level settings (persistent + transient)",
		Description: `Read /_cluster/settings with flat_settings=true. Output shows the
persistent and transient scope side by side; --include-defaults adds
the cluster's built-in defaults so an audit can tell "configured" from
"inherited" without a second call.

--name takes one or more dotted prefixes (e.g.
"cluster.routing.allocation") to narrow the response client-side. The
cluster's own filter is nested-only, which makes prefix queries
awkward; the client-side cut keeps the CLI surface stable across
dialects and cluster versions.

Read-only; safe against any protection tier.`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{
				Name:  "name",
				Usage: "Limit to specific dotted-key prefix(es); repeatable (e.g. 'cluster.routing.allocation')",
			},
			&cli.BoolFlag{
				Name:  "include-defaults",
				Usage: "Include the cluster's built-in default values (server-side ?include_defaults=true)",
			},
		},
		Action: runSettingsGet,
	}
}

func runSettingsGet(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	view, err := esopsops.GetClusterSettings(ctx, s.Client.ClusterSettingsAll, esopsops.SettingsGetOptions{
		Names:           cmd.StringSlice("name"),
		IncludeDefaults: cmd.Bool("include-defaults"),
	})
	if err != nil {
		return err
	}

	s.Log("ops.settings.get",
		"persistent_keys", len(view.Persistent),
		"transient_keys", len(view.Transient),
		"include_defaults", view.IncludeDefaults,
	)
	return output.ClusterSettings(os.Stdout, view, s.Format)
}

func settingsSetCommand() *cli.Command {
	return &cli.Command{
		Name:  "set",
		Usage: "Update cluster-level settings (persistent or transient scope)",
		Description: `Update /_cluster/settings via PUT. Pick the scope with --persistent
or --transient (default: --transient — the safer choice for a temporary
override; a cluster restart drops the value, so a typo doesn't survive
an outage). Body shapes accepted:

  --set key=value (repeatable)   Build the body from one or more
                                 dotted-key/value pairs.
  --body JSON                    Pass a literal JSON object of
                                 key/value pairs (no surrounding
                                 "persistent"/"transient" wrapper —
                                 --scope picks scope).
  --from-file PATH               Read the JSON object from a file
                                 (e.g. one a CI job rendered).

--set and --body/--from-file are mutually exclusive — pick one.

Mutates cluster state. The cluster has no native dry-run for
/_cluster/settings, so --dry-run is a client-side "validate + report"
path that echoes the body that would have been sent. Against a prod-
protected context, --i-know-this-is-prod is required.`,
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "persistent",
				Usage: "Operate on the persistent scope (default: transient)",
			},
			&cli.StringSliceFlag{
				Name:  "set",
				Usage: "key=value pair(s) to set; repeatable (e.g. --set cluster.routing.allocation.enable=primaries)",
			},
			&cli.StringFlag{
				Name:  "body",
				Usage: "Raw JSON object of key/value pairs (no surrounding 'persistent'/'transient' wrapper)",
			},
			&cli.StringFlag{
				Name:  "from-file",
				Usage: "Path to a file containing the JSON body",
			},
		},
		Action: runSettingsSet,
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

	body, err := readSettingsBody(cmd.String("body"), cmd.String("from-file"))
	if err != nil {
		return err
	}
	kv, err := parseClusterKVPairs(cmd.StringSlice("set"))
	if err != nil {
		return err
	}

	scope := esopsops.ScopeTransient
	if cmd.Bool("persistent") {
		scope = esopsops.ScopePersistent
	}

	opts := esopsops.SettingsSetOptions{
		Scope:     scope,
		KeyValues: kv,
		Body:      body,
		DryRun:    cmd.Bool("dry-run"),
	}
	result, err := esopsops.UpdateClusterSettings(ctx, s.Client.ClusterSettingsPut, opts)
	if err != nil {
		return err
	}
	s.Log("ops.settings.set",
		"scope", string(scope),
		"dry_run", opts.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.ClusterSettingsPut(os.Stdout, result, string(scope), opts.DryRun, s.Format)
}

func settingsDiffCommand() *cli.Command {
	return &cli.Command{
		Name:  "diff",
		Usage: "Diff persistent vs transient cluster settings",
		Description: `Read /_cluster/settings and report keys whose persistent and
transient values disagree (including keys set in only one scope).

A key set the same in both scopes is omitted on purpose — the diff
is for finding drift, and "set the same in both scopes" is not
drift. Defaults are not considered.

Read-only; safe against any protection tier.`,
		Action: runSettingsDiff,
	}
}

func runSettingsDiff(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}
	diff, err := esopsops.DiffClusterSettings(ctx, s.Client.ClusterSettingsAll)
	if err != nil {
		return err
	}
	s.Log("ops.settings.diff", "drifted_keys", len(diff.Rows))
	return output.ClusterSettingsDiff(os.Stdout, diff, s.Format)
}

// readSettingsBody resolves --body / --from-file. Mirrors the helper
// in `esops index settings set` — duplicated rather than promoted to
// a shared package because the two commands share a flag *shape* but
// not a destination, and a shared helper is one rename away from
// dragging the wrong validation context across the boundary.
func readSettingsBody(body, fromFile string) ([]byte, error) {
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

// parseClusterKVPairs splits each "k=v" into a key/value entry. Bare
// keys (without a dotted prefix) are not auto-prefixed — the cluster-
// settings space has multiple top-level prefixes (cluster., action.,
// indices., logger.) and there is no single sensible default to add.
func parseClusterKVPairs(in []string) (map[string]string, error) {
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
