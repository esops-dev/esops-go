package snapshot

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/output"
	esopssnapshot "github.com/esops-dev/esops-go/internal/snapshot"
	"github.com/esops-dev/esops-go/pkg/client"
)

// snapshotPolicySuggesters drives tab-completion for `snapshot policy
// *` subcommands — --name maps to SLM/SM policy IDs.
var snapshotPolicySuggesters = map[string]completion.Suggester{
	"name": completion.SuggestSnapshotPolicies,
}

// policyCommand is the parent for `esops snapshot policy
// <list|get|put|delete|execute>`. Backed by SLM on Elasticsearch and
// the Snapshot Management plugin (SM) on OpenSearch — the CLI surface
// is identical, the wire underneath diverges (ES uses /_slm/policy,
// OS uses /_plugins/_sm/policies, and the body shapes differ).
//
// list and get are read-only; put, delete and execute mutate cluster
// state and trigger the protection-tier check. SM was introduced in
// OpenSearch 2.1; older OS clusters return a 404 from
// /_plugins/_sm/policies, which surfaces verbatim — esops doesn't
// fabricate a "this product doesn't support policies" diagnosis when
// the cluster is just running an older version.
func policyCommand() *cli.Command {
	return &cli.Command{
		Name:  "policy",
		Usage: "Manage scheduled snapshot policies (SLM on Elasticsearch, SM on OpenSearch)",
		Commands: []*cli.Command{
			policyListCommand(),
			policyGetCommand(),
			policyDescribeCommand(),
			policyPutCommand(),
			policyDeleteCommand(),
			policyExecuteCommand(),
		},
	}
}

func policyDescribeCommand() *cli.Command {
	return &cli.Command{
		Name:      "describe",
		Usage:     "Show a detailed summary of one snapshot policy (definition + recent runs)",
		ArgsUsage: "NAME",
		Description: `Aggregate the SLM/SM policy body (schedule, retention, enabled
flag, last/next execution timestamps) with a tail of the most
recent snapshots that landed in the policy's target repository so
an operator can answer "is this policy actually running and
producing snapshots".

The cluster reports no back-reference from a snapshot to the
policy that produced it, so the recent-snapshots tail is the
policy's *target repository* sorted most-recent-first — snapshots
written by another policy in the same repo will show up here.
Surfaced as "Recent snapshots in repository" to keep the
interpretation honest.

Read-only and safe against a prod-protected context.`,
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:        "recent-limit",
				Usage:       "Maximum number of recent snapshots to render (0 uses the default)",
				Value:       esopssnapshot.DefaultPolicyRecentLimit,
				DefaultText: fmt.Sprintf("%d", esopssnapshot.DefaultPolicyRecentLimit),
			},
		},
		Action:        runPolicyDescribe,
		ShellComplete: completion.ForPositional(1, completion.SuggestSnapshotPolicies),
	}
}

// recentLimitMax bounds the operator-supplied --recent-limit so a
// typo (e.g. `--recent-limit 100000`) can't pull every snapshot out
// of a long-running repository just to drop all but a tail. The cap
// is generous — a policy operator who legitimately wants more than
// 500 historical runs is probably looking for `snapshot list` instead.
const recentLimitMax = 500

// validateRecentLimit enforces the CLI-side bounds on --recent-limit
// before any cluster call. Negative is a typo; values above the cap
// would issue an outsized /_snapshot listing just to drop the tail.
// Pure function so the CLI-layer test can exercise the guards
// directly without spinning up urfave/cli.
func validateRecentLimit(v int) error {
	if v < 0 {
		return client.UsageError("--recent-limit must be >= 0 (got %d)", v)
	}
	if v > recentLimitMax {
		return client.UsageError("--recent-limit %d exceeds the cap of %d", v, recentLimitMax)
	}
	return nil
}

