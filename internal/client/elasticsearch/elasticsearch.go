// Package elasticsearch adapts the upstream go-elasticsearch/v9 client
// to esops capability interfaces. Nothing in this package leaks out
// into the rest of the codebase: callers get neutral types only.
package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	es "github.com/elastic/go-elasticsearch/v9"
	"github.com/elastic/go-elasticsearch/v9/esapi"

	esopsclient "github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// Client is an ES-backed implementation of the esops client capabilities.
type Client struct {
	api *es.Client
}

// New builds a client against the given addresses + basic-auth credentials.
// TLS and transport tweaks come in via httpTransport.
func New(addrs []string, username, password string, httpTransport http.RoundTripper) (*Client, error) {
	c, err := es.NewClient(es.Config{
		Addresses: addrs,
		Username:  username,
		Password:  password,
		Transport: httpTransport,
	})
	if err != nil {
		return nil, fmt.Errorf("elasticsearch client: %w", err)
	}
	return &Client{api: c}, nil
}

// Health implements client.HealthInspector.
func (c *Client) Health(ctx context.Context) (types.ClusterHealth, error) {
	resp, err := c.api.Cluster.Health(c.api.Cluster.Health.WithContext(ctx))
	if err != nil {
		return types.ClusterHealth{}, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cluster health", resp.StatusCode, resp.Body); err != nil {
		return types.ClusterHealth{}, err
	}

	var h types.ClusterHealth
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return types.ClusterHealth{}, errors.Join(errors.New("decoding cluster health response"), err)
	}
	return h, nil
}

// catShardsRow mirrors the /_cat/shards?format=json payload. Every numeric
// field is quoted by the cluster, and store/docs are null for unassigned
// shards and system indices that haven't materialised data yet. The
// unassigned.* columns are only populated for unassigned shards.
type catShardsRow struct {
	Index             string  `json:"index"`
	Shard             string  `json:"shard"`
	Prirep            string  `json:"prirep"`
	State             string  `json:"state"`
	Docs              *string `json:"docs"`
	Store             *string `json:"store"`
	Node              *string `json:"node"`
	IP                *string `json:"ip"`
	UnassignedReason  *string `json:"unassigned.reason"`
	UnassignedAt      *string `json:"unassigned.at"`
	UnassignedFor     *string `json:"unassigned.for"`
	UnassignedDetails *string `json:"unassigned.details"`
}

// shardsColumns is the explicit h= selector. _cat APIs replace (not extend)
// the default column set when h is specified, so every field we want to
// surface has to be listed here.
var shardsColumns = []string{
	"index", "shard", "prirep", "state",
	"docs", "store", "ip", "node",
	"unassigned.reason", "unassigned.at", "unassigned.for", "unassigned.details",
}

// Shards implements client.ShardInspector. The Index filter is passed to
// the cluster as a path segment; state/node/role are applied client-side
// in the business layer so behaviour matches across dialects.
func (c *Client) Shards(ctx context.Context, filter types.ShardFilter) ([]types.Shard, error) {
	cs := c.api.Cat.Shards
	opts := []func(*esapi.CatShardsRequest){
		cs.WithContext(ctx),
		cs.WithFormat("json"),
		cs.WithBytes("b"),
		cs.WithH(shardsColumns...),
	}
	if filter.Index != "" {
		opts = append(opts, cs.WithIndex(esopsclient.SplitCSV(filter.Index)...))
	}

	resp, err := c.api.Cat.Shards(opts...)
	if err != nil {
		return nil, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cat shards", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var rows []catShardsRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, errors.Join(errors.New("decoding cat shards response"), err)
	}

	out := make([]types.Shard, 0, len(rows))
	for _, r := range rows {
		out = append(out, types.Shard{
			Index:             r.Index,
			Shard:             esopsclient.AtoiOrZero(r.Shard),
			Primary:           r.Prirep == "p",
			State:             r.State,
			Docs:              esopsclient.Atoi64Ptr(r.Docs),
			StoreBytes:        esopsclient.Atoi64Ptr(r.Store),
			Node:              esopsclient.DerefString(r.Node),
			IP:                esopsclient.DerefString(r.IP),
			UnassignedReason:  esopsclient.DerefString(r.UnassignedReason),
			UnassignedAt:      esopsclient.DerefString(r.UnassignedAt),
			UnassignedFor:     esopsclient.DerefString(r.UnassignedFor),
			UnassignedDetails: esopsclient.DerefString(r.UnassignedDetails),
		})
	}
	return out, nil
}

