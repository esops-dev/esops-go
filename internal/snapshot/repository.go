package snapshot

import (
	"context"
	"strings"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// RegisterOptions are the knobs the CLI layer hands to Register. Repository
// is required and must be a literal name. Type is the cluster's repository
// plugin name (`fs`, `s3`, `azure`, `gcs`, …) — required and validated up-
// front so a typo doesn't reach the cluster as an empty `type` field. Settings
// is the type-specific configuration the cluster forwards to the plugin.
//
// Verify maps to ?verify on the create endpoint; nil leaves the cluster
// default (verify after register). Setting it to false is the "register a
// repo whose backing store isn't online yet" path.
//
// DryRun does not contact the cluster; the result reports what would have
// been sent. The cluster has no native dry-run for repository registration.
type RegisterOptions struct {
	Repository string
	Type       string
	Settings   map[string]any
	Verify     *bool
	DryRun     bool
}

// Register is the business-layer entry point for `esops snapshot register`.
// Validation:
//
//  1. Repository is required and must be a literal name.
//  2. Type is required — the cluster's terse 400 on missing type is one of
//     the few cases where a client-side reject is strictly more useful.
//  3. Settings keys are not pre-validated; each repository plugin owns its
//     own settings vocabulary and we forward the operator's map verbatim.
func Register(ctx context.Context, c client.SnapshotRepositoryManager, opts RegisterOptions) (types.SnapshotRepositoryRegisterResult, error) {
	repo := strings.TrimSpace(opts.Repository)
	typ := strings.TrimSpace(opts.Type)
	if repo == "" {
		return types.SnapshotRepositoryRegisterResult{}, client.UsageError("--repository is required")
	}
	if client.IsPatternish(repo) {
		return types.SnapshotRepositoryRegisterResult{}, client.UsageError("--repository must be a literal name, not a pattern (got %q)", repo)
	}
	if typ == "" {
		return types.SnapshotRepositoryRegisterResult{}, client.UsageError("--type is required (e.g. fs, s3, azure, gcs)")
	}

	if opts.DryRun {
		return types.SnapshotRepositoryRegisterResult{
			Action:       "register",
			DryRun:       true,
			Repository:   repo,
			Type:         typ,
			Acknowledged: true,
		}, nil
	}

	return c.RegisterRepository(ctx, types.SnapshotRepositoryRegisterRequest{
		Repository: repo,
		Type:       typ,
		Settings:   opts.Settings,
		Verify:     opts.Verify,
	})
}

// DeregisterOptions are the knobs the CLI layer hands to Deregister.
// Repository is required and must be a literal name; the cluster refuses
// to deregister a repository with in-progress snapshots, surfaced verbatim.
type DeregisterOptions struct {
	Repository string
	DryRun     bool
}

// Deregister is the business-layer entry point for
// `esops snapshot deregister`. Mirrors Register — validation, optional
// dry-run, then the capability call.
func Deregister(ctx context.Context, c client.SnapshotRepositoryManager, opts DeregisterOptions) (types.SnapshotRepositoryDeregisterResult, error) {
	repo := strings.TrimSpace(opts.Repository)
	if repo == "" {
		return types.SnapshotRepositoryDeregisterResult{}, client.UsageError("--repository is required")
	}
	if client.IsPatternish(repo) {
		return types.SnapshotRepositoryDeregisterResult{}, client.UsageError("--repository must be a literal name, not a pattern (got %q)", repo)
	}

	if opts.DryRun {
		return types.SnapshotRepositoryDeregisterResult{
			Action:       "deregister",
			DryRun:       true,
			Repository:   repo,
			Acknowledged: true,
		}, nil
	}

	return c.DeregisterRepository(ctx, types.SnapshotRepositoryDeregisterRequest{
		Repository: repo,
	})
}
