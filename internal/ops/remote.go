package ops

import (
	"context"
	"errors"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// RemoteSummary bundles every half of the remote-cluster surface so a
// single command invocation reports CCS readiness (every dialect) and
// CCR follower lag + auto-follow patterns (Elasticsearch only) without
// three round trips of CLI plumbing.
//
// Followers and AutoFollows are empty when CCR is unsupported on the
// dialect or unlicensed on the cluster — distinct from "no follower
// indices configured" only by the CCRStatus field. CCRStatus carries a
// human-readable hint the renderer surfaces alongside the tables:
//
//   - "ok"           — CCR is licensed and Followers / AutoFollows list every entry.
//   - "unsupported"  — OpenSearch dialect, or 404 from /_ccr/* on a
//     basic-licence ES cluster.
//   - "forbidden"    — the auth user lacks the cluster privilege required
//     to read /_ccr/*. Distinct from "unsupported"
//     so the operator knows whether to fix permissions
//     vs upgrade the licence.
//
// CCRStatus is computed from the FollowerStats outcome. AutoFollow's
// independent failure is folded into the same status — both endpoints
// share a licence gate, so a divergent outcome would be a rare edge
// case (e.g. one endpoint disabled at the proxy). When AutoFollow
// fails on a CCR-licensed cluster the AutoFollowError field carries
// the underlying reason for the renderer to surface.
type RemoteSummary struct {
	Remotes         []types.RemoteCluster
	Followers       []types.FollowerStats
	AutoFollows     []types.AutoFollowPattern
	CCRStatus       string
	AutoFollowError string
}

// ListRemotes runs RemoteClusterInspector.RemoteClusters (mandatory),
// FollowerStats (best-effort), and AutoFollowPatterns (best-effort).
// The CCR calls are best-effort so a basic-licence or OS cluster
// doesn't fail the whole command — the CCS side is informative on
// its own. CCRStatus surfaces *why* the follower list is empty so
// the renderer can label the table.
//
// A 401 on RemoteClusters bubbles up as ErrAuth — without remote info
// the command has nothing to render. A 401 on the CCR call is
// reported through CCRStatus="unauthorized" rather than aborting,
// since the operator's primary question is "are remotes configured
// correctly" not "can I read CCR stats".
//
// AutoFollow's status is folded into CCRStatus when both fail the
// same way; a CCR-licensed cluster where only AutoFollow misbehaves
// (e.g. proxy strips the path) keeps CCRStatus="ok" and surfaces the
// reason through AutoFollowError so the operator can act on it
// without losing the FollowerStats view.
func ListRemotes(ctx context.Context, c esopsclient.RemoteClusterInspector) (RemoteSummary, error) {
	remotes, err := c.RemoteClusters(ctx)
	if err != nil {
		return RemoteSummary{}, err
	}
	out := RemoteSummary{Remotes: remotes, CCRStatus: "ok"}
	followers, err := c.FollowerStats(ctx)
	switch {
	case err == nil:
		out.Followers = followers
	case errors.Is(err, esopsclient.ErrUnsupported):
		out.CCRStatus = "unsupported"
	case errors.Is(err, esopsclient.ErrForbidden):
		out.CCRStatus = "forbidden"
	case errors.Is(err, esopsclient.ErrAuth):
		out.CCRStatus = "unauthorized"
	default:
		// Genuine cluster failure (5xx, transport error). Bubble up so
		// the operator sees the underlying error rather than a silent
		// "no follower stats" cell.
		return RemoteSummary{}, err
	}

	// AutoFollow shares a licence gate with FollowerStats; a
	// CCRStatus other than "ok" is the most common reason AutoFollow
	// would also fail. Skip the call entirely in that case to save a
	// round trip on basic-licence/OS clusters where the answer is
	// "not applicable" by construction.
	if out.CCRStatus != "ok" {
		return out, nil
	}
	patterns, err := c.AutoFollowPatterns(ctx)
	switch {
	case err == nil:
		out.AutoFollows = patterns
	case errors.Is(err, esopsclient.ErrUnsupported),
		errors.Is(err, esopsclient.ErrForbidden),
		errors.Is(err, esopsclient.ErrAuth):
		// Divergent outcome from FollowerStats — record the reason
		// for the renderer; don't downgrade CCRStatus from "ok"
		// since the FollowerStats view is still valid.
		out.AutoFollowError = err.Error()
	default:
		return RemoteSummary{}, err
	}
	return out, nil
}
