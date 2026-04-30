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
	"strings"
	"time"

	es "github.com/elastic/go-elasticsearch/v9"
	"github.com/elastic/go-elasticsearch/v9/esapi"

	"github.com/dj-wasabi/esops-go/internal/auth"
	esopsclient "github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// Client is an ES-backed implementation of the esops client capabilities.
type Client struct {
	api *es.Client
}

// Options bundles the transport-shaping knobs the upstream ES client
// honours but esops exposes via config (max_retries, compress). Bare
// fields rather than functional options because the call site is one
// place (the cluster factory) and a struct keeps the surface obvious
// at a glance.
type Options struct {
	// MaxRetries: 0 = library default (3). Negative disables retries.
	// Mirrors es.Config.MaxRetries; we apply DisableRetry for negatives
	// so "I want no retries" is expressible without conflating zero and
	// "use the library default".
	MaxRetries int

	// CompressRequestBody gzips outbound bodies. Useful for big reindex
	// /_search bodies and for diag captures travelling through narrow
	// links. Off by default; the upstream's default has stayed off
	// across versions and operators expect that.
	CompressRequestBody bool
}

// New builds a client against the given addresses with auth carried by
// the provider. TLS and transport tweaks come in via httpTransport;
// opts plumbs through max_retries / compress from config.
//
// SigV4 is rejected here: SigV4 is currently OpenSearch-only. The
// go-elasticsearch client has no first-class SigV4 helper and adding
// one means hand-rolling the canonical-request math, which belongs
// behind a deliberate feature gate rather than as a quiet fallback
// that 401s every request.
func New(addrs []string, provider auth.Provider, httpTransport http.RoundTripper, opts Options) (*Client, error) {
	if provider == nil {
		return nil, errors.New("elasticsearch client: nil auth provider")
	}
	if provider.Type() == auth.TypeSigV4 {
		return nil, errors.New("auth.type=sigv4 is not supported on Elasticsearch; use bearer or basic")
	}
	cfg := es.Config{
		Addresses: addrs,
		Transport: auth.WrapTransport(provider, httpTransport),
	}
	switch {
	case opts.MaxRetries < 0:
		cfg.DisableRetry = true
	case opts.MaxRetries > 0:
		cfg.MaxRetries = opts.MaxRetries
	}
	if opts.CompressRequestBody {
		cfg.CompressRequestBody = true
	}
	c, err := es.NewClient(cfg)
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

// indexStateChangeWire mirrors a /_open or /_close response. ES and OS
// both return acknowledged + shards_acknowledged at the envelope level;
// /_close additionally carries an indices map ("logs-2024":{"closed":true,...})
// which we don't surface — the resolved per-index list comes from the
// pre-flight cat-indices read in the business layer, and is identical
// for both dialects.
type indexStateChangeWire struct {
	Acknowledged       bool `json:"acknowledged"`
	ShardsAcknowledged bool `json:"shards_acknowledged"`
}

// OpenIndex implements client.IndexStateChanger via /{index}/_open.
func (c *Client) OpenIndex(ctx context.Context, req types.IndexStateChangeRequest) (types.IndexStateChange, error) {
	o := c.api.Indices.Open
	indices := esopsclient.SplitCSV(req.Index)
	opts := []func(*esapi.IndicesOpenRequest){o.WithContext(ctx)}
	if req.IgnoreUnavailable {
		opts = append(opts, o.WithIgnoreUnavailable(true))
	}
	if req.WaitForActiveShards != "" {
		opts = append(opts, o.WithWaitForActiveShards(req.WaitForActiveShards))
	}

	resp, err := c.api.Indices.Open(indices, opts...)
	if err != nil {
		return types.IndexStateChange{}, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("indices open", resp.StatusCode, resp.Body); err != nil {
		return types.IndexStateChange{}, err
	}

	var wire indexStateChangeWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.IndexStateChange{}, errors.Join(errors.New("decoding indices open response"), err)
	}
	return types.IndexStateChange{
		Action:             types.IndexStateOpen,
		Acknowledged:       wire.Acknowledged,
		ShardsAcknowledged: wire.ShardsAcknowledged,
	}, nil
}

// CloseIndex implements client.IndexStateChanger via /{index}/_close.
func (c *Client) CloseIndex(ctx context.Context, req types.IndexStateChangeRequest) (types.IndexStateChange, error) {
	cl := c.api.Indices.Close
	indices := esopsclient.SplitCSV(req.Index)
	opts := []func(*esapi.IndicesCloseRequest){cl.WithContext(ctx)}
	if req.IgnoreUnavailable {
		opts = append(opts, cl.WithIgnoreUnavailable(true))
	}
	if req.WaitForActiveShards != "" {
		opts = append(opts, cl.WithWaitForActiveShards(req.WaitForActiveShards))
	}

	resp, err := c.api.Indices.Close(indices, opts...)
	if err != nil {
		return types.IndexStateChange{}, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("indices close", resp.StatusCode, resp.Body); err != nil {
		return types.IndexStateChange{}, err
	}

	var wire indexStateChangeWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.IndexStateChange{}, errors.Join(errors.New("decoding indices close response"), err)
	}
	return types.IndexStateChange{
		Action:             types.IndexStateClose,
		Acknowledged:       wire.Acknowledged,
		ShardsAcknowledged: wire.ShardsAcknowledged,
	}, nil
}

// forcemergeWire mirrors a /_forcemerge response. Both ES and OS use
// the standard {_shards:{...}} shape with an optional failures[] block.
// Each failure carries a free-form reason from the cluster — kept as
// json.RawMessage on the wire so we can render it to the operator
// without baking version-specific parsing into esops.
type forcemergeWire struct {
	Shards struct {
		Total      int `json:"total"`
		Successful int `json:"successful"`
		Failed     int `json:"failed"`
		Failures   []struct {
			Index  string          `json:"index"`
			Shard  int             `json:"shard"`
			Status string          `json:"status"`
			Reason json.RawMessage `json:"reason"`
		} `json:"failures"`
	} `json:"_shards"`
}

// ForceMerge implements client.IndexOptimizer via /{index}/_forcemerge.
//
// The cluster has no native dry-run for this endpoint; the business
// layer implements --dry-run client-side by resolving the pattern via
// IndexInspector and reporting the would-be merge without firing the
// call. Implementations therefore only need to execute the live merge.
func (c *Client) ForceMerge(ctx context.Context, req types.IndexOptimizeRequest) (types.IndexOptimizeResult, error) {
	fm := c.api.Indices.Forcemerge
	indices := esopsclient.SplitCSV(req.Index)
	opts := []func(*esapi.IndicesForcemergeRequest){fm.WithContext(ctx)}
	if len(indices) > 0 {
		opts = append(opts, fm.WithIndex(indices...))
	}
	if req.MaxNumSegments > 0 {
		opts = append(opts, fm.WithMaxNumSegments(req.MaxNumSegments))
	}
	if req.OnlyExpungeDeletes {
		opts = append(opts, fm.WithOnlyExpungeDeletes(true))
	}
	if req.Flush != nil {
		opts = append(opts, fm.WithFlush(*req.Flush))
	}
	if req.IgnoreUnavailable {
		opts = append(opts, fm.WithIgnoreUnavailable(true))
	}

	resp, err := c.api.Indices.Forcemerge(opts...)
	if err != nil {
		return types.IndexOptimizeResult{}, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("indices forcemerge", resp.StatusCode, resp.Body); err != nil {
		return types.IndexOptimizeResult{}, err
	}

	var wire forcemergeWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.IndexOptimizeResult{}, errors.Join(errors.New("decoding indices forcemerge response"), err)
	}

	out := types.IndexOptimizeResult{
		Action:        "optimize",
		ShardsTotal:   wire.Shards.Total,
		ShardsSuccess: wire.Shards.Successful,
		ShardsFailed:  wire.Shards.Failed,
	}
	for _, f := range wire.Shards.Failures {
		out.Failures = append(out.Failures, fmt.Sprintf("%s[%d] %s: %s", f.Index, f.Shard, f.Status, string(f.Reason)))
	}
	return out, nil
}

// rolloverWire mirrors a /<alias>/_rollover response. ES and OS both
// return the same envelope: acknowledged + shards_acknowledged +
// old_index/new_index + rolled_over + dry_run + a per-condition
// outcome map.
type rolloverWire struct {
	Acknowledged       bool            `json:"acknowledged"`
	ShardsAcknowledged bool            `json:"shards_acknowledged"`
	OldIndex           string          `json:"old_index"`
	NewIndex           string          `json:"new_index"`
	RolledOver         bool            `json:"rolled_over"`
	DryRun             bool            `json:"dry_run"`
	Conditions         map[string]bool `json:"conditions"`
}

// Rollover implements client.IndexRollover via /<alias>/_rollover.
// The body shape is {"conditions":{...}}; an empty conditions map is
// the unconditional form (the cluster always rolls over). The business
// layer guards against unintentional unconditional calls; this adapter
// just serialises whatever the request describes.
func (c *Client) Rollover(ctx context.Context, req types.IndexRolloverRequest) (types.IndexRolloverResult, error) {
	body, err := buildRolloverBody(req.Conditions)
	if err != nil {
		return types.IndexRolloverResult{}, err
	}

	ro := c.api.Indices.Rollover
	opts := []func(*esapi.IndicesRolloverRequest){
		ro.WithContext(ctx),
		ro.WithBody(bytes.NewReader(body)),
	}
	if req.NewIndex != "" {
		opts = append(opts, ro.WithNewIndex(req.NewIndex))
	}
	if req.DryRun {
		opts = append(opts, ro.WithDryRun(true))
	}
	if req.WaitForActiveShards != "" {
		opts = append(opts, ro.WithWaitForActiveShards(req.WaitForActiveShards))
	}

	resp, err := c.api.Indices.Rollover(req.Alias, opts...)
	if err != nil {
		return types.IndexRolloverResult{}, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("indices rollover", resp.StatusCode, resp.Body); err != nil {
		return types.IndexRolloverResult{}, err
	}

	var wire rolloverWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.IndexRolloverResult{}, errors.Join(errors.New("decoding indices rollover response"), err)
	}
	return types.IndexRolloverResult{
		Alias:              req.Alias,
		OldIndex:           wire.OldIndex,
		NewIndex:           wire.NewIndex,
		RolledOver:         wire.RolledOver,
		DryRun:             wire.DryRun || req.DryRun,
		Acknowledged:       wire.Acknowledged,
		ShardsAcknowledged: wire.ShardsAcknowledged,
		ConditionsMet:      wire.Conditions,
	}, nil
}

// buildRolloverBody serialises a conditions block into the cluster's
// expected wire shape. An empty conditions struct produces
// {"conditions":{}} — the unconditional form the cluster also accepts.
// Kept here (rather than a shared helper) so a future per-dialect
// divergence in conditions naming is a per-package change.
func buildRolloverBody(c types.IndexRolloverConditions) ([]byte, error) {
	conditions := make(map[string]any)
	if c.MaxAge != "" {
		conditions["max_age"] = c.MaxAge
	}
	if c.MaxDocs > 0 {
		conditions["max_docs"] = c.MaxDocs
	}
	if c.MaxSize != "" {
		conditions["max_size"] = c.MaxSize
	}
	if c.MaxPrimarySize != "" {
		conditions["max_primary_shard_size"] = c.MaxPrimarySize
	}
	return json.Marshal(map[string]any{"conditions": conditions})
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

// HotThreads implements client.HotThreadsInspector against the typed
// Nodes.HotThreads endpoint. The response is text, not JSON — we read it
// verbatim into types.HotThreads.Text so the caller sees exactly what
// the cluster produced. All four sampling types (cpu/wait/block/mem)
// pass through.
func (c *Client) HotThreads(ctx context.Context, req types.HotThreadsRequest) (types.HotThreads, error) {
	ht := c.api.Nodes.HotThreads
	opts := []func(*esapi.NodesHotThreadsRequest){
		ht.WithContext(ctx),
	}
	if len(req.Nodes) > 0 {
		opts = append(opts, ht.WithNodeID(req.Nodes...))
	}
	if req.Threads != nil {
		opts = append(opts, ht.WithThreads(*req.Threads))
	}
	if req.Interval > 0 {
		opts = append(opts, ht.WithInterval(req.Interval))
	}
	if req.Snapshots != nil {
		opts = append(opts, ht.WithSnapshots(*req.Snapshots))
	}
	if req.Type != "" {
		opts = append(opts, ht.WithDocumentType(req.Type))
	}
	if req.IgnoreIdleThreads != nil {
		opts = append(opts, ht.WithIgnoreIdleThreads(*req.IgnoreIdleThreads))
	}

	resp, err := c.api.Nodes.HotThreads(opts...)
	if err != nil {
		return types.HotThreads{}, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("nodes hot_threads", resp.StatusCode, resp.Body); err != nil {
		return types.HotThreads{}, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return types.HotThreads{}, errors.Join(errors.New("reading hot_threads response"), err)
	}
	return types.HotThreads{Text: string(body)}, nil
}

// DoGET implements client.ClusterDiagnoser by delegating to the ES
// client's BaseClient.Perform — the same path every typed API method
// uses, so auth/transport/retries match the rest of the adapter. The
// product-check header is already satisfied by the first typed call in
// the process, so raw calls layered on after a successful probe don't
// re-trigger it.
//
// 401/403 are translated to the sentinel errors. Transport failures are
// wrapped with ErrUnreachable. Other non-2xx responses return the body
// and status so the diag collector can record them verbatim — a 404 on
// /_plugins/_ism/... is expected data on ES, not a bundle failure.
func (c *Client) DoGET(ctx context.Context, path string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.api.Perform(req)
	if err != nil {
		return nil, 0, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return body, resp.StatusCode, esopsclient.ErrAuth
	case http.StatusForbidden:
		return body, resp.StatusCode, esopsclient.ErrForbidden
	}
	return body, resp.StatusCode, nil
}

// rerouteResponseWire mirrors the /_cluster/reroute response. The cluster
// also returns a "state" object (full routing table on older ES, omitted
// on 9.x with metric=none) — we don't decode it: the operator-facing
// view is acknowledged + explanations, and dragging the routing table
// through the adapter would bloat the binary for no surfaced value.
type rerouteResponseWire struct {
	Acknowledged bool                         `json:"acknowledged"`
	Explanations []rerouteExplanationWire     `json:"explanations,omitempty"`
}

type rerouteExplanationWire struct {
	Command   string                  `json:"command"`
	Decisions []rerouteDecisionWire   `json:"decisions,omitempty"`
}

type rerouteDecisionWire struct {
	Decider     string `json:"decider"`
	Decision    string `json:"decision"`
	Explanation string `json:"explanation,omitempty"`
}

// Reroute implements client.RerouteExecutor against ES's
// /_cluster/reroute. Body shape: {"commands":[{"<action>": {<args>}}, ...]}.
// retry_failed/dry_run/explain are query params; metric=none keeps the
// response small (we don't surface the routing table).
//
// When req.RetryFailed is true and Commands is empty, the body is
// {"commands":[]} — that's a valid request shape and lets the operator
// kick the master into retrying max-retried allocations without naming
// any specific shard.
func (c *Client) Reroute(ctx context.Context, req types.RerouteRequest) (types.RerouteResult, error) {
	body, err := buildRerouteBody(req.Commands)
	if err != nil {
		return types.RerouteResult{}, err
	}

	rr := c.api.Cluster.Reroute
	opts := []func(*esapi.ClusterRerouteRequest){
		rr.WithContext(ctx),
		rr.WithBody(bytes.NewReader(body)),
		// metric=none drops the routing-table dump from the response —
		// we don't surface it and it can be MB on large clusters.
		rr.WithMetric("none"),
	}
	if req.DryRun {
		opts = append(opts, rr.WithDryRun(true))
	}
	if req.Explain {
		opts = append(opts, rr.WithExplain(true))
	}
	if req.RetryFailed {
		opts = append(opts, rr.WithRetryFailed(true))
	}

	resp, err := c.api.Cluster.Reroute(opts...)
	if err != nil {
		return types.RerouteResult{}, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cluster reroute", resp.StatusCode, resp.Body); err != nil {
		return types.RerouteResult{}, err
	}

	var wire rerouteResponseWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.RerouteResult{}, errors.Join(errors.New("decoding cluster reroute response"), err)
	}
	return toRerouteResult(req, wire), nil
}

// buildRerouteBody turns a typed command list into the cluster's
// {"commands":[{"<action>": {<args>}}, ...]} envelope. An empty command
// list is allowed — used by the retry_failed-only path — and serialises
// as {"commands":[]} so ES sees a well-formed request body.
//
// Each action's argument set is built explicitly rather than reflecting
// over RerouteCommand: the cluster validates field names and a stray
// extra key (e.g. accept_data_loss on a "move") is a 400. Listing the
// allowed fields per action here is the place to make that contract
// readable.
func buildRerouteBody(cmds []types.RerouteCommand) ([]byte, error) {
	wire := struct {
		Commands []map[string]any `json:"commands"`
	}{Commands: make([]map[string]any, 0, len(cmds))}

	for _, cmd := range cmds {
		args, err := rerouteArgs(cmd)
		if err != nil {
			return nil, err
		}
		wire.Commands = append(wire.Commands, map[string]any{string(cmd.Action): args})
	}
	return json.Marshal(wire)
}

// rerouteArgs returns the argument map for one reroute command, keyed
// for the cluster's expected JSON shape. RerouteRetryFailed is a
// no-command action — the caller should not include it in Commands;
// it's expressed via the retry_failed=true query param instead.
func rerouteArgs(cmd types.RerouteCommand) (map[string]any, error) {
	switch cmd.Action {
	case types.RerouteMove:
		return map[string]any{
			"index":     cmd.Index,
			"shard":     cmd.Shard,
			"from_node": cmd.FromNode,
			"to_node":   cmd.ToNode,
		}, nil
	case types.RerouteCancel:
		args := map[string]any{
			"index": cmd.Index,
			"shard": cmd.Shard,
			"node":  cmd.FromNode,
		}
		if cmd.AllowPrimary {
			args["allow_primary"] = true
		}
		return args, nil
	case types.RerouteAllocateReplica:
		return map[string]any{
			"index": cmd.Index,
			"shard": cmd.Shard,
			"node":  cmd.ToNode,
		}, nil
	case types.RerouteAllocateEmptyPrimary, types.RerouteAllocateStalePrimary:
		return map[string]any{
			"index":             cmd.Index,
			"shard":             cmd.Shard,
			"node":              cmd.ToNode,
			"accept_data_loss":  cmd.AcceptDataLoss,
		}, nil
	case types.RerouteRetryFailed:
		// Defensive: shouldn't reach here — the business layer routes
		// retry_failed through the query param with no command body.
		return nil, fmt.Errorf("retry_failed has no command body; use RerouteRequest.RetryFailed instead")
	default:
		return nil, fmt.Errorf("unsupported reroute action %q", cmd.Action)
	}
}

// toRerouteResult merges the cluster's response with the request's own
// DryRun flag — ES doesn't echo dry_run in the response body, but a
// reader of the JSON output should see both intent and outcome on one
// line. Commands are the original request's, normalised by the business
// layer; the cluster doesn't return them.
func toRerouteResult(req types.RerouteRequest, wire rerouteResponseWire) types.RerouteResult {
	out := types.RerouteResult{
		Acknowledged: wire.Acknowledged,
		DryRun:       req.DryRun,
		Commands:     req.Commands,
	}
	if len(wire.Explanations) > 0 {
		out.Explanations = make([]types.RerouteExplanation, 0, len(wire.Explanations))
		for _, e := range wire.Explanations {
			ex := types.RerouteExplanation{Command: e.Command}
			if len(e.Decisions) > 0 {
				ex.Decisions = make([]types.RerouteDecision, 0, len(e.Decisions))
				for _, d := range e.Decisions {
					ex.Decisions = append(ex.Decisions, types.RerouteDecision{
						Decider:     d.Decider,
						Decision:    d.Decision,
						Explanation: d.Explanation,
					})
				}
			}
			out.Explanations = append(out.Explanations, ex)
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

// securityUserWire mirrors an entry in GET /_security/user. The endpoint
// returns a JSON object keyed by username; we decode the value side
// into this struct. Metadata._reserved flags built-in users (elastic,
// kibana_system, etc.) that ship with an ES distribution.
type securityUserWire struct {
	Username string   `json:"username"`
	Roles    []string `json:"roles"`
	FullName string   `json:"full_name"`
	Email    string   `json:"email"`
	Enabled  bool     `json:"enabled"`
	Metadata struct {
		Reserved bool `json:"_reserved"`
	} `json:"metadata"`
}

// securityRoleWire mirrors an entry in GET /_security/role. The indices
// block is a list of {names, privileges, ...} records; we flatten the
// names across records so the audit view shows "what patterns this role
// touches" without exploding per-privilege detail at the table layer.
type securityRoleWire struct {
	Cluster  []string `json:"cluster"`
	Indices  []struct {
		Names []string `json:"names"`
	} `json:"indices"`
	Metadata struct {
		Reserved bool `json:"_reserved"`
	} `json:"metadata"`
}

// Audit implements client.SecurityAuditor against ES's native security
// realm. Flow:
//
//  1. GET /_security/user. If it 400s with a body indicating security
//     is not enabled, short-circuit to a "security disabled" report —
//     this is the ES-with-xpack.security.enabled=false case, valid and
//     worth surfacing.
//  2. Otherwise decode users, GET /_security/role, GET
//     /_security/role_mapping, GET /_security/api_key (best-effort).
//
// 401/403 on the user endpoint bubble up as ErrAuth/ErrForbidden — if
// the auth user can't even list users, the audit is fundamentally
// uninformative and exiting non-zero is the right answer. 403s on the
// auxiliary endpoints (role_mapping needs `manage_security`, api_key
// needs `manage_api_key`) are recorded as a per-collection note in the
// Status block and the audit continues — partial visibility beats a
// dropped run.
//
// Other 4xx/5xx on auxiliary endpoints also note rather than abort,
// for the same reason: the operator may be on a managed offering that
// disables some endpoints but still has a usable user/role view.
func (c *Client) Audit(ctx context.Context) (types.SecurityReport, error) {
	report := types.SecurityReport{
		Status: types.SecurityStatus{Dialect: string(types.DialectElasticsearch)},
	}

	// Step 1: probe users sequentially. The "security disabled" branch
	// short-circuits the whole audit, so we have to know that before
	// firing roles/role_mappings/api_keys — which would all 4xx with
	// the same disabled body and waste round-trips.
	users, note, disabled, err := c.fetchESUsers(ctx)
	if err != nil {
		return report, err
	}
	if disabled {
		report.Status.Note = note
		return report, nil
	}
	report.Status.Enabled = true
	report.Users = users

	// Step 2: roles + role_mappings + api_keys in parallel — they're
	// independent reads of the same security backend. On a real cluster
	// each is ~50–200ms; serial costs ~4× that on every audit.
	var (
		roles    []types.SecurityRole
		mappings []types.SecurityRoleMapping
		keys     []types.SecurityAPIKey
		mNote    string
		kNote    string
	)
	err = esopsclient.RunParallel(
		func() error {
			r, e := c.fetchESRoles(ctx)
			roles = r
			return e
		},
		func() error {
			m, n, e := c.fetchESRoleMappings(ctx)
			mappings, mNote = m, n
			return e
		},
		func() error {
			k, n, e := c.fetchESAPIKeys(ctx)
			keys, kNote = k, n
			return e
		},
	)
	if err != nil {
		return report, err
	}
	report.Roles = roles
	report.RoleMappings = mappings
	report.APIKeys = keys
	if mNote != "" {
		report.Status.Notes = append(report.Status.Notes, mNote)
	}
	if kNote != "" {
		report.Status.Notes = append(report.Status.Notes, kNote)
	}
	return report, nil
}

// fetchESUsers returns (users, note, disabled, err). When disabled is
// true, the cluster has security turned off and note carries the human
// explanation — the caller should emit a "security disabled" report
// rather than an error.
func (c *Client) fetchESUsers(ctx context.Context) ([]types.SecurityUser, string, bool, error) {
	resp, err := c.api.Security.GetUser(c.api.Security.GetUser.WithContext(ctx))
	if err != nil {
		return nil, "", false, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, "", false, esopsclient.ClassifyStatus("security get user", resp.StatusCode, nil)
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		if isSecurityDisabledBody(raw) {
			return nil, securityDisabledNote(raw), true, nil
		}
		return nil, "", false, esopsclient.ClassifyStatus("security get user", resp.StatusCode, raw)
	}

	var raw map[string]securityUserWire
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, "", false, errors.Join(errors.New("decoding security users response"), err)
	}
	out := make([]types.SecurityUser, 0, len(raw))
	for name, u := range raw {
		username := u.Username
		if username == "" {
			username = name
		}
		out = append(out, types.SecurityUser{
			Username: username,
			Enabled:  u.Enabled,
			Reserved: u.Metadata.Reserved,
			Roles:    u.Roles,
			FullName: u.FullName,
			Email:    u.Email,
		})
	}
	return out, "", false, nil
}

func (c *Client) fetchESRoles(ctx context.Context) ([]types.SecurityRole, error) {
	resp, err := c.api.Security.GetRole(c.api.Security.GetRole.WithContext(ctx))
	if err != nil {
		return nil, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("security get role", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var raw map[string]securityRoleWire
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, errors.Join(errors.New("decoding security roles response"), err)
	}

	out := make([]types.SecurityRole, 0, len(raw))
	for name, r := range raw {
		patterns := make([]string, 0)
		for _, ix := range r.Indices {
			patterns = append(patterns, ix.Names...)
		}
		out = append(out, types.SecurityRole{
			Name:              name,
			Reserved:          r.Metadata.Reserved,
			ClusterPrivileges: r.Cluster,
			IndexPatterns:     esopsclient.UniqueStrings(patterns),
		})
	}
	return out, nil
}

// roleMappingWire mirrors a single GET /_security/role_mapping entry.
// Roles is the list of granted product roles. Rules is the constraint
// tree (any/all/field) — too varied to surface meaningfully in a flat
// audit row, so we extract the leaf identifiers (usernames, groups,
// realms) into the flat lists Users/BackendRoles/Hosts via flattenRoleMappingRules.
type roleMappingWire struct {
	Enabled  bool            `json:"enabled"`
	Roles    []string        `json:"roles"`
	Rules    json.RawMessage `json:"rules"`
	Metadata struct {
		Reserved bool `json:"_reserved"`
	} `json:"metadata"`
}

// fetchESRoleMappings reads /_security/role_mapping. A 403 (caller
// lacks `manage_security`) returns (nil, "role_mappings: forbidden — needs manage_security cluster privilege", nil)
// so the audit continues with what's available.
func (c *Client) fetchESRoleMappings(ctx context.Context) ([]types.SecurityRoleMapping, string, error) {
	body, status, err := c.doRawGET(ctx, "/_security/role_mapping")
	if err != nil {
		return nil, "", err
	}
	switch status {
	case http.StatusForbidden:
		return nil, "role_mappings: forbidden — needs manage_security cluster privilege", nil
	case http.StatusUnauthorized:
		return nil, "", esopsclient.ErrAuth
	}
	if status >= 400 {
		return nil, fmt.Sprintf("role_mappings: HTTP %d (%s)", status, snippet(body)), nil
	}

	var raw map[string]roleMappingWire
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, "", errors.Join(errors.New("decoding role_mapping response"), err)
	}
	out := make([]types.SecurityRoleMapping, 0, len(raw))
	for name, m := range raw {
		users, backendRoles, hosts := flattenRoleMappingRules(m.Rules)
		out = append(out, types.SecurityRoleMapping{
			Name:         name,
			Reserved:     m.Metadata.Reserved,
			Enabled:      m.Enabled,
			Roles:        m.Roles,
			BackendRoles: backendRoles,
			Users:        users,
			Hosts:        hosts,
		})
	}
	return out, "", nil
}

// flattenRoleMappingRules walks a /_security/role_mapping rules tree
// and pulls out the leaf identifiers an operator wants to see at a
// glance: usernames, group names, hostnames. The cluster's grammar
// supports nested any/all/except clauses; we don't reproduce that
// boolean structure — listing every identifier the rule could match
// is the right shape for an audit ("does any mapping name 'alice'?").
func flattenRoleMappingRules(rules json.RawMessage) (users, backendRoles, hosts []string) {
	var node map[string]json.RawMessage
	if err := json.Unmarshal(rules, &node); err != nil || len(node) == 0 {
		return nil, nil, nil
	}
	var walk func(json.RawMessage)
	walk = func(raw json.RawMessage) {
		var n map[string]json.RawMessage
		if err := json.Unmarshal(raw, &n); err != nil {
			return
		}
		for k, v := range n {
			switch k {
			case "any", "all":
				var children []json.RawMessage
				if err := json.Unmarshal(v, &children); err == nil {
					for _, c := range children {
						walk(c)
					}
				}
			case "except":
				walk(v)
			case "field":
				var field map[string]json.RawMessage
				if err := json.Unmarshal(v, &field); err != nil {
					continue
				}
				for fk, fv := range field {
					values := decodeStringOrSlice(fv)
					switch fk {
					case "username":
						users = append(users, values...)
					case "groups":
						backendRoles = append(backendRoles, values...)
					case "dn":
						users = append(users, values...)
					case "metadata.host", "host":
						hosts = append(hosts, values...)
					}
				}
			}
		}
	}
	walk(rules)
	return esopsclient.UniqueStrings(users), esopsclient.UniqueStrings(backendRoles), esopsclient.UniqueStrings(hosts)
}

// decodeStringOrSlice handles the two shapes a `field` value can take:
// a single string ("alice") or a list (["alice","bob"]). Returns the
// parsed list either way.
func decodeStringOrSlice(raw json.RawMessage) []string {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}
	}
	var multi []string
	if err := json.Unmarshal(raw, &multi); err == nil {
		return multi
	}
	return nil
}

// apiKeyWire mirrors one entry from /_security/api_key. The cluster
// returns wrapped {"api_keys":[...]}; we decode that envelope, then
// each entry. Times are unix-millis ints in the wire format; we
// render them as RFC3339 strings on the way out so the JSON schema
// stays operator-readable.
type apiKeyWire struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Username      string `json:"username"`
	Realm         string `json:"realm"`
	Invalidated   bool   `json:"invalidated"`
	CreationMS    int64  `json:"creation"`
	ExpirationMS  int64  `json:"expiration"`
}

// fetchESAPIKeys reads /_security/api_key. The endpoint requires
// `manage_api_key` cluster privilege; a 403 records a status note and
// returns no keys. Invalidated keys are kept in the output — an
// audit specifically wants to see "this key was revoked, who issued
// it, when?".
func (c *Client) fetchESAPIKeys(ctx context.Context) ([]types.SecurityAPIKey, string, error) {
	// owner=false (default) returns all keys visible to the auth user;
	// requires manage_api_key. We deliberately don't restrict to the
	// caller's own keys — `ops audit` is meant to surface the cluster
	// state, not just the running identity's view.
	body, status, err := c.doRawGET(ctx, "/_security/api_key")
	if err != nil {
		return nil, "", err
	}
	switch status {
	case http.StatusForbidden:
		return nil, "api_keys: forbidden — needs manage_api_key cluster privilege", nil
	case http.StatusUnauthorized:
		return nil, "", esopsclient.ErrAuth
	case http.StatusNotFound:
		// Older ES (pre-7.x) didn't expose the listing endpoint. Treat
		// as "not available" rather than a hard failure.
		return nil, "api_keys: endpoint not available on this cluster", nil
	}
	if status >= 400 {
		return nil, fmt.Sprintf("api_keys: HTTP %d (%s)", status, snippet(body)), nil
	}

	var wrap struct {
		APIKeys []apiKeyWire `json:"api_keys"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return nil, "", errors.Join(errors.New("decoding api_key response"), err)
	}
	out := make([]types.SecurityAPIKey, 0, len(wrap.APIKeys))
	for _, k := range wrap.APIKeys {
		out = append(out, types.SecurityAPIKey{
			ID:          k.ID,
			Name:        k.Name,
			Username:    k.Username,
			Realm:       k.Realm,
			Invalidated: k.Invalidated,
			Creation:    formatEpochMillis(k.CreationMS),
			Expiration:  formatEpochMillis(k.ExpirationMS),
		})
	}
	return out, "", nil
}

// doRawGET performs a plain GET via the underlying ES transport — same
// path the typed helpers use, so auth/timeouts/headers match. Used by
// the audit fetchers that don't have a typed wrapper. Body and status
// come back paired so the caller can either decode JSON or surface
// non-2xx detail in a status note.
func (c *Client) doRawGET(ctx context.Context, path string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.api.Perform(req)
	if err != nil {
		return nil, 0, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// formatEpochMillis turns an ES millisecond timestamp into RFC3339, or
// "" when the cluster did not report the field (zero/negative). Stable
// across ES versions that switched between epoch-ms and ISO strings —
// we always emit the ISO form to scripts.
func formatEpochMillis(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// snippet trims a response body to a short, single-line preview for
// embedding in a Status note. Long error bodies (HTML 502 pages from
// load balancers, multi-line stack traces) get clipped so the note
// stays readable in a table cell.
func snippet(body []byte) string {
	const max = 120
	s := strings.TrimSpace(string(body))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		s = s[:max-1] + "…"
	}
	return s
}

// isSecurityDisabledBody sniffs an ES error body for the signals that
// mean "this cluster has no security feature registered", without which
// there's nothing for `ops audit` to report. Two flavours seen in the
// wild:
//
//   - "Security must be explicitly enabled" / "security is not enabled"
//     (returned by older/licensed paths when the feature exists but is off)
//   - "no handler found for uri [/_security/..." (ES 9.3 with
//     xpack.security.enabled=false — the handler isn't registered at all)
//
// Matched case-insensitively so minor version tweaks don't regress the
// detection. The classifier is intentionally conservative: on a match we
// return "security disabled" as the audit outcome; a miss bubbles up as a
// generic error with the body in the message, which is the right
// behaviour for unknown 4xx/5xx.
func isSecurityDisabledBody(body []byte) bool {
	lower := strings.ToLower(string(body))
	switch {
	case strings.Contains(lower, "security must be explicitly enabled"):
		return true
	case strings.Contains(lower, "security is not enabled"):
		return true
	case strings.Contains(lower, "no handler found") && strings.Contains(lower, "/_security/"):
		return true
	case strings.Contains(lower, "security_exception") && strings.Contains(lower, "disabled"):
		return true
	}
	return false
}

// securityDisabledNote extracts the cluster's own reason string from an
// ES error body so the audit output quotes the cluster rather than
// paraphrasing. Two body shapes: `{"error":{"reason":"..."}}` (the
// typical typed error) and `{"error":"..."}` (the 9.x "no handler found"
// bare-string form). Falls back to a generic message when neither
// matches — surfacing *something* beats going silent.
func securityDisabledNote(body []byte) string {
	var typed struct {
		Error struct {
			Reason string `json:"reason"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &typed); err == nil && typed.Error.Reason != "" {
		return typed.Error.Reason
	}
	var bare struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &bare); err == nil && bare.Error != "" {
		return bare.Error
	}
	return "security is disabled on this cluster"
}

