// Package types holds neutral domain types shared across esops. Nothing
// in here may depend on the upstream ES or OS client packages — these
// types are the boundary that keeps the rest of the codebase dialect-agnostic.
package types

import "time"

// Dialect identifies which product answered a cluster probe.
type Dialect string

const (
	DialectElasticsearch Dialect = "elasticsearch"
	DialectOpenSearch    Dialect = "opensearch"
)

// ClusterInfo is the outcome of the GET / probe.
type ClusterInfo struct {
	Dialect     Dialect
	ClusterName string
	Version     string
	Distribution string // OS only; empty on ES
}

// ClusterHealth is the normalised view of /_cluster/health across both
// dialects. Fields map 1:1 to the ES/OS payloads; anything dialect-specific
// (e.g. "discovered_master" vs "discovered_cluster_manager") is unified here.
type ClusterHealth struct {
	ClusterName                 string  `json:"cluster_name"       yaml:"cluster_name"`
	Status                      string  `json:"status"             yaml:"status"`
	TimedOut                    bool    `json:"timed_out"          yaml:"timed_out"`
	NumberOfNodes               int     `json:"number_of_nodes"    yaml:"number_of_nodes"`
	NumberOfDataNodes           int     `json:"number_of_data_nodes" yaml:"number_of_data_nodes"`
	ActivePrimaryShards         int     `json:"active_primary_shards" yaml:"active_primary_shards"`
	ActiveShards                int     `json:"active_shards"      yaml:"active_shards"`
	RelocatingShards            int     `json:"relocating_shards"  yaml:"relocating_shards"`
	InitializingShards          int     `json:"initializing_shards" yaml:"initializing_shards"`
	UnassignedShards            int     `json:"unassigned_shards"  yaml:"unassigned_shards"`
	DelayedUnassignedShards     int     `json:"delayed_unassigned_shards" yaml:"delayed_unassigned_shards"`
	NumberOfPendingTasks        int     `json:"number_of_pending_tasks" yaml:"number_of_pending_tasks"`
	NumberOfInFlightFetch       int     `json:"number_of_in_flight_fetch" yaml:"number_of_in_flight_fetch"`
	TaskMaxWaitingInQueueMillis int     `json:"task_max_waiting_in_queue_millis" yaml:"task_max_waiting_in_queue_millis"`
	ActiveShardsPercentAsNumber float64 `json:"active_shards_percent_as_number" yaml:"active_shards_percent_as_number"`
}

// Shard is a neutral view of a single shard allocation, normalised from
// /_cat/shards on both ES and OS. Node/IP are empty for unassigned shards.
// StoreBytes is always in bytes (we ask the cluster for bytes=b); 0 for
// unassigned or not-yet-materialised shards.
//
// The UnassignedReason/At/For/Details quartet is populated only for shards
// where the cluster has a reason to report — STARTED shards leave these
// empty. The cluster is the source of truth: if it returns no reason, we
// emit nothing (omitempty) rather than guessing.
type Shard struct {
	Index             string `json:"index"                         yaml:"index"`
	Shard             int    `json:"shard"                         yaml:"shard"`
	Primary           bool   `json:"primary"                       yaml:"primary"`
	State             string `json:"state"                         yaml:"state"`
	Docs              int64  `json:"docs"                          yaml:"docs"`
	StoreBytes        int64  `json:"store_bytes"                   yaml:"store_bytes"`
	Node              string `json:"node,omitempty"                yaml:"node,omitempty"`
	IP                string `json:"ip,omitempty"                  yaml:"ip,omitempty"`
	UnassignedReason  string `json:"unassigned_reason,omitempty"   yaml:"unassigned_reason,omitempty"`
	UnassignedAt      string `json:"unassigned_at,omitempty"       yaml:"unassigned_at,omitempty"`
	UnassignedFor     string `json:"unassigned_for,omitempty"      yaml:"unassigned_for,omitempty"`
	UnassignedDetails string `json:"unassigned_details,omitempty"  yaml:"unassigned_details,omitempty"`
}

// ShardRole is a tri-state filter: any primary/replica status, primaries
// only, or replicas only.
type ShardRole int

const (
	ShardRoleAny ShardRole = iota
	ShardRolePrimary
	ShardRoleReplica
)

// ShardFilter narrows a ShardInspector.Shards call. Zero value = "everything".
// Index is passed to the cluster as a path parameter where possible; the rest
// is applied client-side so behaviour is identical across dialects.
type ShardFilter struct {
	Index string    // comma-separated index patterns; "" = all
	State string    // e.g. "STARTED", "UNASSIGNED"; "" = any; case-insensitive
	Node  string    // exact node name; "" = any
	Role  ShardRole // primary/replica filter
}

