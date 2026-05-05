// Package opensearch adapts the upstream opensearch-go/v4 client to
// esops capability interfaces. Nothing in this package leaks out into
// the rest of the codebase: callers get neutral types only.
package opensearch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"context"

	os "github.com/opensearch-project/opensearch-go/v4"
	osapi "github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"github.com/esops-dev/esops-go/pkg/internal/auth"
	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// Client is an OS-backed implementation of the esops client capabilities.
type Client struct {
	api *osapi.Client
}

// Options bundles the transport-shaping knobs the upstream OS client
// honours but esops exposes via config (max_retries, compress). See
// the matching elasticsearch.Options for the shape rationale.
type Options struct {
	// MaxRetries: 0 = library default (3). Negative disables retries
	// (DisableRetry on the upstream).
	MaxRetries int

	// CompressRequestBody gzips outbound bodies.
	CompressRequestBody bool
}

// New builds a client against the given addresses with auth carried by
// the provider. TLS and transport tweaks come in via httpTransport;
// opts plumbs through max_retries / compress from config.
//
// API key auth is rejected here: it's an Elasticsearch-only header
// scheme. OpenSearch users wanting key-based auth set up a bearer token
// at the proxy in front of the cluster (or use SigV4 when fronted by
// IAM); failing fast at construction beats every request returning 401
// with no clue why.
func New(addrs []string, provider auth.Provider, httpTransport http.RoundTripper, opts Options) (*Client, error) {
	if provider == nil {
		return nil, errors.New("opensearch client: nil auth provider")
	}
	if provider.Type() == auth.TypeAPIKey {
		return nil, errors.New("auth.type=api_key is Elasticsearch-only; OpenSearch has no equivalent — use bearer, basic, or sigv4")
	}
	osCfg := os.Config{
		Addresses: addrs,
		Transport: auth.WrapTransport(provider, httpTransport),
	}
	switch {
	case opts.MaxRetries < 0:
		osCfg.DisableRetry = true
	case opts.MaxRetries > 0:
		osCfg.MaxRetries = opts.MaxRetries
	}
	if opts.CompressRequestBody {
		osCfg.CompressRequestBody = true
	}
	c, err := osapi.NewClient(osapi.Config{Client: osCfg})
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
			Docs:              clientutil.Atoi64Ptr(s.Docs),
			StoreBytes:        clientutil.Atoi64Ptr(s.Store),
			Node:              clientutil.DerefString(s.Node),
			IP:                clientutil.DerefString(s.IP),
			UnassignedReason:  clientutil.DerefString(s.UnassignedReason),
			UnassignedAt:      clientutil.DerefString(s.UnassignedAt),
			UnassignedFor:     clientutil.DerefString(s.UnassignedFor),
			UnassignedDetails: clientutil.DerefString(s.UnassignedDetails),
		})
	}
	return out, nil
}

// Indices implements client.IndexInspector. The OS v4 client returns a
// fully-typed CatIndexResp — we pick just the fields esops surfaces and
// drop the long tail of per-operation counters upstream exposes. bytes=b
// forces raw-bytes store sizes regardless of OS version.
func (c *Client) Indices(ctx context.Context, filter types.IndexFilter) ([]types.Index, error) {
	// expand_wildcards reflects the caller's --hidden flag so the cluster
	// filters dot-prefixed system indices server-side. The business layer
	// no longer has to drop them after the fact.
	expand := "open,closed"
	if filter.Hidden {
		expand = "open,closed,hidden"
	}
	req := &osapi.CatIndicesReq{
		Params: osapi.CatIndicesParams{
			Bytes:           "b",
			ExpandWildcards: expand,
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
			Primaries:          clientutil.IntPtrOrZero(r.Primary),
			Replicas:           clientutil.IntPtrOrZero(r.Replica),
			DocsCount:          clientutil.Int64PtrOrZero(r.DocsCount),
			DocsDeleted:        clientutil.Int64PtrOrZero(r.DocDeleted),
			StoreBytes:         clientutil.Atoi64Ptr(r.StoreSize),
			PrimaryStoreBytes:  clientutil.Atoi64Ptr(r.PrimaryStoreSize),
			CreationDateMillis: int64(r.CreationDate),
		})
	}
	return out, nil
}