func runPolicyDescribe(ctx context.Context, cmd *cli.Command) error {
	args := cmd.Args()
	if args.Len() != 1 {
		return client.UsageError("describe takes exactly one positional argument (NAME); got %d", args.Len())
	}
	name := args.First()

	recentLimit := int(cmd.Int("recent-limit"))
	if err := validateRecentLimit(recentLimit); err != nil {
		return err
	}

	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsNoWide,
	})
	if err != nil {
		return err
	}

	desc, err := esopssnapshot.DescribePolicy(ctx, esopssnapshot.PolicyDescribeCapabilities{
		Policies:  s.Client.SnapshotPolicyRead,
		Snapshots: s.Client.Snapshot,
		Dialect:   string(s.Client.Info.Dialect),
	}, esopssnapshot.PolicyDescribeOptions{
		Name:        name,
		RecentLimit: recentLimit,
	})
	if err != nil {
		return err
	}

	s.Log("snapshot.policy.describe",
		"name", desc.Policy.Name,
		"repository", desc.Policy.Repository,
		"recent", len(desc.RecentSnapshots),
		"warnings", len(desc.SectionWarnings),
	)
	return output.SnapshotPolicyDescribe(os.Stdout, desc, s.Format)
}

func policyListCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List scheduled snapshot policies",
		Description: `List the cluster's snapshot policies. The default table shows
name, repository, schedule, version and last-success age; -o wide
adds the modified date and next-execution time (where the cluster
reports them).

Backs onto GET /_slm/policy on Elasticsearch and
GET /_plugins/_sm/policies on OpenSearch. Read-only and safe to run
against a prod-protected context at any time.

--name takes one or more policy names to narrow the response. ES
honours a comma-joined name list at the path segment; OS fans out
per-id under the hood — same flag, same behaviour at the CLI surface.`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{
				Name:  "name",
				Usage: "Limit to specific policy name(s); repeatable",
			},
		},
		Action:        runPolicyList,
		ShellComplete: completion.ForFlags(snapshotPolicySuggesters),
	}
}

func runPolicyList(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	rows, err := esopssnapshot.ListPolicies(ctx, s.Client.SnapshotPolicy, esopssnapshot.PolicyListOptions{
		Names: cmd.StringSlice("name"),
	})
	if err != nil {
		return err
	}

	s.Log("snapshot.policy.list", "rows", len(rows))
	return output.SnapshotPolicies(os.Stdout, rows, s.Format)
}

func policyGetCommand() *cli.Command {
	return &cli.Command{
		Name:  "get",
		Usage: "Get a single snapshot policy",
		Description: `Get one snapshot policy by name. --name must be a literal policy
name (no patterns); for pattern matching, use 'snapshot policy list'.

In table mode the default view is a one-line header; -o wide appends
the full policy body (schedule, repository, retention, etc.) as
indented JSON. JSON/YAML emit the neutral SnapshotPolicy shape
directly.

Read-only.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Policy name (literal)",
			},
		},
		Action:        runPolicyGet,
		ShellComplete: completion.ForFlags(snapshotPolicySuggesters),
	}
}

func runPolicyGet(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	p, err := esopssnapshot.GetPolicy(ctx, s.Client.SnapshotPolicy, cmd.String("name"))
	if err != nil {
		return err
	}

	s.Log("snapshot.policy.get", "name", p.Name)
	return output.SnapshotPolicy(os.Stdout, p, s.Format)
}

func policyPutCommand() *cli.Command {
	return &cli.Command{
		Name:  "put",
		Usage: "Create or update a snapshot policy",
		Description: `Create or update a snapshot policy. The body shape differs by
dialect (ES SLM expects schedule/name/repository/config/retention;
OS SM expects creation/deletion/snapshot_config) and esops forwards
the body verbatim — the cluster validates the inner shape, esops
only checks that the body is a JSON object so cluster-side errors
stay the source of truth.

Body input is exactly one of:
  --body JSON         Literal JSON object on the command line
  --from-file PATH    Read the JSON object from a file

Mutates cluster state. The cluster has no native dry-run for either
dialect's policy PUT, so --dry-run is a client-side "validate +
report" path. Against a prod-protected context, --i-know-this-is-prod
is required.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Policy name (literal, no patterns)",
			},
			&cli.StringFlag{
				Name:  "body",
				Usage: "Raw JSON object (policy definition)",
			},
			&cli.StringFlag{
				Name:  "from-file",
				Usage: "Path to a file containing the JSON policy body",
			},
		},
		Action:        runPolicyPut,
		ShellComplete: completion.ForFlags(snapshotPolicySuggesters),
	}
}