// Index is a neutral view of a single index, normalised from /_cat/indices
// on both ES and OS. Health/Status come straight from the cluster:
// Health is "green"/"yellow"/"red" and Status is "open"/"close".
// StoreBytes and PrimaryStoreBytes are bytes (we ask for bytes=b);
// PrimaryStoreBytes is the primary-only subset of StoreBytes.
//
// Counts and sizes can be zero for just-created indices or closed indices
// where the cluster has not reported them yet — the cluster is the source
// of truth. CreationDateMillis is the unix-ms timestamp of the cluster's
// index metadata, 0 when unavailable (e.g. closed indices on older
// dialects).
type Index struct {
	Name               string `json:"name"                            yaml:"name"`
	UUID               string `json:"uuid,omitempty"                  yaml:"uuid,omitempty"`
	Health             string `json:"health"                          yaml:"health"`
	Status             string `json:"status"                          yaml:"status"`
	Primaries          int    `json:"primaries"                       yaml:"primaries"`
	Replicas           int    `json:"replicas"                        yaml:"replicas"`
	DocsCount          int64  `json:"docs_count"                      yaml:"docs_count"`
	DocsDeleted        int64  `json:"docs_deleted"                    yaml:"docs_deleted"`
	StoreBytes         int64  `json:"store_bytes"                     yaml:"store_bytes"`
	PrimaryStoreBytes  int64  `json:"primary_store_bytes"             yaml:"primary_store_bytes"`
	CreationDateMillis int64  `json:"creation_date_millis,omitempty"  yaml:"creation_date_millis,omitempty"`
}

// IndexSort is the sort key applied to IndexFilter results. The cluster
// response is unordered, so sort is applied business-side regardless of
// dialect.
type IndexSort int

const (
	IndexSortName IndexSort = iota // alphabetical by index name (default)
	IndexSortSize                  // StoreBytes descending (largest first)
	IndexSortDocs                  // DocsCount descending (most docs first)
)

// IndexFilter narrows an IndexInspector.Indices call. Zero value = "all
// non-hidden indices, sorted by name ascending".
//
// Index patterns are forwarded to the cluster as path parameters.
// Health/Status/Hidden are enforced client-side so behaviour is identical
// across dialects — the OS and ES cat APIs treat hidden indices slightly
// differently and we want one story at the CLI surface.
type IndexFilter struct {
	Index  string    // comma-separated index patterns; "" = "*"
	Health string    // "green"|"yellow"|"red"; "" = any; case-insensitive
	Status string    // "open"|"close"; "" = any; case-insensitive
	Hidden bool      // include hidden (dot-prefixed / system) indices
	Sort   IndexSort // ordering applied after filtering
}

// IndexStateAction names a single index lifecycle transition. The
// values are the cluster's wire path segments ("_open", "_close") with
// the underscore stripped — the rendered/logged form an operator types
// at the CLI.
type IndexStateAction string

const (
	IndexStateOpen  IndexStateAction = "open"
	IndexStateClose IndexStateAction = "close"
)

// IndexStateChangeRequest is the input to an IndexStateChanger call.
// Index is the comma-separated pattern the cluster receives (the same
// shape /_cat/indices accepts); the business layer resolves it client-
// side before the call to enforce the blast-radius limit and to render
// a useful dry-run.
//
// IgnoreUnavailable maps to ?ignore_unavailable=true — useful when a
// pattern legitimately matches nothing and the operator wants a no-op
// rather than a 404. WaitForActiveShards is forwarded as-is to the
// cluster (numeric like "1" or symbolic like "all"); empty leaves the
// cluster default in place.
type IndexStateChangeRequest struct {
	Index               string
	IgnoreUnavailable   bool
	WaitForActiveShards string
}

// IndexStateChange is the normalised view of a /_open or /_close
// response. Acknowledged is the cluster's own ack; ShardsAcknowledged
// is populated by both ES and OS for these calls.
//
// Action is the verb the request submitted ("open" or "close") so a
// captured JSON record is interpretable on its own. DryRun mirrors the
// request's --dry-run flag — the cluster has no native dry-run for
// these endpoints, so dry-run is implemented client-side as
// "resolve + report"; a reader of the JSON shouldn't have to guess
// which mode produced the record.
//
// Indices is the resolved list of index names the request would
// transition (currently in the *opposite* state). Skipped is the list
// the cluster would consider a no-op (already in the target state) —
// surfaced so an operator running `index close --index logs-*` against
// a mixed open/closed pattern sees what was actually changed.
type IndexStateChange struct {
	Action             IndexStateAction `json:"action"                yaml:"action"`
	DryRun             bool             `json:"dry_run"               yaml:"dry_run"`
	Acknowledged       bool             `json:"acknowledged"          yaml:"acknowledged"`
	ShardsAcknowledged bool             `json:"shards_acknowledged"   yaml:"shards_acknowledged"`
	Indices            []string         `json:"indices"               yaml:"indices"`
	Skipped            []string         `json:"skipped,omitempty"     yaml:"skipped,omitempty"`
}

// AllocationExplainRequest narrows an AllocationExplainer.Explain call.
// Zero value (Index == "") asks the cluster to pick any unassigned shard
// it finds — this mirrors how operators usually invoke the endpoint when
// hunting the first yellow/red index.
//
// When Index is set, Shard and Primary fully identify the shard to
// explain (including shard 0 of a replica, which is the common case).
// IncludeDiskInfo surfaces the cluster-level disk watermarks and
// per-node free space in the response; IncludeYesDecisions makes the
// cluster return every decider it considered — useful when debugging
// "why did it pick node X over Y" for an assigned shard, but noisy
// otherwise.
type AllocationExplainRequest struct {
	Index               string
	Shard               int
	Primary             bool
	IncludeDiskInfo     bool
	IncludeYesDecisions bool
}