// catIndicesRowOS mirrors a single /_cat/indices?format=json row on
// OpenSearch. Identical wire shape to the ES adapter's catIndicesRow
// (the cat API is byte-compatible across dialects); kept per-package
// so a future divergence in column tags is a per-package change.
type catIndicesRowOS struct {
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

// IndicesStream implements client.IndexInspector against OpenSearch
// via raw HTTP — the v4 typed CatIndicesReq decodes the full response
// into a struct before returning, which would defeat the streaming
// contract. We hand-build /_cat/indices?... and stream-decode with a
// json.Decoder per array element so a 20k-index cluster's first row
// reaches stdout before the cluster has finished sending the last one.
func (c *Client) IndicesStream(ctx context.Context, filter types.IndexFilter, yield func(types.Index) error) error {
	expand := "open,closed"
	if filter.Hidden {
		expand = "open,closed,hidden"
	}
	q := url.Values{}
	q.Set("format", "json")
	q.Set("bytes", "b")
	q.Set("h", strings.Join([]string{
		"health", "status", "index", "uuid",
		"pri", "rep",
		"docs.count", "docs.deleted",
		"store.size", "pri.store.size",
		"creation.date",
	}, ","))
	q.Set("expand_wildcards", expand)

	path := "/_cat/indices"
	if filter.Index != "" {
		path += "/" + strings.Join(esopsclient.SplitCSV(filter.Index), ",")
	}
	path += "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	resp, err := c.api.Client.Perform(req)
	if err != nil {
		return clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(clientutil.NewBoundedReader(resp.Body, 64*1024, "cat indices (stream) error"))
		return clientutil.ClassifyStatus("cat indices (stream)", resp.StatusCode, body)
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "cat indices (stream)")
	dec := json.NewDecoder(body)
	if tok, err := dec.Token(); err != nil {
		return errors.Join(errors.New("decoding cat indices (stream) response"), err)
	} else if d, ok := tok.(json.Delim); !ok || d != '[' {
		return fmt.Errorf("cat indices (stream): expected JSON array, got %v", tok)
	}
	for dec.More() {
		var r catIndicesRowOS
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
// to Indices: only asks for (index, status) columns. See the ES
// adapter docstring for the rationale.
func (c *Client) IndicesNamesStatus(ctx context.Context, filter types.IndexFilter) ([]types.IndexNameStatus, error) {
	expand := "open,closed"
	if filter.Hidden {
		expand = "open,closed,hidden"
	}
	req := &osapi.CatIndicesReq{
		Params: osapi.CatIndicesParams{
			ExpandWildcards: expand,
			H:               []string{"index", "status"},
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
	out := make([]types.IndexNameStatus, 0, len(resp.Indices))
	for _, r := range resp.Indices {
		out = append(out, types.IndexNameStatus{Name: r.Index, Status: r.Status})
	}
	return out, nil
}

// OpenIndex implements client.IndexStateChanger via /{index}/_open.
// IndicesOpenReq.Index expects a single comma-separated string, not a
// slice — the OS typed wrapper joins for the URL path.
func (c *Client) OpenIndex(ctx context.Context, req types.IndexStateChangeRequest) (types.IndexStateChange, error) {
	osReq := osapi.IndicesOpenReq{
		Index:  strings.Join(req.Indices, ","),
		Params: osapi.IndicesOpenParams{},
	}
	if req.IgnoreUnavailable {
		t := true
		osReq.Params.IgnoreUnavailable = &t
	}
	if req.WaitForActiveShards != "" {
		osReq.Params.WaitForActiveShards = req.WaitForActiveShards
	}

	resp, err := c.api.Indices.Open(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.IndexStateChange{}, classifyResponseErr(err, status)
	}
	return types.IndexStateChange{
		Action:             types.IndexStateOpen,
		Acknowledged:       resp.Acknowledged,
		ShardsAcknowledged: resp.ShardsAcknowledged,
	}, nil
}

// CloseIndex implements client.IndexStateChanger via /{index}/_close.
func (c *Client) CloseIndex(ctx context.Context, req types.IndexStateChangeRequest) (types.IndexStateChange, error) {
	osReq := osapi.IndicesCloseReq{
		Index:  strings.Join(req.Indices, ","),
		Params: osapi.IndicesCloseParams{},
	}
	if req.IgnoreUnavailable {
		t := true
		osReq.Params.IgnoreUnavailable = &t
	}
	if req.WaitForActiveShards != "" {
		osReq.Params.WaitForActiveShards = req.WaitForActiveShards
	}

	resp, err := c.api.Indices.Close(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.IndexStateChange{}, classifyResponseErr(err, status)
	}
	return types.IndexStateChange{
		Action:             types.IndexStateClose,
		Acknowledged:       resp.Acknowledged,
		ShardsAcknowledged: resp.ShardsAcknowledged,
	}, nil
}

// ForceMerge implements client.IndexOptimizer via /{index}/_forcemerge.
// The OS v4 typed client materialises a {_shards:{...}} response with
// failures[] carrying per-shard reasons; we reshape it into the neutral
// IndexOptimizeResult so output rendering stays dialect-agnostic.
//
// The cluster has no native dry-run for this endpoint — the business
// layer covers that client-side with the IndexInspector pattern resolve.
func (c *Client) ForceMerge(ctx context.Context, req types.IndexOptimizeRequest) (types.IndexOptimizeResult, error) {
	osReq := &osapi.IndicesForcemergeReq{
		Indices: req.Indices,
		Params:  osapi.IndicesForcemergeParams{},
	}
	if req.MaxNumSegments > 0 {
		v := req.MaxNumSegments
		osReq.Params.MaxNumSegments = &v
	}
	if req.OnlyExpungeDeletes {
		t := true
		osReq.Params.OnlyExpungeDeletes = &t
	}
	if req.Flush != nil {
		v := *req.Flush
		osReq.Params.Flush = &v
	}
	if req.IgnoreUnavailable {
		t := true
		osReq.Params.IgnoreUnavailable = &t
	}

	resp, err := c.api.Indices.Forcemerge(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.IndexOptimizeResult{}, classifyResponseErr(err, status)
	}

	out := types.IndexOptimizeResult{
		Action:        "optimize",
		ShardsTotal:   resp.Shards.Total,
		ShardsSuccess: resp.Shards.Successful,
		ShardsFailed:  resp.Shards.Failed,
	}
	for _, f := range resp.Shards.Failures {
		out.Failures = append(out.Failures,
			fmt.Sprintf("%s[%d] %s: %s: %s", f.Index, f.Shard, f.Status, f.Reason.Type, f.Reason.Reason))
	}
	return out, nil
}

// Rollover implements client.IndexRollover via /<alias>/_rollover.
// The OS typed client returns a fully-decoded IndicesRolloverResp; we
// reshape into the neutral type so consumers don't depend on upstream
// types. Body shape (conditions) is built by the shared helper below.
func (c *Client) Rollover(ctx context.Context, req types.IndexRolloverRequest) (types.IndexRolloverResult, error) {
	body, err := buildRolloverBody(req.Conditions)
	if err != nil {
		return types.IndexRolloverResult{}, err
	}

	osReq := osapi.IndicesRolloverReq{
		Alias:  req.Alias,
		Index:  req.NewIndex,
		Body:   bytes.NewReader(body),
		Params: osapi.IndicesRolloverParams{},
	}
	if req.DryRun {
		t := true
		osReq.Params.DryRun = &t
	}
	if req.WaitForActiveShards != "" {
		osReq.Params.WaitForActiveShards = req.WaitForActiveShards
	}

	resp, err := c.api.Indices.Rollover(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.IndexRolloverResult{}, classifyResponseErr(err, status)
	}
	return types.IndexRolloverResult{
		Alias:              req.Alias,
		OldIndex:           resp.OldIndex,
		NewIndex:           resp.NewIndex,
		RolledOver:         resp.RolledOver,
		DryRun:             resp.DryRun || req.DryRun,
		Acknowledged:       resp.Acknowledged,
		ShardsAcknowledged: resp.ShardsAcknowledged,
		ConditionsMet:      resp.Conditions,
	}, nil
}

// buildRolloverBody serialises a conditions block into the cluster's
// {"conditions":{...}} wire shape. An empty conditions struct yields
// {"conditions":{}} which the cluster treats as the unconditional
// form; the business layer guards that case so an absent-mindedly-
// typed `index rollover --alias logs` doesn't roll a fresh index.
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

// AllocationExplain implements client.AllocationExplainer.
//
// When req.Index is empty we pass nil — the cluster picks any unassigned
// shard it finds, matching the raw endpoint's default. Otherwise we set
// the typed body {index, shard, primary}. include_disk_info /
// include_yes_decisions go through Params as query strings.
//
// OS's typed response always materialises CurrentNode and UnassignedInfo
// as zero-valued structs; we collapse those back to nil on the neutral
// type so JSON/YAML output omits them (matching ES behaviour).
func (c *Client) AllocationExplain(ctx context.Context, req types.AllocationExplainRequest) (types.AllocationExplanation, error) {
	osReq := &osapi.ClusterAllocationExplainReq{
		Params: osapi.ClusterAllocationExplainParams{},
	}
	if req.IncludeDiskInfo {
		t := true
		osReq.Params.IncludeDiskInfo = &t
	}
	if req.IncludeYesDecisions {
		t := true
		osReq.Params.IncludeYesDecisions = &t
	}
	if req.Index != "" {
		osReq.Body = &osapi.ClusterAllocationExplainBody{
			Index:   req.Index,
			Shard:   req.Shard,
			Primary: req.Primary,
		}
	}

	resp, err := c.api.Cluster.AllocationExplain(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.AllocationExplanation{}, classifyResponseErr(err, status)
	}

	out := types.AllocationExplanation{
		Index:                  resp.Index,
		Shard:                  resp.Shard,
		Primary:                resp.Primary,
		CurrentState:           resp.CurrentState,
		CanAllocate:            resp.CanAllocate,
		AllocateExplanation:    resp.AllocateExplanation,
		CanRemainOnCurrentNode: resp.CanRemainOnCurrentNode,
		CanRebalanceCluster:    resp.CanRebalanceCluster,
		RebalanceExplanation:   resp.RebalanceExplanation,
	}
	// OS materialises these as zero-valued structs even when absent from
	// the JSON — treat "has identifying field" as "present". ID/Name are
	// the only fields the cluster guarantees to set for a real current_node;
	// Reason is the one unassigned_info guarantees.
	if resp.CurrentNode.NodeID != "" || resp.CurrentNode.NodeName != "" {
		out.CurrentNode = &types.AllocationNode{
			ID:               resp.CurrentNode.NodeID,
			Name:             resp.CurrentNode.NodeName,
			TransportAddress: resp.CurrentNode.TransportAddress,
			WeightRanking:    resp.CurrentNode.WeightRanking,
		}
	}
	if resp.UnassignedInfo.Reason != "" {
		out.UnassignedInfo = &types.AllocationUnassigned{
			Reason:               resp.UnassignedInfo.Reason,
			At:                   resp.UnassignedInfo.At,
			LastAllocationStatus: resp.UnassignedInfo.LastAllocationStatus,
		}
	}
	if len(resp.NodeAllocationDecisions) > 0 {
		out.NodeDecisions = make([]types.AllocationNodeDecision, 0, len(resp.NodeAllocationDecisions))
		for _, d := range resp.NodeAllocationDecisions {
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
	return out, nil
}

// rerouteResponseWire mirrors the /_cluster/reroute response. The OS
// typed ClusterRerouteResp also exposes a State field carrying the full
// routing table — we don't surface it (operator-facing view is
// acknowledged + explanations), so a hand-decoded shape keeps the
// adapter boundary clean and matches the ES side byte-for-byte.
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

// Reroute implements client.RerouteExecutor against OS's
// /_cluster/reroute. The wire shape is identical to ES (commands array
// + dry_run/explain/retry_failed query flags), so the adapter
// delegates the body construction and response normalisation to the
// shared helpers in this package.
//
// We bypass the typed ClusterRerouteResp because it materialises a full
// routing-table dump on every response — bytes we don't surface and
// don't want to pay the decode for. A raw POST through the underlying
// transport keeps the response decode targeted.
func (c *Client) Reroute(ctx context.Context, req types.RerouteRequest) (types.RerouteResult, error) {
	body, err := buildRerouteBody(req.Commands)
	if err != nil {
		return types.RerouteResult{}, err
	}

	path := "/_cluster/reroute?metric=none"
	if req.DryRun {
		path += "&dry_run=true"
	}
	if req.Explain {
		path += "&explain=true"
	}
	if req.RetryFailed {
		path += "&retry_failed=true"
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return types.RerouteResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.RerouteResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return types.RerouteResult{}, errors.Join(errors.New("reading cluster reroute response"), err)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return types.RerouteResult{}, esopsclient.ErrAuth
	case http.StatusForbidden:
		return types.RerouteResult{}, esopsclient.ErrForbidden
	}
	if resp.StatusCode >= 400 {
		return types.RerouteResult{}, clientutil.ClassifyStatus("cluster reroute", resp.StatusCode, respBody)
	}

	var wire rerouteResponseWire
	if err := json.Unmarshal(respBody, &wire); err != nil {
		return types.RerouteResult{}, errors.Join(errors.New("decoding cluster reroute response"), err)
	}
	return toRerouteResult(req, wire), nil
}

// buildRerouteBody turns a typed command list into the cluster's
// {"commands":[{"<action>": {<args>}}, ...]} envelope. The body shape
// is identical to ES — kept in this package (rather than promoted to
// the shared `client` package) because the per-action argument keys
// are part of the cluster contract, and a future divergence between
// dialects (OS adding a new action ES doesn't have, or vice versa)
// is a per-package change, not a shared-helper churn.
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
		return nil, fmt.Errorf("retry_failed has no command body; use RerouteRequest.RetryFailed instead")
	default:
		return nil, fmt.Errorf("unsupported reroute action %q", cmd.Action)
	}
}

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
	return clientutil.ClassifyTransport(err)
}

// osInternalUserWire mirrors an entry from GET /_plugins/_security/api/internalusers.
// Reserved flags defaults shipped with the plugin (admin, kibanaserver).
// Hidden is true for system entities the UI hides by default — we
// surface them anyway, that's what an audit is for. OpenDistro legacy
// naming on opendistro_security_roles is preserved in the wire type for
// clarity, then flattened to Roles in the neutral view.
type osInternalUserWire struct {
	Reserved      bool     `json:"reserved"`
	Hidden        bool     `json:"hidden"`
	BackendRoles  []string `json:"backend_roles"`
	SecurityRoles []string `json:"opendistro_security_roles"`
	Description   string   `json:"description"`
}

// osRoleWire mirrors an entry from GET /_plugins/_security/api/roles.
// Index patterns are embedded per-block alongside the actions they grant;
// we flatten the patterns across blocks for the audit view.
type osRoleWire struct {
	Reserved           bool     `json:"reserved"`
	Hidden             bool     `json:"hidden"`
	Description        string   `json:"description"`
	ClusterPermissions []string `json:"cluster_permissions"`
	IndexPermissions   []struct {
		IndexPatterns []string `json:"index_patterns"`
	} `json:"index_permissions"`
}

// Audit implements client.SecurityAuditor against the OpenSearch security
// plugin. The plugin ships with every production OS build, so unlike ES
// we don't expect a "security disabled" branch — if the endpoint 404s we
// still return a disabled status (user removed the plugin) rather than
// erroring out. 401/403 on internalusers bubble up for the operator to
// diagnose, since users + roles are the bare minimum for a useful audit.
//
// Role mappings and tenants are fetched best-effort: a 403 on either
// (the auth user lacks `restapi:admin/rolesmapping` or
// `restapi:admin/tenants`) lands in Status.Notes rather than aborting.
// Partial visibility beats a dropped run.
func (c *Client) Audit(ctx context.Context) (types.SecurityReport, error) {
	report := types.SecurityReport{
		Status: types.SecurityStatus{Dialect: string(types.DialectOpenSearch)},
	}

	// Step 1: probe users sequentially. A 404 on internalusers means
	// the security plugin isn't there, and the whole audit short-
	// circuits — same reasoning as the ES side.
	users, disabled, note, err := c.fetchOSUsers(ctx)
	if err != nil {
		return report, err
	}
	if disabled {
		report.Status.Note = note
		return report, nil
	}
	report.Status.Enabled = true
	report.Users = users

	// Step 2: roles + role_mappings + tenants in parallel — three
	// independent reads of the security plugin's admin API. Serial
	// would multiply latency by 3 on every audit; the plugin happily
	// fields concurrent reads.
	var (
		roles    []types.SecurityRole
		mappings []types.SecurityRoleMapping
		tenants  []types.SecurityTenant
		mNote    string
		tNote    string
	)
	err = clientutil.RunParallel(
		func() error {
			r, e := c.fetchOSRoles(ctx)
			roles = r
			return e
		},
		func() error {
			m, n, e := c.fetchOSRoleMappings(ctx)
			mappings, mNote = m, n
			return e
		},
		func() error {
			t, n, e := c.fetchOSTenants(ctx)
			tenants, tNote = t, n
			return e
		},
	)
	if err != nil {
		return report, err
	}
	report.Roles = roles
	report.RoleMappings = mappings
	report.Tenants = tenants
	if mNote != "" {
		report.Status.Notes = append(report.Status.Notes, mNote)
	}
	if tNote != "" {
		report.Status.Notes = append(report.Status.Notes, tNote)
	}
	return report, nil
}

func (c *Client) fetchOSUsers(ctx context.Context) ([]types.SecurityUser, bool, string, error) {
	body, status, err := c.doSecurityGET(ctx, "/_plugins/_security/api/internalusers")
	if err != nil {
		return nil, false, "", err
	}
	if status == http.StatusNotFound {
		return nil, true, "security plugin endpoint not found at /_plugins/_security/api/internalusers", nil
	}
	if status == http.StatusUnauthorized {
		return nil, false, "", esopsclient.ErrAuth
	}
	if status == http.StatusForbidden {
		return nil, false, "", esopsclient.ErrForbidden
	}
	if status >= 400 {
		return nil, false, "", clientutil.ClassifyStatus("security internalusers", status, body)
	}

	var raw map[string]osInternalUserWire
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, false, "", errors.Join(errors.New("decoding OS internalusers response"), err)
	}
	out := make([]types.SecurityUser, 0, len(raw))
	for name, u := range raw {
		out = append(out, types.SecurityUser{
			Username: name,
			// OS internalusers has no explicit enable flag. Built-ins (admin,
			// kibanaserver) are always active; user-defined accounts can be
			// "disabled" only by deleting them. Report true so the table
			// column is meaningful on ES and OS alike.
			Enabled:      true,
			Reserved:     u.Reserved,
			Roles:        u.SecurityRoles,
			BackendRoles: u.BackendRoles,
			FullName:     u.Description,
		})
	}
	return out, false, "", nil
}

func (c *Client) fetchOSRoles(ctx context.Context) ([]types.SecurityRole, error) {
	body, status, err := c.doSecurityGET(ctx, "/_plugins/_security/api/roles")
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		return nil, esopsclient.ErrAuth
	}
	if status == http.StatusForbidden {
		return nil, esopsclient.ErrForbidden
	}
	if status >= 400 {
		return nil, clientutil.ClassifyStatus("security roles", status, body)
	}

	var raw map[string]osRoleWire
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.Join(errors.New("decoding OS roles response"), err)
	}
	out := make([]types.SecurityRole, 0, len(raw))
	for name, r := range raw {
		patterns := make([]string, 0)
		for _, ix := range r.IndexPermissions {
			patterns = append(patterns, ix.IndexPatterns...)
		}
		out = append(out, types.SecurityRole{
			Name:              name,
			Reserved:          r.Reserved,
			ClusterPrivileges: r.ClusterPermissions,
			IndexPatterns:     clientutil.UniqueStrings(patterns),
			Description:       r.Description,
		})
	}
	return out, nil
}

// osRoleMappingWire mirrors a single
// /_plugins/_security/api/rolesmapping entry. Roles is the list of
// product roles the mapping grants; backend_roles/users/hosts are the
// external identifiers that satisfy it. Reserved/Hidden flag built-in
// or system mappings.
type osRoleMappingWire struct {
	Reserved     bool     `json:"reserved"`
	Hidden       bool     `json:"hidden"`
	BackendRoles []string `json:"backend_roles"`
	Hosts        []string `json:"hosts"`
	Users        []string `json:"users"`
	// The OS plugin does not expose an explicit `enabled` flag on a
	// mapping — entries either exist or they don't. We surface
	// Enabled=true for every present mapping so the JSON shape lines
	// up with the ES side, where the flag is real.
}

// fetchOSRoleMappings reads /_plugins/_security/api/rolesmapping. A 403
// (caller lacks restapi:admin/rolesmapping) records a status note and
// returns no mappings, so the audit continues with what's available.
func (c *Client) fetchOSRoleMappings(ctx context.Context) ([]types.SecurityRoleMapping, string, error) {
	body, status, err := c.doSecurityGET(ctx, "/_plugins/_security/api/rolesmapping")
	if err != nil {
		return nil, "", err
	}
	switch status {
	case http.StatusForbidden:
		return nil, "role_mappings: forbidden — needs restapi:admin/rolesmapping permission", nil
	case http.StatusUnauthorized:
		return nil, "", esopsclient.ErrAuth
	case http.StatusNotFound:
		return nil, "role_mappings: endpoint not available on this cluster", nil
	}
	if status >= 400 {
		return nil, fmt.Sprintf("role_mappings: HTTP %d", status), nil
	}

	var raw map[string]osRoleMappingWire
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, "", errors.Join(errors.New("decoding OS rolesmapping response"), err)
	}
	out := make([]types.SecurityRoleMapping, 0, len(raw))
	for name, m := range raw {
		out = append(out, types.SecurityRoleMapping{
			Name:         name,
			Reserved:     m.Reserved,
			Enabled:      true,
			Roles:        []string{name}, // OS keys mappings by the role name itself
			BackendRoles: m.BackendRoles,
			Users:        m.Users,
			Hosts:        m.Hosts,
		})
	}
	return out, "", nil
}

