package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// esRemoteEntryWire mirrors one entry in GET /_remote/info. The
// envelope is a top-level map keyed by remote-cluster name. The two
// connection modes (sniff / proxy) overlap on the connected/skip
// fields and diverge on which "node count" they report — sniff uses
// `num_nodes_connected`, proxy uses `num_proxy_sockets_connected`.
// Both are surfaced into the neutral type's NumNodesConnected field
// so the rule consumer treats "is this remote actually reachable"
// uniformly across modes.
//
// Seeds appears only in sniff mode; an empty SeedHosts on a proxy-
// mode remote is the right neutral shape (the proxy address is the
// gateway, not a seed list — the rule consumer would be wrong to
// treat the absence as misconfiguration).
type esRemoteEntryWire struct {
	Mode                     string   `json:"mode"`
	Connected                bool     `json:"connected"`
	NumNodesConnected        int      `json:"num_nodes_connected"`
	NumProxySocketsConnected int      `json:"num_proxy_sockets_connected"`
	Seeds                    []string `json:"seeds"`
	SkipUnavailable          bool     `json:"skip_unavailable"`
}

// RemoteClusters implements client.RemoteClusterInspector. The cluster
// reports an empty object when no remotes are configured, which we
// surface as an empty slice — a valid "no remotes" outcome rather than
// a failure.
func (c *Client) RemoteClusters(ctx context.Context) ([]types.RemoteCluster, error) {
	body, status, err := c.doRawGET(ctx, "/_remote/info")
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusUnauthorized:
		return nil, esopsclient.ErrAuth
	case http.StatusForbidden:
		return nil, esopsclient.ErrForbidden
	}
	if status >= 400 {
		return nil, clientutil.ClassifyStatus("remote info", status, body)
	}

	var wire map[string]esRemoteEntryWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, errors.Join(errors.New("decoding remote info response"), err)
	}
	out := make([]types.RemoteCluster, 0, len(wire))
	for name, e := range wire {
		// In proxy mode the cluster reports per-socket connection counts;
		// surface that as NumNodesConnected so the rule asks one question
		// across modes — "how many cluster-side connections are live".
		nodes := e.NumNodesConnected
		if e.Mode == "proxy" && e.NumProxySocketsConnected > 0 {
			nodes = e.NumProxySocketsConnected
		}
		out = append(out, types.RemoteCluster{
			Name:              name,
			Mode:              e.Mode,
			Connected:         e.Connected,
			NumNodesConnected: nodes,
			SeedHosts:         e.Seeds,
			SkipUnavailable:   e.SkipUnavailable,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// esCCRStatsWire mirrors the slice of GET /_ccr/stats we read. The
// cluster groups follower indices into `follow_stats.indices[]`, each
// with one or more per-shard stat blocks. We aggregate to one row per
// follower index — the rule consumer asks "is this index keeping up"
// at the index level, not per shard, and the maximum across shards is
// the right summary (one lagging shard makes the whole index lagged).
type esCCRStatsWire struct {
	FollowStats struct {
		Indices []struct {
			Index  string            `json:"index"`
			Shards []esCCRStatShWire `json:"shards"`
		} `json:"indices"`
	} `json:"follow_stats"`
}

type esCCRStatShWire struct {
	RemoteCluster        string `json:"remote_cluster"`
	LeaderIndex          string `json:"leader_index"`
	FollowerIndex        string `json:"follower_index"`
	OperationsWrittenLag int64  `json:"operations_written_lag"`
	// Shard-level pause flag. The cluster does not surface a single
	// per-index "paused" boolean; we treat the index as paused when any
	// of its shards are paused, mirroring the lag aggregation.
	Paused            bool `json:"paused"`
	LastReadException struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	} `json:"read_exceptions"`
	FailedReadRequests int64 `json:"failed_read_requests"`
	// RetentionLeaseAgeMillis is per-shard; the FollowerStats() loop
	// aggregates max across shards into the neutral type's
	// RetentionLeaseAgeMillis field. Zero when the cluster did not
	// surface a value — older ES, or a freshly-created follower whose
	// shards have not yet acquired a lease.
	RetentionLeaseAgeMillis int64 `json:"retention_lease_age_in_millis"`
}

// esAutoFollowWire mirrors the slice of GET /_ccr/auto_follow we read.
// The cluster wraps each pattern under a top-level `patterns` array
// where each entry is a {name, pattern{...}} pair. We flatten that
// into a single AutoFollowPattern row so the rule consumer doesn't
// have to do the unwrap.
//
// `active` lives on the inner pattern object on recent ES; older 7.x
// reported it on the outer entry. Both shapes are accepted — the
// JSON decoder picks the first non-nil source.
type esAutoFollowWire struct {
	Patterns []struct {
		Name        string `json:"name"`
		ActiveOuter *bool  `json:"active"`
		Pattern     struct {
			RemoteCluster       string   `json:"remote_cluster"`
			LeaderIndexPatterns []string `json:"leader_index_patterns"`
			FollowIndexPattern  string   `json:"follow_index_pattern"`
			ActiveInner         *bool    `json:"active"`
		} `json:"pattern"`
	} `json:"patterns"`
}

// AutoFollowPatterns implements client.RemoteClusterInspector. The
// cluster wraps configured patterns in `patterns[]`; we flatten and
// sort by Name for stable output. 404 → ErrUnsupported on the same
// licence-gated terms as FollowerStats: the endpoint isn't registered
// without a CCR-licensed cluster, so the rule consumer should treat
// it as "not applicable here" and auto-skip.
func (c *Client) AutoFollowPatterns(ctx context.Context) ([]types.AutoFollowPattern, error) {
	body, status, err := c.doRawGET(ctx, "/_ccr/auto_follow")
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusUnauthorized:
		return nil, esopsclient.ErrAuth
	case http.StatusForbidden:
		return nil, esopsclient.ErrForbidden
	case http.StatusNotFound:
		return nil, errors.Join(esopsclient.ErrUnsupported,
			errors.New("/_ccr/auto_follow not exposed by this cluster (CCR not licensed or disabled)"))
	}
	if status >= 400 {
		return nil, clientutil.ClassifyStatus("ccr auto_follow", status, body)
	}

	var wire esAutoFollowWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, errors.Join(errors.New("decoding ccr auto_follow response"), err)
	}
	out := make([]types.AutoFollowPattern, 0, len(wire.Patterns))
	for _, p := range wire.Patterns {
		// `active` has lived in two places across ES versions; prefer
		// the inner spelling (recent) and fall back to the outer
		// (older 7.x). Default true on the assumption that an explicitly
		// paused pattern always reports the field — matches the
		// cluster's own /_ccr/auto_follow/{name}/pause semantics.
		active := true
		if p.Pattern.ActiveInner != nil {
			active = *p.Pattern.ActiveInner
		} else if p.ActiveOuter != nil {
			active = *p.ActiveOuter
		}
		out = append(out, types.AutoFollowPattern{
			Name:                p.Name,
			RemoteCluster:       p.Pattern.RemoteCluster,
			LeaderIndexPatterns: p.Pattern.LeaderIndexPatterns,
			FollowIndexPattern:  p.Pattern.FollowIndexPattern,
			Active:              active,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// FollowerStats implements client.RemoteClusterInspector. Aggregates
// the cluster's per-shard view into one row per follower index.
//
// 404 on /_ccr/stats is a real outcome on a basic-licence cluster:
// CCR requires platinum/enterprise, and the endpoint isn't registered
// without it. Surface as ErrUnsupported so the rule consumer treats it
// as "not applicable on this cluster" (same translation the apikeys
// reader uses for the security-disabled case) rather than a generic
// 4xx — saves the rule from writing its own status-code switch.
func (c *Client) FollowerStats(ctx context.Context) ([]types.FollowerStats, error) {
	body, status, err := c.doRawGET(ctx, "/_ccr/stats")
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusUnauthorized:
		return nil, esopsclient.ErrAuth
	case http.StatusForbidden:
		return nil, esopsclient.ErrForbidden
	case http.StatusNotFound:
		return nil, errors.Join(esopsclient.ErrUnsupported,
			errors.New("/_ccr/stats not exposed by this cluster (CCR not licensed or disabled)"))
	}
	if status >= 400 {
		return nil, clientutil.ClassifyStatus("ccr stats", status, body)
	}

	var wire esCCRStatsWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, errors.Join(errors.New("decoding ccr stats response"), err)
	}
	out := make([]types.FollowerStats, 0, len(wire.FollowStats.Indices))
	for _, idx := range wire.FollowStats.Indices {
		row := types.FollowerStats{Index: idx.Index}
		for _, sh := range idx.Shards {
			if row.LeaderIndex == "" {
				row.LeaderIndex = sh.LeaderIndex
				row.LeaderCluster = sh.RemoteCluster
			}
			if sh.OperationsWrittenLag > row.FollowerLagOps {
				row.FollowerLagOps = sh.OperationsWrittenLag
			}
			if sh.Paused {
				row.Paused = true
			}
			if row.LastReadFailure == "" && sh.LastReadException.Reason != "" {
				row.LastReadFailure = sh.LastReadException.Reason
			} else if row.LastReadFailure == "" && sh.FailedReadRequests > 0 && sh.LastReadException.Type != "" {
				row.LastReadFailure = sh.LastReadException.Type
			}
			// Track the max retention-lease age across the index's shards.
			// One expired-lease shard makes the whole follower
			// unrecoverable, so the max is the operator-facing summary.
			if sh.RetentionLeaseAgeMillis > row.RetentionLeaseAgeMillis {
				row.RetentionLeaseAgeMillis = sh.RetentionLeaseAgeMillis
			}
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out, nil
}