// AllocationExplanation is the normalised view of
// /_cluster/allocation/explain on both dialects. The endpoint returns a
// single record (the cluster explains one shard per call); this type is
// therefore a struct, not a slice.
//
// Empty/absent fields are omitted (omitempty) so the JSON/YAML output
// stays compact for the common case — an assigned shard carries no
// UnassignedInfo; an unassigned shard carries no CurrentNode. The
// cluster is the source of truth: when it doesn't report a field, we
// emit nothing rather than a synthetic default.
type AllocationExplanation struct {
	Index                  string                   `json:"index"                               yaml:"index"`
	Shard                  int                      `json:"shard"                               yaml:"shard"`
	Primary                bool                     `json:"primary"                             yaml:"primary"`
	CurrentState           string                   `json:"current_state"                       yaml:"current_state"`
	CurrentNode            *AllocationNode          `json:"current_node,omitempty"              yaml:"current_node,omitempty"`
	UnassignedInfo         *AllocationUnassigned    `json:"unassigned_info,omitempty"           yaml:"unassigned_info,omitempty"`
	CanAllocate            string                   `json:"can_allocate,omitempty"              yaml:"can_allocate,omitempty"`
	AllocateExplanation    string                   `json:"allocate_explanation,omitempty"      yaml:"allocate_explanation,omitempty"`
	CanRemainOnCurrentNode string                   `json:"can_remain_on_current_node,omitempty" yaml:"can_remain_on_current_node,omitempty"`
	CanRebalanceCluster    string                   `json:"can_rebalance_cluster,omitempty"     yaml:"can_rebalance_cluster,omitempty"`
	MoveExplanation        string                   `json:"move_explanation,omitempty"          yaml:"move_explanation,omitempty"`
	RebalanceExplanation   string                   `json:"rebalance_explanation,omitempty"     yaml:"rebalance_explanation,omitempty"`
	Note                   string                   `json:"note,omitempty"                      yaml:"note,omitempty"`
	NodeDecisions          []AllocationNodeDecision `json:"node_decisions,omitempty"            yaml:"node_decisions,omitempty"`
}

// AllocationNode is the current-node record embedded in an explanation
// for assigned shards.
type AllocationNode struct {
	ID               string `json:"id"                         yaml:"id"`
	Name             string `json:"name"                       yaml:"name"`
	TransportAddress string `json:"transport_address,omitempty" yaml:"transport_address,omitempty"`
	WeightRanking    int    `json:"weight_ranking,omitempty"   yaml:"weight_ranking,omitempty"`
}

// AllocationUnassigned mirrors the cluster's unassigned_info block. ES
// populates Details; OS only reports LastAllocationStatus. Both dialects
// always set Reason when the shard is unassigned.
type AllocationUnassigned struct {
	Reason               string `json:"reason"                           yaml:"reason"`
	At                   string `json:"at,omitempty"                     yaml:"at,omitempty"`
	Details              string `json:"details,omitempty"                yaml:"details,omitempty"`
	LastAllocationStatus string `json:"last_allocation_status,omitempty" yaml:"last_allocation_status,omitempty"`
}

// AllocationNodeDecision is one node's decision in node_allocation_decisions.
// The deciders list is the per-rule breakdown the cluster evaluated for
// this shard on this node ("same_shard", "disk_threshold", ...).
type AllocationNodeDecision struct {
	NodeID           string              `json:"node_id"                    yaml:"node_id"`
	NodeName         string              `json:"node_name"                  yaml:"node_name"`
	TransportAddress string              `json:"transport_address,omitempty" yaml:"transport_address,omitempty"`
	NodeDecision     string              `json:"node_decision"              yaml:"node_decision"`
	WeightRanking    int                 `json:"weight_ranking,omitempty"   yaml:"weight_ranking,omitempty"`
	Deciders         []AllocationDecider `json:"deciders,omitempty"         yaml:"deciders,omitempty"`
}

// AllocationDecider is one rule evaluation in a node decision's
// deciders list. Explanation is free-form prose from the cluster.
type AllocationDecider struct {
	Decider     string `json:"decider"               yaml:"decider"`
	Decision    string `json:"decision"              yaml:"decision"`
	Explanation string `json:"explanation,omitempty" yaml:"explanation,omitempty"`
}

// SecurityReport is what the cluster tells us about its identity model
// — users, roles, role-mappings, and dialect-specific extras (ES API
// keys, OS tenants) — plus a per-cluster status block. This is the
// adapter's half of an `ops audit`; the TLS posture is added by the
// business layer because it comes from the config.Context, not from
// the cluster.
//
// When Status.Enabled is false (security disabled on ES, or plugin
// absent on OS), every collection is empty: the cluster has nothing
// to report, not "we didn't look".
//
// Per-collection partial failures: if the auth user has rights to read
// users but not API keys, the API-keys block is left empty and the
// reason lands in Status.Notes rather than aborting the whole audit.
// "I'm not allowed to see API keys" is signal an operator wants
// surfaced, not the failure mode.
type SecurityReport struct {
	Status       SecurityStatus        `json:"status"                  yaml:"status"`
	Users        []SecurityUser        `json:"users,omitempty"         yaml:"users,omitempty"`
	Roles        []SecurityRole        `json:"roles,omitempty"         yaml:"roles,omitempty"`
	RoleMappings []SecurityRoleMapping `json:"role_mappings,omitempty" yaml:"role_mappings,omitempty"`
	APIKeys      []SecurityAPIKey      `json:"api_keys,omitempty"      yaml:"api_keys,omitempty"`
	Tenants      []SecurityTenant      `json:"tenants,omitempty"       yaml:"tenants,omitempty"`
}

