// Package opensearch adapts the upstream opensearch-go/v4 client to
// esops capability interfaces. Nothing in this package leaks out into
// the rest of the codebase: callers get neutral types only.
package opensearch

import (
	"fmt"
	"net/http"

	"context"

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
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.ClusterHealth{}, classifyResponseErr(err, status)
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
		req.Indices = esopsclient.SplitCSV(filter.Index)
	}

	resp, err := c.api.Cat.Shards(ctx, req)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return nil, classifyResponseErr(err, status)
	}

	out := make([]types.Shard, 0, len(resp.Shards))
	for _, s := range resp.Shards {
		out = append(out, types.Shard{
			Index:             s.Index,
			Shard:             s.Shard,
			Primary:           s.Prirep == "p",
			State:             s.State,
			Docs:              esopsclient.Atoi64Ptr(s.Docs),
			StoreBytes:        esopsclient.Atoi64Ptr(s.Store),
			Node:              esopsclient.DerefString(s.Node),
			IP:                esopsclient.DerefString(s.IP),
			UnassignedReason:  esopsclient.DerefString(s.UnassignedReason),
			UnassignedAt:      esopsclient.DerefString(s.UnassignedAt),
			UnassignedFor:     esopsclient.DerefString(s.UnassignedFor),
			UnassignedDetails: esopsclient.DerefString(s.UnassignedDetails),
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
		req.Indices = esopsclient.SplitCSV(filter.Index)
	}

	resp, err := c.api.Cat.Indices(ctx, req)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return nil, classifyResponseErr(err, status)
	}

	out := make([]types.Index, 0, len(resp.Indices))
	for _, r := range resp.Indices {
		out = append(out, types.Index{
			Name:               r.Index,
			UUID:               r.UUID,
			Health:             r.Health,
			Status:             r.Status,
			Primaries:          esopsclient.IntPtrOrZero(r.Primary),
			Replicas:           esopsclient.IntPtrOrZero(r.Replica),
			DocsCount:          esopsclient.Int64PtrOrZero(r.DocsCount),
			DocsDeleted:        esopsclient.Int64PtrOrZero(r.DocDeleted),
			StoreBytes:         esopsclient.Atoi64Ptr(r.StoreSize),
			PrimaryStoreBytes:  esopsclient.Atoi64Ptr(r.PrimaryStoreSize),
			CreationDateMillis: int64(r.CreationDate),
		})
	}
	return out, nil
}

// statusOf returns the HTTP status code from the opensearch-go Response
// embedded in a typed response's Inspect(). Returns 0 when the transport
// errored before a response came back — the caller treats that as
// "unreachable" regardless of status.
func statusOf(r *os.Response) int {
	if r == nil {
		return 0
	}
	return r.StatusCode
}

// classifyResponseErr unifies error classification: auth/forbidden go to
// the sentinel errors by status code; everything else bubbles up as
// ErrUnreachable wrapping the original transport error.
func classifyResponseErr(err error, status int) error {
	switch status {
	case http.StatusUnauthorized:
		return esopsclient.ErrAuth
	case http.StatusForbidden:
		return esopsclient.ErrForbidden
	}
	return esopsclient.ClassifyTransport(err)
}
