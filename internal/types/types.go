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