// SecurityStatus captures whether the cluster exposes a security model
// at all. Dialect is always set; Enabled is best-effort based on probing
// the dialect-specific endpoint. Note carries a short explanation when
// Enabled is false (e.g. "security disabled on this cluster") — surfaced
// in the table so operators aren't left wondering.
//
// Notes is a list of per-collection messages for partial failures: a
// 403 on /_security/api_key (missing manage_api_key) lands here as
// "api_keys: forbidden" rather than aborting the whole audit. Empty
// on a clean run.
type SecurityStatus struct {
	Dialect string   `json:"dialect"          yaml:"dialect"`
	Enabled bool     `json:"enabled"          yaml:"enabled"`
	Note    string   `json:"note,omitempty"   yaml:"note,omitempty"`
	Notes   []string `json:"notes,omitempty"  yaml:"notes,omitempty"`
}

// SecurityUser is the normalised view of a user from the native realm
// (Elasticsearch) or the internal-users database (OpenSearch).
//
// Roles are the product-native role names granted directly to the user.
// On OpenSearch, users also carry BackendRoles — external identities
// (LDAP groups, OIDC claims) that role mappings expand into product
// roles. ES reports only Roles; the OS-only fields stay empty.
//
// Reserved flags a built-in entity — an operator auditing for "who did
// somebody create here?" uses this column to scan past the defaults
// quickly.
type SecurityUser struct {
	Username     string   `json:"username"                 yaml:"username"`
	Enabled      bool     `json:"enabled"                  yaml:"enabled"`
	Reserved     bool     `json:"reserved"                 yaml:"reserved"`
	Roles        []string `json:"roles,omitempty"          yaml:"roles,omitempty"`
	BackendRoles []string `json:"backend_roles,omitempty"  yaml:"backend_roles,omitempty"`
	FullName     string   `json:"full_name,omitempty"      yaml:"full_name,omitempty"`
	Email        string   `json:"email,omitempty"          yaml:"email,omitempty"`
}

// SecurityRole is the normalised view of a role definition. We surface
// the cluster-level privilege list and the union of index patterns the
// role grants any access to — enough to scan for "does anything have
// `all` on `*`?" without drowning in per-action detail. The JSON/YAML
// output keeps the fields stable; deeper introspection (per-action
// permissions, field-level security, document-level security) is
// deliberately left for a future `--deep` flag.
type SecurityRole struct {
	Name              string   `json:"name"                              yaml:"name"`
	Reserved          bool     `json:"reserved"                          yaml:"reserved"`
	ClusterPrivileges []string `json:"cluster_privileges,omitempty"      yaml:"cluster_privileges,omitempty"`
	IndexPatterns     []string `json:"index_patterns,omitempty"          yaml:"index_patterns,omitempty"`
	Description       string   `json:"description,omitempty"             yaml:"description,omitempty"`
}

// SecurityRoleMapping links external identities — backend roles (LDAP
// groups, OIDC claims), explicit usernames, or hostnames — to product
// roles. ES exposes this under /_security/role_mapping; OS under
// /_plugins/_security/api/rolesmapping. The shape is normalised across
// both: a list of mappings, each carrying the produce-roles it grants
// and the external identifiers that satisfy it.
//
// On OS, role mappings are how backend roles become security roles —
// so a user-side audit ("who can act as admin?") needs role mappings
// to be complete, not just users + roles.
type SecurityRoleMapping struct {
	Name         string   `json:"name"                          yaml:"name"`
	Reserved     bool     `json:"reserved,omitempty"            yaml:"reserved,omitempty"`
	Enabled      bool     `json:"enabled"                       yaml:"enabled"`
	Roles        []string `json:"roles,omitempty"               yaml:"roles,omitempty"`
	BackendRoles []string `json:"backend_roles,omitempty"       yaml:"backend_roles,omitempty"`
	Users        []string `json:"users,omitempty"               yaml:"users,omitempty"`
	Hosts        []string `json:"hosts,omitempty"               yaml:"hosts,omitempty"`
}

// SecurityAPIKey is the ES-only view of an active API key from
// /_security/api_key. Empty on OpenSearch (the OS security plugin has
// no equivalent surface). Expiration/Creation are RFC3339 strings when
// the cluster reports them; empty when absent (no expiry set, or older
// ES versions that omit the field).
//
// We do not surface the actual key material — the GET endpoint
// doesn't return it (only the metadata) and even if it did, putting it
// through `ops audit` would be the wrong place.
type SecurityAPIKey struct {
	ID          string `json:"id"                     yaml:"id"`
	Name        string `json:"name"                   yaml:"name"`
	Username    string `json:"username,omitempty"     yaml:"username,omitempty"`
	Realm       string `json:"realm,omitempty"        yaml:"realm,omitempty"`
	Invalidated bool   `json:"invalidated,omitempty"  yaml:"invalidated,omitempty"`
	Creation    string `json:"creation,omitempty"     yaml:"creation,omitempty"`
	Expiration  string `json:"expiration,omitempty"   yaml:"expiration,omitempty"`
}

// SecurityTenant is the OpenSearch-only multi-tenancy concept exposed
// under /_plugins/_security/api/tenants. Empty on Elasticsearch (no
// equivalent — Spaces in Kibana sit at the application layer, not the
// security plugin). Tenants gate which Dashboards saved-objects each
// user can see; an audit that omitted them would miss a real lever.
type SecurityTenant struct {
	Name        string `json:"name"                   yaml:"name"`
	Reserved    bool   `json:"reserved,omitempty"     yaml:"reserved,omitempty"`
	Description string `json:"description,omitempty"  yaml:"description,omitempty"`
}

