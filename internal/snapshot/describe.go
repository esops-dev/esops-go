package snapshot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// DescribeOptions is the business-layer input to Describe. Spec is
// the operator-supplied positional argument in `repo/snap` form;
// patterns are refused — describe is the single-snapshot verb.
type DescribeOptions struct {
	Spec string
}

// DescribeCapabilities is the read-only capability bundle the
// snapshot describe aggregator consumes. SnapshotInspector covers
// both the listing call (for metadata) and the status call (for
// per-shard progress).
type DescribeCapabilities struct {
	Snapshot client.SnapshotInspector
	Dialect  string
}

// Describe is the business-layer entry point for
// `esops snapshot describe REPO/NAME`. Parses the positional
// argument into (repository, name), fetches the snapshot record,
// then fetches the live /_status block and folds both into a
// neutral SnapshotDescription.
//
// The two calls are sequential — /_status needs the resolved name,
// and the status call is the only follow-on so there's no parallel
// fan-out to set up. Status failures land in SectionWarnings
// (except a 404, which is silently swallowed: a snapshot that was
// just pruned out from under the listing returns a 404 on /_status
// and the metadata view we already have is the right answer).
//
// Refusals:
//   - empty / malformed Spec (ErrUsage)
//   - patternish repository or snapshot name (ErrUsage)
//   - the resolved (repo, name) pair returns no snapshot (ErrNotFound)
func Describe(ctx context.Context, caps DescribeCapabilities, opts DescribeOptions) (types.SnapshotDescription, error) {
	repo, name, err := parseSnapshotSpec(opts.Spec)
	if err != nil {
		return types.SnapshotDescription{}, err
	}

	rows, err := caps.Snapshot.Snapshots(ctx, types.SnapshotFilter{
		Repository: repo,
		Snapshot:   name,
	})
	if err != nil {
		return types.SnapshotDescription{}, err
	}
	info, ok := pickSnapshot(rows, repo, name)
	if !ok {
		return types.SnapshotDescription{}, fmt.Errorf("%w: snapshot %q in repository %q", client.ErrNotFound, name, repo)
	}

	desc := types.SnapshotDescription{
		Repository: info.Repository,
		Name:       info.Name,
		Dialect:    caps.Dialect,
		Info:       info,
	}

	status, err := caps.Snapshot.SnapshotStatus(ctx, types.SnapshotStatusRequest{
		Repository: info.Repository,
		Snapshot:   info.Name,
	})
	switch {
	case errors.Is(err, client.ErrNotFound):
		// The snapshot was pruned between the listing and the status
		// fetch — the metadata above is still the right answer, so
		// silently skip the status block.
	case err != nil:
		desc.SectionWarnings = map[string]string{"status": err.Error()}
	default:
		desc.Status = &status
	}

	return desc, nil
}

// parseSnapshotSpec splits the operator's `repo/snap` positional into
// (repository, name). Refuses patterns on either side, refuses an
// empty / single-segment spec, and trims whitespace so a trailing
// shell continuation doesn't leak into the cluster URL.
func parseSnapshotSpec(spec string) (string, string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", client.UsageError("REPO/NAME is required")
	}
	idx := strings.IndexByte(spec, '/')
	if idx <= 0 || idx == len(spec)-1 {
		return "", "", client.UsageError("REPO/NAME must be a non-empty repository and snapshot name separated by `/` (got %q)", spec)
	}
	repo := strings.TrimSpace(spec[:idx])
	name := strings.TrimSpace(spec[idx+1:])
	if repo == "" || name == "" {
		return "", "", client.UsageError("REPO/NAME must be a non-empty repository and snapshot name separated by `/` (got %q)", spec)
	}
	if client.IsPatternish(repo) {
		return "", "", client.UsageError("repository must be a literal name, not a pattern (got %q)", repo)
	}
	if client.IsPatternish(name) {
		return "", "", client.UsageError("snapshot name must be a literal, not a pattern (got %q)", name)
	}
	return repo, name, nil
}

// pickSnapshot finds the exact (repo, name) pair in a /_snapshot
// listing. The cluster returns one row when both filters are
// literal; an empty match means the snapshot does not exist.
func pickSnapshot(rows []types.Snapshot, repo, name string) (types.Snapshot, bool) {
	for _, r := range rows {
		if r.Repository == repo && r.Name == name {
			return r, true
		}
	}
	return types.Snapshot{}, false
}
