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
//  1. GET /_security/user. If it 400s with a body indicating security is
//     not enabled, short-circuit to a "security disabled" report — this
//     is the ES-with-xpack.security.enabled=false case, valid and worth
//     surfacing.
//  2. Otherwise decode users, then GET /_security/role and decode roles.
//
// 401/403 bubble up as ErrAuth/ErrForbidden. Other 4xx/5xx bubble up
// wrapped. The report's Status always identifies the dialect.
func (c *Client) Audit(ctx context.Context) (types.SecurityReport, error) {
	report := types.SecurityReport{
		Status: types.SecurityStatus{Dialect: string(types.DialectElasticsearch)},
	}

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

	roles, err := c.fetchESRoles(ctx)
	if err != nil {
		return report, err
	}
	report.Roles = roles
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