// osTenantWire mirrors a single /_plugins/_security/api/tenants entry.
// Reserved/Hidden flag built-in or system tenants ("global_tenant",
// "admin_tenant"). Description is operator-supplied, often empty.
type osTenantWire struct {
	Reserved    bool   `json:"reserved"`
	Hidden      bool   `json:"hidden"`
	Description string `json:"description"`
}

// fetchOSTenants reads /_plugins/_security/api/tenants. Same partial-
// failure pattern as role mappings: 403/404 surface as a status note
// rather than aborting.
func (c *Client) fetchOSTenants(ctx context.Context) ([]types.SecurityTenant, string, error) {
	body, status, err := c.doSecurityGET(ctx, "/_plugins/_security/api/tenants")
	if err != nil {
		return nil, "", err
	}
	switch status {
	case http.StatusForbidden:
		return nil, "tenants: forbidden — needs restapi:admin/tenants permission", nil
	case http.StatusUnauthorized:
		return nil, "", esopsclient.ErrAuth
	case http.StatusNotFound:
		return nil, "tenants: endpoint not available (cluster has multitenancy disabled)", nil
	}
	if status >= 400 {
		return nil, fmt.Sprintf("tenants: HTTP %d", status), nil
	}

	var raw map[string]osTenantWire
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, "", errors.Join(errors.New("decoding OS tenants response"), err)
	}
	out := make([]types.SecurityTenant, 0, len(raw))
	for name, t := range raw {
		out = append(out, types.SecurityTenant{
			Name:        name,
			Reserved:    t.Reserved,
			Description: t.Description,
		})
	}
	return out, "", nil
}

