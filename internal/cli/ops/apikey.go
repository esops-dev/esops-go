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

// apikeyCommand is the parent for `esops ops apikey <list|get|create|invalidate>`.
// list and get are read-only; create and invalidate mutate cluster
// state and trigger the protection-tier check.
//
// API keys are an Elasticsearch-only feature; on an OpenSearch context
// every subcommand surfaces ErrUnsupported with exit code 6 (the
// capability stub installed by the factory).
func apikeyCommand() *cli.Command {
	return &cli.Command{
		Name:  "apikey",
		Usage: "Manage Elasticsearch API keys (list, get, create, invalidate)",
		Description: `Lifecycle commands for the cluster's native API keys. The key value
returned by 'create' is visible exactly once at creation time —
'create' prints the secret to stdout only; metadata (id, name, owner,
expiration) lands on stderr and in the structured log. Never to
--log-file, never to the audit log.

Elasticsearch-only. Against an OpenSearch context every subcommand
exits with code 6 (operation unsupported on this cluster dialect).`,
		Commands: []*cli.Command{
			apikeyListCommand(),
			apikeyGetCommand(),
			apikeyCreateCommand(),
			apikeyInvalidateCommand(),
		},
	}
}

func apikeyListCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List active API keys (id, name, owner, created, expiration, status)",
		Description: `Reads /_security/api_key?owner=false. Output is sorted by creation
time descending, then id. Read-only; safe against any protection tier.`,
		Action: runAPIKeyList,
	}
}

func runAPIKeyList(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}
	keys, err := esopsops.ListAPIKeys(ctx, s.Client.APIKeys)
	if err != nil {
		return err
	}
	s.Log("ops.apikey.list", "keys", len(keys))
	return output.APIKeyList(os.Stdout, keys, s.Format)
}

func apikeyGetCommand() *cli.Command {
	return &cli.Command{
		Name:      "get",
		Usage:     "Show metadata for a single API key by id (never returns the secret)",
		ArgsUsage: "ID",
		Description: `Looks up one key via /_security/api_key?id=ID. Returns metadata only;
the cluster never re-emits the raw key after creation. ErrNotFound
maps to exit code 1 with a precise "no such key" message. Read-only.`,
		Action: runAPIKeyGet,
	}
}

func runAPIKeyGet(ctx context.Context, cmd *cli.Command) error {
	if cmd.NArg() != 1 {
		return client.UsageError("get takes exactly one api key id")
	}
	id := cmd.Args().First()
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}
	key, err := esopsops.GetAPIKey(ctx, s.Client.APIKeys, id)
	if err != nil {
		return err
	}
	s.Log("ops.apikey.get", "id", key.ID, "name", key.Name)
	return output.APIKeyDescribe(os.Stdout, key, s.Format)
}

