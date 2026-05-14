package snapshot

import (
	"context"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/output"
	esopssnapshot "github.com/esops-dev/esops-go/internal/snapshot"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// repositoryCommand is the parent for `esops snapshot repository
// <list|register|remove>`. All repository management lives here so
// the top-level `esops snapshot` tree stays focused on *snapshots*
// and repository churn doesn't crowd it.
//
// `register` and `remove` mutate cluster state (PUT/DELETE
// /_snapshot/<repo>); `list` is read-only.
func repositoryCommand() *cli.Command {
	return &cli.Command{
		Name:  "repository",
		Usage: "Register, remove, or list snapshot repositories",
		Commands: []*cli.Command{
			repositoryListCommand(),
			repositoryRegisterCommand(),
			repositoryRemoveCommand(),
		},
	}
}

// repositoryListCommand is `esops snapshot repository list`. Mirrors
// the no-flag form of `snapshot list` — the latter dispatches to
// repository listing by historical accident, this is the spec-aligned
// home of the verb. By default redacts known-secret keys
// (access_key, secret_key, etc.) from the settings cell — a captured
// table from a CI run is safe to paste into a ticket without leaking
// repository credentials.
func repositoryListCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List registered snapshot repositories",
		Description: `List the cluster's registered snapshot repositories via
GET /_snapshot. The default table shows name and type; -o wide adds a
flat key=value preview of each repository's settings.

By default redacts known-secret keys (access_key, secret_key,
client_secret, …) from the settings cell. Pass --show-secrets to
print the raw values; useful for reproducing a config and dangerous
to feed into shell history or shared captures.

Read-only and safe to run against a prod-protected context at any
time.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "repository",
				Usage: "Limit to specific repository name(s), comma-separated",
			},
			&cli.BoolFlag{
				Name:  "show-secrets",
				Usage: "Print repository settings verbatim including credential keys (default: redacted)",
			},
		},
		Action: runRepositoryList,
		ShellComplete: completion.ForFlags(map[string]completion.Suggester{
			"repository": completion.SuggestRepositories,
		}),
	}
}

func runRepositoryList(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	rows, err := esopssnapshot.ListRepositories(ctx, s.Client.Snapshot, types.SnapshotRepositoryFilter{
		Repository: cmd.String("repository"),
	})
	if err != nil {
		return err
	}
	if !cmd.Bool("show-secrets") {
		rows = esopssnapshot.RedactRepositorySettings(rows)
	}
	s.Log("snapshot.repository.list", "repositories", len(rows))
	return output.SnapshotRepositories(os.Stdout, rows, s.Format)
}

func repositoryRegisterCommand() *cli.Command {
	return &cli.Command{
		Name:  "register",
		Usage: "Register a snapshot repository",
		Description: `Register a snapshot repository via PUT /_snapshot/<repo>.

--type names the cluster's repository plugin (e.g. fs, s3, azure, gcs).
--setting forwards a key=value pair into the body's settings block; use
it once per setting (e.g. --setting bucket=my-backups --setting region=eu-west-1).
--settings-file accepts a YAML or JSON object whose keys merge under
settings — convenient for repositories with many keys or nested structures.

By default the cluster verifies the repository after registration; pass
--no-verify to skip the post-registration node connectivity check.

Mutates cluster state. Use --dry-run first; against a prod-protected
context, --i-know-this-is-prod is required.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "repository",
				Required: true,
				Usage:    "Repository name to register (literal, no patterns)",
			},
			&cli.StringFlag{
				Name:     "type",
				Required: true,
				Usage:    "Repository plugin: fs | s3 | azure | gcs | hdfs | (custom plugin name)",
			},
			&cli.StringSliceFlag{
				Name:  "setting",
				Usage: "Repeatable KEY=VALUE setting forwarded to the plugin (e.g. --setting bucket=my-backups)",
			},
			&cli.StringFlag{
				Name:  "settings-file",
				Usage: "YAML/JSON file whose keys merge under the body's settings block",
			},
			&cli.BoolFlag{
				Name:  "no-verify",
				Usage: "Skip the cluster's post-register connectivity check (verify=false)",
			},
		},
		Action: runRepositoryRegister,
		// --repository is the new-repo name on register, so completion
		// would only confuse the operator; leave it un-suggested.
	}
}

func runRepositoryRegister(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	settings, err := parseKeyValueSettings(cmd.StringSlice("setting"))
	if err != nil {
		return err
	}
	if path := strings.TrimSpace(cmd.String("settings-file")); path != "" {
		fileSettings, err := loadKVMapFile(path)
		if err != nil {
			return client.UsageError("--settings-file: %v", err)
		}
		for k, v := range fileSettings {
			if _, exists := settings[k]; exists {
				return client.UsageError("setting %q is set both via --setting and --settings-file (refusing to silently pick one)", k)
			}
			settings[k] = v
		}
	}
	if len(settings) == 0 {
		settings = nil
	}

	opts := esopssnapshot.RegisterOptions{
		Repository: cmd.String("repository"),
		Type:       cmd.String("type"),
		Settings:   settings,
		DryRun:     cmd.Bool("dry-run"),
	}
	if cmd.Bool("no-verify") {
		v := false
		opts.Verify = &v
	}

	result, err := esopssnapshot.Register(ctx, s.Client.SnapshotRepoMgr, opts)
	if err != nil {
		return err
	}
	s.Log("snapshot.repository.register",
		"repository", result.Repository,
		"type", result.Type,
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.SnapshotRegister(os.Stdout, result, s.Format)
}

func repositoryRemoveCommand() *cli.Command {
	return &cli.Command{
		Name:  "remove",
		Usage: "Remove a snapshot repository registration",
		Description: `Remove a snapshot repository via DELETE /_snapshot/<repo>.

Removes the registration only — does not touch the snapshots stored in
the backing store. The cluster refuses to remove a repository with
in-progress snapshots; that 4xx is surfaced verbatim.

Mutates cluster state. Use --dry-run first; against a prod-protected
context, --i-know-this-is-prod is required.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "repository",
				Required: true,
				Usage:    "Repository name to remove (literal, no patterns)",
			},
		},
		Action: runRepositoryRemove,
		ShellComplete: completion.ForFlags(map[string]completion.Suggester{
			"repository": completion.SuggestRepositories,
		}),
	}
}

func runRepositoryRemove(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopssnapshot.DeregisterOptions{
		Repository: cmd.String("repository"),
		DryRun:     cmd.Bool("dry-run"),
	}
	result, err := esopssnapshot.Deregister(ctx, s.Client.SnapshotRepoMgr, opts)
	if err != nil {
		return err
	}
	s.Log("snapshot.repository.remove",
		"repository", result.Repository,
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.SnapshotDeregister(os.Stdout, result, s.Format)
}

// parseKeyValueSettings converts the --setting KEY=VALUE repeatable flag
// into the map the cluster expects under the body's `settings` block.
// Values stay strings — the cluster coerces ints/bools as needed, and a
// "real" structured value belongs in --settings-file.
func parseKeyValueSettings(values []string) (map[string]any, error) {
	out := make(map[string]any, len(values))
	for _, raw := range values {
		k, v, ok := strings.Cut(raw, "=")
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if !ok || k == "" {
			return nil, client.UsageError("--setting expects KEY=VALUE (got %q)", raw)
		}
		if _, exists := out[k]; exists {
			return nil, client.UsageError("--setting key %q specified more than once", k)
		}
		out[k] = v
	}
	return out, nil
}