// catNodesRow mirrors a /_cat/nodes?h=name,node.role row. node.role is
// the comma-separated full-name role list (data, data_hot, ingest,
// master, …) — distinct from the deprecated abbreviated `role` column
// which packs the roles into single chars and would force us to maintain
// a translation table per cluster version.
type catNodesRow struct {
	Name string `json:"name"`
	Role string `json:"node.role"`
}

// Nodes implements client.NodeInspector via /_cat/nodes. We surface the
// authoritative node→role map so the analysis layer can identify real
// data nodes — including ones that hold zero shards, which is precisely
// the case the analysis exists to catch.
func (c *Client) Nodes(ctx context.Context) ([]types.Node, error) {
	cn := c.api.Cat.Nodes
	resp, err := c.api.Cat.Nodes(
		cn.WithContext(ctx),
		cn.WithFormat("json"),
		cn.WithH("name", "node.role"),
	)
	if err != nil {
		return nil, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cat nodes", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var rows []catNodesRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, errors.Join(errors.New("decoding cat nodes response"), err)
	}

	out := make([]types.Node, 0, len(rows))
	for _, r := range rows {
		roles := splitNodeRoles(r.Role)
		out = append(out, types.Node{
			Name:       r.Name,
			Roles:      roles,
			IsDataNode: hasDataRole(roles),
		})
	}
	return out, nil
}

