package snapshot

import (
	"context"
	"strings"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// VerifyOptions are the knobs the CLI layer hands to Verify. Repository
// is required and must be a literal repo name — verify is single-repo
// by design (operators typically run it just before a manual snapshot).
type VerifyOptions struct {
	Repository string
}

// Verify is the business-layer entry point for `esops snapshot verify`.
// The cluster's verify endpoint does the real work — talking to the
// repository's backing store from every node — and we surface the
// list of nodes that successfully connected. Read-only from the
// operator's perspective; safe to run against a prod-protected context.
//
// An empty VerifiedNodes slice with no error means the cluster accepted
// the request but no node responded — rare and worth surfacing as-is so
// the operator notices, rather than fabricating a synthetic error.
func Verify(ctx context.Context, c client.SnapshotVerifier, opts VerifyOptions) (types.SnapshotVerifyResult, error) {
	repo := strings.TrimSpace(opts.Repository)
	if repo == "" {
		return types.SnapshotVerifyResult{}, client.UsageError("--repository is required")
	}
	if isSnapshotPatternish(repo) {
		return types.SnapshotVerifyResult{}, client.UsageError("--repository must be a literal name, not a pattern (got %q)", repo)
	}
	return c.VerifyRepository(ctx, types.SnapshotVerifyRequest{Repository: repo})
}