// doSecurityGET drops down to the underlying opensearch-go transport so
// we can hit the /_plugins/_security/api/* endpoints that opensearch-go/v4
// doesn't wrap in typed helpers. Returns (body, status, err) — the body
// is read into memory so the caller can use it in either a decode or an
// error message without a second pass over the response.
func (c *Client) doSecurityGET(ctx context.Context, path string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.api.Client.Perform(req)
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

// HotThreads implements client.HotThreadsInspector. The OS v4 SDK's
// typed NodesHotThreadsParams does not expose the `type` query
// parameter, so a non-default Type (wait/block/mem) falls back to DoGET
// via buildHotThreadsPath — OS does accept the param on the wire, only
// the typed wrapper omits it.
func (c *Client) HotThreads(ctx context.Context, req types.HotThreadsRequest) (types.HotThreads, error) {
	if req.Type != "" && !strings.EqualFold(req.Type, "cpu") {
		return c.hotThreadsViaGET(ctx, req)
	}

	osReq := &osapi.NodesHotThreadsReq{
		NodeID: req.Nodes,
		Params: osapi.NodesHotThreadsParams{
			Threads:           req.Threads,
			Interval:          req.Interval,
			Snapshots:         req.Snapshots,
			IgnoreIdleThreads: req.IgnoreIdleThreads,
		},
	}
	resp, err := c.api.Nodes.HotThreads(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp)
		}
		return types.HotThreads{}, classifyResponseErr(err, status)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return types.HotThreads{}, esopsclient.ErrAuth
	case http.StatusForbidden:
		return types.HotThreads{}, esopsclient.ErrForbidden
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return types.HotThreads{}, errors.Join(errors.New("reading hot_threads response"), err)
	}
	if resp.StatusCode >= 400 {
		return types.HotThreads{}, clientutil.ClassifyStatus("nodes hot_threads", resp.StatusCode, body)
	}
	return types.HotThreads{Text: string(body)}, nil
}

// buildHotThreadsPath renders /_nodes[/<id>]/hot_threads with the query
// parameters the cluster honours. Used only by hotThreadsViaGET — the
// typed SDK wrapper handles the happy path. Keys are alphabetically
// ordered so a test of "was type=wait sent?" can pin the full URL
// without worrying about map iteration order.
func buildHotThreadsPath(req types.HotThreadsRequest) string {
	var p strings.Builder
	p.WriteString("/_nodes")
	if len(req.Nodes) > 0 {
		p.WriteString("/")
		p.WriteString(strings.Join(req.Nodes, ","))
	}
	p.WriteString("/hot_threads")

	q := make([]string, 0, 6)
	if req.IgnoreIdleThreads != nil {
		q = append(q, "ignore_idle_threads="+strconv.FormatBool(*req.IgnoreIdleThreads))
	}
	if req.Interval > 0 {
		q = append(q, "interval="+req.Interval.String())
	}
	if req.Snapshots != nil {
		q = append(q, "snapshots="+strconv.Itoa(*req.Snapshots))
	}
	if req.Threads != nil {
		q = append(q, "threads="+strconv.Itoa(*req.Threads))
	}
	if req.Type != "" {
		q = append(q, "type="+req.Type)
	}
	if len(q) > 0 {
		p.WriteString("?")
		p.WriteString(strings.Join(q, "&"))
	}
	return p.String()
}

// hotThreadsViaGET is the fallback for Type values (wait/block/mem) the
// typed OS client doesn't expose. The cluster accepts the param via the
// wire, so we just assemble the URL ourselves and GET it.
func (c *Client) hotThreadsViaGET(ctx context.Context, req types.HotThreadsRequest) (types.HotThreads, error) {
	body, status, err := c.doSecurityGET(ctx, buildHotThreadsPath(req))
	if err != nil {
		return types.HotThreads{}, err
	}
	switch status {
	case http.StatusUnauthorized:
		return types.HotThreads{}, esopsclient.ErrAuth
	case http.StatusForbidden:
		return types.HotThreads{}, esopsclient.ErrForbidden
	}
	if status >= 400 {
		return types.HotThreads{}, clientutil.ClassifyStatus("nodes hot_threads", status, body)
	}
	return types.HotThreads{Text: string(body)}, nil
}