// splitNodeRoles parses /_cat/nodes node.role into a clean slice. The
// cluster format is "data,ingest,master" — empty entries (which can
// arise from a coordinating-only node returning "") are dropped so
// hasDataRole's prefix check doesn't accidentally match "".
func splitNodeRoles(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// hasDataRole returns true when any role is "data" or one of the
// tier-aware data_* variants (data_hot / data_warm / data_cold /
// data_content / data_frozen). HasPrefix("data") covers all of them
// in one check.
func hasDataRole(roles []string) bool {
	for _, r := range roles {
		if strings.HasPrefix(r, "data") {
			return true
		}
	}
	return false
}

// shrinkWire mirrors a /<source>/_shrink/<target> response. Both ES
// and OS return the same envelope: acknowledged + shards_acknowledged
// + index (the new target name, echoed back).
type shrinkWire struct {
	Acknowledged       bool   `json:"acknowledged"`
	ShardsAcknowledged bool   `json:"shards_acknowledged"`
	Index              string `json:"index"`
}

// Shrink implements client.IndexShrinker via /<source>/_shrink/<target>.
// Body shape is {settings: {index.number_of_shards, index.number_of_replicas}}
// — aliases on the target are deferred to the separate `index aliases`
// command rather than coupled to shrink, keeping the destructive
// surface narrow.
//
// The cluster has no native dry-run for this endpoint; the business
// layer covers --dry-run client-side. Implementations therefore only
// need to execute the live shrink.
func (c *Client) Shrink(ctx context.Context, req types.IndexShrinkRequest) (types.IndexShrinkResult, error) {
	body, err := buildShrinkBody(req)
	if err != nil {
		return types.IndexShrinkResult{}, err
	}

	sh := c.api.Indices.Shrink
	opts := []func(*esapi.IndicesShrinkRequest){sh.WithContext(ctx)}
	if req.WaitForActiveShards != "" {
		opts = append(opts, sh.WithWaitForActiveShards(req.WaitForActiveShards))
	}

	resp, err := c.api.Indices.Shrink(req.Source, bytes.NewReader(body), req.Target, opts...)
	if err != nil {
		return types.IndexShrinkResult{}, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("indices shrink", resp.StatusCode, resp.Body); err != nil {
		return types.IndexShrinkResult{}, err
	}

	var wire shrinkWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.IndexShrinkResult{}, errors.Join(errors.New("decoding indices shrink response"), err)
	}
	return types.IndexShrinkResult{
		Action:             "shrink",
		Source:             req.Source,
		Target:             req.Target,
		Acknowledged:       wire.Acknowledged,
		ShardsAcknowledged: wire.ShardsAcknowledged,
		TargetShards:       req.TargetShards,
	}, nil
}

// buildShrinkBody renders the JSON body the cluster expects on a
// shrink call. We always set a non-empty settings block when the
// operator named TargetShards or TargetReplicas — an empty settings
// block is also valid (the cluster falls back to defaults), which is
// what an unconditional `--shards 0` produces.
//
// CopySettings is not in the body — the cluster honours it as the
// ?copy_settings=true query param. The ES typed client doesn't expose
// a typed setter for it; rather than reaching for a raw query string
// here, we surface the knob in the request type and document it as a
// future add when an operator asks. The default (false) is what
// scripts already expect.
func buildShrinkBody(req types.IndexShrinkRequest) ([]byte, error) {
	settings := make(map[string]any)
	if req.TargetShards > 0 {
		settings["index.number_of_shards"] = req.TargetShards
	}
	if req.TargetReplicas != nil {
		settings["index.number_of_replicas"] = *req.TargetReplicas
	}
	body := map[string]any{}
	if len(settings) > 0 {
		body["settings"] = settings
	}
	return json.Marshal(body)
}

// catAliasesRow mirrors a /_cat/aliases?format=json row. Filter,
// routing, and is_write_index columns return as bare strings on both
// dialects; the cluster uses "-" for "not set" and "true"/"false" for
// the boolean column. We normalise both forms here.
type catAliasesRow struct {
	Alias         string `json:"alias"`
	Index         string `json:"index"`
	Filter        string `json:"filter"`
	RoutingIndex  string `json:"routing.index"`
	RoutingSearch string `json:"routing.search"`
	IsWriteIndex  string `json:"is_write_index"`
}

// aliasesColumns is the explicit h= selector for /_cat/aliases. _cat
// APIs replace (not extend) the default column set when h is set, so
// every field we surface has to be listed.
var aliasesColumns = []string{
	"alias", "index", "filter",
	"routing.index", "routing.search", "is_write_index",
}

// Aliases implements client.AliasInspector via /_cat/aliases. Both
// dialects accept name (alias) and index path filters; we forward the
// AliasFilter Alias and Index fields verbatim.
func (c *Client) Aliases(ctx context.Context, filter types.AliasFilter) ([]types.Alias, error) {
	ca := c.api.Cat.Aliases
	opts := []func(*esapi.CatAliasesRequest){
		ca.WithContext(ctx),
		ca.WithFormat("json"),
		ca.WithH(aliasesColumns...),
	}
	if filter.Alias != "" {
		opts = append(opts, ca.WithName(esopsclient.SplitCSV(filter.Alias)...))
	}

	resp, err := c.api.Cat.Aliases(opts...)
	if err != nil {
		return nil, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cat aliases", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var rows []catAliasesRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, errors.Join(errors.New("decoding cat aliases response"), err)
	}
	return rowsToAliases(rows, filter.Index), nil
}

// rowsToAliases maps the cat-aliases wire rows into the neutral
// []types.Alias view, dropping the cluster's "-" sentinel for unset
// fields and parsing is_write_index ("true"/"false"/"-") into a bool.
//
// indexFilter is applied client-side: /_cat/aliases takes a name path
// segment for alias filtering but no index segment, so we filter to
// match the AliasFilter contract on both dialects from one place.
func rowsToAliases(rows []catAliasesRow, indexFilter string) []types.Alias {
	wantedIndex := strings.TrimSpace(indexFilter)
	wantedSet := map[string]struct{}{}
	if wantedIndex != "" {
		for _, name := range strings.Split(wantedIndex, ",") {
			n := strings.TrimSpace(name)
			if n != "" {
				wantedSet[n] = struct{}{}
			}
		}
	}

	out := make([]types.Alias, 0, len(rows))
	for _, r := range rows {
		if len(wantedSet) > 0 {
			if _, ok := wantedSet[r.Index]; !ok {
				continue
			}
		}
		out = append(out, types.Alias{
			Alias:         r.Alias,
			Index:         r.Index,
			Filter:        emptyOnDash(r.Filter),
			RoutingIndex:  emptyOnDash(r.RoutingIndex),
			RoutingSearch: emptyOnDash(r.RoutingSearch),
			IsWriteIndex:  parseClusterBool(r.IsWriteIndex),
		})
	}
	return out
}

// emptyOnDash maps the cluster's "-" sentinel to an empty string so
// the JSON shape uses omitempty cleanly. Other values pass through
// unchanged.
func emptyOnDash(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

// parseClusterBool maps the cluster's tri-state "true"/"false"/"-"
// (or "") into a Go bool. Anything other than a literal "true" maps
// to false — matching the cluster's own semantics for the
// is_write_index column.
func parseClusterBool(s string) bool {
	return strings.EqualFold(strings.TrimSpace(s), "true")
}

// updateAliasesWire mirrors the POST /_aliases response. Both dialects
// return only the acknowledged flag.
type updateAliasesWire struct {
	Acknowledged bool `json:"acknowledged"`
}

// UpdateAliases implements client.AliasUpdater via POST /_aliases.
// Body shape: {"actions":[{"<add|remove>":{<args>}}, ...]}.
//
// The cluster has no native dry-run for /_aliases; the business layer
// implements --dry-run client-side as "validate + report" so the
// adapter only executes live calls.
func (c *Client) UpdateAliases(ctx context.Context, req types.AliasUpdateRequest) (types.AliasUpdateResult, error) {
	body, err := buildUpdateAliasesBody(req.Actions)
	if err != nil {
		return types.AliasUpdateResult{}, err
	}

	ua := c.api.Indices.UpdateAliases
	resp, err := c.api.Indices.UpdateAliases(bytes.NewReader(body), ua.WithContext(ctx))
	if err != nil {
		return types.AliasUpdateResult{}, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("indices update_aliases", resp.StatusCode, resp.Body); err != nil {
		return types.AliasUpdateResult{}, err
	}

	var wire updateAliasesWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.AliasUpdateResult{}, errors.Join(errors.New("decoding update_aliases response"), err)
	}
	return types.AliasUpdateResult{
		Action:       "alias-update",
		Acknowledged: wire.Acknowledged,
		Actions:      req.Actions,
	}, nil
}

// buildUpdateAliasesBody renders the wire shape POST /_aliases expects.
// Per-action argument keys (alias/index/filter/routing/is_write_index)
// are written explicitly rather than reflecting over AliasUpdateAction
// — the cluster validates field names and a stray extra key is a 400,
// so listing the allowed set per action keeps the contract readable
// here.
//
// Filter is parsed from the operator-supplied JSON string and embedded
// as a JSON object, not a string — the cluster expects an object on
// the wire. An empty Filter is omitted from the body.
func buildUpdateAliasesBody(actions []types.AliasUpdateAction) ([]byte, error) {
	wire := struct {
		Actions []map[string]any `json:"actions"`
	}{Actions: make([]map[string]any, 0, len(actions))}

	for _, a := range actions {
		args, err := aliasActionArgs(a)
		if err != nil {
			return nil, err
		}
		wire.Actions = append(wire.Actions, map[string]any{string(a.Action): args})
	}
	return json.Marshal(wire)
}

// aliasActionArgs returns the arg map for one alias action, keyed for
// the cluster's expected JSON shape. Add carries the optional knobs
// (filter/routing/is_write_index); remove only carries alias+index.
func aliasActionArgs(a types.AliasUpdateAction) (map[string]any, error) {
	switch a.Action {
	case types.AliasActionAdd:
		args := map[string]any{
			"index": a.Index,
			"alias": a.Alias,
		}
		if a.Filter != "" {
			var f any
			if err := json.Unmarshal([]byte(a.Filter), &f); err != nil {
				return nil, fmt.Errorf("invalid alias filter JSON: %w", err)
			}
			args["filter"] = f
		}
		if a.Routing != "" {
			args["routing"] = a.Routing
		}
		if a.IsWriteIndex != nil {
			args["is_write_index"] = *a.IsWriteIndex
		}
		return args, nil
	case types.AliasActionRemove:
		return map[string]any{
			"index": a.Index,
			"alias": a.Alias,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported alias action %q (expected: add, remove)", a.Action)
	}
}