func runPolicyPut(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	body, err := resolvePolicyBody(cmd.String("body"), cmd.String("from-file"))
	if err != nil {
		return err
	}

	opts := esopssnapshot.PolicyPutOptions{
		Name:   cmd.String("name"),
		Body:   body,
		DryRun: cmd.Bool("dry-run"),
	}
	result, err := esopssnapshot.PutPolicy(ctx, s.Client.SnapshotPolicy, opts)
	if err != nil {
		return err
	}

	s.Log("snapshot.policy.put",
		"name", result.Name,
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.SnapshotPolicyPut(os.Stdout, result, s.Format)
}

func policyDeleteCommand() *cli.Command {
	return &cli.Command{
		Name:  "delete",
		Usage: "Delete a snapshot policy",
		Description: `Delete a snapshot policy by name. ES SLM removes the schedule
immediately; existing snapshots are unaffected. OS SM does the same
— deleting the policy does not touch snapshots already in the
repository.

Mutates cluster state. The cluster has no native dry-run for either
dialect's policy DELETE; --dry-run is a client-side "validate +
report" path. Against a prod-protected context, --i-know-this-is-prod
is required.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Policy name (literal, no patterns)",
			},
		},
		Action:        runPolicyDelete,
		ShellComplete: completion.ForFlags(snapshotPolicySuggesters),
	}
}

func runPolicyDelete(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopssnapshot.PolicyDeleteOptions{
		Name:   cmd.String("name"),
		DryRun: cmd.Bool("dry-run"),
	}
	result, err := esopssnapshot.DeletePolicy(ctx, s.Client.SnapshotPolicy, opts)
	if err != nil {
		return err
	}

	s.Log("snapshot.policy.delete",
		"name", result.Name,
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.SnapshotPolicyDelete(os.Stdout, result, s.Format)
}

func policyExecuteCommand() *cli.Command {
	return &cli.Command{
		Name:  "execute",
		Usage: "Run a snapshot policy now (cluster semantics differ by dialect)",
		Description: `Trigger a snapshot policy outside its normal schedule.

ES (SLM):     POST /_slm/policy/<name>/_execute synthesises a one-off
              snapshot from the policy's template and returns the
              synthesised snapshot name in the result.

OS (SM):      POST /_plugins/_sm/policies/<name>/_start toggles the
              policy's enabled flag on; the snapshot lands on the
              next scheduled cron tick. The result reports
              "acknowledged" but no snapshot name — confirm with
              'snapshot list --repository <repo>' shortly after.

Mutates cluster state. Against a prod-protected context,
--i-know-this-is-prod is required. --dry-run short-circuits without
contacting the cluster (neither dialect supports a server-side
dry-run for execute).`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Policy name (literal, no patterns)",
			},
		},
		Action:        runPolicyExecute,
		ShellComplete: completion.ForFlags(snapshotPolicySuggesters),
	}
}

func runPolicyExecute(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopssnapshot.PolicyExecuteOptions{
		Name:   cmd.String("name"),
		DryRun: cmd.Bool("dry-run"),
	}
	result, err := esopssnapshot.ExecutePolicy(ctx, s.Client.SnapshotPolicy, opts)
	if err != nil {
		return err
	}

	s.Log("snapshot.policy.execute",
		"name", result.Name,
		"dry_run", result.DryRun,
		"snapshot_name", result.SnapshotName,
	)
	return output.SnapshotPolicyExecute(os.Stdout, result, s.Format)
}

// resolvePolicyBody reads the policy body from --body (literal JSON)
// or --from-file (path). Mirrors index/settings.go's resolveBody but
// lives in this package so the snapshot CLI doesn't depend on the
// index command tree. Both empty is a usage error — the business
// layer's empty-body check is a defence-in-depth, this is the loud
// front door.
func resolvePolicyBody(body, fromFile string) ([]byte, error) {
	body = strings.TrimSpace(body)
	fromFile = strings.TrimSpace(fromFile)
	if body != "" && fromFile != "" {
		return nil, client.UsageError("--body and --from-file are mutually exclusive")
	}
	if body == "" && fromFile == "" {
		return nil, client.UsageError("policy body is required (use --body or --from-file)")
	}
	if fromFile != "" {
		buf, err := os.ReadFile(fromFile) // #nosec G304 -- path is the operator-supplied --from-file flag
		if err != nil {
			return nil, fmt.Errorf("reading --from-file %q: %w", fromFile, err)
		}
		return buf, nil
	}
	return []byte(body), nil
}