// DoGET implements client.ClusterDiagnoser via the same path the typed
// opensearch-go client uses, so auth/transport match the rest of the
// adapter. 401/403 translate to sentinel errors; transport failures wrap
// ErrUnreachable; any other non-2xx returns body and status so the diag
// collector records the cluster's own response verbatim — a 404 on
// /_ilm/... is expected on OpenSearch, not a bundle failure.
func (c *Client) DoGET(ctx context.Context, path string) ([]byte, int, error) {
	body, status, err := c.doSecurityGET(ctx, path)
	if err != nil {
		return nil, status, err
	}
	switch status {
	case http.StatusUnauthorized:
		return body, status, esopsclient.ErrAuth
	case http.StatusForbidden:
		return body, status, esopsclient.ErrForbidden
	}
	return body, status, nil
}

// Nodes implements client.NodeInspector via /_cat/nodes. The OS API uses
// node.roles (plural) for the comma-separated full-name role list — on
// ES that column is node.role (singular); the typed clients hide the
// naming split, but the column requested here has to match the OS spelling.
//
// We request both the (abbreviated) Role and (full-name) Roles columns
// and prefer Roles. Older OS clusters that omit Roles fall through to
// a best-effort parse of the abbreviated form so the data-node identity
// stays accurate even on legacy versions.
//
// bytes=b is required so the disk/heap columns arrive as bare integers
// rather than the cluster's human-readable suffix ("12.3kb"), which
// varies subtly across versions and would force a per-version parser.
// The full column list is what `ops nodes` renders.
func (c *Client) Nodes(ctx context.Context) ([]types.Node, error) {
	req := &osapi.CatNodesReq{
		Params: osapi.CatNodesParams{
			Bytes: "b",
			H: []string{
				"name", "node.role", "node.roles", "ip", "version", "master",
				"heap.percent", "heap.current", "heap.max",
				"disk.used", "disk.avail", "disk.total", "disk.used_percent",
				"cpu", "load_1m", "uptime",
			},
		},
	}
	resp, err := c.api.Cat.Nodes(ctx, req)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return nil, classifyResponseErr(err, status)
	}

	out := make([]types.Node, 0, len(resp.Nodes))
	for _, n := range resp.Nodes {
		roles := splitNodeRoles(n.Roles)
		if len(roles) == 0 {
			roles = abbreviatedRolesToFull(n.Role)
		}
		master := false
		// Both clusters report '*' on the elected master row; OS also
		// surfaces the cluster_manager column as the canonical name on
		// 2.x+. Either flag is good enough — operators care about the
		// boolean, not which column carried it.
		if n.Master == "*" || n.ClusterManager == "*" {
			master = true
		}
		out = append(out, types.Node{
			Name:            n.Name,
			Roles:           roles,
			IsDataNode:      hasDataRole(roles),
			IP:              n.IP,
			Version:         n.Version,
			Master:          master,
			HeapPercent:     intPtrValue(n.HeapPercent),
			HeapUsedBytes:   parseInt64(deref(n.HeapCurrent)),
			HeapMaxBytes:    parseInt64(deref(n.HeapMax)),
			DiskUsedBytes:   parseInt64(deref(n.DiskUsed)),
			DiskAvailBytes:  parseInt64(deref(n.DiskAvail)),
			DiskTotalBytes:  parseInt64(deref(n.DiskTotal)),
			DiskUsedPercent: parsePercent(deref(n.DiskUsedPercent)),
			CPUPercent:      intPtrValue(n.CPU),
			Load1m:          deref(n.Load1M),
			Uptime:          deref(n.Uptime),
		})
	}
	return out, nil
}

