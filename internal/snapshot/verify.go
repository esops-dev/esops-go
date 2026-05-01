package snapshot

import (
	"context"
	"strings"
	"time"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// VerifyOptions are the knobs the CLI layer hands to Verify. Repository
// is required and must be a literal repo name — verify is single-repo
// by design (operators typically run it just before a manual snapshot).
//
// Deep enables the cluster's repository _analyze probe (ES 7.4+, OS 2.x)
// — writes a small set of test blobs, reads them back from every node,
// then deletes them. Strictly more thorough than the default
// connectivity check. Off by default because it does write *scratch
// data* (and so trips a prod-protected context's safety logic), and
// because operators reach for `verify` more often than `verify --deep`.
//
// AnalyzeKnobs lets the operator scale the probe up for production-
// sized repositories where the cluster's defaults complete in a few
// seconds and don't exercise the consistency surface S3 actually has
// in production.
type VerifyOptions struct {
	Repository   string
	Deep         bool
	AnalyzeKnobs AnalyzeKnobs
}

// AnalyzeKnobs is the operator-facing slice of SnapshotAnalyzeRequest.
// Empty / zero fields fall through to the cluster's defaults.
type AnalyzeKnobs struct {
	BlobCount     int
	Concurrency   int
	ReadNodeCount int
	EarlyReadNode int
	MaxBlobSize   string
	MaxTotalSize  string
	Timeout       time.Duration
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
	if client.IsPatternish(repo) {
		return types.SnapshotVerifyResult{}, client.UsageError("--repository must be a literal name, not a pattern (got %q)", repo)
	}
	return c.VerifyRepository(ctx, types.SnapshotVerifyRequest{Repository: repo})
}

// VerifyDeep is the `--deep` companion: same arguments, runs the
// _analyze probe instead of the connectivity check. Returns the full
// cluster report (per-blob counts, coordinating node, status) so an
// operator can compare against a known-good baseline.
//
// Older clusters without _analyze surface ErrUnsupported with a clear
// message; the CLI translates that to exit code 6 the same way every
// other unsupported-on-this-dialect path does.
func VerifyDeep(ctx context.Context, c client.SnapshotVerifier, opts VerifyOptions) (types.SnapshotAnalyzeResult, error) {
	repo := strings.TrimSpace(opts.Repository)
	if repo == "" {
		return types.SnapshotAnalyzeResult{}, client.UsageError("--repository is required")
	}
	if client.IsPatternish(repo) {
		return types.SnapshotAnalyzeResult{}, client.UsageError("--repository must be a literal name, not a pattern (got %q)", repo)
	}
	return c.AnalyzeRepository(ctx, types.SnapshotAnalyzeRequest{
		Repository:    repo,
		BlobCount:     opts.AnalyzeKnobs.BlobCount,
		Concurrency:   opts.AnalyzeKnobs.Concurrency,
		ReadNodeCount: opts.AnalyzeKnobs.ReadNodeCount,
		EarlyReadNode: opts.AnalyzeKnobs.EarlyReadNode,
		MaxBlobSize:   opts.AnalyzeKnobs.MaxBlobSize,
		MaxTotalSize:  opts.AnalyzeKnobs.MaxTotalSize,
		Timeout:       opts.AnalyzeKnobs.Timeout,
	})
}
