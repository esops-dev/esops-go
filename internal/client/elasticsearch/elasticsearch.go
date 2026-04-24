// Package elasticsearch adapts the upstream go-elasticsearch/v9 client
// to esops capability interfaces. Nothing in this package leaks out
// into the rest of the codebase: callers get neutral types only.
package elasticsearch

import (
	"bytes"
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

// allocationExplainWire mirrors the /_cluster/allocation/explain payload.
// ES 9's typedapi exposes a fully-typed Response, but its types.Duration
// values are custom and awkward to consume outside the package; decoding
// into a local shape keeps the adapter boundary clean (no upstream types
// leaking out) and makes the OS/ES contract trivially identical.
type allocationExplainWire struct {
	Index                  string                          `json:"index"`
	Shard                  int                             `json:"shard"`
	Primary                bool                            `json:"primary"`
	CurrentState           string                          `json:"current_state"`
	CurrentNode            *allocationNodeWire             `json:"current_node,omitempty"`
	UnassignedInfo         *allocationUnassignedWire       `json:"unassigned_info,omitempty"`
	CanAllocate            string                          `json:"can_allocate,omitempty"`
	AllocateExplanation    string                          `json:"allocate_explanation,omitempty"`
	CanRemainOnCurrentNode string                          `json:"can_remain_on_current_node,omitempty"`
	CanRebalanceCluster    string                          `json:"can_rebalance_cluster,omitempty"`
	MoveExplanation        string                          `json:"move_explanation,omitempty"`
	RebalanceExplanation   string                          `json:"rebalance_explanation,omitempty"`
	Note                   string                          `json:"note,omitempty"`
	NodeAllocationDecisions []allocationNodeDecisionWire   `json:"node_allocation_decisions,omitempty"`
}

type allocationNodeWire struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	TransportAddress string `json:"transport_address,omitempty"`
	WeightRanking    int    `json:"weight_ranking,omitempty"`
}

type allocationUnassignedWire struct {
	Reason               string `json:"reason"`
	At                   string `json:"at,omitempty"`
	Details              string `json:"details,omitempty"`
	LastAllocationStatus string `json:"last_allocation_status,omitempty"`
}

type allocationNodeDecisionWire struct {
	NodeID           string                  `json:"node_id"`
	NodeName         string                  `json:"node_name"`
	TransportAddress string                  `json:"transport_address,omitempty"`
	NodeDecision     string                  `json:"node_decision"`
	WeightRanking    int                     `json:"weight_ranking,omitempty"`
	Deciders         []allocationDeciderWire `json:"deciders,omitempty"`
}

type allocationDeciderWire struct {
	Decider     string `json:"decider"`
	Decision    string `json:"decision"`
	Explanation string `json:"explanation,omitempty"`
}

// AllocationExplain implements client.AllocationExplainer.
//
// Shape: when req.Index is empty we send no body — the cluster picks any
// unassigned shard it finds, matching the raw endpoint's default. When
// set, we pass {index, shard, primary} as the body; include_disk_info /
// include_yes_decisions go on as query params.
func (c *Client) AllocationExplain(ctx context.Context, req types.AllocationExplainRequest) (types.AllocationExplanation, error) {
	ae := c.api.Cluster.AllocationExplain
	opts := []func(*esapi.ClusterAllocationExplainRequest){
		ae.WithContext(ctx),
	}
	if req.IncludeDiskInfo {
		opts = append(opts, ae.WithIncludeDiskInfo(true))
	}
	if req.IncludeYesDecisions {
		opts = append(opts, ae.WithIncludeYesDecisions(true))
	}
	if req.Index != "" {
		body, err := json.Marshal(map[string]any{
			"index":   req.Index,
			"shard":   req.Shard,
			"primary": req.Primary,
		})
		if err != nil {
			return types.AllocationExplanation{}, fmt.Errorf("encoding allocation explain request: %w", err)
		}
		opts = append(opts, ae.WithBody(bytes.NewReader(body)))
	}

	resp, err := c.api.Cluster.AllocationExplain(opts...)
	if err != nil {
		return types.AllocationExplanation{}, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("allocation explain", resp.StatusCode, resp.Body); err != nil {
		return types.AllocationExplanation{}, err
	}

	var wire allocationExplainWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.AllocationExplanation{}, errors.Join(errors.New("decoding allocation explain response"), err)
	}
	return toAllocationExplanation(wire), nil
}

func toAllocationExplanation(w allocationExplainWire) types.AllocationExplanation {
	out := types.AllocationExplanation{
		Index:                  w.Index,
		Shard:                  w.Shard,
		Primary:                w.Primary,
		CurrentState:           w.CurrentState,
		CanAllocate:            w.CanAllocate,
		AllocateExplanation:    w.AllocateExplanation,
		CanRemainOnCurrentNode: w.CanRemainOnCurrentNode,
		CanRebalanceCluster:    w.CanRebalanceCluster,
		MoveExplanation:        w.MoveExplanation,
		RebalanceExplanation:   w.RebalanceExplanation,
		Note:                   w.Note,
	}
	if w.CurrentNode != nil {
		out.CurrentNode = &types.AllocationNode{
			ID:               w.CurrentNode.ID,
			Name:             w.CurrentNode.Name,
			TransportAddress: w.CurrentNode.TransportAddress,
			WeightRanking:    w.CurrentNode.WeightRanking,
		}
	}
	if w.UnassignedInfo != nil {
		out.UnassignedInfo = &types.AllocationUnassigned{
			Reason:               w.UnassignedInfo.Reason,
			At:                   w.UnassignedInfo.At,
			Details:              w.UnassignedInfo.Details,
			LastAllocationStatus: w.UnassignedInfo.LastAllocationStatus,
		}
	}
	if len(w.NodeAllocationDecisions) > 0 {
		out.NodeDecisions = make([]types.AllocationNodeDecision, 0, len(w.NodeAllocationDecisions))
		for _, d := range w.NodeAllocationDecisions {
			nd := types.AllocationNodeDecision{
				NodeID:           d.NodeID,
				NodeName:         d.NodeName,
				TransportAddress: d.TransportAddress,
				NodeDecision:     d.NodeDecision,
				WeightRanking:    d.WeightRanking,
			}
			if len(d.Deciders) > 0 {
				nd.Deciders = make([]types.AllocationDecider, 0, len(d.Deciders))
				for _, dd := range d.Deciders {
					nd.Deciders = append(nd.Deciders, types.AllocationDecider{
						Decider:     dd.Decider,
						Decision:    dd.Decision,
						Explanation: dd.Explanation,
					})
				}
			}
			out.NodeDecisions = append(out.NodeDecisions, nd)
		}
	}
	return out
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
