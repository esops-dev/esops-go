package snapshot

import (
	"context"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/dj-wasabi/esops-go/internal/cli/cmdsetup"
	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/output"
	esopssnapshot "github.com/dj-wasabi/esops-go/internal/snapshot"
)

func registerCommand() *cli.Command {
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
		Action: runRegister,
	}
}

func runRegister(ctx context.Context, cmd *cli.Command) error {
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
	s.Log("snapshot.register",
		"repository", result.Repository,
		"type", result.Type,
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.SnapshotRegister(os.Stdout, result, s.Format)
}

func deregisterCommand() *cli.Command {
	return &cli.Command{
		Name:  "deregister",
		Usage: "Remove a snapshot repository registration",
		Description: `Deregister a snapshot repository via DELETE /_snapshot/<repo>.

Removes the registration only — does not touch the snapshots stored in
the backing store. The cluster refuses to deregister a repository with
in-progress snapshots; that 4xx is surfaced verbatim.

Mutates cluster state. Use --dry-run first; against a prod-protected
context, --i-know-this-is-prod is required.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "repository",
				Required: true,
				Usage:    "Repository name to deregister (literal, no patterns)",
			},
		},
		Action: runDeregister,
	}
}

func runDeregister(ctx context.Context, cmd *cli.Command) error {
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
	s.Log("snapshot.deregister",
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

