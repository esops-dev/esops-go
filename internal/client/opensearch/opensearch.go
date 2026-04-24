// Package opensearch adapts the upstream opensearch-go/v4 client to
// esops capability interfaces. Nothing in this package leaks out into
// the rest of the codebase: callers get neutral types only.
package opensearch

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	os "github.com/opensearch-project/opensearch-go/v4"
	osapi "github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	esopsclient "github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// Client is an OS-backed implementation of the esops client capabilities.
type Client struct {
	api *osapi.Client
}

func New(addrs []string, username, password string, httpTransport http.RoundTripper) (*Client, error) {
	c, err := osapi.NewClient(osapi.Config{
		Client: os.Config{
			Addresses: addrs,
			Username:  username,
			Password:  password,
			Transport: httpTransport,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("opensearch client: %w", err)
	}
	return &Client{api: c}, nil
}

// Health implements client.HealthInspector.
func (c *Client) Health(ctx context.Context) (types.ClusterHealth, error) {
	resp, err := c.api.Cluster.Health(ctx, nil)
	if err != nil {
		return types.ClusterHealth{}, classifyOSError(err, resp)
	}
	return types.ClusterHealth{
		ClusterName:                 resp.ClusterName,
		Status:                      resp.Status,
		TimedOut:                    resp.TimedOut,
		NumberOfNodes:               resp.NumberOfNodes,
		NumberOfDataNodes:           resp.NumberOfDataNodes,
		ActivePrimaryShards:         resp.ActivePrimaryShards,
		ActiveShards:                resp.ActiveShards,
		RelocatingShards:            resp.RelocatingShards,
		InitializingShards:          resp.InitializingShards,
		UnassignedShards:            resp.UnassignedShards,
		DelayedUnassignedShards:     resp.DelayedUnassignedShards,
		NumberOfPendingTasks:        resp.NumberOfPendingTasks,
		NumberOfInFlightFetch:       resp.NumberOfInFlightFetch,
		TaskMaxWaitingInQueueMillis: resp.TaskMaxWaitingInQueueMillis,
		ActiveShardsPercentAsNumber: resp.ActiveShardsPercentAsNumber,
	}, nil
}

// Shards implements client.ShardInspector. Index patterns are passed to
// the cluster; state/node/role are filtered in the business layer so
// behaviour matches across dialects.
func (c *Client) Shards(ctx context.Context, filter types.ShardFilter) ([]types.Shard, error) {
	req := &osapi.CatShardsReq{
		Params: osapi.CatShardsParams{
			Bytes: "b",
			// Explicit column selection: _cat APIs replace (not extend) the
			// default columns when h is set, so every field we surface has to
			// be listed.
			H: []string{
				"index", "shard", "prirep", "state",
				"docs", "store", "ip", "node",
				"unassigned.reason", "unassigned.at", "unassigned.for", "unassigned.details",
			},
		},
	}
	if filter.Index != "" {
		req.Indices = splitCSV(filter.Index)
	}

	resp, err := c.api.Cat.Shards(ctx, req)
	if err != nil {
		return nil, classifyShardsError(err, resp)
	}

	out := make([]types.Shard, 0, len(resp.Shards))
	for _, s := range resp.Shards {
		out = append(out, types.Shard{
			Index:             s.Index,
			Shard:             s.Shard,
			Primary:           s.Prirep == "p",
			State:             s.State,
			Docs:              atoi64Ptr(s.Docs),
			StoreBytes:        atoi64Ptr(s.Store),
			Node:              derefString(s.Node),
			IP:                derefString(s.IP),
			UnassignedReason:  derefString(s.UnassignedReason),
			UnassignedAt:      derefString(s.UnassignedAt),
			UnassignedFor:     derefString(s.UnassignedFor),
			UnassignedDetails: derefString(s.UnassignedDetails),
		})
	}
	return out, nil
}

// Indices implements client.IndexInspector. The OS v4 client returns a
// fully-typed CatIndexResp — we pick just the fields esops surfaces and
// drop the long tail of per-operation counters upstream exposes. bytes=b
// forces raw-bytes store sizes regardless of OS version.
func (c *Client) Indices(ctx context.Context, filter types.IndexFilter) ([]types.Index, error) {
	req := &osapi.CatIndicesReq{
		Params: osapi.CatIndicesParams{
			Bytes: "b",
			// OS expand_wildcards accepts a comma-separated list via a single
			// string; we pass "open,closed,hidden" to mirror the ES adapter
			// so hidden/closed filtering stays a business-layer concern.
			ExpandWildcards: "open,closed,hidden",
			H: []string{
				"health", "status", "index", "uuid",
				"pri", "rep",
				"docs.count", "docs.deleted",
				"store.size", "pri.store.size",
				"creation.date",
			},
		},
	}
	if filter.Index != "" {
		req.Indices = splitCSV(filter.Index)
	}

	resp, err := c.api.Cat.Indices(ctx, req)
	if err != nil {
		return nil, classifyIndicesError(err, resp)
	}

	out := make([]types.Index, 0, len(resp.Indices))
	for _, r := range resp.Indices {
		out = append(out, types.Index{
			Name:               r.Index,
			UUID:               r.UUID,
			Health:             r.Health,
			Status:             r.Status,
			Primaries:          intPtrOrZero(r.Primary),
			Replicas:           intPtrOrZero(r.Replica),
			DocsCount:          int64PtrOrZero(r.DocsCount),
			DocsDeleted:        int64PtrOrZero(r.DocDeleted),
			StoreBytes:         atoi64Ptr(r.StoreSize),
			PrimaryStoreBytes:  atoi64Ptr(r.PrimaryStoreSize),
			CreationDateMillis: int64(r.CreationDate),
		})
	}
	return out, nil
}

func intPtrOrZero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func int64PtrOrZero(p *int) int64 {
	if p == nil {
		return 0
	}
	return int64(*p)
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func atoi64Ptr(p *string) int64 {
	if p == nil {
		return 0
	}
	n, _ := strconv.ParseInt(*p, 10, 64)
	return n
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func classifyShardsError(err error, resp *osapi.CatShardsResp) error {
	if resp != nil {
		r := resp.Inspect().Response
		if r != nil {
			switch r.StatusCode {
			case http.StatusUnauthorized:
				return esopsclient.ErrAuth
			case http.StatusForbidden:
				return esopsclient.ErrForbidden
			}
		}
	}
	return fmt.Errorf("%w: %v", esopsclient.ErrUnreachable, err)
}

func classifyIndicesError(err error, resp *osapi.CatIndicesResp) error {
	if resp != nil {
		r := resp.Inspect().Response
		if r != nil {
			switch r.StatusCode {
			case http.StatusUnauthorized:
				return esopsclient.ErrAuth
			case http.StatusForbidden:
				return esopsclient.ErrForbidden
			}
		}
	}
	return fmt.Errorf("%w: %v", esopsclient.ErrUnreachable, err)
}

func classifyOSError(err error, resp *osapi.ClusterHealthResp) error {
	if resp != nil {
		r := resp.Inspect().Response
		if r != nil {
			switch r.StatusCode {
			case http.StatusUnauthorized:
				return esopsclient.ErrAuth
			case http.StatusForbidden:
				return esopsclient.ErrForbidden
			}
		}
	}
	return fmt.Errorf("%w: %v", esopsclient.ErrUnreachable, err)
}