func apikeyCreateCommand() *cli.Command {
	return &cli.Command{
		Name:  "create",
		Usage: "Create a new API key (prints the secret to stdout exactly once)",
		Description: `Calls POST /_security/api_key. The cluster returns the raw key value
exactly once; esops prints it to stdout only. Stderr (and the
structured log) get a non-secret summary — id, name, owner, expiration.

By default, when stdout is a TTY, the command refuses with exit code 2
to keep the secret out of the operator's scrollback. Pass
--allow-tty-output to override, or redirect stdout to a file. The
recommended invocation is:

  esops ops apikey create --name svc-foo --output json > key.json

--role-descriptors FILE is Elasticsearch-only and forwards the raw JSON
object verbatim under role_descriptors. --expiration accepts the same
duration string the cluster does ("1d", "30m", …); omit for a key
that never expires.

--dry-run echoes the resolved spec without contacting the cluster (no
key is created, no secret is issued). The TTY guard still applies so
operators don't habituate to seeing key-shaped output in their
terminal.

Mutating. Against a prod-protected context, --i-know-this-is-prod is
required.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Name of the new API key (cluster-visible label, not a secret)",
			},
			&cli.StringFlag{
				Name:  "expiration",
				Usage: "Duration after which the key expires (e.g. '1d', '30m'); empty for no expiry",
			},
			&cli.StringFlag{
				Name:  "role-descriptors",
				Usage: "Path to a JSON file containing the role_descriptors object (Elasticsearch-only)",
			},
			&cli.BoolFlag{
				Name:  "allow-tty-output",
				Usage: "Permit printing the secret when stdout is a terminal (default refuses; redirect to a file instead)",
			},
		},
		Action: runAPIKeyCreate,
	}
}

func runAPIKeyCreate(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsNoWide,
	})
	if err != nil {
		return err
	}
	if cmdsetup.IsTTY(os.Stdout) && !cmd.Bool("allow-tty-output") {
		fmt.Fprintln(os.Stderr,
			"refusing to print the api key to a terminal: the secret would land in scrollback. "+
				"Redirect stdout to a file (e.g. '--output json > key.json') or pass --allow-tty-output to override.")
		return client.UsageError("stdout is a terminal; pass --allow-tty-output or redirect stdout")
	}
	created, err := esopsops.CreateAPIKey(ctx, s.Client.APIKeysManage, s.Client.APIKeys, esopsops.CreateAPIKeyOptions{
		Name:                cmd.String("name"),
		Expiration:          cmd.String("expiration"),
		RoleDescriptorsPath: cmd.String("role-descriptors"),
		DryRun:              cmd.Bool("dry-run"),
	})
	if err != nil {
		return err
	}

	summary := esopsops.SummariseCreated(created)
	// Stderr gets the non-secret summary — operators reviewing scrollback
	// or capturing 2>&1 see what was created without the secret tagging
	// along. The structured log carries the same fields; redaction
	// covers the api_key field by name, but we still keep it out of the
	// attrs entirely.
	verb := "created"
	if summary.DryRun {
		verb = "(dry-run) would create"
	}
	fmt.Fprintf(os.Stderr, "%s api key id=%s name=%s owner=%s expiration=%s\n",
		verb,
		dashOr(summary.ID),
		summary.Name,
		dashOr(summary.Owner),
		dashOr(summary.Expiration),
	)
	s.Log("ops.apikey.create",
		"id", summary.ID,
		"name", summary.Name,
		"owner", summary.Owner,
		"expiration", summary.Expiration,
		"dry_run", summary.DryRun,
	)
	return output.APIKeyCreated(os.Stdout, created, s.Format)
}

// dashOr is the per-package "empty → dash" helper. Output already has
// its own; we don't import it here because depending on a renderer
// package for a 4-line helper would invert the layering.
func dashOr(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func apikeyInvalidateCommand() *cli.Command {
	return &cli.Command{
		Name:      "invalidate",
		Usage:     "Revoke one or more API keys by id",
		ArgsUsage: "ID [ID...]",
		Description: `Calls DELETE /_security/api_key with the supplied ids. The cluster
echoes which ids were invalidated, which were already invalidated, and
any per-id failures.

Refuses more than 10 ids by default — pass --max-keys N to override.
--dry-run resolves and enforces the blast-radius cap, then prints the
ids that would be revoked without contacting the cluster.

Mutating; against a prod-protected context, --i-know-this-is-prod is
required.`,
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:  "max-keys",
				Usage: "Override the default 10-key blast-radius cap on the supplied id list",
			},
		},
		Action: runAPIKeyInvalidate,
	}
}

func runAPIKeyInvalidate(ctx context.Context, cmd *cli.Command) error {
	if cmd.NArg() < 1 {
		return client.UsageError("invalidate takes at least one api key id")
	}
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}
	result, err := esopsops.InvalidateAPIKey(ctx, s.Client.APIKeysManage, esopsops.InvalidateAPIKeyOptions{
		IDs:     cmd.Args().Slice(),
		MaxKeys: cmd.Int("max-keys"),
		DryRun:  cmd.Bool("dry-run"),
	})
	if err != nil {
		return err
	}
	// Audit-grade log: record the resolved id list (post-trim, post-
	// dedup), not just counts — operators reading the trail want to
	// answer "which keys did this invocation revoke" from grep, not
	// by cross-referencing the cluster's own audit log.
	s.Log("ops.apikey.invalidate",
		"requested", result.Requested,
		"invalidated", result.Invalidated,
		"previously_invalidated", result.PreviouslyInvalidated,
		"errors", result.ErrorCount,
		"dry_run", result.DryRun,
	)
	return output.APIKeyInvalidated(os.Stdout, result, s.Format)
}