// catIndicesRow mirrors a single /_cat/indices?format=json row. Every
// numeric column is quoted by the cluster; store/docs are null for
// closed or freshly-created indices. Fields not present in the `h=`
// selector are simply absent from the JSON payload.
type catIndicesRow struct {
	Health       string  `json:"health"`
	Status       string  `json:"status"`
	Index        string  `json:"index"`
	UUID         string  `json:"uuid"`
	Pri          *string `json:"pri"`
	Rep          *string `json:"rep"`
	DocsCount    *string `json:"docs.count"`
	DocsDeleted  *string `json:"docs.deleted"`
	StoreSize    *string `json:"store.size"`
	PriStoreSize *string `json:"pri.store.size"`
	CreationDate *string `json:"creation.date"`
}

// indicesColumns is the explicit h= selector for /_cat/indices. _cat
// APIs replace (not extend) the default columns when h is set, so every
// field we surface must be listed here.
var indicesColumns = []string{
	"health", "status", "index", "uuid",
	"pri", "rep",
	"docs.count", "docs.deleted",
	"store.size", "pri.store.size",
	"creation.date",
}

// Indices implements client.IndexInspector. Index patterns are forwarded
// to the cluster as a path segment; we always ask for bytes=b and a full
// wildcard expansion (open+closed+hidden) so the business layer can
// honour the neutral IndexFilter semantics uniformly.
func (c *Client) Indices(ctx context.Context, filter types.IndexFilter) ([]types.Index, error) {
	ci := c.api.Cat.Indices
	opts := []func(*esapi.CatIndicesRequest){
		ci.WithContext(ctx),
		ci.WithFormat("json"),
		ci.WithBytes("b"),
		ci.WithH(indicesColumns...),
		// Ask for everything; the business layer filters hidden/closed.
		// Without this, ES 8+ hides dot-prefixed indices by default.
		ci.WithExpandWildcards("open,closed,hidden"),
	}
	if filter.Index != "" {
		opts = append(opts, ci.WithIndex(esopsclient.SplitCSV(filter.Index)...))
	}

	resp, err := c.api.Cat.Indices(opts...)
	if err != nil {
		return nil, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cat indices", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var rows []catIndicesRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, errors.Join(errors.New("decoding cat indices response"), err)
	}

	out := make([]types.Index, 0, len(rows))
	for _, r := range rows {
		out = append(out, types.Index{
			Name:               r.Index,
			UUID:               r.UUID,
			Health:             r.Health,
			Status:             r.Status,
			Primaries:          esopsclient.AtoiOrZero(esopsclient.DerefString(r.Pri)),
			Replicas:           esopsclient.AtoiOrZero(esopsclient.DerefString(r.Rep)),
			DocsCount:          esopsclient.Atoi64Ptr(r.DocsCount),
			DocsDeleted:        esopsclient.Atoi64Ptr(r.DocsDeleted),
			StoreBytes:         esopsclient.Atoi64Ptr(r.StoreSize),
			PrimaryStoreBytes:  esopsclient.Atoi64Ptr(r.PriStoreSize),
			CreationDateMillis: esopsclient.Atoi64Ptr(r.CreationDate),
		})
	}
	return out, nil
}

// classifyResponse reads a drained response body only on error so the
// happy path keeps streaming straight into json.Decoder. Returns nil
// for 2xx/3xx; ErrAuth/ErrForbidden for 401/403; a wrapped error for
// other 4xx/5xx with the body in the message.
func classifyResponse(op string, status int, body io.Reader) error {
	if status < 400 {
		return nil
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return esopsclient.ClassifyStatus(op, status, nil)
	}
	raw, _ := io.ReadAll(body)
	return esopsclient.ClassifyStatus(op, status, raw)
}
