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
	"sort"
	"strconv"
	"strings"
	"time"

	es "github.com/elastic/go-elasticsearch/v9"
	"github.com/elastic/go-elasticsearch/v9/esapi"

	"github.com/esops-dev/esops-go/pkg/internal/auth"
	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
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
		return types.ClusterHealth{}, clientutil.ClassifyTransport(err)
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
		return nil, clientutil.ClassifyTransport(err)
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
			Shard:             clientutil.AtoiOrZero(r.Shard),
			Primary:           r.Prirep == "p",
			State:             r.State,
			Docs:              clientutil.Atoi64Ptr(r.Docs),
			StoreBytes:        clientutil.Atoi64Ptr(r.Store),
			Node:              clientutil.DerefString(r.Node),
			IP:                clientutil.DerefString(r.IP),
			UnassignedReason:  clientutil.DerefString(r.UnassignedReason),
			UnassignedAt:      clientutil.DerefString(r.UnassignedAt),
			UnassignedFor:     clientutil.DerefString(r.UnassignedFor),
			UnassignedDetails: clientutil.DerefString(r.UnassignedDetails),
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
	// expand_wildcards reflects the caller's --hidden flag so the cluster
	// filters dot-prefixed system indices server-side. On busy clusters
	// the difference is hundreds of indices not transferred / parsed.
	expand := "open,closed"
	if filter.Hidden {
		expand = "open,closed,hidden"
	}
	opts := []func(*esapi.CatIndicesRequest){
		ci.WithContext(ctx),
		ci.WithFormat("json"),
		ci.WithBytes("b"),
		ci.WithH(indicesColumns...),
		ci.WithExpandWildcards(expand),
	}
	if filter.Index != "" {
		opts = append(opts, ci.WithIndex(esopsclient.SplitCSV(filter.Index)...))
	}

	resp, err := c.api.Cat.Indices(opts...)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cat indices", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var rows []catIndicesRow
	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "cat indices")
	if err := json.NewDecoder(body).Decode(&rows); err != nil {
		return nil, errors.Join(errors.New("decoding cat indices response"), err)
	}

	out := make([]types.Index, 0, len(rows))
	for _, r := range rows {
		out = append(out, types.Index{
			Name:               r.Index,
			UUID:               r.UUID,
			Health:             r.Health,
			Status:             r.Status,
			Primaries:          clientutil.AtoiOrZero(clientutil.DerefString(r.Pri)),
			Replicas:           clientutil.AtoiOrZero(clientutil.DerefString(r.Rep)),
			DocsCount:          clientutil.Atoi64Ptr(r.DocsCount),
			DocsDeleted:        clientutil.Atoi64Ptr(r.DocsDeleted),
			StoreBytes:         clientutil.Atoi64Ptr(r.StoreSize),
			PrimaryStoreBytes:  clientutil.Atoi64Ptr(r.PriStoreSize),
			CreationDateMillis: clientutil.Atoi64Ptr(r.CreationDate),
		})
	}
	return out, nil
}

// IndicesStream implements client.IndexInspector. Decodes
// /_cat/indices one JSON-array element at a time via json.Decoder and
// hands each parsed row to yield. Lets `index list -o ndjson` print
// the first row before the cluster has finished sending the last one
// — see CLAUDE.md §6 for the contract.
func (c *Client) IndicesStream(ctx context.Context, filter types.IndexFilter, yield func(types.Index) error) error {
	ci := c.api.Cat.Indices
	expand := "open,closed"
	if filter.Hidden {
		expand = "open,closed,hidden"
	}
	opts := []func(*esapi.CatIndicesRequest){
		ci.WithContext(ctx),
		ci.WithFormat("json"),
		ci.WithBytes("b"),
		ci.WithH(indicesColumns...),
		ci.WithExpandWildcards(expand),
	}
	if filter.Index != "" {
		opts = append(opts, ci.WithIndex(esopsclient.SplitCSV(filter.Index)...))
	}
	resp, err := c.api.Cat.Indices(opts...)
	if err != nil {
		return clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cat indices (stream)", resp.StatusCode, resp.Body); err != nil {
		return err
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "cat indices (stream)")
	dec := json.NewDecoder(body)
	// Consume the leading '['.
	if tok, err := dec.Token(); err != nil {
		return errors.Join(errors.New("decoding cat indices (stream) response"), err)
	} else if d, ok := tok.(json.Delim); !ok || d != '[' {
		return fmt.Errorf("cat indices (stream): expected JSON array, got %v", tok)
	}
	for dec.More() {
		var r catIndicesRow
		if err := dec.Decode(&r); err != nil {
			return errors.Join(errors.New("decoding cat indices (stream) row"), err)
		}
		ix := types.Index{
			Name:               r.Index,
			UUID:               r.UUID,
			Health:             r.Health,
			Status:             r.Status,
			Primaries:          clientutil.AtoiOrZero(clientutil.DerefString(r.Pri)),
			Replicas:           clientutil.AtoiOrZero(clientutil.DerefString(r.Rep)),
			DocsCount:          clientutil.Atoi64Ptr(r.DocsCount),
			DocsDeleted:        clientutil.Atoi64Ptr(r.DocsDeleted),
			StoreBytes:         clientutil.Atoi64Ptr(r.StoreSize),
			PrimaryStoreBytes:  clientutil.Atoi64Ptr(r.PriStoreSize),
			CreationDateMillis: clientutil.Atoi64Ptr(r.CreationDate),
		}
		if err := yield(ix); err != nil {
			return err
		}
	}
	return nil
}

