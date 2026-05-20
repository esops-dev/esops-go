package snapshot

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// RepositoryDescribeOptions is the business-layer input to
// DescribeRepository. Name is the operator-supplied repo name;
// patterns are refused. Verify toggles the cluster-side connectivity
// probe (POST /_snapshot/<repo>/_verify) — operators sometimes want
// describe to be strictly read-only against repos whose backing
// store is intermittent, so the probe is gated.
//
// ShowSecrets, when false (the default), redacts known-credential
// keys in the rendered Settings map via RedactRepositorySettings so
// captured describe output is safe to paste into tickets. The
// operator passes --show-secrets to see raw values.
type RepositoryDescribeOptions struct {
	Name        string
	Verify      bool
	ShowSecrets bool
}

// RepositoryDescribeCapabilities is the read-only capability bundle
// the aggregator consumes. The verifier and policy reader are
// nil-tolerant — tests can skip them, and dialect-only oddities
// (OS without SM plugin) surface as SectionWarnings rather than
// hard failures.
type RepositoryDescribeCapabilities struct {
	Snapshots client.SnapshotInspector
	Verifier  client.SnapshotVerifier
	Policies  client.SnapshotPolicyInspector
	Dialect   string
}

// DescribeRepository is the business-layer entry point for
// `esops snapshot repository describe`. Fetches the repository
// config, optionally runs the cluster-side verify probe, sums the
// snapshot count/state breakdown by listing the repo, and finds
// SLM/SM policies that target the repository.
//
// Refusals:
//   - empty / patternish Name (ErrUsage)
//   - the resolved name doesn't match any registered repository
//     (ErrNotFound)
//
// Verify is gated behind opts.Verify because POST /_snapshot/<repo>/
// _verify can take seconds on a slow backing store and operators
// sometimes describe a known-flaky repo without wanting the probe to
// run. Policies / snapshot-count failures are non-fatal warnings.
func DescribeRepository(ctx context.Context, caps RepositoryDescribeCapabilities, opts RepositoryDescribeOptions) (types.SnapshotRepositoryDescription, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return types.SnapshotRepositoryDescription{}, client.UsageError("NAME is required (a single literal repository name)")
	}
	if client.IsPatternish(name) {
		return types.SnapshotRepositoryDescription{}, client.UsageError("NAME must be a literal repository name, not a pattern (got %q)", name)
	}

	repos, err := caps.Snapshots.Repositories(ctx, types.SnapshotRepositoryFilter{Repository: name})
	if err != nil {
		return types.SnapshotRepositoryDescription{}, err
	}
	target, ok := pickRepository(repos, name)
	if !ok {
		return types.SnapshotRepositoryDescription{}, fmt.Errorf("%w: repository %q", client.ErrNotFound, name)
	}

	if !opts.ShowSecrets {
		// Redact known-credential keys before the describe shape
		// crosses any output boundary. Same allowlist `snapshot
		// repository list` uses, so the two commands agree.
		redacted := RedactRepositorySettings([]types.SnapshotRepository{target})
		target = redacted[0]
	}

	desc := types.SnapshotRepositoryDescription{
		Name:            target.Name,
		Dialect:         caps.Dialect,
		Info:            target,
		SectionWarnings: map[string]string{},
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	warn := func(section string, err error) {
		mu.Lock()
		desc.SectionWarnings[section] = err.Error()
		mu.Unlock()
	}
	run := func(section string, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil {
				warn(section, err)
			}
		}()
	}

	// Snapshot summary — counts and per-state breakdown. Slim
	// envelope: we don't need indices or per-shard counters here,
	// just (Name, State) per row.
	run("summary", func() error {
		rows, err := caps.Snapshots.Snapshots(ctx, types.SnapshotFilter{
			Repository: target.Name,
			Slim:       true,
		})
		if err != nil {
			return err
		}
		desc.Summary = summariseRepoSnapshots(rows)
		return nil
	})

	if opts.Verify && caps.Verifier != nil {
		run("verify", func() error {
			v, err := caps.Verifier.VerifyRepository(ctx, types.SnapshotVerifyRequest{Repository: target.Name})
			if err != nil {
				return err
			}
			desc.Verify = &v
			return nil
		})
	}

	if caps.Policies != nil {
		run("policies", func() error {
			policies, err := caps.Policies.ListSnapshotPolicies(ctx, nil)
			if err != nil {
				return err
			}
			matched := make([]types.SnapshotPolicy, 0)
			for _, p := range policies {
				if p.Repository == target.Name {
					matched = append(matched, p)
				}
			}
			sort.SliceStable(matched, func(i, j int) bool { return matched[i].Name < matched[j].Name })
			if len(matched) > 0 {
				desc.Policies = matched
			}
			return nil
		})
	}

	wg.Wait()
	if len(desc.SectionWarnings) == 0 {
		desc.SectionWarnings = nil
	}
	return desc, nil
}

// pickRepository finds the exact-name row in a /_snapshot response.
// The cluster echoes literal names verbatim — defensive matching is
// only needed when an operator's pattern unexpectedly hits the
// fallback path of the adapter.
func pickRepository(rows []types.SnapshotRepository, name string) (types.SnapshotRepository, bool) {
	for _, r := range rows {
		if r.Name == name {
			return r, true
		}
	}
	return types.SnapshotRepository{}, false
}

// summariseRepoSnapshots produces the per-state breakdown the
// describe header surfaces. State strings are uppercase on the wire
// ("SUCCESS", "FAILED", "IN_PROGRESS", "PARTIAL", "INCOMPATIBLE",
// "ABORTED", "MISSING"). PARTIAL counts as a failure for the
// operator's "did anything go wrong here" header summary. ABORTED
// and MISSING land in OtherCount so the four explicit buckets plus
// OtherCount always sum to SnapshotCount.
func summariseRepoSnapshots(rows []types.Snapshot) types.SnapshotRepositorySummary {
	out := types.SnapshotRepositorySummary{SnapshotCount: len(rows)}
	for _, r := range rows {
		switch strings.ToUpper(r.State) {
		case "SUCCESS":
			out.SuccessCount++
		case "IN_PROGRESS", "STARTED":
			out.InProgressCount++
		case "FAILED", "PARTIAL", "INCOMPATIBLE":
			out.FailedCount++
		default:
			out.OtherCount++
		}
	}
	return out
}