// deref unwraps a *string pointer, returning "" for nil. The OS
// cat-nodes response models every optional column as *string; coordinating
// nodes legitimately omit disk/heap fields, so a nil pointer is information
// (no value) rather than an error.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// intPtrValue is the int sibling of deref for the percent and CPU columns
// that the OS client decodes directly into *int.
func intPtrValue(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// parseInt64 turns a /_cat/nodes byte string (rendered with bytes=b, so
// "12345" not "12.3kb") into an int64. An empty cell yields zero rather
// than an error: this is best-effort enrichment for `ops nodes` and a
// missing column is information, not a failure.
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

// splitNodeRoles parses a /_cat/nodes "data,ingest,master"-style list
// into a clean slice. Empty entries (which arise from a coordinating-only
// node returning "") are dropped so hasDataRole's prefix check doesn't
// accidentally match the empty string.
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

// abbreviatedRolesToFull is a best-effort decode of the legacy single-char
// role column ("dim" for data+ingest+master). Only the chars relevant to
// data-node identification are mapped — anything else gets surfaced
// verbatim so an operator inspecting the raw JSON sees what the cluster
// reported. Used only when node.roles is absent (older OS versions).
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
		'r': "remote_cluster_client",
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

// Shrink implements client.IndexShrinker via /<source>/_shrink/<target>.
// Body shape is identical to ES — {settings: {…}} — built by the
// shared helper in this package.
//
// The cluster has no native dry-run; the business layer simulates one
// client-side. WaitForActiveShards is forwarded as the cluster's own
// query param.
func (c *Client) Shrink(ctx context.Context, req types.IndexShrinkRequest) (types.IndexShrinkResult, error) {
	body, err := buildShrinkBody(req)
	if err != nil {
		return types.IndexShrinkResult{}, err
	}

	osReq := osapi.IndicesShrinkReq{
		Index:  req.Source,
		Target: req.Target,
		Body:   bytes.NewReader(body),
		Params: osapi.IndicesShrinkParams{},
	}
	if req.WaitForActiveShards != "" {
		osReq.Params.WaitForActiveShards = req.WaitForActiveShards
	}
	if req.CopySettings != nil {
		v := *req.CopySettings
		osReq.Params.CopySettings = &v
	}

	resp, err := c.api.Indices.Shrink(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.IndexShrinkResult{}, classifyResponseErr(err, status)
	}
	return types.IndexShrinkResult{
		Action:             "shrink",
		Source:             req.Source,
		Target:             req.Target,
		Acknowledged:       resp.Acknowledged,
		ShardsAcknowledged: resp.ShardsAcknowledged,
		TargetShards:       req.TargetShards,
	}, nil
}

// buildShrinkBody renders the JSON body the cluster expects on a
// shrink call. Mirrors the ES adapter — same wire shape on both
// dialects, kept per-package because a future divergence
// (ES-specific `aliases` block, OS-specific tunables) would be a
// per-package change rather than churn in a shared helper.
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

// Aliases implements client.AliasInspector via /_cat/aliases. The OS
// v4 typed client returns CatAliasResp rows whose IsWriteIndex is a
// raw "true"/"false"/"-" string, matching the ES adapter — one parser
// for both.
//
// AliasFilter.Index is applied client-side: /_cat/aliases takes a
// name path segment for alias filtering but no index segment, so the
// adapter filters by index name in memory to match the AliasFilter
// contract.
func (c *Client) Aliases(ctx context.Context, filter types.AliasFilter) ([]types.Alias, error) {
	req := &osapi.CatAliasesReq{
		Params: osapi.CatAliasesParams{
			H: []string{
				"alias", "index", "filter",
				"routing.index", "routing.search", "is_write_index",
			},
			// Server-side sort matches the ES adapter so a captured
			// stdout is stable across dialects without a client-side
			// pass.
			Sort: []string{"alias", "index"},
		},
	}
	if filter.Alias != "" {
		req.Aliases = esopsclient.SplitCSV(filter.Alias)
	}

	resp, err := c.api.Cat.Aliases(ctx, req)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return nil, classifyResponseErr(err, status)
	}

	wantedSet := map[string]struct{}{}
	for _, name := range esopsclient.SplitCSV(filter.Index) {
		n := strings.TrimSpace(name)
		if n != "" {
			wantedSet[n] = struct{}{}
		}
	}

	out := make([]types.Alias, 0, len(resp.Aliases))
	for _, r := range resp.Aliases {
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
	return out, nil
}

// catAliasesRowOS mirrors a /_cat/aliases?format=json row on
// OpenSearch — same wire shape as the ES adapter's catAliasesRow.
type catAliasesRowOS struct {
	Alias         string `json:"alias"`
	Index         string `json:"index"`
	Filter        string `json:"filter"`
	RoutingIndex  string `json:"routing.index"`
	RoutingSearch string `json:"routing.search"`
	IsWriteIndex  string `json:"is_write_index"`
}

// AliasesStream implements client.AliasInspector against OpenSearch
// via raw HTTP — same rationale as IndicesStream: the v4 typed
// CatAliasesReq buffers the response, defeating the streaming
// contract. Per-index filtering is row-by-row so the streaming path
// matches the buffered Aliases call.
func (c *Client) AliasesStream(ctx context.Context, filter types.AliasFilter, yield func(types.Alias) error) error {
	q := url.Values{}
	q.Set("format", "json")
	q.Set("h", strings.Join([]string{
		"alias", "index", "filter",
		"routing.index", "routing.search", "is_write_index",
	}, ","))
	q.Set("s", "alias,index")

	path := "/_cat/aliases"
	if filter.Alias != "" {
		path += "/" + strings.Join(esopsclient.SplitCSV(filter.Alias), ",")
	}
	path += "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	resp, err := c.api.Client.Perform(req)
	if err != nil {
		return clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(clientutil.NewBoundedReader(resp.Body, 64*1024, "cat aliases (stream) error"))
		return clientutil.ClassifyStatus("cat aliases (stream)", resp.StatusCode, body)
	}

	wanted := map[string]struct{}{}
	for _, n := range esopsclient.SplitCSV(filter.Index) {
		if t := strings.TrimSpace(n); t != "" {
			wanted[t] = struct{}{}
		}
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "cat aliases (stream)")
	dec := json.NewDecoder(body)
	if tok, err := dec.Token(); err != nil {
		return errors.Join(errors.New("decoding cat aliases (stream) response"), err)
	} else if d, ok := tok.(json.Delim); !ok || d != '[' {
		return fmt.Errorf("cat aliases (stream): expected JSON array, got %v", tok)
	}
	for dec.More() {
		var r catAliasesRowOS
		if err := dec.Decode(&r); err != nil {
			return errors.Join(errors.New("decoding cat aliases (stream) row"), err)
		}
		if len(wanted) > 0 {
			if _, ok := wanted[r.Index]; !ok {
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

// emptyOnDash maps the cluster's "-" sentinel to "" so JSON omitempty
// renders cleanly. Other values pass through unchanged.
func emptyOnDash(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

// parseClusterBool maps the cluster's tri-state "true"/"false"/"-"
// (or "") into a Go bool. Anything other than a literal "true" maps
// to false — same semantics as the ES adapter.
func parseClusterBool(s string) bool {
	return strings.EqualFold(strings.TrimSpace(s), "true")
}

// UpdateAliases implements client.AliasUpdater via POST /_aliases.
// Body shape is identical to ES (actions array of {add|remove}
// envelopes); the OS typed client wraps it as AliasesReq with a Body
// reader and surfaces only the acknowledged flag in AliasesResp.
func (c *Client) UpdateAliases(ctx context.Context, req types.AliasUpdateRequest) (types.AliasUpdateResult, error) {
	body, err := buildUpdateAliasesBody(req.Actions)
	if err != nil {
		return types.AliasUpdateResult{}, err
	}

	osReq := osapi.AliasesReq{
		Body: bytes.NewReader(body),
	}
	resp, err := c.api.Aliases(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.AliasUpdateResult{}, classifyResponseErr(err, status)
	}
	return types.AliasUpdateResult{
		Action:       "alias-update",
		Acknowledged: resp.Acknowledged,
		Actions:      req.Actions,
	}, nil
}

// buildUpdateAliasesBody renders the body shape POST /_aliases expects.
// Mirrors the ES adapter byte-for-byte; kept in this package so a
// future per-dialect divergence is a per-package change rather than
// shared-helper churn.
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

// settingsGetEntryWire mirrors one entry of GET /<indices>/_settings.
// The OS typed client decodes Settings into a json.RawMessage rather
// than walking the nested object — we re-decode it ourselves so the
// neutral type stays free of upstream types.
type settingsGetEntryWire struct {
	Settings map[string]any `json:"settings"`
	Defaults map[string]any `json:"defaults"`
}

// GetSettings implements client.IndexSettingsInspector via
// GET /<indices>/_settings/<names>. The OS typed client decodes only
// the Settings block; we bypass it and decode the cluster's full
// envelope ourselves so the include_defaults code path can merge
// defaults+settings the same way the ES adapter does.
func (c *Client) GetSettings(ctx context.Context, req types.IndexSettingsGetRequest) ([]types.IndexSettings, error) {
	names := req.Names

	var path strings.Builder
	if len(req.Indices) > 0 {
		path.WriteString("/")
		path.WriteString(strings.Join(req.Indices, ","))
	}
	path.WriteString("/_settings")
	if len(names) > 0 {
		path.WriteString("/")
		path.WriteString(strings.Join(names, ","))
	}

	q := path.String()
	params := []string{}
	if req.IncludeDefaults {
		params = append(params, "include_defaults=true")
	}
	if req.FlatSettings {
		params = append(params, "flat_settings=true")
	}
	if req.IgnoreUnavailable {
		params = append(params, "ignore_unavailable=true")
	}
	if len(params) > 0 {
		q += "?" + strings.Join(params, "&")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, q, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "indices get_settings"))
	if err != nil {
		return nil, errors.Join(errors.New("reading indices get_settings response"), err)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, esopsclient.ErrAuth
	case http.StatusForbidden:
		return nil, esopsclient.ErrForbidden
	}
	if resp.StatusCode >= 400 {
		return nil, clientutil.ClassifyStatus("indices get_settings", resp.StatusCode, body)
	}

	var wire map[string]settingsGetEntryWire
	if err := json.Unmarshal(body, &wire); err != nil {
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

// GetSettingsStream implements client.IndexSettingsInspector via raw
// HTTP. Streams the cluster's top-level {"index1": {...}, ...}
// envelope one entry at a time so a `settings get -o ndjson
// --include-defaults` against a multi-thousand-index fleet doesn't
// have to buffer the full nested-defaults payload into memory.
func (c *Client) GetSettingsStream(ctx context.Context, req types.IndexSettingsGetRequest, yield func(types.IndexSettings) error) error {
	names := req.Names

	var path strings.Builder
	if len(req.Indices) > 0 {
		path.WriteString("/")
		path.WriteString(strings.Join(req.Indices, ","))
	}
	path.WriteString("/_settings")
	if len(names) > 0 {
		path.WriteString("/")
		path.WriteString(strings.Join(names, ","))
	}

	q := path.String()
	params := []string{}
	if req.IncludeDefaults {
		params = append(params, "include_defaults=true")
	}
	if req.FlatSettings {
		params = append(params, "flat_settings=true")
	}
	if req.IgnoreUnavailable {
		params = append(params, "ignore_unavailable=true")
	}
	if len(params) > 0 {
		q += "?" + strings.Join(params, "&")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, q, nil)
	if err != nil {
		return err
	}
	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(clientutil.NewBoundedReader(resp.Body, 64*1024, "indices get_settings (stream) error"))
		return clientutil.ClassifyStatus("indices get_settings (stream)", resp.StatusCode, errBody)
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "indices get_settings (stream)")
	dec := json.NewDecoder(body)
	if tok, err := dec.Token(); err != nil {
		return errors.Join(errors.New("decoding indices get_settings (stream) response"), err)
	} else if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("indices get_settings (stream): expected JSON object, got %v", tok)
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return errors.Join(errors.New("decoding indices get_settings (stream) key"), err)
		}
		name, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("indices get_settings (stream): expected string key, got %v", keyTok)
		}
		var entry settingsGetEntryWire
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

// PutSettings implements client.IndexSettingsUpdater via
// PUT /<indices>/_settings. The OS typed wrapper takes Indices as a
// []string and a Body io.Reader; the cluster validates field names.
func (c *Client) PutSettings(ctx context.Context, req types.IndexSettingsUpdateRequest) (types.IndexSettingsUpdateResult, error) {
	osReq := osapi.SettingsPutReq{
		Indices: req.Indices,
		Body:    bytes.NewReader(req.Body),
		Params:  osapi.SettingsPutParams{},
	}
	if req.PreserveExisting {
		t := true
		osReq.Params.PreserveExisting = &t
	}
	if req.IgnoreUnavailable {
		t := true
		osReq.Params.IgnoreUnavailable = &t
	}

	resp, err := c.api.Indices.Settings.Put(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.IndexSettingsUpdateResult{}, classifyResponseErr(err, status)
	}
	return types.IndexSettingsUpdateResult{
		Action:       "settings-update",
		Indices:      req.Indices,
		Acknowledged: resp.Acknowledged,
	}, nil
}

// indexTemplateGetWire mirrors GET /_index_template[/<name>] on OS. The
// OS typed client exposes IndexTemplateGetResp whose Template fields
// are json.RawMessage; decoding the cluster envelope ourselves keeps
// the adapter boundary free of upstream types and matches the ES
// adapter's neutral output shape exactly.
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
// GET /_index_template/<names>. Empty names asks the cluster for every
// template. Mirrors the ES adapter shape.
func (c *Client) GetTemplates(ctx context.Context, names []string) ([]types.IndexTemplate, error) {
	osReq := &osapi.IndexTemplateGetReq{
		IndexTemplates: names,
	}
	resp, err := c.api.IndexTemplate.Get(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return nil, classifyResponseErr(err, status)
	}

	// Bypass the typed response (which exposes the inner Template/
	// DataStream blocks as json.RawMessage) and re-decode the raw body
	// into our neutral shape. This keeps the public types upstream-free
	// and lets us share the renderer with the ES adapter.
	raw := resp.Inspect().Response
	if raw == nil || raw.Body == nil {
		return nil, errors.New("opensearch get_index_template: empty response body")
	}
	body, err := io.ReadAll(clientutil.NewBoundedReader(raw.Body, clientutil.DefaultMaxResponseBytes, "get_index_template"))
	if err != nil {
		return nil, errors.Join(errors.New("reading get_index_template response"), err)
	}
	defer func() { _ = raw.Body.Close() }()

	var wire indexTemplateGetWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, errors.Join(errors.New("decoding get_index_template response"), err)
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
// 404s.
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

// PutTemplate implements client.IndexTemplateUpdater via
// PUT /_index_template/<name>. Body is forwarded verbatim. The OS
// typed client exposes a Create flag we forward as ?create=true on
// the cluster side.
func (c *Client) PutTemplate(ctx context.Context, req types.IndexTemplatePutRequest) (types.IndexTemplatePutResult, error) {
	osReq := osapi.IndexTemplateCreateReq{
		IndexTemplate: req.Name,
		Body:          bytes.NewReader(req.Body),
		Params:        osapi.IndexTemplateCreateParams{},
	}
	if req.Create {
		t := true
		osReq.Params.Create = &t
	}

	resp, err := c.api.IndexTemplate.Create(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.IndexTemplatePutResult{}, classifyResponseErr(err, status)
	}
	return types.IndexTemplatePutResult{
		Action:       "template-put",
		Name:         req.Name,
		Acknowledged: resp.Acknowledged,
	}, nil
}

// DeleteTemplate implements client.IndexTemplateUpdater via
// DELETE /_index_template/<name>.
func (c *Client) DeleteTemplate(ctx context.Context, req types.IndexTemplateDeleteRequest) (types.IndexTemplateDeleteResult, error) {
	osReq := osapi.IndexTemplateDeleteReq{
		IndexTemplate: req.Name,
	}
	resp, err := c.api.IndexTemplate.Delete(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.IndexTemplateDeleteResult{}, classifyResponseErr(err, status)
	}
	return types.IndexTemplateDeleteResult{
		Action:       "template-delete",
		Name:         req.Name,
		Acknowledged: resp.Acknowledged,
	}, nil
}

// GetClusterSettings implements client.ClusterSettingsManager. The
// flat_settings query param flattens nested keys, so the exclude entry
// arrives as "cluster.routing.allocation.exclude._name" — far easier to
// reach for than the cluster's nested-object default.
func (c *Client) GetClusterSettings(ctx context.Context) (types.ClusterSettingsView, error) {
	flat := true
	req := &osapi.ClusterGetSettingsReq{
		Params: osapi.ClusterGetSettingsParams{
			FlatSettings: &flat,
		},
	}
	resp, err := c.api.Cluster.GetSettings(ctx, req)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.ClusterSettingsView{}, classifyResponseErr(err, status)
	}

	persistent, err := unmarshalSettingsScope(resp.Persistent)
	if err != nil {
		return types.ClusterSettingsView{}, err
	}
	transient, err := unmarshalSettingsScope(resp.Transient)
	if err != nil {
		return types.ClusterSettingsView{}, err
	}
	return types.ClusterSettingsView{
		PersistentExcludeName: parseAllocationExcludeName(persistent),
		TransientExcludeName:  parseAllocationExcludeName(transient),
	}, nil
}

// PutAllocationExclude implements client.ClusterSettingsManager. Body
// shape mirrors the ES adapter — one scope, one key, JSON null when
// the slice is empty so the cluster removes the key rather than keep
// an empty-string value visible in /_cluster/settings.
func (c *Client) PutAllocationExclude(ctx context.Context, req types.ClusterAllocationExcludeUpdate) (types.ClusterSettingsUpdateResult, error) {
	scope := "transient"
	if req.Persistent {
		scope = "persistent"
	}

	var value any
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

	flat := true
	osReq := osapi.ClusterPutSettingsReq{
		Body: bytes.NewReader(body),
		Params: osapi.ClusterPutSettingsParams{
			FlatSettings: &flat,
		},
	}
	resp, err := c.api.Cluster.PutSettings(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.ClusterSettingsUpdateResult{}, classifyResponseErr(err, status)
	}
	return types.ClusterSettingsUpdateResult{Acknowledged: resp.Acknowledged}, nil
}

// unmarshalSettingsScope decodes one scope (persistent or transient) of
// /_cluster/settings into a flat map. flat_settings on the request
// makes this a one-level json.RawMessage decode; an empty scope is the
// most common shape and yields an empty (non-nil) map for the parser.
func unmarshalSettingsScope(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errors.Join(errors.New("decoding cluster settings scope"), err)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// parseAllocationExcludeName mirrors the ES helper of the same name:
// pulls cluster.routing.allocation.exclude._name out of a flat map and
// splits the comma-separated value into a clean slice.
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
// POST /_cluster/voting_config_exclusions. The OS API takes a single
// `node_names` query parameter (comma-separated); the cluster appends
// to the existing list and de-duplicates, so the call is idempotent on
// re-add.
//
// Both OS 1.x → 3.x expose the same endpoint and accept the same
// query shape — `cluster_manager` terminology is purely cosmetic in
// docs, the wire parameter is still `node_names`.
func (c *Client) AddVotingExclusions(ctx context.Context, names []string) (types.VotingExclusionsResult, error) {
	if len(names) == 0 {
		return types.VotingExclusionsResult{}, fmt.Errorf("at least one node name is required")
	}
	osReq := osapi.ClusterPostVotingConfigExclusionsReq{
		Params: osapi.ClusterPostVotingConfigExclusionsParams{
			NodeNames: strings.Join(names, ","),
		},
	}
	resp, err := c.api.Cluster.PostVotingConfigExclusions(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp)
		}
		return types.VotingExclusionsResult{}, classifyResponseErr(err, status)
	}
	return types.VotingExclusionsResult{Acknowledged: true}, nil
}

// ClearVotingExclusions implements client.VotingExclusionManager via
// DELETE /_cluster/voting_config_exclusions. The cluster API only
// supports clear-all (no per-node delete), so this wipes every entry
// in the list.
//
// wait_for_removal=false is set explicitly because the calling path
// (`ops uncordon --clear-voting`) is "I drained nodes but they're
// staying / coming back": the nodes named in the exclusion list are
// still in the cluster, so the cluster default (wait until the
// excluded nodes leave the cluster) hangs until the master timeout
// and 429s.
func (c *Client) ClearVotingExclusions(ctx context.Context) (types.VotingExclusionsResult, error) {
	waitFalse := false
	osReq := osapi.ClusterDeleteVotingConfigExclusionsReq{
		Params: osapi.ClusterDeleteVotingConfigExclusionsParams{
			WaitForRemoval: &waitFalse,
		},
	}
	resp, err := c.api.Cluster.DeleteVotingConfigExclusions(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp)
		}
		return types.VotingExclusionsResult{}, classifyResponseErr(err, status)
	}
	return types.VotingExclusionsResult{Acknowledged: true}, nil
}

// GetVotingExclusions reads the current list via /_cluster/state with
// metric=metadata. The OS typed client decodes the
// VotingConfigExclusions array into a strongly-typed slice already,
// so we just walk it and project the names.
//
// filter_path narrows the response so the cluster doesn't ship the
// whole metadata blob — important on a fleet where /_cluster/state
// can be tens of MB.
func (c *Client) GetVotingExclusions(ctx context.Context) ([]string, error) {
	osReq := &osapi.ClusterStateReq{
		Metrics: []string{"metadata"},
		Params: osapi.ClusterStateParams{
			FilterPath: []string{"metadata.cluster_coordination.voting_config_exclusions.node_name"},
		},
	}
	resp, err := c.api.Cluster.State(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return nil, classifyResponseErr(err, status)
	}

	excl := resp.Metadata.ClusterCoordination.VotingConfigExclusions
	out := make([]string, 0, len(excl))
	for _, e := range excl {
		if e.NodeName != "" {
			out = append(out, e.NodeName)
		}
	}
	return out, nil
}

// Tasks implements client.TasksInspector. The default cluster
// grouping (`nodes.<node_id>.tasks.<composite>`) is the only shape
// every OS version reports as a map — `group_by=none` returns a flat
// array the typed client can't decode (its `Tasks` field is typed as
// map). The adapter walks the per-node `Tasks` map directly.
func (c *Client) Tasks(ctx context.Context, filter types.TasksFilter) ([]types.Task, error) {
	params := osapi.TasksListParams{
		Actions:      filter.Actions,
		Nodes:        filter.Nodes,
		ParentTaskID: filter.ParentTaskID,
	}
	if filter.Detailed {
		d := true
		params.Detailed = &d
	}
	req := &osapi.TasksListReq{Params: params}
	resp, err := c.api.Tasks.List(ctx, req)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return nil, classifyResponseErr(err, status)
	}
	flat := map[string]osapi.TasksListTask{}
	for _, node := range resp.Nodes {
		for id, t := range node.Tasks {
			flat[id] = t
		}
	}
	return tasksFromListResp(flat), nil
}

// TasksStream implements client.TasksInspector. The opensearch-go
// client does not expose a streaming JSON decoder for /_tasks (the
// response is parsed into a typed struct in one shot), so the adapter
// reuses the buffered List call and yields one task at a time. The
// callback API still gives the renderer the streaming-friendly shape;
// memory pressure is bounded by the cluster's own response size, the
// same as the buffered Tasks() path.
//
// Sort order is left to map iteration so streaming consumers do not
// pay for a sort they did not ask for; the buffered Tasks() path is
// what guarantees alphabetic order by ID.
func (c *Client) TasksStream(ctx context.Context, filter types.TasksFilter, yield func(types.Task) error) error {
	params := osapi.TasksListParams{
		Actions:      filter.Actions,
		Nodes:        filter.Nodes,
		ParentTaskID: filter.ParentTaskID,
	}
	if filter.Detailed {
		d := true
		params.Detailed = &d
	}
	req := &osapi.TasksListReq{Params: params}
	resp, err := c.api.Tasks.List(ctx, req)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return classifyResponseErr(err, status)
	}
	for _, node := range resp.Nodes {
		for id, t := range node.Tasks {
			task := types.Task{
				ID:               id,
				Node:             t.Node,
				Type:             t.Type,
				Action:           t.Action,
				Description:      t.Description,
				StartTimeMillis:  t.StartTimeInMillis,
				RunningTimeNanos: t.RunningTimeInNanos,
				Cancellable:      t.Cancellable,
				Cancelled:        t.Cancelled,
				ParentTaskID:     t.ParentTaskID,
			}
			if err := yield(task); err != nil {
				return err
			}
		}
	}
	return nil
}

