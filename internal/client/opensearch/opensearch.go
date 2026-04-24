// Package opensearch adapts the upstream opensearch-go/v4 client to
// esops capability interfaces. Nothing in this package leaks out into
// the rest of the codebase: callers get neutral types only.
package opensearch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
// erroring out. 401/403 bubble up for the operator to diagnose.
func (c *Client) Audit(ctx context.Context) (types.SecurityReport, error) {
	report := types.SecurityReport{
		Status: types.SecurityStatus{Dialect: string(types.DialectOpenSearch)},
	}

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

	roles, err := c.fetchOSRoles(ctx)
	if err != nil {
		return report, err
	}
	report.Roles = roles
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
		return nil, false, "", esopsclient.ClassifyStatus("security internalusers", status, body)
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
		return nil, esopsclient.ClassifyStatus("security roles", status, body)
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
			IndexPatterns:     esopsclient.UniqueStrings(patterns),
			Description:       r.Description,
		})
	}
	return out, nil
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
		return nil, 0, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}
