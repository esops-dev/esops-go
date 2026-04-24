// Package types holds neutral domain types shared across esops. Nothing
// in here may depend on the upstream ES or OS client packages — these
// types are the boundary that keeps the rest of the codebase dialect-agnostic.
package types

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
// — users and roles — plus a per-cluster status block. This is the
// adapter's half of an `ops audit`; the TLS posture is added by the
// business layer because it comes from the config.Context, not from
// the cluster.
//
// When Status.Enabled is false (security disabled on ES, or plugin
// absent on OS), Users and Roles are empty: the cluster has nothing
// to report, not "we didn't look".
type SecurityReport struct {
	Status SecurityStatus `json:"status" yaml:"status"`
	Users  []SecurityUser `json:"users,omitempty" yaml:"users,omitempty"`
	Roles  []SecurityRole `json:"roles,omitempty" yaml:"roles,omitempty"`
}

// SecurityStatus captures whether the cluster exposes a security model
// at all. Dialect is always set; Enabled is best-effort based on probing
// the dialect-specific endpoint. Note carries a short explanation when
// Enabled is false (e.g. "security disabled on this cluster") — surfaced
// in the table so operators aren't left wondering.
type SecurityStatus struct {
	Dialect string `json:"dialect"         yaml:"dialect"`
	Enabled bool   `json:"enabled"         yaml:"enabled"`
	Note    string `json:"note,omitempty"  yaml:"note,omitempty"`
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

// SecurityAudit is the full `ops audit` output: the cluster-side report
// plus the TLS posture we can report from the context config without
// asking the cluster anything. Keeping these together lets scripts read
// one JSON blob rather than stitching two calls.
type SecurityAudit struct {
	TLS      TLSPosture     `json:"tls"      yaml:"tls"`
	Status   SecurityStatus `json:"status"   yaml:"status"`
	Users    []SecurityUser `json:"users,omitempty" yaml:"users,omitempty"`
	Roles    []SecurityRole `json:"roles,omitempty" yaml:"roles,omitempty"`
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