// SecurityAudit is the full `ops audit` output: the cluster-side report
// plus the TLS posture we can report from the context config without
// asking the cluster anything. Keeping these together lets scripts read
// one JSON blob rather than stitching two calls.
type SecurityAudit struct {
	TLS          TLSPosture            `json:"tls"                       yaml:"tls"`
	Status       SecurityStatus        `json:"status"                    yaml:"status"`
	Users        []SecurityUser        `json:"users,omitempty"           yaml:"users,omitempty"`
	Roles        []SecurityRole        `json:"roles,omitempty"           yaml:"roles,omitempty"`
	RoleMappings []SecurityRoleMapping `json:"role_mappings,omitempty"   yaml:"role_mappings,omitempty"`
	APIKeys      []SecurityAPIKey      `json:"api_keys,omitempty"        yaml:"api_keys,omitempty"`
	Tenants      []SecurityTenant      `json:"tenants,omitempty"         yaml:"tenants,omitempty"`
}

// TLSPosture is what the context's connection looks like from the
// client's side. Not from the cluster — the cluster can't tell us
// whether we set `insecure: true` locally. Scheme is derived from the
// first configured URL; InsecureSkipVerify and CACertSet surface the
// two config knobs that weaken or strengthen trust; AuthType and
// ClientCertSet surface which credential the tool is presenting.
type TLSPosture struct {
	Scheme             string `json:"scheme"                       yaml:"scheme"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"         yaml:"insecure_skip_verify"`
	CACertSet          bool   `json:"cacert_set"                   yaml:"cacert_set"`
	AuthType           string `json:"auth_type"                    yaml:"auth_type"`
	ClientCertSet      bool   `json:"client_cert_set"              yaml:"client_cert_set"`
}

// HotThreadsRequest narrows a HotThreadsInspector.HotThreads call. A
// zero value is "all nodes, cluster defaults" — matching how the raw
// endpoint behaves with no query params. Pointer-typed knobs are nil
// when unset so the adapter can tell "user didn't pass --threads" apart
// from "user passed --threads 0".
//
// Type is the sampling dimension ("cpu", "wait", "block", "mem"). ES
// supports all four; OpenSearch's typed client only wires CPU. When the
// user requests a non-cpu Type on OS the adapter returns ErrUnsupported
// so the limitation surfaces loudly instead of silently falling back to
// CPU sampling.
type HotThreadsRequest struct {
	Nodes             []string
	Threads           *int
	Interval          time.Duration
	Snapshots         *int
	Type              string
	IgnoreIdleThreads *bool
}

// HotThreads is the sampled-thread view from /_nodes/hot_threads. The
// response is a free-form text blob — boundaries between nodes are
// marked by "::: {node_name}..." headers that ship with the cluster, not
// a stable schema. Keeping Text opaque avoids baking version-specific
// parsing into esops.
type HotThreads struct {
	Text string `json:"text" yaml:"text"`
}

// RerouteAction names a single _cluster/reroute command kind. The
// values are the JSON keys the cluster expects ("move", "cancel",
// "allocate_replica", …) so the adapter doesn't need a translation
// table — it can build the body straight from the typed value.
type RerouteAction string

const (
	RerouteMove                 RerouteAction = "move"
	RerouteCancel               RerouteAction = "cancel"
	RerouteAllocateReplica      RerouteAction = "allocate_replica"
	RerouteAllocateEmptyPrimary RerouteAction = "allocate_empty_primary"
	RerouteAllocateStalePrimary RerouteAction = "allocate_stale_primary"
	RerouteRetryFailed          RerouteAction = "retry_failed"
)

// RerouteCommand is one shard movement instruction submitted to
// /_cluster/reroute. Not every field is meaningful for every Action —
// the business layer validates the combinations before the adapter
// builds the wire body, so the adapter can serialise a known-good
// command without re-checking shape.
//
// The RerouteRetryFailed action carries no command body of its own;
// the cluster expresses it via the retry_failed=true query param. It
// stays in this enum so the CLI surface is "pick exactly one action"
// rather than "pick an action OR set --retry-failed".
type RerouteCommand struct {
	Action         RerouteAction `json:"action"                      yaml:"action"`
	Index          string        `json:"index,omitempty"             yaml:"index,omitempty"`
	Shard          int           `json:"shard,omitempty"             yaml:"shard,omitempty"`
	FromNode       string        `json:"from_node,omitempty"         yaml:"from_node,omitempty"`
	ToNode         string        `json:"to_node,omitempty"           yaml:"to_node,omitempty"`
	AllowPrimary   bool          `json:"allow_primary,omitempty"     yaml:"allow_primary,omitempty"`
	AcceptDataLoss bool          `json:"accept_data_loss,omitempty"  yaml:"accept_data_loss,omitempty"`
}

// RerouteRequest is the input to a RerouteExecutor call. DryRun and
// Explain are query-string flags on the cluster side; RetryFailed is
// the standalone "retry shards that hit max-retries" knob — when true,
// Commands is typically empty (the action is the request itself).
//
// The cluster supports multiple commands in one request and applies
// them as a single cluster-state update; esops keeps the slice shape
// so a future "drain a node" helper can submit a batch atomically,
// but `ops rebalance` only sends one command per invocation today —
// safer in shell history, simpler to undo.
type RerouteRequest struct {
	Commands    []RerouteCommand
	DryRun      bool
	Explain     bool
	RetryFailed bool
}