// CancelTasks implements client.TasksCanceller. The OS typed cancel
// response groups by node (Nodes:{<id>:{Tasks:{...}}}) regardless of
// any group_by hint — the parameter is not honoured on the cancel
// endpoint — so the adapter flattens the nested map client-side into
// the same row-per-task slice the list path returns.
//
// At most one TaskIDs entry is honoured per call (the cluster's
// /_tasks/<id>/_cancel endpoint takes a single ID); multi-ID requests
// are fanned out by the business layer and folded back together.
func (c *Client) CancelTasks(ctx context.Context, req types.TasksCancelRequest) (types.TasksCancelResult, error) {
	if len(req.TaskIDs) > 1 {
		return types.TasksCancelResult{}, fmt.Errorf("opensearch tasks cancel: capability accepts at most one TaskIDs entry; got %d (business layer should fan out)", len(req.TaskIDs))
	}
	taskID := ""
	if len(req.TaskIDs) == 1 {
		taskID = req.TaskIDs[0]
	}
	params := osapi.TasksCancelParams{
		Actions:      req.Filter.Actions,
		Nodes:        req.Filter.Nodes,
		ParentTaskID: req.Filter.ParentTaskID,
	}
	if req.WaitForCompletion {
		w := true
		params.WaitForCompletion = &w
	}
	osReq := osapi.TasksCancelReq{TaskID: taskID, Params: params}
	resp, err := c.api.Tasks.Cancel(ctx, osReq)
	if err != nil {
		status := 0
		if resp != nil {
			status = statusOf(resp.Inspect().Response)
		}
		return types.TasksCancelResult{}, classifyResponseErr(err, status)
	}

	flat := make([]types.Task, 0)
	for _, node := range resp.Nodes {
		for id, t := range node.Tasks {
			flat = append(flat, types.Task{
				ID:               id,
				Node:             t.Node,
				Type:             t.Type,
				Action:           t.Action,
				StartTimeMillis:  t.StartTimeInMillis,
				RunningTimeNanos: int64(t.RunningTimeInNanos),
				Cancellable:      t.Cancellable,
				Cancelled:        t.Cancelled,
			})
		}
	}
	sort.Slice(flat, func(i, j int) bool { return flat[i].ID < flat[j].ID })

	failures := make([]string, 0, len(resp.NodeFailures))
	for _, f := range resp.NodeFailures {
		failures = append(failures, formatNodeFailure(f))
	}
	out := types.TasksCancelResult{
		Cancelled:         flat,
		NodeFailures:      failures,
		WaitForCompletion: req.WaitForCompletion,
	}
	if taskID != "" {
		out.TaskIDs = []string{taskID}
	}
	return out, nil
}

