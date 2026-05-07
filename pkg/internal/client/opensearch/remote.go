package opensearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// osRemoteEntryWire mirrors one entry in GET /_remote/info. The wire
// shape is identical to ES — kept duplicated rather than shared
// because pkg/internal/client/elasticsearch and opensearch are
// peer-internal packages with no shared dependency they can cross.
// The two-mode handling (sniff/proxy) and the merge into the neutral
// type mirrors the ES adapter exactly so a contract test exercises
// both implementations the same way.
type osRemoteEntryWire struct {
	Mode                     string   `json:"mode"`
	Connected                bool     `json:"connected"`
	NumNodesConnected        int      `json:"num_nodes_connected"`
	NumProxySocketsConnected int      `json:"num_proxy_sockets_connected"`
	Seeds                    []string `json:"seeds"`
	SkipUnavailable          bool     `json:"skip_unavailable"`
}

// RemoteClusters implements client.RemoteClusterInspector. CCS is
// shared across both dialects — see the matching ES adapter for the
// rationale.
func (c *Client) RemoteClusters(ctx context.Context) ([]types.RemoteCluster, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/_remote/info", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.Client.Perform(req)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "remote info"))
	if err != nil {
		return nil, errors.Join(errors.New("reading remote info response"), err)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, esopsclient.ErrAuth
	case http.StatusForbidden:
		return nil, esopsclient.ErrForbidden
	}
	if resp.StatusCode >= 400 {
		return nil, clientutil.ClassifyStatus("remote info", resp.StatusCode, body)
	}

	var wire map[string]osRemoteEntryWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, errors.Join(errors.New("decoding remote info response"), err)
	}
	out := make([]types.RemoteCluster, 0, len(wire))
	for name, e := range wire {
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

// FollowerStats implements client.RemoteClusterInspector. CCR is
// Elasticsearch-only — OpenSearch ships its own cross-cluster
// replication plugin under /_plugins/_replication with a divergent
// API and feature set, but the doctor rule that consumes this
// capability is dialect-tagged for the ES surface. Returning
// ErrUnsupported here keeps the dialect mismatch loud and stable
// (exit code 6) and lets the rule auto-skip with a documented reason.
func (c *Client) FollowerStats(ctx context.Context) ([]types.FollowerStats, error) {
	return nil, fmt.Errorf("%w: cross-cluster replication stats are Elasticsearch-only; OpenSearch exposes a divergent /_plugins/_replication surface that the rule does not consume", esopsclient.ErrUnsupported)
}

// AutoFollowPatterns implements client.RemoteClusterInspector. CCR
// auto-follow is Elasticsearch-only on the same terms as FollowerStats
// — see that method's comment for the rationale.
func (c *Client) AutoFollowPatterns(ctx context.Context) ([]types.AutoFollowPattern, error) {
	return nil, fmt.Errorf("%w: cross-cluster replication auto-follow patterns are Elasticsearch-only; OpenSearch exposes a divergent /_plugins/_replication surface that the rule does not consume", esopsclient.ErrUnsupported)
}