// RerouteResult is the normalised view of a /_cluster/reroute response.
// Acknowledged is the cluster's own ack flag (false means the master
// rejected the request); DryRun mirrors the request's DryRun so a
// reader of the JSON output sees both intent and outcome on one line.
//
// Explanations is populated only when the request set Explain=true.
// One entry per submitted command, with the per-decider breakdown
// the cluster evaluated for that command on each candidate node.
type RerouteResult struct {
	Acknowledged bool                  `json:"acknowledged"           yaml:"acknowledged"`
	DryRun       bool                  `json:"dry_run"                yaml:"dry_run"`
	Commands     []RerouteCommand      `json:"commands"               yaml:"commands"`
	Explanations []RerouteExplanation  `json:"explanations,omitempty" yaml:"explanations,omitempty"`
}

// RerouteExplanation is one explanation block from /_cluster/reroute
// when explain=true. Command is the verb echoed back by the cluster
// ("move", "allocate_replica", …); Decisions enumerates the deciders
// the cluster ran and how each ruled.
type RerouteExplanation struct {
	Command   string            `json:"command"             yaml:"command"`
	Decisions []RerouteDecision `json:"decisions,omitempty" yaml:"decisions,omitempty"`
}

// RerouteDecision is one decider's ruling on a reroute command. Mirrors
// AllocationDecider in shape — same data, different endpoint.
type RerouteDecision struct {
	Decider     string `json:"decider"               yaml:"decider"`
	Decision    string `json:"decision"              yaml:"decision"`
	Explanation string `json:"explanation,omitempty" yaml:"explanation,omitempty"`
}

// DiagRequest carries the knobs `ops diag` exposes: the opt-in
// collectors that make bundles noticeably bigger (mappings), produce
// noise by default (idle threads), policy-sensitive (slow-log
// settings), and the concurrency cap for the collectors themselves.
type DiagRequest struct {
	IncludeSlowlog     bool
	IncludeMappings    bool
	IncludeIdleThreads bool

	// Concurrency caps how many collectors run in flight. <= 0 uses
	// the package default (4). Operators on overloaded clusters set
	// this to 1; on healthy clusters the default 4× speedup is the
	// difference between a bundle in 8s and one in 30s.
	Concurrency int
}

// DiagArtifact is one file in the bundle: a single cluster endpoint's
// response, captured verbatim from the cluster. Bundles are human-shared
// at support-ticket time — keeping the cluster's raw bytes rather than
// esops's normalised view means Elastic/OpenSearch support can parse
// them with their own tools without second-guessing what we reshaped.
//
// Status carries the HTTP status code the cluster returned. A 404 on a
// dialect-specific endpoint (ILM on OS, ISM on ES) is expected data, not
// an error — the manifest records the status so a reviewer knows the
// probe happened and came back empty. Error is populated only when the
// collector never reached the cluster (transport error before we had a
// response); in that case the tarball has no entry for this artifact
// and Bytes is 0.
//
// SHA256 is the hex digest of the body bytes written to the tarball,
// populated whenever a body was actually captured. Empty for transport
// failures (no body), and absent from the JSON shape via omitempty so
// older bundle readers don't choke on the extra field. Operators verify
// a bundle by re-hashing the extracted file and comparing — see the
// `ops diag` --help for the recipe.
//
// The artifact body is not held on the struct: Diag streams each
// response body straight into the tarball as collectors run, so big
// captures (mappings on a 10k-index cluster) don't sit in RAM next to
// every other artifact's body.
type DiagArtifact struct {
	Name   string `json:"name"              yaml:"name"`
	Path   string `json:"path"              yaml:"path"`
	File   string `json:"file"              yaml:"file"`
	Status int    `json:"status"            yaml:"status"`
	Bytes  int    `json:"bytes"             yaml:"bytes"`
	SHA256 string `json:"sha256,omitempty"  yaml:"sha256,omitempty"`
	Error  string `json:"error,omitempty"   yaml:"error,omitempty"`
}

// DiagManifest is the top-level index written to manifest.json in every
// bundle. It summarises what was collected, against which cluster, and
// by which tool version — everything a reviewer needs to orient inside
// the tarball without having to guess at filenames.
//
// Deliberately excluded: the cluster URL, credentials, auth config. The
// manifest is the single place in the bundle where those fields could
// leak; "don't write them" is the one-line redaction rule.
//
// SHA256 carries the hex SHA-256 of the manifest.json bytes that were
// written to the tarball. It is NOT serialised into the manifest itself
// (json:"-") because the hash describes the rendered bytes — embedding
// it inside them would be a chicken-and-egg. The Diag function returns
// the populated value so the CLI can surface it on stderr; an operator
// recording "manifest sha256: a3f5…" in their incident ticket gets a
// tamper check that survives independently of the bundle file.
type DiagManifest struct {
	Tool      string             `json:"tool"        yaml:"tool"`
	Version   string             `json:"version"     yaml:"version"`
	Commit    string             `json:"commit,omitempty" yaml:"commit,omitempty"`
	CreatedAt string             `json:"created_at"  yaml:"created_at"`
	Cluster   DiagClusterSummary `json:"cluster"     yaml:"cluster"`
	Artifacts []DiagArtifact     `json:"artifacts"   yaml:"artifacts"`
	SHA256    string             `json:"-"           yaml:"-"`
}