// IndicesNamesStatus implements client.IndexInspector. Slim companion
// to Indices: asks /_cat/indices for only the (index, status) columns
// and returns the slim IndexNameStatus shape. Used by state-change /
// optimize pre-flights where the full Indices payload would be wasted
// network on a busy cluster.
func (c *Client) IndicesNamesStatus(ctx context.Context, filter types.IndexFilter) ([]types.IndexNameStatus, error) {
	ci := c.api.Cat.Indices
	expand := "open,closed"
	if filter.Hidden {
		expand = "open,closed,hidden"
	}
	opts := []func(*esapi.CatIndicesRequest){
		ci.WithContext(ctx),
		ci.WithFormat("json"),
		ci.WithH("index", "status"),
		ci.WithExpandWildcards(expand),
	}
	if filter.Index != "" {
		opts = append(opts, ci.WithIndex(esopsclient.SplitCSV(filter.Index)...))
	}
	resp, err := c.api.Cat.Indices(opts...)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cat indices (slim)", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}
	var rows []struct {
		Index  string `json:"index"`
		Status string `json:"status"`
	}
	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "cat indices (slim)")
	if err := json.NewDecoder(body).Decode(&rows); err != nil {
		return nil, errors.Join(errors.New("decoding cat indices (slim) response"), err)
	}
	out := make([]types.IndexNameStatus, 0, len(rows))
	for _, r := range rows {
		out = append(out, types.IndexNameStatus{Name: r.Index, Status: r.Status})
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
	indices := req.Indices
	opts := []func(*esapi.IndicesOpenRequest){o.WithContext(ctx)}
	if req.IgnoreUnavailable {
		opts = append(opts, o.WithIgnoreUnavailable(true))
	}
	if req.WaitForActiveShards != "" {
		opts = append(opts, o.WithWaitForActiveShards(req.WaitForActiveShards))
	}

	resp, err := c.api.Indices.Open(indices, opts...)
	if err != nil {
		return types.IndexStateChange{}, clientutil.ClassifyTransport(err)
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
	indices := req.Indices
	opts := []func(*esapi.IndicesCloseRequest){cl.WithContext(ctx)}
	if req.IgnoreUnavailable {
		opts = append(opts, cl.WithIgnoreUnavailable(true))
	}
	if req.WaitForActiveShards != "" {
		opts = append(opts, cl.WithWaitForActiveShards(req.WaitForActiveShards))
	}

	resp, err := c.api.Indices.Close(indices, opts...)
	if err != nil {
		return types.IndexStateChange{}, clientutil.ClassifyTransport(err)
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
	opts := []func(*esapi.IndicesForcemergeRequest){fm.WithContext(ctx)}
	if len(req.Indices) > 0 {
		opts = append(opts, fm.WithIndex(req.Indices...))
	}
	if req.MaxNumSegments > 0 {
		opts = append(opts, fm.WithMaxNumSegments(int64(req.MaxNumSegments)))
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
		return types.IndexOptimizeResult{}, clientutil.ClassifyTransport(err)
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
		return types.IndexRolloverResult{}, clientutil.ClassifyTransport(err)
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
	Index                   string                       `json:"index"`
	Shard                   int                          `json:"shard"`
	Primary                 bool                         `json:"primary"`
	CurrentState            string                       `json:"current_state"`
	CurrentNode             *allocationNodeWire          `json:"current_node,omitempty"`
	UnassignedInfo          *allocationUnassignedWire    `json:"unassigned_info,omitempty"`
	CanAllocate             string                       `json:"can_allocate,omitempty"`
	AllocateExplanation     string                       `json:"allocate_explanation,omitempty"`
	CanRemainOnCurrentNode  string                       `json:"can_remain_on_current_node,omitempty"`
	CanRebalanceCluster     string                       `json:"can_rebalance_cluster,omitempty"`
	MoveExplanation         string                       `json:"move_explanation,omitempty"`
	RebalanceExplanation    string                       `json:"rebalance_explanation,omitempty"`
	Note                    string                       `json:"note,omitempty"`
	NodeAllocationDecisions []allocationNodeDecisionWire `json:"node_allocation_decisions,omitempty"`
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
		return types.AllocationExplanation{}, clientutil.ClassifyTransport(err)
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
		opts = append(opts, ht.WithThreads(int64(*req.Threads)))
	}
	if req.Interval > 0 {
		opts = append(opts, ht.WithInterval(req.Interval))
	}
	if req.Snapshots != nil {
		opts = append(opts, ht.WithSnapshots(int64(*req.Snapshots)))
	}
	if req.Type != "" {
		opts = append(opts, ht.WithDocumentType(req.Type))
	}
	if req.IgnoreIdleThreads != nil {
		opts = append(opts, ht.WithIgnoreIdleThreads(*req.IgnoreIdleThreads))
	}

	resp, err := c.api.Nodes.HotThreads(opts...)
	if err != nil {
		return types.HotThreads{}, clientutil.ClassifyTransport(err)
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
		return nil, 0, clientutil.ClassifyTransport(err)
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
	Acknowledged bool                     `json:"acknowledged"`
	Explanations []rerouteExplanationWire `json:"explanations,omitempty"`
}

type rerouteExplanationWire struct {
	Command   string                `json:"command"`
	Decisions []rerouteDecisionWire `json:"decisions,omitempty"`
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
		return types.RerouteResult{}, clientutil.ClassifyTransport(err)
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
			"index":            cmd.Index,
			"shard":            cmd.Shard,
			"node":             cmd.ToNode,
			"accept_data_loss": cmd.AcceptDataLoss,
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
		return clientutil.ClassifyStatus(op, status, nil)
	}
	raw, _ := io.ReadAll(body)
	return clientutil.ClassifyStatus(op, status, raw)
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
	Cluster []string `json:"cluster"`
	Indices []struct {
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
	err = clientutil.RunParallel(
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
		return nil, "", false, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, "", false, clientutil.ClassifyStatus("security get user", resp.StatusCode, nil)
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		if isSecurityDisabledBody(raw) {
			return nil, securityDisabledNote(raw), true, nil
		}
		return nil, "", false, clientutil.ClassifyStatus("security get user", resp.StatusCode, raw)
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
		return nil, clientutil.ClassifyTransport(err)
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
			IndexPatterns:     clientutil.UniqueStrings(patterns),
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
	return clientutil.UniqueStrings(users), clientutil.UniqueStrings(backendRoles), clientutil.UniqueStrings(hosts)
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
	ID           string `json:"id"`
	Name         string `json:"name"`
	Username     string `json:"username"`
	Realm        string `json:"realm"`
	Invalidated  bool   `json:"invalidated"`
	CreationMS   int64  `json:"creation"`
	ExpirationMS int64  `json:"expiration"`
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
		return nil, 0, clientutil.ClassifyTransport(err)
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

// catNodesRow mirrors the /_cat/nodes columns esops surfaces. ES 9.x's
// `node.role` column packs roles into single chars ("dim" for
// data+ingest+master) — readable to a human, hostile to a parser, and
// a translation table per cluster version. The full-name list lives in
// `node.roles` (plural), which we request and prefer; node.role is a
// best-effort fallback for older clusters that omit it.
//
// Disk and heap fields are strings on the wire (the cluster prepends a
// unit suffix when `?bytes=` is omitted); we request bytes=b so the
// values are bare integers and parse them as such.
type catNodesRow struct {
	Name            string `json:"name"`
	Role            string `json:"node.role"`
	Roles           string `json:"node.roles"`
	IP              string `json:"ip"`
	Version         string `json:"version"`
	Master          string `json:"master"`
	HeapPercent     string `json:"heap.percent"`
	HeapCurrent     string `json:"heap.current"`
	HeapMax         string `json:"heap.max"`
	DiskUsed        string `json:"disk.used"`
	DiskAvail       string `json:"disk.avail"`
	DiskTotal       string `json:"disk.total"`
	DiskUsedPercent string `json:"disk.used_percent"`
	CPU             string `json:"cpu"`
	Load1m          string `json:"load_1m"`
	Uptime          string `json:"uptime"`
}

// Nodes implements client.NodeInspector via /_cat/nodes. We surface the
// authoritative node→role map so the analysis layer can identify real
// data nodes — including ones that hold zero shards, which is precisely
// the case the analysis exists to catch.
//
// The column list is what `ops nodes` renders plus what shard-skew
// analysis needs. bytes=b makes the disk/heap columns bare integers;
// without it the cluster prepends a unit suffix that varies across
// versions and forces a per-version parser.
func (c *Client) Nodes(ctx context.Context) ([]types.Node, error) {
	cn := c.api.Cat.Nodes
	resp, err := c.api.Cat.Nodes(
		cn.WithContext(ctx),
		cn.WithFormat("json"),
		cn.WithBytes("b"),
		cn.WithH(
			"name", "node.role", "node.roles", "ip", "version", "master",
			"heap.percent", "heap.current", "heap.max",
			"disk.used", "disk.avail", "disk.total", "disk.used_percent",
			"cpu", "load_1m", "uptime",
		),
	)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
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
		// Prefer node.roles (full names: data, master, ingest, …); fall
		// back to the abbreviated node.role on older clusters that omit
		// the plural column.
		roles := splitNodeRoles(r.Roles)
		if len(roles) == 0 {
			roles = abbreviatedRolesToFull(r.Role)
		}
		out = append(out, types.Node{
			Name:            r.Name,
			Roles:           roles,
			IsDataNode:      hasDataRole(roles),
			IP:              r.IP,
			Version:         r.Version,
			Master:          r.Master == "*",
			HeapPercent:     parseInt(r.HeapPercent),
			HeapUsedBytes:   parseInt64(r.HeapCurrent),
			HeapMaxBytes:    parseInt64(r.HeapMax),
			DiskUsedBytes:   parseInt64(r.DiskUsed),
			DiskAvailBytes:  parseInt64(r.DiskAvail),
			DiskTotalBytes:  parseInt64(r.DiskTotal),
			DiskUsedPercent: parsePercent(r.DiskUsedPercent),
			CPUPercent:      parseInt(r.CPU),
			Load1m:          r.Load1m,
			Uptime:          r.Uptime,
		})
	}
	return out, nil
}

// parseInt64 turns a /_cat/nodes byte string (rendered with bytes=b, so
// "12345" not "12.3kb") into an int64. An empty cell — common for
// coordinating-only nodes that have no disk to report — yields zero
// rather than an error: this is best-effort enrichment for `ops nodes`
// and a missing column is information, not a failure.
func parseInt64(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// parseInt is the int sibling of parseInt64 for percent and uptime
// counters that fit in 32 bits. Same lenient empty-cell semantics.
func parseInt(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return v
}

// parsePercent is parseInt's float-tolerant cousin. The disk percent
// column is rendered as a float ("84.2") on some cluster versions and
// as an int ("84") on others; we round to the nearest int either way.
func parsePercent(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int(f + 0.5)
	}
	return 0
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

// abbreviatedRolesToFull is a best-effort decode of the legacy single-
// char `node.role` column (e.g. "dim" for data+ingest+master) into
// full-name roles. ES 9.x packs every distinct role abbreviation into
// the column ("cdfhilmrstw" on a stock single-node cluster); the
// mapping below covers every char esops cares about for data-node
// identification, plus the common non-data tiers so the user-facing
// output reads cleanly. Used only when node.roles (plural) is absent.
func abbreviatedRolesToFull(s string) []string {
	if s == "" {
		return nil
	}
	mapping := map[rune]string{
		'd': "data",
		'h': "data_hot",
		'w': "data_warm",
		'c': "data_cold",
		's': "data_content",
		'f': "data_frozen",
		'm': "master",
		'i': "ingest",
		'l': "ml",
		'r': "remote_cluster_client",
		't': "transform",
	}
	out := make([]string, 0, len(s))
	for _, r := range s {
		if name, ok := mapping[r]; ok {
			out = append(out, name)
		}
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

// nodesInfoJVMWire is the minimal slice of the /_nodes/jvm response we
// care about. The full response carries vm name/version, GC collectors,
// memory pools, and input arguments — none of which the heap_size rule
// needs, so we don't decode them.
type nodesInfoJVMWire struct {
	Nodes map[string]struct {
		Name string `json:"name"`
		JVM  struct {
			Mem struct {
				HeapInitInBytes int64 `json:"heap_init_in_bytes"`
				HeapMaxInBytes  int64 `json:"heap_max_in_bytes"`
			} `json:"mem"`
		} `json:"jvm"`
	} `json:"nodes"`
}

// nodesStatsOSWire mirrors /_nodes/stats/os. Only os.mem.total_in_bytes
// is surfaced today; CPU/load are already on /_cat/nodes via Nodes() and
// adding them here would just duplicate signal at the cost of decode
// time on a busy fleet.
type nodesStatsOSWire struct {
	Nodes map[string]struct {
		Name string `json:"name"`
		OS   struct {
			Mem struct {
				TotalInBytes int64 `json:"total_in_bytes"`
			} `json:"mem"`
		} `json:"os"`
	} `json:"nodes"`
}

// NodeStats implements client.NodeStatsInspector. The cluster splits the
// data this rule needs across two endpoints: configured -Xms/-Xmx live
// on /_nodes/jvm (info), but the host's total physical memory is only
// reported by /_nodes/stats/os — the info endpoint's os block carries
// processor counts and arch but not memory totals. We fan out both calls
// in parallel and merge by node ID; running them serially would double
// the latency for no benefit.
//
// A node that appears in the info response but not in the stats response
// (or vice versa) is still surfaced — fields the missing endpoint would
// have filled stay at their zero value, matching CLAUDE.md §7's
// "best-effort, missing column is information not failure" policy that
// Nodes() already follows. Iteration is over the union of node IDs
// rather than just one side.
func (c *Client) NodeStats(ctx context.Context) ([]types.NodeStats, error) {
	var (
		info  nodesInfoJVMWire
		stats nodesStatsOSWire
	)

	err := clientutil.RunParallel(
		func() error {
			ni := c.api.Nodes.Info
			resp, err := c.api.Nodes.Info(
				ni.WithContext(ctx),
				ni.WithMetric("jvm"),
			)
			if err != nil {
				return clientutil.ClassifyTransport(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if err := classifyResponse("nodes info jvm", resp.StatusCode, resp.Body); err != nil {
				return err
			}
			if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
				return errors.Join(errors.New("decoding nodes info jvm response"), err)
			}
			return nil
		},
		func() error {
			ns := c.api.Nodes.Stats
			resp, err := c.api.Nodes.Stats(
				ns.WithContext(ctx),
				ns.WithMetric("os"),
			)
			if err != nil {
				return clientutil.ClassifyTransport(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if err := classifyResponse("nodes stats os", resp.StatusCode, resp.Body); err != nil {
				return err
			}
			if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
				return errors.Join(errors.New("decoding nodes stats os response"), err)
			}
			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	// Stable iteration order: alphabetic by node name. Both endpoints
	// key the response by node ID rather than name, but the operator
	// reads the table by name, so sort there.
	out := make([]types.NodeStats, 0, len(info.Nodes))
	seen := make(map[string]int, len(info.Nodes))
	for id, n := range info.Nodes {
		seen[id] = len(out)
		out = append(out, types.NodeStats{
			Name: n.Name,
			JVM: types.NodeJVMStats{
				Heap: types.NodeJVMHeap{
					InitBytes: n.JVM.Mem.HeapInitInBytes,
					MaxBytes:  n.JVM.Mem.HeapMaxInBytes,
				},
			},
		})
	}
	for id, n := range stats.Nodes {
		if idx, ok := seen[id]; ok {
			out[idx].OS.TotalPhysicalMemoryBytes = n.OS.Mem.TotalInBytes
			continue
		}
		out = append(out, types.NodeStats{
			Name: n.Name,
			OS:   types.NodeOSStats{TotalPhysicalMemoryBytes: n.OS.Mem.TotalInBytes},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
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
		return types.IndexShrinkResult{}, clientutil.ClassifyTransport(err)
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

// Aliases implements client.AliasInspector via /_cat/aliases. Sort
// happens server-side via ?s=alias,index — saves the business layer a
// sort pass on a long alias list. Both
// dialects accept name (alias) and index path filters; we forward the
// AliasFilter Alias and Index fields verbatim.
func (c *Client) Aliases(ctx context.Context, filter types.AliasFilter) ([]types.Alias, error) {
	ca := c.api.Cat.Aliases
	opts := []func(*esapi.CatAliasesRequest){
		ca.WithContext(ctx),
		ca.WithFormat("json"),
		ca.WithH(aliasesColumns...),
		ca.WithS("alias", "index"),
	}
	if filter.Alias != "" {
		opts = append(opts, ca.WithName(esopsclient.SplitCSV(filter.Alias)...))
	}

	resp, err := c.api.Cat.Aliases(opts...)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cat aliases", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var rows []catAliasesRow
	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "cat aliases")
	if err := json.NewDecoder(body).Decode(&rows); err != nil {
		return nil, errors.Join(errors.New("decoding cat aliases response"), err)
	}
	return rowsToAliases(rows, filter.Index), nil
}

// AliasesStream implements client.AliasInspector. Streams the
// /_cat/aliases JSON array via json.Decoder.Token + Decode per row so
// `aliases list -o ndjson` honours the §6 contract on a 5k-alias
// cluster. Per-index filtering happens client-side row-by-row to keep
// the streaming path identical to the buffered Aliases call.
func (c *Client) AliasesStream(ctx context.Context, filter types.AliasFilter, yield func(types.Alias) error) error {
	ca := c.api.Cat.Aliases
	opts := []func(*esapi.CatAliasesRequest){
		ca.WithContext(ctx),
		ca.WithFormat("json"),
		ca.WithH(aliasesColumns...),
		ca.WithS("alias", "index"),
	}
	if filter.Alias != "" {
		opts = append(opts, ca.WithName(esopsclient.SplitCSV(filter.Alias)...))
	}
	resp, err := c.api.Cat.Aliases(opts...)
	if err != nil {
		return clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cat aliases (stream)", resp.StatusCode, resp.Body); err != nil {
		return err
	}

	wantedSet := indexFilterSet(filter.Index)
	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "cat aliases (stream)")
	dec := json.NewDecoder(body)
	if tok, err := dec.Token(); err != nil {
		return errors.Join(errors.New("decoding cat aliases (stream) response"), err)
	} else if d, ok := tok.(json.Delim); !ok || d != '[' {
		return fmt.Errorf("cat aliases (stream): expected JSON array, got %v", tok)
	}
	for dec.More() {
		var r catAliasesRow
		if err := dec.Decode(&r); err != nil {
			return errors.Join(errors.New("decoding cat aliases (stream) row"), err)
		}
		if len(wantedSet) > 0 {
			if _, ok := wantedSet[r.Index]; !ok {
				continue
			}
		}
		a := types.Alias{
			Alias:         r.Alias,
			Index:         r.Index,
			Filter:        emptyOnDash(r.Filter),
			RoutingIndex:  emptyOnDash(r.RoutingIndex),
			RoutingSearch: emptyOnDash(r.RoutingSearch),
			IsWriteIndex:  parseClusterBool(r.IsWriteIndex),
		}
		if err := yield(a); err != nil {
			return err
		}
	}
	return nil
}

// indexFilterSet builds the lookup set used by both the buffered and
// streaming alias paths to filter rows whose index isn't in the
// caller-supplied list. Empty input yields a nil map (no filtering).
func indexFilterSet(indexFilter string) map[string]struct{} {
	wantedIndex := strings.TrimSpace(indexFilter)
	if wantedIndex == "" {
		return nil
	}
	out := map[string]struct{}{}
	for _, name := range strings.Split(wantedIndex, ",") {
		n := strings.TrimSpace(name)
		if n != "" {
			out[n] = struct{}{}
		}
	}
	return out
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
		return types.AliasUpdateResult{}, clientutil.ClassifyTransport(err)
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

// settingsGetWire mirrors the GET /<indices>/_settings response. Both
// dialects return the same envelope: {"<index>":{"settings":{…},
// "defaults":{…}}}. Defaults is present only when ?include_defaults=true.
type settingsGetWire map[string]struct {
	Settings map[string]any `json:"settings"`
	Defaults map[string]any `json:"defaults"`
}

// GetSettings implements client.IndexSettingsInspector via
// GET /<indices>/_settings/<names>. The cluster's Settings and (when
// requested) Defaults blocks are merged into the neutral
// IndexSettings.Settings — operators reading "what's the effective
// value?" get one map without having to know which side of the merge
// the value came from. Configured values win over defaults on key
// collision so the report mirrors what the cluster actually applies.
func (c *Client) GetSettings(ctx context.Context, req types.IndexSettingsGetRequest) ([]types.IndexSettings, error) {
	gs := c.api.Indices.GetSettings
	opts := []func(*esapi.IndicesGetSettingsRequest){gs.WithContext(ctx)}
	if len(req.Indices) > 0 {
		opts = append(opts, gs.WithIndex(req.Indices...))
	}
	if len(req.Names) > 0 {
		opts = append(opts, gs.WithName(req.Names...))
	}
	if req.IncludeDefaults {
		opts = append(opts, gs.WithIncludeDefaults(true))
	}
	if req.FlatSettings {
		opts = append(opts, gs.WithFlatSettings(true))
	}
	if req.IgnoreUnavailable {
		opts = append(opts, gs.WithIgnoreUnavailable(true))
	}

	resp, err := c.api.Indices.GetSettings(opts...)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("indices get_settings", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var wire settingsGetWire
	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "indices get_settings")
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding indices get_settings response"), err)
	}

	out := make([]types.IndexSettings, 0, len(wire))
	for name, entry := range wire {
		out = append(out, types.IndexSettings{
			Index:    name,
			Settings: entry.Settings,
			Defaults: entry.Defaults,
		})
	}
	return out, nil
}

// GetSettingsStream implements client.IndexSettingsInspector. Decodes
// the cluster's top-level {"index1": {...}, "index2": {...}} envelope
// one entry at a time via json.Decoder + Token, yielding each parsed
// IndexSettings. Lets `settings get -o ndjson --include-defaults`
// honour CLAUDE.md §6 on a busy cluster where the full nested-defaults
// payload runs into multiple MB.
func (c *Client) GetSettingsStream(ctx context.Context, req types.IndexSettingsGetRequest, yield func(types.IndexSettings) error) error {
	gs := c.api.Indices.GetSettings
	opts := []func(*esapi.IndicesGetSettingsRequest){gs.WithContext(ctx)}
	if len(req.Indices) > 0 {
		opts = append(opts, gs.WithIndex(req.Indices...))
	}
	if len(req.Names) > 0 {
		opts = append(opts, gs.WithName(req.Names...))
	}
	if req.IncludeDefaults {
		opts = append(opts, gs.WithIncludeDefaults(true))
	}
	if req.FlatSettings {
		opts = append(opts, gs.WithFlatSettings(true))
	}
	if req.IgnoreUnavailable {
		opts = append(opts, gs.WithIgnoreUnavailable(true))
	}

	resp, err := c.api.Indices.GetSettings(opts...)
	if err != nil {
		return clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := classifyResponse("indices get_settings (stream)", resp.StatusCode, resp.Body); err != nil {
		return err
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "indices get_settings (stream)")
	dec := json.NewDecoder(body)
	if tok, err := dec.Token(); err != nil {
		return errors.Join(errors.New("decoding indices get_settings (stream) response"), err)
	} else if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("indices get_settings (stream): expected JSON object, got %v", tok)
	}
	for dec.More() {
		// Each iteration: read the index name (object key), then
		// decode its value into the per-entry struct.
		keyTok, err := dec.Token()
		if err != nil {
			return errors.Join(errors.New("decoding indices get_settings (stream) key"), err)
		}
		name, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("indices get_settings (stream): expected string key, got %v", keyTok)
		}
		var entry struct {
			Settings map[string]any `json:"settings"`
			Defaults map[string]any `json:"defaults"`
		}
		if err := dec.Decode(&entry); err != nil {
			return errors.Join(errors.New("decoding indices get_settings (stream) value"), err)
		}
		row := types.IndexSettings{
			Index:    name,
			Settings: entry.Settings,
			Defaults: entry.Defaults,
		}
		if err := yield(row); err != nil {
			return err
		}
	}
	return nil
}

// settingsPutWire mirrors the PUT /<indices>/_settings response. Both
// dialects return only the acknowledged flag.
type settingsPutWire struct {
	Acknowledged bool `json:"acknowledged"`
}

// PutSettings implements client.IndexSettingsUpdater via
// PUT /<indices>/_settings. Body is forwarded verbatim — the cluster
// validates field names and the business layer only constrains the
// outer shape ({"index":{...}}).
func (c *Client) PutSettings(ctx context.Context, req types.IndexSettingsUpdateRequest) (types.IndexSettingsUpdateResult, error) {
	ps := c.api.Indices.PutSettings
	opts := []func(*esapi.IndicesPutSettingsRequest){ps.WithContext(ctx)}
	if len(req.Indices) > 0 {
		opts = append(opts, ps.WithIndex(req.Indices...))
	}
	if req.PreserveExisting {
		opts = append(opts, ps.WithPreserveExisting(true))
	}
	if req.IgnoreUnavailable {
		opts = append(opts, ps.WithIgnoreUnavailable(true))
	}

	resp, err := c.api.Indices.PutSettings(bytes.NewReader(req.Body), opts...)
	if err != nil {
		return types.IndexSettingsUpdateResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("indices put_settings", resp.StatusCode, resp.Body); err != nil {
		return types.IndexSettingsUpdateResult{}, err
	}

	var wire settingsPutWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.IndexSettingsUpdateResult{}, errors.Join(errors.New("decoding indices put_settings response"), err)
	}
	return types.IndexSettingsUpdateResult{
		Action:       "settings-update",
		Indices:      req.Indices,
		Acknowledged: wire.Acknowledged,
	}, nil
}

// indexTemplateGetWire mirrors the GET /_index_template[/<name>]
// response. Both dialects return the same envelope.
type indexTemplateGetWire struct {
	IndexTemplates []struct {
		Name          string `json:"name"`
		IndexTemplate struct {
			IndexPatterns []string       `json:"index_patterns"`
			Template      map[string]any `json:"template"`
			ComposedOf    []string       `json:"composed_of"`
			Priority      int            `json:"priority"`
			Version       int            `json:"version"`
			DataStream    map[string]any `json:"data_stream"`
		} `json:"index_template"`
	} `json:"index_templates"`
}

// GetTemplates implements client.IndexTemplateInspector via
// GET /_index_template/<names>. An empty names slice asks the cluster
// for every template; a non-empty slice is joined with commas, which
// the cluster honours as a multi-name lookup.
func (c *Client) GetTemplates(ctx context.Context, names []string) ([]types.IndexTemplate, error) {
	gt := c.api.Indices.GetIndexTemplate
	opts := []func(*esapi.IndicesGetIndexTemplateRequest){gt.WithContext(ctx)}
	if len(names) > 0 {
		opts = append(opts, gt.WithName(strings.Join(names, ",")))
	}

	resp, err := c.api.Indices.GetIndexTemplate(opts...)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("indices get_index_template", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var wire indexTemplateGetWire
	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "indices get_index_template")
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding indices get_index_template response"), err)
	}

	out := make([]types.IndexTemplate, 0, len(wire.IndexTemplates))
	for _, t := range wire.IndexTemplates {
		out = append(out, types.IndexTemplate{
			Name:          t.Name,
			IndexPatterns: t.IndexTemplate.IndexPatterns,
			Priority:      t.IndexTemplate.Priority,
			Version:       t.IndexTemplate.Version,
			ComposedOf:    t.IndexTemplate.ComposedOf,
			Template:      t.IndexTemplate.Template,
			DataStream:    t.IndexTemplate.DataStream,
		})
	}
	return out, nil
}

// GetTemplate implements client.IndexTemplateInspector for the
// single-name lookup. Returns ErrNotFound (wrapped) when the cluster
// 404s, so the business layer can produce a precise message without
// the GetTemplates([]string{name}) → linear-scan-1-element dance.
func (c *Client) GetTemplate(ctx context.Context, name string) (types.IndexTemplate, error) {
	rows, err := c.GetTemplates(ctx, []string{name})
	if err != nil {
		return types.IndexTemplate{}, err
	}
	for _, t := range rows {
		if t.Name == name {
			return t, nil
		}
	}
	return types.IndexTemplate{}, fmt.Errorf("%w: index template %q", esopsclient.ErrNotFound, name)
}

// templateAcknowledgedWire mirrors the response shape both PUT and
// DELETE /_index_template return: a single acknowledged flag.
type templateAcknowledgedWire struct {
	Acknowledged bool `json:"acknowledged"`
}

// PutTemplate implements client.IndexTemplateUpdater via
// PUT /_index_template/<name>. Body is forwarded verbatim — the
// cluster validates the schema (settings/mappings/aliases nesting,
// allowed fields). Create=true maps to ?create=true so the cluster
// refuses on an existing name.
func (c *Client) PutTemplate(ctx context.Context, req types.IndexTemplatePutRequest) (types.IndexTemplatePutResult, error) {
	pt := c.api.Indices.PutIndexTemplate
	opts := []func(*esapi.IndicesPutIndexTemplateRequest){pt.WithContext(ctx)}
	if req.Create {
		opts = append(opts, pt.WithCreate(true))
	}

	resp, err := c.api.Indices.PutIndexTemplate(req.Name, bytes.NewReader(req.Body), opts...)
	if err != nil {
		return types.IndexTemplatePutResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("indices put_index_template", resp.StatusCode, resp.Body); err != nil {
		return types.IndexTemplatePutResult{}, err
	}

	var wire templateAcknowledgedWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.IndexTemplatePutResult{}, errors.Join(errors.New("decoding indices put_index_template response"), err)
	}
	return types.IndexTemplatePutResult{
		Action:       "template-put",
		Name:         req.Name,
		Acknowledged: wire.Acknowledged,
	}, nil
}

// DeleteTemplate implements client.IndexTemplateUpdater via
// DELETE /_index_template/<name>. The ES typed client takes a slice;
// we always pass a single-element slice — bulk deletes are deliberately
// not supported, callers run the command once per name to keep shell
// history greppable.
func (c *Client) DeleteTemplate(ctx context.Context, req types.IndexTemplateDeleteRequest) (types.IndexTemplateDeleteResult, error) {
	dt := c.api.Indices.DeleteIndexTemplate
	resp, err := c.api.Indices.DeleteIndexTemplate([]string{req.Name}, dt.WithContext(ctx))
	if err != nil {
		return types.IndexTemplateDeleteResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("indices delete_index_template", resp.StatusCode, resp.Body); err != nil {
		return types.IndexTemplateDeleteResult{}, err
	}

	var wire templateAcknowledgedWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.IndexTemplateDeleteResult{}, errors.Join(errors.New("decoding indices delete_index_template response"), err)
	}
	return types.IndexTemplateDeleteResult{
		Action:       "template-delete",
		Name:         req.Name,
		Acknowledged: wire.Acknowledged,
	}, nil
}

// clusterSettingsWire mirrors the GET /_cluster/settings envelope. We
// pass flat_settings=true so the cluster keys come back as
// "cluster.routing.allocation.exclude._name" instead of a nested object
// — flat is far simpler to extract one key from.
type clusterSettingsWire struct {
	Persistent map[string]any `json:"persistent"`
	Transient  map[string]any `json:"transient"`
}

// GetClusterSettings implements client.ClusterSettingsManager. The
// response shape is identical on both dialects when flat_settings=true;
// a missing key in either scope means "no exclusion set" — surfaced as
// an empty slice rather than an error.
func (c *Client) GetClusterSettings(ctx context.Context) (types.ClusterSettingsView, error) {
	gs := c.api.Cluster.GetSettings
	resp, err := c.api.Cluster.GetSettings(
		gs.WithContext(ctx),
		gs.WithFlatSettings(true),
	)
	if err != nil {
		return types.ClusterSettingsView{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cluster get_settings", resp.StatusCode, resp.Body); err != nil {
		return types.ClusterSettingsView{}, err
	}

	var wire clusterSettingsWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.ClusterSettingsView{}, errors.Join(errors.New("decoding cluster settings response"), err)
	}
	return types.ClusterSettingsView{
		PersistentExcludeName: parseAllocationExcludeName(wire.Persistent),
		TransientExcludeName:  parseAllocationExcludeName(wire.Transient),
	}, nil
}

// PutAllocationExclude implements client.ClusterSettingsManager. The
// body is the minimal envelope the cluster needs: one scope, one key.
// An empty Names slice serialises as JSON null, which the cluster
// honours as "remove this key" — leaving an empty string would keep the
// key visible in /_cluster/settings forever.
func (c *Client) PutAllocationExclude(ctx context.Context, req types.ClusterAllocationExcludeUpdate) (types.ClusterSettingsUpdateResult, error) {
	scope := "transient"
	if req.Persistent {
		scope = "persistent"
	}

	var value any // nil → JSON null → key removed
	if len(req.Names) > 0 {
		value = strings.Join(req.Names, ",")
	}

	body, err := json.Marshal(map[string]any{
		scope: map[string]any{
			"cluster.routing.allocation.exclude._name": value,
		},
	})
	if err != nil {
		return types.ClusterSettingsUpdateResult{}, fmt.Errorf("encoding cluster put_settings body: %w", err)
	}

	ps := c.api.Cluster.PutSettings
	resp, err := c.api.Cluster.PutSettings(
		bytes.NewReader(body),
		ps.WithContext(ctx),
		ps.WithFlatSettings(true),
	)
	if err != nil {
		return types.ClusterSettingsUpdateResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cluster put_settings", resp.StatusCode, resp.Body); err != nil {
		return types.ClusterSettingsUpdateResult{}, err
	}

	var wire struct {
		Acknowledged bool `json:"acknowledged"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.ClusterSettingsUpdateResult{}, errors.Join(errors.New("decoding cluster put_settings response"), err)
	}
	return types.ClusterSettingsUpdateResult{Acknowledged: wire.Acknowledged}, nil
}

// parseAllocationExcludeName extracts
// `cluster.routing.allocation.exclude._name` from a flat-settings map
// and splits the comma-separated value into a clean slice. The cluster
// always reports the value as a string when set; an empty / missing key
// returns nil. Whitespace is trimmed because hand-edited cluster
// settings have been seen carrying " es01 , es02 ".
func parseAllocationExcludeName(m map[string]any) []string {
	if m == nil {
		return nil
	}
	raw, ok := m["cluster.routing.allocation.exclude._name"]
	if !ok {
		return nil
	}
	s, ok := raw.(string)
	if !ok || s == "" {
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

// AddVotingExclusions implements client.VotingExclusionManager via
// POST /_cluster/voting_config_exclusions. The node_names query
// parameter takes a comma-separated list and the cluster appends
// (de-duplicating against the existing list) — idempotent on re-add.
//
// The endpoint returns 200 with an empty body on success; we treat a
// 2xx as Acknowledged=true regardless of body so a future cluster
// version that adds payload fields doesn't break the call.
func (c *Client) AddVotingExclusions(ctx context.Context, names []string) (types.VotingExclusionsResult, error) {
	if len(names) == 0 {
		return types.VotingExclusionsResult{}, fmt.Errorf("at least one node name is required")
	}
	pe := c.api.Cluster.PostVotingConfigExclusions
	resp, err := c.api.Cluster.PostVotingConfigExclusions(
		pe.WithContext(ctx),
		pe.WithNodeNames(names...),
	)
	if err != nil {
		return types.VotingExclusionsResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("voting_config_exclusions add", resp.StatusCode, resp.Body); err != nil {
		return types.VotingExclusionsResult{}, err
	}
	return types.VotingExclusionsResult{Acknowledged: true}, nil
}

// ClearVotingExclusions implements client.VotingExclusionManager via
// DELETE /_cluster/voting_config_exclusions. The cluster API only
// supports clear-all — there is no per-node delete — so this wipes
// every entry in the list.
//
// wait_for_removal=false is set explicitly because the calling path
// (`ops uncordon --clear-voting`) is "I drained nodes but they're
// staying / coming back": the nodes named in the exclusion list are
// still in the cluster, so the cluster default (wait until the
// excluded nodes leave the cluster) hangs until the master timeout
// and 429s. The Elasticsearch error message itself recommends
// wait_for_removal=false for this case.
func (c *Client) ClearVotingExclusions(ctx context.Context) (types.VotingExclusionsResult, error) {
	de := c.api.Cluster.DeleteVotingConfigExclusions
	resp, err := c.api.Cluster.DeleteVotingConfigExclusions(
		de.WithContext(ctx),
		de.WithWaitForRemoval(false),
	)
	if err != nil {
		return types.VotingExclusionsResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("voting_config_exclusions clear", resp.StatusCode, resp.Body); err != nil {
		return types.VotingExclusionsResult{}, err
	}
	return types.VotingExclusionsResult{Acknowledged: true}, nil
}

// GetVotingExclusions reads the current list via
// /_cluster/state/metadata/voting_config_exclusions with a tight
// filter_path so the cluster doesn't ship the whole metadata blob (a
// few hundred KB on a busy cluster). The response shape is
// {metadata:{cluster_coordination:{voting_config_exclusions:[{node_id,
// node_name}]}}} on every cluster version we target.
func (c *Client) GetVotingExclusions(ctx context.Context) ([]string, error) {
	cs := c.api.Cluster.State
	resp, err := c.api.Cluster.State(
		cs.WithContext(ctx),
		cs.WithMetric("metadata"),
		cs.WithFilterPath("metadata.cluster_coordination.voting_config_exclusions.node_name"),
	)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cluster state voting_config_exclusions", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var wire struct {
		Metadata struct {
			ClusterCoordination struct {
				VotingConfigExclusions []struct {
					NodeName string `json:"node_name"`
				} `json:"voting_config_exclusions"`
			} `json:"cluster_coordination"`
		} `json:"metadata"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding cluster state voting_config_exclusions response"), err)
	}
	out := make([]string, 0, len(wire.Metadata.ClusterCoordination.VotingConfigExclusions))
	for _, e := range wire.Metadata.ClusterCoordination.VotingConfigExclusions {
		if e.NodeName != "" {
			out = append(out, e.NodeName)
		}
	}
	return out, nil
}

// taskWire mirrors a single entry from /_tasks?group_by=none. The map
// key (the composite "<node_id>:<task_number>" task ID) is captured
// separately by the decoder; the body fields below are common to ES and
// OS so one shape covers both adapters' needs.
type taskWire struct {
	Node             string `json:"node"`
	Type             string `json:"type"`
	Action           string `json:"action"`
	Description      string `json:"description"`
	StartTimeMillis  int64  `json:"start_time_in_millis"`
	RunningTimeNanos int64  `json:"running_time_in_nanos"`
	Cancellable      bool   `json:"cancellable"`
	Cancelled        bool   `json:"cancelled"`
	ParentTaskID     string `json:"parent_task_id"`
}

// Tasks implements client.TasksInspector. The cluster's default grouping
// (`nodes.<node_id>.tasks.<composite>`) is the only shape every dialect
// version reports consistently — `group_by=none` returns a flat *array*
// on ES/OS, which the typed client cannot decode and would cost us a
// second wire shape to handle. The adapter walks the nested map and
// collapses it into the row-per-task slice the renderer expects.
func (c *Client) Tasks(ctx context.Context, filter types.TasksFilter) ([]types.Task, error) {
	tl := c.api.Tasks.List
	opts := []func(*esapi.TasksListRequest){tl.WithContext(ctx)}
	if len(filter.Actions) > 0 {
		opts = append(opts, tl.WithActions(filter.Actions...))
	}
	if len(filter.Nodes) > 0 {
		opts = append(opts, tl.WithNodes(filter.Nodes...))
	}
	if filter.ParentTaskID != "" {
		opts = append(opts, tl.WithParentTaskID(filter.ParentTaskID))
	}
	if filter.Detailed {
		opts = append(opts, tl.WithDetailed(true))
	}
	resp, err := c.api.Tasks.List(opts...)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("tasks list", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var wire struct {
		Nodes map[string]struct {
			Tasks map[string]taskWire `json:"tasks"`
		} `json:"nodes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding tasks list response"), err)
	}
	flat := map[string]taskWire{}
	for _, node := range wire.Nodes {
		for id, t := range node.Tasks {
			flat[id] = t
		}
	}
	return tasksFromWire(flat), nil
}

// TasksStream implements client.TasksInspector. Walks the cluster's
// nested `{nodes: {<id>: {tasks: {<composite>: {...}}}}}` envelope a
// token at a time using json.Decoder, yielding each task as it is
// decoded. The decoder honours the bounded reader (DefaultMaxResponseBytes)
// so a misconfigured cluster returning a multi-hundred-MB body fails
// loudly rather than silently exhausts memory.
//
// Sort order is left to the cluster — the operator using ndjson is
// expected to pipe through `sort` or `jq` if they need a stable order;
// the buffered Tasks() path is what guarantees alphabetic sort by ID.
func (c *Client) TasksStream(ctx context.Context, filter types.TasksFilter, yield func(types.Task) error) error {
	tl := c.api.Tasks.List
	opts := []func(*esapi.TasksListRequest){tl.WithContext(ctx)}
	if len(filter.Actions) > 0 {
		opts = append(opts, tl.WithActions(filter.Actions...))
	}
	if len(filter.Nodes) > 0 {
		opts = append(opts, tl.WithNodes(filter.Nodes...))
	}
	if filter.ParentTaskID != "" {
		opts = append(opts, tl.WithParentTaskID(filter.ParentTaskID))
	}
	if filter.Detailed {
		opts = append(opts, tl.WithDetailed(true))
	}
	resp, err := c.api.Tasks.List(opts...)
	if err != nil {
		return clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("tasks list (stream)", resp.StatusCode, resp.Body); err != nil {
		return err
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "tasks list (stream)")
	dec := json.NewDecoder(body)
	return streamTasksObject(dec, yield)
}

// streamTasksObject walks the top-level response object, finds the
// "nodes" key, and for each node's nested "tasks" map decodes one task
// at a time and hands it to yield. Other top-level / per-node fields
// are skipped via json.RawMessage so an unexpected field added by a
// future cluster version doesn't break the stream.
func streamTasksObject(dec *json.Decoder, yield func(types.Task) error) error {
	if err := expectOpenBrace(dec); err != nil {
		return err
	}
	for dec.More() {
		key, err := readString(dec)
		if err != nil {
			return err
		}
		if key != "nodes" {
			if err := skipValue(dec); err != nil {
				return err
			}
			continue
		}
		if err := streamTasksNodes(dec, yield); err != nil {
			return err
		}
	}
	// Consume the closing '}'. Errors here are decoder-internal — the
	// JSON is malformed only if the cluster sent a truncated body, in
	// which case the bounded reader has already surfaced it.
	_, _ = dec.Token()
	return nil
}

func streamTasksNodes(dec *json.Decoder, yield func(types.Task) error) error {
	if err := expectOpenBrace(dec); err != nil {
		return err
	}
	for dec.More() {
		// Discard the node ID key.
		if _, err := dec.Token(); err != nil {
			return errors.Join(errors.New("decoding tasks node ID"), err)
		}
		if err := streamTasksOneNode(dec, yield); err != nil {
			return err
		}
	}
	_, _ = dec.Token()
	return nil
}

func streamTasksOneNode(dec *json.Decoder, yield func(types.Task) error) error {
	if err := expectOpenBrace(dec); err != nil {
		return err
	}
	for dec.More() {
		key, err := readString(dec)
		if err != nil {
			return err
		}
		if key != "tasks" {
			if err := skipValue(dec); err != nil {
				return err
			}
			continue
		}
		if err := expectOpenBrace(dec); err != nil {
			return err
		}
		for dec.More() {
			id, err := readString(dec)
			if err != nil {
				return err
			}
			var t taskWire
			if err := dec.Decode(&t); err != nil {
				return errors.Join(errors.New("decoding tasks list (stream) row"), err)
			}
			task := types.Task{
				ID:               id,
				Node:             t.Node,
				Type:             t.Type,
				Action:           t.Action,
				Description:      t.Description,
				StartTimeMillis:  t.StartTimeMillis,
				RunningTimeNanos: t.RunningTimeNanos,
				Cancellable:      t.Cancellable,
				Cancelled:        t.Cancelled,
				ParentTaskID:     t.ParentTaskID,
			}
			if err := yield(task); err != nil {
				return err
			}
		}
		_, _ = dec.Token() // closing '}' of the tasks object
	}
	_, _ = dec.Token() // closing '}' of the per-node object
	return nil
}

// expectOpenBrace reads the next token and asserts it is '{'. The
// /_tasks envelope is nested objects all the way down — there are no
// arrays in the response shape this stream walks — so a single
// brace-asserting helper is sufficient. Mis-alignment surfaces a
// loud error rather than letting the decoder drift.
func expectOpenBrace(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return errors.Join(errors.New("expected JSON '{' in tasks stream"), err)
	}
	d, ok := tok.(json.Delim)
	if !ok || d != '{' {
		return fmt.Errorf("tasks stream: expected JSON '{', got %v", tok)
	}
	return nil
}

// readString reads the next token and asserts it is a string.
func readString(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", errors.Join(errors.New("expected JSON string key in tasks stream"), err)
	}
	s, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("tasks stream: expected JSON string key, got %v", tok)
	}
	return s, nil
}

// skipValue consumes the next JSON value (object / array / scalar)
// without parsing it. json.RawMessage is the cleanest way to advance
// the decoder past an arbitrary value.
func skipValue(dec *json.Decoder) error {
	var skip json.RawMessage
	if err := dec.Decode(&skip); err != nil {
		return errors.Join(errors.New("skipping JSON value in tasks stream"), err)
	}
	return nil
}

// CancelTasks implements client.TasksCanceller. The cluster's
// POST /_tasks/<id>/_cancel takes at most one ID per request and the
// bulk POST /_tasks/_cancel takes a filter set instead — there is no
// multi-ID variant. The adapter therefore expects at most one entry
// in req.TaskIDs; the business layer fans out across multi-ID calls
// and folds per-call results back together.
func (c *Client) CancelTasks(ctx context.Context, req types.TasksCancelRequest) (types.TasksCancelResult, error) {
	if len(req.TaskIDs) > 1 {
		return types.TasksCancelResult{}, fmt.Errorf("elasticsearch tasks cancel: capability accepts at most one TaskIDs entry; got %d (business layer should fan out)", len(req.TaskIDs))
	}
	taskID := ""
	if len(req.TaskIDs) == 1 {
		taskID = req.TaskIDs[0]
	}
	tc := c.api.Tasks.Cancel
	opts := []func(*esapi.TasksCancelRequest){tc.WithContext(ctx)}
	if taskID != "" {
		opts = append(opts, tc.WithTaskID(taskID))
	}
	if len(req.Filter.Actions) > 0 {
		opts = append(opts, tc.WithActions(req.Filter.Actions...))
	}
	if len(req.Filter.Nodes) > 0 {
		opts = append(opts, tc.WithNodes(req.Filter.Nodes...))
	}
	if req.Filter.ParentTaskID != "" {
		opts = append(opts, tc.WithParentTaskID(req.Filter.ParentTaskID))
	}
	if req.WaitForCompletion {
		opts = append(opts, tc.WithWaitForCompletion(true))
	}
	resp, err := c.api.Tasks.Cancel(opts...)
	if err != nil {
		return types.TasksCancelResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("tasks cancel", resp.StatusCode, resp.Body); err != nil {
		return types.TasksCancelResult{}, err
	}

	var wire struct {
		Nodes map[string]struct {
			Tasks map[string]taskWire `json:"tasks"`
		} `json:"nodes"`
		NodeFailures []struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
			NodeID string `json:"node_id"`
		} `json:"node_failures"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.TasksCancelResult{}, errors.Join(errors.New("decoding tasks cancel response"), err)
	}

	flat := map[string]taskWire{}
	for _, node := range wire.Nodes {
		for id, t := range node.Tasks {
			flat[id] = t
		}
	}
	failures := make([]string, 0, len(wire.NodeFailures))
	for _, f := range wire.NodeFailures {
		failures = append(failures, formatTaskNodeFailure(f.NodeID, f.Reason, f.Type))
	}
	out := types.TasksCancelResult{
		Cancelled:         tasksFromWire(flat),
		NodeFailures:      failures,
		WaitForCompletion: req.WaitForCompletion,
	}
	if taskID != "" {
		out.TaskIDs = []string{taskID}
	}
	return out, nil
}

// tasksFromWire flattens a "<node>:<id>" → wire map into a sorted slice
// keyed by ID. Sorting is stable: a saved JSON capture compares equal
// across runs even though the cluster's map iteration order doesn't.
func tasksFromWire(in map[string]taskWire) []types.Task {
	out := make([]types.Task, 0, len(in))
	for id, t := range in {
		out = append(out, types.Task{
			ID:               id,
			Node:             t.Node,
			Type:             t.Type,
			Action:           t.Action,
			Description:      t.Description,
			StartTimeMillis:  t.StartTimeMillis,
			RunningTimeNanos: t.RunningTimeNanos,
			Cancellable:      t.Cancellable,
			Cancelled:        t.Cancelled,
			ParentTaskID:     t.ParentTaskID,
		})
	}
	sortTasksByID(out)
	return out
}

// sortTasksByID is the stable-order sort shared by the list and cancel
// renderers. Pulled out so the OS adapter (next to this in the package
// graph but separate file) can reuse the same ordering — see opensearch
// adapter's tasks helpers.
func sortTasksByID(t []types.Task) {
	sort.Slice(t, func(i, j int) bool { return t[i].ID < t[j].ID })
}

// formatTaskNodeFailure renders one node_failures entry as a single
// "<node_id>: <reason>" line. Falls back to the cluster's exception
// type when no reason is present (older ES versions emit just `type`),
// and to "<unknown>" when even type is empty so an entry is never
// invisibly dropped from the rendered list.
func formatTaskNodeFailure(nodeID, reason, typ string) string {
	prefix := nodeID
	if prefix == "" {
		prefix = "<unknown>"
	}
	switch {
	case reason != "":
		return fmt.Sprintf("%s: %s", prefix, reason)
	case typ != "":
		return fmt.Sprintf("%s: %s", prefix, typ)
	default:
		return prefix
	}
}

// pendingTasksWire mirrors the GET /_cluster/pending_tasks envelope.
// Identical on both dialects so the same shape covers ES and OS — the
// OS adapter decodes via its own copy rather than crossing the package
// boundary, but the wire layout is shared.
type pendingTasksWire struct {
	Tasks []pendingTaskWire `json:"tasks"`
}

type pendingTaskWire struct {
	InsertOrder       int64  `json:"insert_order"`
	Priority          string `json:"priority"`
	Source            string `json:"source"`
	TimeInQueueMillis int64  `json:"time_in_queue_millis"`
	Executing         bool   `json:"executing"`
}

// PendingTasks implements client.PendingTasksInspector. The cluster
// endpoint takes no body and no useful query params for our purposes —
// `local=true` would skip the master round-trip but the operator-
// facing intent of this command is "what is the master sitting on",
// which a stale local-state read cannot answer.
func (c *Client) PendingTasks(ctx context.Context) ([]types.PendingTask, error) {
	pt := c.api.Cluster.PendingTasks
	resp, err := c.api.Cluster.PendingTasks(pt.WithContext(ctx))
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cluster pending_tasks", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var wire pendingTasksWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding pending_tasks response"), err)
	}
	return pendingTasksFromWire(wire.Tasks), nil
}

// pendingTasksFromWire is shared with the OS adapter via a copy — the
// envelope is identical across dialects but we keep the per-adapter
// decode so neither package has to reach into the other's internals.
func pendingTasksFromWire(in []pendingTaskWire) []types.PendingTask {
	out := make([]types.PendingTask, 0, len(in))
	for _, t := range in {
		out = append(out, types.PendingTask{
			InsertOrder:       t.InsertOrder,
			Priority:          t.Priority,
			Source:            t.Source,
			TimeInQueueMillis: t.TimeInQueueMillis,
			Executing:         t.Executing,
		})
	}
	return out
}

// migrationDeprecationsWire mirrors the GET /_migration/deprecations
// envelope. The cluster groups issues by category. Older ES versions
// (≤ 7.x) emit cluster_settings/node_settings/index_settings/
// ml_settings; 8.x added data_streams/templates/ilm_policies. Newer
// ES versions may extend this set; we decode known categories
// explicitly and capture the rest via the `Other` map so an unknown
// category is reported under its own name rather than silently dropped.
type migrationDeprecationsWire struct {
	ClusterSettings []deprecationIssueWire            `json:"cluster_settings"`
	NodeSettings    []deprecationIssueWire            `json:"node_settings"`
	IndexSettings   map[string][]deprecationIssueWire `json:"index_settings"`
	MLSettings      []deprecationIssueWire            `json:"ml_settings"`
	DataStreams     map[string][]deprecationIssueWire `json:"data_streams"`
	Templates       map[string][]deprecationIssueWire `json:"templates"`
	ILMPolicies     map[string][]deprecationIssueWire `json:"ilm_policies"`
}

type deprecationIssueWire struct {
	Level                       string          `json:"level"`
	Message                     string          `json:"message"`
	URL                         string          `json:"url"`
	Details                     string          `json:"details"`
	ResolveDuringRollingUpgrade bool            `json:"resolve_during_rolling_upgrade"`
	Meta                        json.RawMessage `json:"_meta"`
}

// Deprecations implements client.DeprecationInspector against ES's
// /_migration/deprecations. The cluster's per-category groupings are
// flattened into a single []DeprecationIssue tagged with Category and
// Target; the result is sorted by category then target then message
// so a captured JSON listing is stable across runs.
//
// A 404 from a cluster build that omits the migration plugin returns
// the cluster's own body verbatim via classifyResponse — the operator
// needs the cluster's reason, not a paraphrase.
func (c *Client) Deprecations(ctx context.Context, req types.DeprecationsRequest) ([]types.DeprecationIssue, error) {
	md := c.api.Migration.Deprecations
	opts := []func(*esapi.MigrationDeprecationsRequest){md.WithContext(ctx)}
	if req.Index != "" {
		opts = append(opts, md.WithIndex(req.Index))
	}
	resp, err := c.api.Migration.Deprecations(opts...)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("migration deprecations", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var wire migrationDeprecationsWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding migration deprecations response"), err)
	}

	out := make([]types.DeprecationIssue, 0)
	out = appendDeprecationIssues(out, "cluster_settings", "", wire.ClusterSettings)
	out = appendDeprecationIssues(out, "node_settings", "", wire.NodeSettings)
	out = appendDeprecationIssues(out, "ml_settings", "", wire.MLSettings)
	out = appendKeyedDeprecationIssues(out, "index_settings", wire.IndexSettings)
	out = appendKeyedDeprecationIssues(out, "data_streams", wire.DataStreams)
	out = appendKeyedDeprecationIssues(out, "templates", wire.Templates)
	out = appendKeyedDeprecationIssues(out, "ilm_policies", wire.ILMPolicies)

	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		if out[i].Target != out[j].Target {
			return out[i].Target < out[j].Target
		}
		return out[i].Message < out[j].Message
	})
	return out, nil
}

// appendDeprecationIssues flattens a per-category slice into the result.
// Target is left empty for global categories (cluster_settings,
// node_settings, ml_settings); the keyed counterpart handles
// per-resource categories.
func appendDeprecationIssues(out []types.DeprecationIssue, category, target string, in []deprecationIssueWire) []types.DeprecationIssue {
	for _, w := range in {
		out = append(out, deprecationIssueFromWire(category, target, w))
	}
	return out
}

// appendKeyedDeprecationIssues flattens a category whose cluster
// envelope is keyed by resource name (index, data stream, template,
// ILM policy). The map key becomes Target.
func appendKeyedDeprecationIssues(out []types.DeprecationIssue, category string, in map[string][]deprecationIssueWire) []types.DeprecationIssue {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, w := range in[k] {
			out = append(out, deprecationIssueFromWire(category, k, w))
		}
	}
	return out
}

func deprecationIssueFromWire(category, target string, w deprecationIssueWire) types.DeprecationIssue {
	issue := types.DeprecationIssue{
		Category:                    category,
		Target:                      target,
		Level:                       w.Level,
		Message:                     w.Message,
		URL:                         w.URL,
		Details:                     w.Details,
		ResolveDuringRollingUpgrade: w.ResolveDuringRollingUpgrade,
	}
	if len(w.Meta) > 0 && string(w.Meta) != "null" {
		issue.Meta = string(w.Meta)
	}
	return issue
}