// tasksFromListResp turns the typed map[id]TasksListTask into a sorted
// row-per-task slice. Sorting is stable so a saved JSON capture compares
// equal across runs even though Go's map iteration order doesn't.
func tasksFromListResp(in map[string]osapi.TasksListTask) []types.Task {
	out := make([]types.Task, 0, len(in))
	for id, t := range in {
		out = append(out, types.Task{
			ID:               id,
			Node:             t.Node,
			Type:             t.Type,
			Action:           t.Action,
			Description:      t.Description,
			StartTimeMillis:  t.StartTimeInMillis,
			RunningTimeNanos: t.RunningTimeInNanos,
			Cancellable:      t.Cancellable,
			Cancelled:        t.Cancelled,
			ParentTaskID:     t.ParentTaskID,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// formatNodeFailure renders the typed FailuresCause as the same
// "<node_id>: <reason>" string the ES adapter produces, so a captured
// TasksCancelResult.NodeFailures slice reads identically across
// dialects. Falls back to the cluster's exception type when no reason
// is present, and to "<unknown>" when even type is empty so an entry
// is never invisibly dropped from the rendered list.
func formatNodeFailure(f osapi.FailuresCause) string {
	prefix := f.NodeID
	if prefix == "" {
		prefix = "<unknown>"
	}
	switch {
	case f.Reason != "":
		return fmt.Sprintf("%s: %s", prefix, f.Reason)
	case f.Type != "":
		return fmt.Sprintf("%s: %s", prefix, f.Type)
	default:
		return prefix
	}
}