// DiagClusterSummary is the cluster-side identity captured in the manifest:
// the product dialect, its reported cluster name, and the version. No URL.
type DiagClusterSummary struct {
	Name    string `json:"name,omitempty"  yaml:"name,omitempty"`
	Dialect string `json:"dialect"         yaml:"dialect"`
	Version string `json:"version"         yaml:"version"`
}

// DiagVerifyStatus is the per-artifact outcome of `ops diag verify`.
type DiagVerifyStatus string

const (
	// DiagVerifyOK: the artifact's recomputed SHA-256 matches the
	// manifest entry. The body the bundle carries is the body the
	// collector wrote.
	DiagVerifyOK DiagVerifyStatus = "ok"

	// DiagVerifyMismatch: the artifact is in the tarball, the manifest
	// recorded a hash, but the recomputed hash differs. Tampering or
	// corruption between bundle creation and now.
	DiagVerifyMismatch DiagVerifyStatus = "mismatch"

	// DiagVerifyMissing: the manifest names the artifact but the
	// tarball has no matching entry. Either an incomplete bundle or a
	// removed artifact.
	DiagVerifyMissing DiagVerifyStatus = "missing"

	// DiagVerifyNoHash: the manifest entry has no recorded SHA-256
	// (either an older bundle from before sha256 was recorded, or a
	// transport-failure artifact with no body to hash). Informational —
	// not a failure, but also not a verification.
	DiagVerifyNoHash DiagVerifyStatus = "no_hash"
)

// DiagVerifyArtifact is one row in the verify report.
type DiagVerifyArtifact struct {
	Name     string           `json:"name"               yaml:"name"`
	File     string           `json:"file"               yaml:"file"`
	Expected string           `json:"expected,omitempty" yaml:"expected,omitempty"`
	Actual   string           `json:"actual,omitempty"   yaml:"actual,omitempty"`
	Status   DiagVerifyStatus `json:"status"             yaml:"status"`
}

// DiagVerifySummary collapses the per-artifact rows into the count
// fields a script wants for "did this verify?". Pass implies
// Mismatched + Missing + Unknown == 0; NoHash entries don't fail the
// run on their own (they're informational), but they're surfaced via
// Total - OK so a reader can tell "X of Y were checkable".
type DiagVerifySummary struct {
	Total      int `json:"total"      yaml:"total"`
	OK         int `json:"ok"         yaml:"ok"`
	Mismatched int `json:"mismatched" yaml:"mismatched"`
	Missing    int `json:"missing"    yaml:"missing"`
	NoHash     int `json:"no_hash"    yaml:"no_hash"`
	Unknown    int `json:"unknown"    yaml:"unknown"`
}

// DiagVerifyResult is the structured output of `ops diag verify`.
//
// ManifestSHA256 is the recomputed hash of the manifest.json bytes
// inside the tarball — matches what `ops diag` printed to stderr when
// the bundle was created. Pass is true iff every artifact verified and
// no extra entries were found in the tar that the manifest didn't
// list. Cluster echoes the manifest's recorded cluster identity so
// scripts don't have to re-parse the manifest themselves.
type DiagVerifyResult struct {
	ManifestSHA256  string               `json:"manifest_sha256"            yaml:"manifest_sha256"`
	Cluster         DiagClusterSummary   `json:"cluster"                    yaml:"cluster"`
	Tool            string               `json:"tool,omitempty"             yaml:"tool,omitempty"`
	Version         string               `json:"version,omitempty"          yaml:"version,omitempty"`
	CreatedAt       string               `json:"created_at,omitempty"       yaml:"created_at,omitempty"`
	Artifacts       []DiagVerifyArtifact `json:"artifacts"                  yaml:"artifacts"`
	UnknownEntries  []string             `json:"unknown_entries,omitempty"  yaml:"unknown_entries,omitempty"`
	Summary         DiagVerifySummary    `json:"summary"                    yaml:"summary"`
	Pass            bool                 `json:"pass"                       yaml:"pass"`
}

// Node is the neutral view of one cluster node from /_cat/nodes. Roles
// is the cluster's reported role list ("master", "data", "data_hot",
// "ingest", "ml", ...) — preserved verbatim because tier-aware roles
// (data_hot/data_warm/data_cold) carry information the analysis layer
// may want to surface separately from a single boolean.
//
// IsDataNode is the derived "does this node hold primary/replica
// shards" flag: true when any role equals "data" or starts with
// "data_". Computed on the adapter side so callers don't have to
// re-implement the role-string heuristic in every consumer.
type Node struct {
	Name       string   `json:"name"                  yaml:"name"`
	Roles      []string `json:"roles,omitempty"       yaml:"roles,omitempty"`
	IsDataNode bool     `json:"is_data_node"          yaml:"is_data_node"`
}

// ShardAnalysisRequest is the input to ops.AnalyzeShards. Threshold
// fields are bytes; a zero value on any threshold means "use the
// package default", computed in AnalyzeShards so the active values land
// in the response's Thresholds block whether the operator overrode
// them or not.
//
// Sections gates which sections the response carries. Empty = all four
// (nodes, indices, top, unassigned). Values outside the known set are
// rejected at the CLI layer, not silently ignored.
//
// IncludeClosed is intentionally absent in v1: neither the ES nor OS
// typed cat-shards client exposes expand_wildcards, so closed-index
// analysis needs a raw-HTTP shim — deferred until an operator asks for it.
type ShardAnalysisRequest struct {
	TopN               int
	MaxShardBytes      int64
	CriticalShardBytes int64
	MinShardBytes      int64
	SkewPercent        int
	IncludeHidden      bool
	Sections           []string
}

// ShardAnalysisThresholds echoes the active threshold values into the
// response so a saved JSON capture is interpretable on its own — the
// reader doesn't have to know which CLI flags were passed.
type ShardAnalysisThresholds struct {
	OversizedBytes  int64 `json:"oversized_bytes"   yaml:"oversized_bytes"`
	CriticalBytes   int64 `json:"critical_bytes"    yaml:"critical_bytes"`
	UndersizedBytes int64 `json:"undersized_bytes"  yaml:"undersized_bytes"`
	SkewPercent     int   `json:"skew_percent"      yaml:"skew_percent"`
}

// ShardNodeStat is one row in the per-node distribution section.
// ShardsDeviationPercent and StoreDeviationPercent are signed deltas
// from the median across data nodes (rounded to whole percent). Status
// is "ok", "SKEW", or "n/a" — "n/a" appears only when fewer than three
// data nodes make the median uninformative.
type ShardNodeStat struct {
	Node                   string `json:"node"                       yaml:"node"`
	Shards                 int    `json:"shards"                     yaml:"shards"`
	StoreBytes             int64  `json:"store_bytes"                yaml:"store_bytes"`
	ShardsDeviationPercent int    `json:"shards_deviation_percent"   yaml:"shards_deviation_percent"`
	StoreDeviationPercent  int    `json:"store_deviation_percent"    yaml:"store_deviation_percent"`
	Status                 string `json:"status"                     yaml:"status"`
}

// ShardHotEntry is one row in the top-N largest-shards section. Status
// is "ok", "OVERSIZED", or "CRITICAL" depending on which threshold the
// shard's StoreBytes crosses.
type ShardHotEntry struct {
	Index      string `json:"index"        yaml:"index"`
	Shard      int    `json:"shard"        yaml:"shard"`
	Primary    bool   `json:"primary"      yaml:"primary"`
	Node       string `json:"node"         yaml:"node"`
	StoreBytes int64  `json:"store_bytes"  yaml:"store_bytes"`
	Status     string `json:"status"       yaml:"status"`
}

// ShardIndexStat is one row in the indices-with-issues section. Only
// non-ok rows are emitted — the section exists to focus attention, not
// to repeat the full index list. AvgPrimaryBytes is the mean across
// primaries (replicas mirror primaries in size and add no information).
// Note carries a short qualifier ("system index", "closed") when one
// applies.
type ShardIndexStat struct {
	Index           string `json:"index"                       yaml:"index"`
	Primaries       int    `json:"primaries"                   yaml:"primaries"`
	AvgPrimaryBytes int64  `json:"avg_primary_bytes"           yaml:"avg_primary_bytes"`
	MaxPrimaryBytes int64  `json:"max_primary_bytes"           yaml:"max_primary_bytes"`
	DocsCount       int64  `json:"docs_count"                  yaml:"docs_count"`
	Hidden          bool   `json:"hidden,omitempty"            yaml:"hidden,omitempty"`
	Status          string `json:"status"                      yaml:"status"`
	Note            string `json:"note,omitempty"              yaml:"note,omitempty"`
}

// ShardUnassignedSummary collapses the unassigned section to "how many
// shards across how many indices". The full per-shard view stays
// available via `ops shards --unassigned`; analyze only summarises and
// points at the next command.
type ShardUnassignedSummary struct {
	Total   int      `json:"total"             yaml:"total"`
	Indices []string `json:"indices,omitempty" yaml:"indices,omitempty"`
}

// ShardAnalysisSummary is the headline-counters block. SkewSkippedReason
// is set ("fewer than 3 data nodes") when skew analysis was deliberately
// not performed; empty otherwise. DataNodes counts what the analysis
// considered as data nodes (from /_cat/nodes), so a reader can verify
// the denominator.
type ShardAnalysisSummary struct {
	OversizedShards   int    `json:"oversized_shards"             yaml:"oversized_shards"`
	CriticalShards    int    `json:"critical_shards"              yaml:"critical_shards"`
	UndersizedIndices int    `json:"undersized_indices"           yaml:"undersized_indices"`
	SkewedNodes       int    `json:"skewed_nodes"                 yaml:"skewed_nodes"`
	UnassignedShards  int    `json:"unassigned_shards"            yaml:"unassigned_shards"`
	DataNodes         int    `json:"data_nodes"                   yaml:"data_nodes"`
	SkewSkippedReason string `json:"skew_skipped_reason,omitempty" yaml:"skew_skipped_reason,omitempty"`
}

// ShardAnalysis is the top-level response from ops.AnalyzeShards. Every
// section is always present in the JSON shape (omitempty would let
// scripts mistake "no skewed nodes" for "the section was disabled");
// when the request's Sections list excludes one, its slice is left
// nil, which still serialises stably.
type ShardAnalysis struct {
	Thresholds    ShardAnalysisThresholds `json:"thresholds"      yaml:"thresholds"`
	Nodes         []ShardNodeStat         `json:"nodes"           yaml:"nodes"`
	TopShards     []ShardHotEntry         `json:"top_shards"      yaml:"top_shards"`
	IndicesAtRisk []ShardIndexStat        `json:"indices_at_risk" yaml:"indices_at_risk"`
	Unassigned    ShardUnassignedSummary  `json:"unassigned"      yaml:"unassigned"`
	Summary       ShardAnalysisSummary    `json:"summary"         yaml:"summary"`
}
