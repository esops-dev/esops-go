// Package types holds neutral domain types shared across esops. Nothing
// in here may depend on the upstream ES or OS client packages — these
// types are the boundary that keeps the rest of the codebase dialect-agnostic.
package types

import (
	"encoding/json"
	"time"
)

// Dialect identifies which product answered a cluster probe.
type Dialect string

const (
	DialectElasticsearch Dialect = "elasticsearch"
	DialectOpenSearch    Dialect = "opensearch"
)

// ClusterInfo is the outcome of the GET / probe.
type ClusterInfo struct {
	Dialect      Dialect
	ClusterName  string
	Version      string
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

// IndexNameStatus is the slim (name, status) pair returned by
// IndexInspector.IndicesNamesStatus — used by the state-change and
// optimize pre-flights, which only need the open/closed split. Drops
// the long tail of /_cat/indices columns (uuid, doc counts, byte
// sizes) so a 100k-index cluster transfers and parses ~one column
// instead of twelve.
type IndexNameStatus struct {
	Name   string `json:"name"   yaml:"name"`
	Status string `json:"status" yaml:"status"`
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
	Index          string    // comma-separated index patterns; "" = "*"
	Health         string    // "green"|"yellow"|"red"; "" = any; case-insensitive
	Status         string    // "open"|"close"; "" = any; case-insensitive
	Hidden         bool      // include hidden (dot-prefixed / system) indices
	ExcludeManaged bool      // hide data-stream backing indices (`.ds-*`)
	Sort           IndexSort // ordering applied after filtering
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
// Indices is the resolved literal name list (already partitioned by
// the business layer's pre-flight); the adapter joins them for the URL
// path. Carrying []string end-to-end avoids the join → split → re-join
// round trip that used to happen between layers.
//
// IgnoreUnavailable maps to ?ignore_unavailable=true — useful when a
// pattern legitimately matches nothing and the operator wants a no-op
// rather than a 404. WaitForActiveShards is forwarded as-is to the
// cluster (numeric like "1" or symbolic like "all"); empty leaves the
// cluster default in place.
type IndexStateChangeRequest struct {
	Indices             []string
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
	// OnError is populated when the operator passed --on-error
	// {skip,rollback}; nil otherwise (the field is omitted from JSON /
	// YAML output under the default abort policy). Each item maps to
	// the index the command attempted to transition.
	OnError *OnErrorSummary `json:"on_error,omitempty"    yaml:"on_error,omitempty"`
}

// IndexOptimizeRequest is the input to an IndexOptimizer.ForceMerge call.
// Index is the comma-separated pattern the cluster receives (the same
// shape /_cat/indices accepts); the business layer resolves it client-
// side before the call to enforce the blast-radius limit and render a
// useful dry-run.
//
// MaxNumSegments forwards to ?max_num_segments=N. The cluster default
// is dynamic ("merge until it doesn't help any more"); the typical
// operator value for a sealed-archive merge is 1. A zero value here
// means "let the cluster decide".
//
// OnlyExpungeDeletes maps to ?only_expunge_deletes=true — the cheap
// variant that only rewrites segments holding tombstoned docs. Useful
// after a delete-by-query sweep on an otherwise-stable index.
//
// Flush mirrors ?flush=true|false. The cluster default is true; we
// keep that behaviour with a *bool so the operator can suppress it.
//
// IgnoreUnavailable maps to ?ignore_unavailable=true — useful when a
// pattern legitimately matches nothing and the operator wants a no-op
// rather than a 404.
type IndexOptimizeRequest struct {
	Indices            []string
	MaxNumSegments     int
	OnlyExpungeDeletes bool
	Flush              *bool
	IgnoreUnavailable  bool
}

// IndexOptimizeResult is the normalised view of a /_forcemerge
// response. Both ES and OS return a {_shards:{total,successful,failed}}
// envelope; on synchronous calls, failed > 0 means at least one shard
// hit a merge error which the cluster carries in Failures.
//
// Action is fixed to "optimize" so a captured JSON record is
// interpretable alongside an IndexStateChange capture. DryRun mirrors
// the request's --dry-run flag — the cluster has no native dry-run for
// _forcemerge, so dry-run is implemented client-side as
// "resolve + report".
//
// Indices is the resolved list of index names the request targeted
// (the same names sent to the cluster). Per-shard failure descriptors
// are kept opaque (Failures []string) — the cluster's own reasons are
// the right thing to surface in a ticket and rephrasing them would
// lose signal.
type IndexOptimizeResult struct {
	Action         string   `json:"action"                  yaml:"action"`
	DryRun         bool     `json:"dry_run"                 yaml:"dry_run"`
	Indices        []string `json:"indices"                 yaml:"indices"`
	MaxNumSegments int      `json:"max_num_segments,omitempty" yaml:"max_num_segments,omitempty"`
	ShardsTotal    int      `json:"shards_total"            yaml:"shards_total"`
	ShardsSuccess  int      `json:"shards_successful"       yaml:"shards_successful"`
	ShardsFailed   int      `json:"shards_failed"           yaml:"shards_failed"`
	Failures       []string `json:"failures,omitempty"      yaml:"failures,omitempty"`
	// OnError is populated when the operator passed --on-error skip and
	// the business layer dispatched per-index. Rollback is not
	// supported for force-merge (irreversible); the CLI refuses it
	// with a usage error before dispatch.
	OnError *OnErrorSummary `json:"on_error,omitempty"   yaml:"on_error,omitempty"`
}

// IndexRolloverConditions are the rollover triggers the cluster
// evaluates against the alias's current write index. All fields are
// zero-valued by default; a zero value means "no condition on this
// dimension". When every condition is zero the cluster always rolls
// over (the unconditional form). The business layer enforces "either
// at least one condition or --force" so an absent-mindedly-typed
// `index rollover --alias logs` doesn't roll a freshly-created index.
//
// MaxAge is forwarded as a duration string ("7d", "1h"). MaxDocs is a
// document count; MaxSize/MaxPrimarySize are strings forwarded
// verbatim (e.g. "50gb") — the cluster parses them. Strings rather
// than typed bytes because both ES and OS accept the same human-size
// notation, and parsing it client-side would risk drift.
type IndexRolloverConditions struct {
	MaxAge         string `json:"max_age,omitempty"          yaml:"max_age,omitempty"`
	MaxDocs        int64  `json:"max_docs,omitempty"         yaml:"max_docs,omitempty"`
	MaxSize        string `json:"max_size,omitempty"         yaml:"max_size,omitempty"`
	MaxPrimarySize string `json:"max_primary_size,omitempty" yaml:"max_primary_size,omitempty"`
}

// IsEmpty reports whether the conditions block carries no triggers —
// i.e. an unconditional rollover. The CLI uses this to require an
// explicit --force opt-in for that case.
func (c IndexRolloverConditions) IsEmpty() bool {
	return c.MaxAge == "" && c.MaxDocs == 0 && c.MaxSize == "" && c.MaxPrimarySize == ""
}

// IndexRolloverRequest is the input to an IndexRollover call. Alias is
// the rollover target — the cluster looks up the current write index
// behind the alias and either rolls it over or refuses with a reason
// when no conditions match. NewIndex is optional: the cluster derives
// a sensible name from the existing index when it ends in `-NNNNNN`,
// so this is only set when the operator wants to override.
//
// DryRun maps to the cluster's native ?dry_run=true — unlike open/close
// the cluster evaluates conditions and reports rolled_over without
// actually creating the new index. WaitForActiveShards forwards as-is.
type IndexRolloverRequest struct {
	Alias               string
	NewIndex            string
	Conditions          IndexRolloverConditions
	DryRun              bool
	WaitForActiveShards string
}

// IndexRolloverResult is the normalised view of an /<alias>/_rollover
// response. RolledOver is the cluster's own verdict — false means the
// conditions did not match (and on a non-dry-run, no index was
// created). DryRun mirrors the request flag.
//
// ConditionsMet maps each condition the operator submitted to the
// cluster's per-condition outcome (e.g. "max_age": true). Empty for
// the unconditional form. The keys come straight from the cluster so
// captured JSON stays interpretable alongside Elastic/OpenSearch docs.
type IndexRolloverResult struct {
	Alias              string          `json:"alias"               yaml:"alias"`
	OldIndex           string          `json:"old_index,omitempty" yaml:"old_index,omitempty"`
	NewIndex           string          `json:"new_index,omitempty" yaml:"new_index,omitempty"`
	RolledOver         bool            `json:"rolled_over"         yaml:"rolled_over"`
	DryRun             bool            `json:"dry_run"             yaml:"dry_run"`
	Acknowledged       bool            `json:"acknowledged"        yaml:"acknowledged"`
	ShardsAcknowledged bool            `json:"shards_acknowledged" yaml:"shards_acknowledged"`
	ConditionsMet      map[string]bool `json:"conditions,omitempty" yaml:"conditions,omitempty"`
}

// IndexShrinkRequest is the input to an IndexShrinker.Shrink call. The
// cluster's shrink endpoint is /<source>/_shrink/<target> with a JSON
// body of {settings, aliases}. Shrink halves (or quarters, etc.) a
// source index's primary shard count by hard-linking segments into a
// new target index — much cheaper than a reindex when the operator
// just needs to reduce primaries.
//
// The cluster requires the source to be read-only and have all
// primaries co-located on a single node before shrink will succeed.
// esops does not flip those settings automatically — they are policy
// decisions an operator should make before invoking shrink. The
// command surfaces the requirement in --help and we let the cluster
// produce the canonical error if the source isn't ready.
//
// TargetShards is the requested primary count on the new index. Must
// be a factor of the source's primary count (4→2, 4→1, 8→4 …) — the
// cluster validates this. When 0, the cluster default applies (a
// single primary). TargetReplicas is the replica count on the target;
// 0 is a valid value distinct from "leave the cluster default" because
// the typical post-shrink follow-up is to flip replicas back up.
//
// CopySettings mirrors ?copy_settings=true — when set, the source
// index's settings (other than allocation requirements that wouldn't
// make sense on the target) are copied across. A *bool so the operator
// can set it explicitly to false.
//
// WaitForActiveShards forwards as ?wait_for_active_shards=<value>;
// numeric like "1" or symbolic like "all". Empty leaves the cluster
// default in place.
type IndexShrinkRequest struct {
	Source              string
	Target              string
	TargetShards        int
	TargetReplicas      *int
	CopySettings        *bool
	WaitForActiveShards string
}

// IndexShrinkResult is the normalised view of a /<source>/_shrink/<target>
// response. Both ES and OS return acknowledged + shards_acknowledged +
// the new index name. DryRun mirrors the request flag (the cluster has
// no native dry-run for shrink, so dry-run is implemented client-side
// as "validate + report").
//
// Source/Target echo the request so a captured JSON record stays
// interpretable on its own.
type IndexShrinkResult struct {
	Action             string `json:"action"                yaml:"action"`
	DryRun             bool   `json:"dry_run"               yaml:"dry_run"`
	Source             string `json:"source"                yaml:"source"`
	Target             string `json:"target"                yaml:"target"`
	Acknowledged       bool   `json:"acknowledged"          yaml:"acknowledged"`
	ShardsAcknowledged bool   `json:"shards_acknowledged"   yaml:"shards_acknowledged"`
	TargetShards       int    `json:"target_shards,omitempty" yaml:"target_shards,omitempty"`
}

// IndexSettings is one index's settings block from
// GET /<indices>/_settings. The cluster splits its response into two
// blocks: `settings` (values the operator configured on this index)
// and `defaults` (built-in values returned only when
// ?include_defaults=true is set). They are kept separate here so a
// consumer can answer "is this knob actually configured, or am I
// looking at the default?" without having to re-query.
//
// Settings is always populated; Defaults is nil unless
// IncludeDefaults was set on the request. Both maps follow the
// cluster's hierarchical shape (`index` → `number_of_shards`, …);
// for table output the renderer flattens them to dotted keys.
type IndexSettings struct {
	Index    string         `json:"index"              yaml:"index"`
	Settings map[string]any `json:"settings"           yaml:"settings"`
	Defaults map[string]any `json:"defaults,omitempty" yaml:"defaults,omitempty"`
}

// IndexSettingsGetRequest narrows GET /<indices>/_settings/<names>.
// Index is the comma-separated pattern the cluster receives. Names is
// an optional list of setting key prefixes the cluster filters on
// (e.g. "index.number_of_replicas"); empty returns the full settings
// block.
//
// IncludeDefaults asks the cluster to add the built-in default values
// the index inherits but doesn't override; useful for "is this knob
// actually set, or am I looking at the default?". FlatSettings asks
// the cluster to return dotted keys instead of nested objects — esops
// flattens client-side regardless when rendering a table, but the flag
// is forwarded so JSON output mirrors the cluster's chosen shape when
// requested.
type IndexSettingsGetRequest struct {
	Indices           []string
	Names             []string
	IncludeDefaults   bool
	FlatSettings      bool
	IgnoreUnavailable bool
}

// IndexSettingsUpdateRequest is the input to a settings update via
// PUT /<indices>/_settings. Index is the comma-separated pattern the
// cluster receives; Body is the raw JSON the operator handed in (or
// the business layer constructed from --set key=value pairs). The body
// shape the cluster expects is {"index":{"<key>":<value>, ...}}.
//
// PreserveExisting maps to ?preserve_existing=true — when true, the
// cluster keeps the current value for any key where Body would have
// overwritten it. IgnoreUnavailable maps to ?ignore_unavailable=true.
//
// DryRun mirrors the global --dry-run flag — the cluster has no native
// dry-run for /_settings, so dry-run is implemented client-side as
// "validate + report" and Body is not sent to the cluster.
type IndexSettingsUpdateRequest struct {
	Indices           []string
	Body              []byte
	PreserveExisting  bool
	IgnoreUnavailable bool
}

// IndexSettingsUpdateResult is the normalised view of a
// PUT /<indices>/_settings response. The cluster only returns an
// acknowledged flag; we surface the operator's resolved Index list so
// a captured JSON record stays interpretable on its own.
type IndexSettingsUpdateResult struct {
	Action       string   `json:"action"        yaml:"action"`
	DryRun       bool     `json:"dry_run"       yaml:"dry_run"`
	Indices      []string `json:"indices"       yaml:"indices"`
	Acknowledged bool     `json:"acknowledged"  yaml:"acknowledged"`
	Body         string   `json:"body,omitempty" yaml:"body,omitempty"`
	// OnError is populated when the operator passed --on-error skip and
	// the business layer dispatched per-index. Rollback would require
	// capturing prior values per index; the CLI refuses --on-error
	// rollback for this command pending that capability.
	OnError *OnErrorSummary `json:"on_error,omitempty" yaml:"on_error,omitempty"`
}

// IndexTemplate is the neutral view of one index template from
// GET /_index_template/<name>. The shape mirrors what both ES and OS
// expose under the v2 (composable) template API — legacy
// /_template entries are out of scope; both products consider them
// deprecated, and surfacing one set of templates keeps the CLI story
// straight.
//
// Template carries the inner {settings, mappings, aliases} block as a
// decoded map so callers can render it as nested JSON/YAML without
// re-parsing. ComposedOf names the component templates this template
// merges in. DataStream is set only when the template targets a data
// stream.
type IndexTemplate struct {
	Name          string         `json:"name"                     yaml:"name"`
	IndexPatterns []string       `json:"index_patterns,omitempty" yaml:"index_patterns,omitempty"`
	Priority      int            `json:"priority,omitempty"       yaml:"priority,omitempty"`
	Version       int            `json:"version,omitempty"        yaml:"version,omitempty"`
	ComposedOf    []string       `json:"composed_of,omitempty"    yaml:"composed_of,omitempty"`
	Template      map[string]any `json:"template,omitempty"       yaml:"template,omitempty"`
	DataStream    map[string]any `json:"data_stream,omitempty"    yaml:"data_stream,omitempty"`
}

// IndexTemplatePutRequest is the input to a PUT /_index_template/<name>
// call. Name is the literal template name (no patterns — templates are
// updated one at a time). Body is the raw JSON template definition the
// operator handed in. Create maps to ?create=true, which makes the
// cluster refuse the call when a template with the same name already
// exists — the safe-by-default flow for first-time installs in shell
// pipelines.
type IndexTemplatePutRequest struct {
	Name   string
	Body   []byte
	Create bool
}

// IndexTemplatePutResult is the normalised view of a
// PUT /_index_template/<name> response. The cluster only returns an
// acknowledged flag; Name is echoed so a captured JSON record is
// self-describing.
type IndexTemplatePutResult struct {
	Action       string `json:"action"             yaml:"action"`
	DryRun       bool   `json:"dry_run"            yaml:"dry_run"`
	Name         string `json:"name"               yaml:"name"`
	Acknowledged bool   `json:"acknowledged"       yaml:"acknowledged"`
}

// IndexTemplateDeleteRequest is the input to a
// DELETE /_index_template/<name> call. Name is the literal template
// name; patterns are not supported — like PUT, deletes are per-template
// to keep the destructive surface narrow.
type IndexTemplateDeleteRequest struct {
	Name string
}

// IndexTemplateDeleteResult is the normalised view of a
// DELETE /_index_template/<name> response. Mirrors the put-result
// shape for symmetry.
type IndexTemplateDeleteResult struct {
	Action       string `json:"action"             yaml:"action"`
	DryRun       bool   `json:"dry_run"            yaml:"dry_run"`
	Name         string `json:"name"               yaml:"name"`
	Acknowledged bool   `json:"acknowledged"       yaml:"acknowledged"`
}

// Alias is the neutral view of one alias→index binding from
// /_cat/aliases on both dialects. The shape mirrors the cat output
// flat-row form: one alias name + one index name per record. An alias
// fronting N indices materialises as N records (one per index), which
// is what scripts grepping the JSON expect.
//
// Filter carries the raw JSON filter string the alias was created with
// (or "-" / "" when no filter is set). Routing fields are the same
// shape — the cluster reports them as bare strings and we preserve the
// rendering. IsWriteIndex is the boolean view of the cluster's
// "is_write_index" column, which the cat APIs return as "true"/"false"
// (or "-" when unset on a regular alias).
type Alias struct {
	Alias         string `json:"alias"                       yaml:"alias"`
	Index         string `json:"index"                       yaml:"index"`
	Filter        string `json:"filter,omitempty"            yaml:"filter,omitempty"`
	RoutingIndex  string `json:"routing_index,omitempty"     yaml:"routing_index,omitempty"`
	RoutingSearch string `json:"routing_search,omitempty"    yaml:"routing_search,omitempty"`
	IsWriteIndex  bool   `json:"is_write_index,omitempty"    yaml:"is_write_index,omitempty"`
}

// AliasFilter narrows an AliasInspector.Aliases call. Both fields are
// passed to the cluster as path parameters; the cluster honours its
// own glob semantics. Zero value = "all aliases".
type AliasFilter struct {
	Alias string // comma-separated alias names or patterns; "" = all
	Index string // comma-separated index names; "" = all
}

// AliasAction names a single /_aliases body action. The values are the
// JSON keys the cluster expects ("add" / "remove") so the adapter
// doesn't need a translation table — it can build the body straight
// from the typed value.
type AliasAction string

const (
	AliasActionAdd    AliasAction = "add"
	AliasActionRemove AliasAction = "remove"
)

// AliasUpdateAction is one entry in the /_aliases bulk-actions body.
// Index is the concrete index the alias points at — patterns are
// supported by the cluster but esops requires a literal name on the
// add/remove path, so a typo doesn't bind an alias to a hundred
// indices in one call. Use the cluster's own raw API for that case.
//
// Filter is a raw JSON object string ('{"term":{"tenant":"x"}}'); we
// pass it verbatim and let the cluster validate. Routing/IsWriteIndex
// mirror the cluster's optional knobs on add — IsWriteIndex is a
// pointer so the operator can set it to false explicitly (rare, but
// "demote this alias from being the write index" is a real flow).
//
// On a remove action, only Alias and Index are meaningful; the other
// fields are ignored.
type AliasUpdateAction struct {
	Action       AliasAction `json:"action"                  yaml:"action"`
	Alias        string      `json:"alias"                   yaml:"alias"`
	Index        string      `json:"index"                   yaml:"index"`
	Filter       string      `json:"filter,omitempty"        yaml:"filter,omitempty"`
	Routing      string      `json:"routing,omitempty"       yaml:"routing,omitempty"`
	IsWriteIndex *bool       `json:"is_write_index,omitempty" yaml:"is_write_index,omitempty"`
}

// AliasUpdateRequest is the input to an AliasUpdater call. Actions are
// applied as a single cluster-state update — atomic from the cluster's
// perspective. The business layer constructs the slice for one action
// per command invocation today (`index aliases add` / `... remove`),
// but the wire shape supports a future bulk-action `index aliases
// apply --from FILE` without an API change.
type AliasUpdateRequest struct {
	Actions []AliasUpdateAction
}

// AliasUpdateResult is the normalised view of a POST /_aliases
// response. Both dialects return a single acknowledged flag; the body
// otherwise echoes nothing useful, so we surface the actions list from
// the request side (rounded-tripped through the result) for a
// captured-JSON reader.
//
// DryRun mirrors the request's --dry-run flag — the cluster has no
// native dry-run for /_aliases, so dry-run is implemented client-side
// as "validate + report".
type AliasUpdateResult struct {
	Action       string              `json:"action"                yaml:"action"`
	DryRun       bool                `json:"dry_run"               yaml:"dry_run"`
	Acknowledged bool                `json:"acknowledged"          yaml:"acknowledged"`
	Actions      []AliasUpdateAction `json:"actions"               yaml:"actions"`
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
// — users, roles, role-mappings, anonymous-access posture, and
// dialect-specific extras (ES API keys, OS tenants) — plus a per-
// cluster status block. This is the adapter's half of an `ops audit`;
// the TLS posture is added by the business layer because it comes
// from the config.Context, not from the cluster.
//
// When Status.Enabled is false (security disabled on ES, or plugin
// absent on OS), every collection is empty and Anonymous is nil: the
// cluster has nothing to report, and the adapter skipped the
// anonymous probe rather than fabricate a posture.
//
// Per-collection partial failures: if the auth user has rights to read
// users but not API keys, the API-keys block is left empty and the
// reason lands in Status.Notes rather than aborting the whole audit.
// Anonymous follows the same pattern — a forbidden /_cluster/settings
// read leaves Anonymous nil and records a note. "I'm not allowed to
// see API keys" is signal an operator wants surfaced, not the failure
// mode.
type SecurityReport struct {
	Status       SecurityStatus          `json:"status"                  yaml:"status"`
	Anonymous    *AnonymousAccessPosture `json:"anonymous,omitempty"     yaml:"anonymous,omitempty"`
	Users        []SecurityUser          `json:"users,omitempty"         yaml:"users,omitempty"`
	Roles        []SecurityRole          `json:"roles,omitempty"         yaml:"roles,omitempty"`
	RoleMappings []SecurityRoleMapping   `json:"role_mappings,omitempty" yaml:"role_mappings,omitempty"`
	APIKeys      []SecurityAPIKey        `json:"api_keys,omitempty"      yaml:"api_keys,omitempty"`
	Tenants      []SecurityTenant        `json:"tenants,omitempty"       yaml:"tenants,omitempty"`
	// Realms is the configured authentication realm chain — the
	// dialect-neutral view of "how does this cluster identify a
	// request?". Populated by the audit pipeline calling RealmsInspector
	// alongside the user/role reads. A 403 on the realms read (common
	// on OS without admin certs) lands as a Status.Notes entry rather
	// than an error and Realms stays empty — same partial-failure
	// pattern API keys and role mappings follow.
	Realms []Realm `json:"realms,omitempty" yaml:"realms,omitempty"`
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
	// PasswordChangedAt carries the cluster-reported timestamp of the
	// user's last password change, when the cluster exposes it. Empty
	// when the cluster does not surface the field — neither ES's
	// /_security/user nor the OpenSearch security plugin's
	// /_plugins/_security/api/internalusers expose this on every
	// supported version, so a missing value is "the cluster did not
	// tell us", not "never changed". Doctor's default-credentials rule
	// uses a non-empty value as evidence that the password is no
	// longer the shipped default.
	PasswordChangedAt string `json:"password_changed_at,omitempty"  yaml:"password_changed_at,omitempty"`
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

// AnonymousAccessPosture describes how the cluster treats unauthenticated
// requests. A non-nil value on a SecurityReport / SecurityAudit means
// the adapter probed the cluster's anonymous-access settings; a nil
// value means it did not (security disabled, endpoint unavailable, or
// the caller lacks permission — the reason lands in
// SecurityStatus.Notes). Distinguishing "not collected" from
// "collected and disabled" matters to downstream consumers (notably
// esops-doctor's anonymous-access rule) which would otherwise treat
// silence as a green light.
//
// Source carries the cluster-settings scope that supplied the effective
// value: "transient" overrides "persistent" overrides "default", which
// is the precedence order both ES and OS evaluate. When every scope is
// silent (default off, never overridden) the field is "default".
//
// Dialect-specific fields (Username, AuthzException) are ES-only; OS
// has no equivalent at the cluster-settings layer. Roles is shared in
// shape but populated differently — see the field comment.
type AnonymousAccessPosture struct {
	// Enabled is the effective answer: true when the cluster will let
	// an unauthenticated request through. ES infers this from the
	// presence of `xpack.security.authc.anonymous.roles` (no roles =
	// no implicit identity); OS reads
	// `plugins.security.authcz.anonymous_auth_enabled` directly.
	Enabled bool `json:"enabled"                          yaml:"enabled"`

	// Username is the identity ES grants to unauthenticated requests
	// when anonymous access is enabled (default `_anonymous`). Empty
	// on OS — the security plugin has no equivalent setting.
	Username string `json:"username,omitempty"           yaml:"username,omitempty"`

	// Roles are the roles the anonymous identity inherits.
	//   - ES: product roles from `xpack.security.authc.anonymous.roles`.
	//   - OS: backend roles from the security plugin's anonymous
	//     config when accessible; empty when only the cluster-settings
	//     bit was readable (the richer plugin endpoint requires admin
	//     and may not have been probed).
	Roles []string `json:"roles,omitempty"             yaml:"roles,omitempty"`

	// AuthzException mirrors `xpack.security.authc.anonymous.authz_exception`
	// (ES-only, default true): when true, an anonymous request that
	// fails authorization gets a 403; when false, the cluster falls
	// through and challenges with a 401 instead. Audit-relevant
	// because the false setting changes how clients perceive the
	// cluster's auth posture.
	//
	// Pointer-typed so a nil value means "not applicable on this
	// dialect" (OS has no equivalent setting), distinct from a
	// non-nil *false ("operator explicitly turned it off"). Without
	// the pointer, OS would render an always-false bool that looks
	// like an operator decision.
	AuthzException *bool `json:"authz_exception,omitempty"    yaml:"authz_exception,omitempty"`

	// Source names the cluster-settings scope that supplied the
	// effective value: "transient", "persistent", or "default".
	// Operators reading "anonymous is on" want to know whether to
	// chase a transient override (gone after a master restart) or a
	// persisted one.
	Source string `json:"source,omitempty"               yaml:"source,omitempty"`
}

// SecurityAudit is the full `ops audit` output: the cluster-side report
// plus the TLS posture we can report from the context config without
// asking the cluster anything. Keeping these together lets scripts read
// one JSON blob rather than stitching two calls.
type SecurityAudit struct {
	TLS          TLSPosture              `json:"tls"                       yaml:"tls"`
	Status       SecurityStatus          `json:"status"                    yaml:"status"`
	Anonymous    *AnonymousAccessPosture `json:"anonymous,omitempty"       yaml:"anonymous,omitempty"`
	Users        []SecurityUser          `json:"users,omitempty"           yaml:"users,omitempty"`
	Roles        []SecurityRole          `json:"roles,omitempty"           yaml:"roles,omitempty"`
	RoleMappings []SecurityRoleMapping   `json:"role_mappings,omitempty"   yaml:"role_mappings,omitempty"`
	APIKeys      []SecurityAPIKey        `json:"api_keys,omitempty"        yaml:"api_keys,omitempty"`
	Tenants      []SecurityTenant        `json:"tenants,omitempty"         yaml:"tenants,omitempty"`
	Realms       []Realm                 `json:"realms,omitempty"          yaml:"realms,omitempty"`
}

// TLSPosture is what the context's connection looks like from the
// client's side. Scheme is derived from the first configured URL;
// InsecureSkipVerify and CACertSet surface the two config knobs that
// weaken or strengthen trust; AuthType and ClientCertSet surface which
// credential the tool is presenting.
//
// TransportTLSEnabled / TransportTLSVerified are server-side fields —
// distinct from the client-side fields above. They mirror what the
// cluster reports about its own node-to-node transport TLS config and
// are populated only by callers that consulted TransportTLSInspector
// (esops-doctor's node_to_node_encryption rule). Left unset (omitempty)
// when the caller didn't fan out to the cluster — the rest of the
// posture is meaningful without them.
type TLSPosture struct {
	Scheme             string `json:"scheme"                       yaml:"scheme"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"         yaml:"insecure_skip_verify"`
	CACertSet          bool   `json:"cacert_set"                   yaml:"cacert_set"`
	AuthType           string `json:"auth_type"                    yaml:"auth_type"`
	ClientCertSet      bool   `json:"client_cert_set"              yaml:"client_cert_set"`

	TransportTLSEnabled  bool `json:"transport_tls_enabled,omitempty"  yaml:"transport_tls_enabled,omitempty"`
	TransportTLSVerified bool `json:"transport_tls_verified,omitempty" yaml:"transport_tls_verified,omitempty"`
}

// TransportTLS is the cluster-side view of node-to-node transport TLS
// posture, returned by TransportTLSInspector. Distinct from TLSPosture
// because the latter mixes client-side connection facts (Scheme,
// InsecureSkipVerify, …) that only the operator's config knows; this
// type carries only what the cluster reports about itself, so an
// inspector that reads /_nodes/settings has somewhere clean to put it.
//
// Enabled is true when every reachable node reports transport TLS as
// enabled — homogeneity matters here, since one misconfigured node is
// the exact case the rule has to flag, so we do not fudge a majority
// vote into a green outcome. Verified mirrors the same all-or-nothing
// aggregation over the verification-mode setting (ES) /
// enforce_hostname_verification (OS).
type TransportTLS struct {
	Enabled  bool `json:"transport_tls_enabled"            yaml:"transport_tls_enabled"`
	Verified bool `json:"transport_tls_verified,omitempty" yaml:"transport_tls_verified,omitempty"`
}

// HTTPTLSPosture is the cluster-side view of HTTP-layer TLS posture,
// returned by HTTPTLSInspector. Sibling of TransportTLS — the transport
// layer is node-to-node, the HTTP layer is what an external client
// (esops itself, dashboards, application traffic) connects to, so the
// two deserve separate capabilities. Whether the HTTP layer accepts
// plaintext is a different question from whether nodes encrypt to each
// other; conflating them would mask half the posture.
//
// Enabled / ClientAuth / Protocols / CipherSuites are aggregated across
// the reachable node set:
//   - Enabled is true only when every node reports HTTP TLS as on.
//     One misconfigured node is exactly what the rule has to flag, so
//     a majority vote would defeat the rule's purpose.
//   - ClientAuth is the homogeneous value when every node agrees;
//     "mixed" when nodes disagree. The disagreement itself is the
//     finding — operators reading the posture want to see it.
//   - Protocols and CipherSuites are the union across nodes — the
//     attacker picks the weakest accepted by any node, so the rule
//     evaluates the weakest member, not a hypothetical intersection.
//
// PerNode carries the per-node breakdown so a CEL rule can flag the
// specific outliers when the aggregate says "mixed" or one node is
// off. Empty when every node agrees and the rule only needs the
// aggregate.
type HTTPTLSPosture struct {
	Enabled      bool             `json:"enabled"                   yaml:"enabled"`
	ClientAuth   string           `json:"client_auth,omitempty"     yaml:"client_auth,omitempty"`
	Protocols    []string         `json:"protocols,omitempty"       yaml:"protocols,omitempty"`
	CipherSuites []string         `json:"cipher_suites,omitempty"   yaml:"cipher_suites,omitempty"`
	PerNode      []NodeTLSPosture `json:"per_node,omitempty"        yaml:"per_node,omitempty"`
}

// NodeTLSPosture is a single node's HTTP-layer TLS posture as the
// cluster reports it. Surfaced inside HTTPTLSPosture.PerNode so a CEL
// rule can flag specific outliers when nodes disagree; the field set
// matches the aggregate so rules can compose the two with the same
// idioms.
//
// ClientAuth is the cluster's literal string ("required", "optional",
// "none" on ES; the OS plugin's analogue mapped to the same vocabulary)
// rather than a bool so the three-way distinction survives marshalling.
type NodeTLSPosture struct {
	Name         string   `json:"name"                       yaml:"name"`
	Enabled      bool     `json:"enabled"                    yaml:"enabled"`
	ClientAuth   string   `json:"client_auth,omitempty"      yaml:"client_auth,omitempty"`
	Protocols    []string `json:"protocols,omitempty"        yaml:"protocols,omitempty"`
	CipherSuites []string `json:"cipher_suites,omitempty"    yaml:"cipher_suites,omitempty"`
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
	Acknowledged bool                 `json:"acknowledged"           yaml:"acknowledged"`
	DryRun       bool                 `json:"dry_run"                yaml:"dry_run"`
	Commands     []RerouteCommand     `json:"commands"               yaml:"commands"`
	Explanations []RerouteExplanation `json:"explanations,omitempty" yaml:"explanations,omitempty"`
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
	ManifestSHA256 string               `json:"manifest_sha256"            yaml:"manifest_sha256"`
	Cluster        DiagClusterSummary   `json:"cluster"                    yaml:"cluster"`
	Tool           string               `json:"tool,omitempty"             yaml:"tool,omitempty"`
	Version        string               `json:"version,omitempty"          yaml:"version,omitempty"`
	CreatedAt      string               `json:"created_at,omitempty"       yaml:"created_at,omitempty"`
	Artifacts      []DiagVerifyArtifact `json:"artifacts"                  yaml:"artifacts"`
	UnknownEntries []string             `json:"unknown_entries,omitempty"  yaml:"unknown_entries,omitempty"`
	Summary        DiagVerifySummary    `json:"summary"                    yaml:"summary"`
	Pass           bool                 `json:"pass"                       yaml:"pass"`
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
//
// The remaining fields populate `ops nodes` — version, IP, master flag,
// heap usage, and disk usage. They are best-effort: /_cat/nodes leaves
// some columns out for coordinating-only nodes (no disk, no heap) and
// the byte fields are zero when the cluster did not report a value.
// Numeric fields are bytes; percent fields are 0–100 ints.
type Node struct {
	Name            string   `json:"name"                          yaml:"name"`
	Roles           []string `json:"roles,omitempty"               yaml:"roles,omitempty"`
	IsDataNode      bool     `json:"is_data_node"                  yaml:"is_data_node"`
	Version         string   `json:"version,omitempty"             yaml:"version,omitempty"`
	IP              string   `json:"ip,omitempty"                  yaml:"ip,omitempty"`
	Master          bool     `json:"master,omitempty"              yaml:"master,omitempty"`
	HeapUsedBytes   int64    `json:"heap_used_bytes,omitempty"     yaml:"heap_used_bytes,omitempty"`
	HeapMaxBytes    int64    `json:"heap_max_bytes,omitempty"      yaml:"heap_max_bytes,omitempty"`
	HeapPercent     int      `json:"heap_percent,omitempty"        yaml:"heap_percent,omitempty"`
	DiskUsedBytes   int64    `json:"disk_used_bytes,omitempty"     yaml:"disk_used_bytes,omitempty"`
	DiskAvailBytes  int64    `json:"disk_avail_bytes,omitempty"    yaml:"disk_avail_bytes,omitempty"`
	DiskTotalBytes  int64    `json:"disk_total_bytes,omitempty"    yaml:"disk_total_bytes,omitempty"`
	DiskUsedPercent int      `json:"disk_used_percent,omitempty"   yaml:"disk_used_percent,omitempty"`
	CPUPercent      int      `json:"cpu_percent,omitempty"         yaml:"cpu_percent,omitempty"`
	Load1m          string   `json:"load_1m,omitempty"             yaml:"load_1m,omitempty"`
	Uptime          string   `json:"uptime,omitempty"              yaml:"uptime,omitempty"`
}

// NodeStats is the neutral view of one cluster node's JVM and OS
// introspection — the configured heap (-Xms/-Xmx) and the kernel-reported
// total physical memory of the host. Distinct from Node (which wraps
// /_cat/nodes and reports current heap usage) because the underlying
// endpoints, response shapes, and payload sizes are all larger here:
// callers that only need /_cat/nodes columns should not pay for it.
//
// The capability behind this type fans across /_nodes/jvm (info — for
// heap_init_in_bytes / heap_max_in_bytes; stats does not carry init)
// and /_nodes/stats/os (for total physical memory; the info endpoint's
// os section omits memory totals on ES and OS alike). Numeric fields
// are bytes and zero when the cluster did not report a value.
type NodeStats struct {
	Name string       `json:"name"           yaml:"name"`
	JVM  NodeJVMStats `json:"jvm,omitempty"  yaml:"jvm,omitempty"`
	OS   NodeOSStats  `json:"os,omitempty"   yaml:"os,omitempty"`
}

// NodeJVMStats carries the JVM-level introspection NodeStats exposes.
// Currently just the heap configuration — non-heap, GC, and pool
// breakdowns live on /_nodes/stats and are out of scope for the heap-
// sizing rule that drives this capability.
type NodeJVMStats struct {
	Heap NodeJVMHeap `json:"heap" yaml:"heap"`
}

// NodeJVMHeap mirrors the configured -Xms/-Xmx the JVM was started with.
// InitBytes is jvm.mem.heap_init_in_bytes (the -Xms floor); MaxBytes is
// jvm.mem.heap_max_in_bytes (the -Xmx ceiling). Both come from the
// /_nodes (info) endpoint — /_nodes/stats reports current usage, not
// the configured floor, which is exactly the data the heap_size rule
// needs to check init <= 50% of RAM.
type NodeJVMHeap struct {
	InitBytes int64 `json:"init_bytes,omitempty" yaml:"init_bytes,omitempty"`
	MaxBytes  int64 `json:"max_bytes,omitempty"  yaml:"max_bytes,omitempty"`
}

// NodeOSStats carries the OS-level introspection NodeStats exposes.
// Today that is only the host's total physical memory; CPU, swap, and
// load averages are surfaced through Node (/_cat/nodes) and /_nodes/
// stats/os already, so duplicating them here would just bloat the
// payload for callers that only want the heap-vs-RAM ratio.
type NodeOSStats struct {
	TotalPhysicalMemoryBytes int64 `json:"total_physical_memory_bytes,omitempty" yaml:"total_physical_memory_bytes,omitempty"`
}

// NodeDrainRequest is the input to ops.DrainNode. NodeNames carries the
// nodes the operator wants excluded from shard allocation; each must
// already be present in the cluster (validated client-side via
// NodeInspector before any setting is touched). Multi-node calls write
// one PUT to /_cluster/settings — the cluster honours
// `cluster.routing.allocation.exclude._name` as a single comma-separated
// value, so adding three nodes is one request, not three.
//
// Persistent toggles between /_cluster/settings transient (default —
// clears on master restart, the safer choice for a temporary drain)
// and persistent (survives restart; pair with `ops uncordon` to remove).
//
// AlsoExcludeVoting toggles the voting-config exclusion the cluster
// uses to retire master-eligible nodes safely. POST
// /_cluster/voting_config_exclusions?node_names=… runs after the
// allocation PUT acknowledges, so the voting state never leads the
// allocation state. The cluster accepts non-master nodes as a no-op,
// so the flag is safe to set on a mixed-role drain.
//
// DryRun records the intended transition without firing the PUT/POST.
// Wait blocks until every named node has relocated all of its shards
// (or the context is cancelled); WaitTimeout caps the wait,
// WaitInterval controls how often the shard count is re-polled
// (default 5s).
type NodeDrainRequest struct {
	NodeNames         []string
	Persistent        bool
	DryRun            bool
	Wait              bool
	WaitTimeout       time.Duration
	WaitInterval      time.Duration
	AlsoExcludeVoting bool
}

// NodeDrainResult records the cluster-state transition. Acknowledged
// mirrors the cluster's `acknowledged` field on the PUT
// /_cluster/settings call. Before/After are the
// `cluster.routing.allocation.exclude._name` values before and after
// the change — a captured DryRun listing is the same transition the
// live run would apply.
//
// AlreadyExcluded records the subset of NodeNames already in the
// exclude list before the call (idempotency tracking). When every
// requested node was already excluded, AlreadyExcluded == NodeNames
// and the cluster is not touched.
//
// ShardsRemaining is the total shard count across the named nodes at
// the time Wait returned (zero on a clean drain, or the count that
// was still resident when the timeout fired). PerNodeShardsRemaining
// breaks that count down per node so the operator sees which node is
// the laggard.
//
// VotingExclusionsAdded names the nodes that were appended to the
// voting-config exclusions list — the same as NodeNames when
// AlsoExcludeVoting was set and the POST acknowledged, empty
// otherwise. VotingExclusionAcked is the acknowledgement flag for
// that secondary call so a partial-success can be surfaced
// (allocation PUT acked, voting POST refused).
type NodeDrainResult struct {
	NodeNames              []string       `json:"node_names"                          yaml:"node_names"`
	Acknowledged           bool           `json:"acknowledged"                        yaml:"acknowledged"`
	DryRun                 bool           `json:"dry_run"                             yaml:"dry_run"`
	Persistent             bool           `json:"persistent"                          yaml:"persistent"`
	AlreadyExcluded        []string       `json:"already_excluded"                    yaml:"already_excluded"`
	Before                 []string       `json:"before"                              yaml:"before"`
	After                  []string       `json:"after"                               yaml:"after"`
	Waited                 bool           `json:"waited,omitempty"                    yaml:"waited,omitempty"`
	ShardsRemaining        int            `json:"shards_remaining"                    yaml:"shards_remaining"`
	PerNodeShardsRemaining map[string]int `json:"per_node_shards_remaining,omitempty" yaml:"per_node_shards_remaining,omitempty"`
	VotingExclusionsAdded  []string       `json:"voting_exclusions_added,omitempty"   yaml:"voting_exclusions_added,omitempty"`
	VotingExclusionAcked   bool           `json:"voting_exclusion_acked,omitempty"    yaml:"voting_exclusion_acked,omitempty"`
}

// NodeUncordonRequest is the input to ops.UncordonNode. NodeNames is
// the list of nodes to remove from the allocation-exclude list; the
// nodes do not need to be present in the cluster (a drained node may
// have already been decommissioned, leaving the exclusion behind —
// cleaning that up is exactly what uncordon is for).
//
// ClearVoting toggles a DELETE /_cluster/voting_config_exclusions
// call alongside the allocation PUT. The cluster's API for voting
// exclusions only supports clear-all (no per-node delete), so this is
// a cluster-wide sweep — call it during the standard "bring nodes
// back online" flow when the voting list is known stale.
//
// Either NodeNames or ClearVoting must be set: an empty uncordon
// request is rejected at the validation layer rather than silently
// no-op.
type NodeUncordonRequest struct {
	NodeNames   []string
	Persistent  bool
	DryRun      bool
	ClearVoting bool
}

// NodeUncordonResult mirrors NodeDrainResult for the uncordon path.
// NotInList records the subset of NodeNames that were not actually in
// the exclude list — a no-op uncordon (NotInList == NodeNames) is
// reported, not errored, so a sweep across a fleet doesn't fail on
// the first node that was never drained.
//
// VotingExclusionsCleared / VotingExclusionAcked report the
// outcome of the secondary DELETE call.
type NodeUncordonResult struct {
	NodeNames               []string `json:"node_names"                          yaml:"node_names"`
	Acknowledged            bool     `json:"acknowledged"                        yaml:"acknowledged"`
	DryRun                  bool     `json:"dry_run"                             yaml:"dry_run"`
	Persistent              bool     `json:"persistent"                          yaml:"persistent"`
	NotInList               []string `json:"not_in_list"                         yaml:"not_in_list"`
	Before                  []string `json:"before"                              yaml:"before"`
	After                   []string `json:"after"                               yaml:"after"`
	VotingExclusionsCleared bool     `json:"voting_exclusions_cleared,omitempty" yaml:"voting_exclusions_cleared,omitempty"`
	VotingExclusionAcked    bool     `json:"voting_exclusion_acked,omitempty"    yaml:"voting_exclusion_acked,omitempty"`
}

// NodeDrainListResult is the read-only view returned by `ops drain
// --list`: which nodes are currently excluded, by scope, plus the
// cluster's voting-config exclusion list. Listing is a separate
// result type from NodeDrainResult because the shape (no acked, no
// before/after, just a snapshot) shares almost nothing with the
// mutating result.
type NodeDrainListResult struct {
	PersistentExcluded []string `json:"persistent_excluded" yaml:"persistent_excluded"`
	TransientExcluded  []string `json:"transient_excluded"  yaml:"transient_excluded"`
	VotingExcluded     []string `json:"voting_excluded"     yaml:"voting_excluded"`
}

// ClusterSettingsView is the slim read-side view of /_cluster/settings
// the drain/uncordon path needs: the current
// cluster.routing.allocation.exclude._name from each scope. Persistent
// and Transient are always populated (empty slices when the scope has
// no exclusions); the adapter is responsible for parsing the cluster's
// nested-or-flat envelope into these flat slices.
type ClusterSettingsView struct {
	PersistentExcludeName []string
	TransientExcludeName  []string
}

// ClusterAllocationExcludeUpdate carries the desired
// cluster.routing.allocation.exclude._name value into the adapter.
// Persistent picks the scope. Names is the post-update list; an empty
// slice clears the setting (the adapter sends a JSON null to the
// cluster so the key is removed rather than left as an empty string,
// which the cluster would treat as "exclude no nodes" but keep the key
// in /_cluster/settings forever).
type ClusterAllocationExcludeUpdate struct {
	Persistent bool
	Names      []string
}

// ClusterSettingsUpdateResult is the cluster's `acknowledged` flag after
// a PUT /_cluster/settings call.
type ClusterSettingsUpdateResult struct {
	Acknowledged bool `json:"acknowledged" yaml:"acknowledged"`
}

// VotingExclusionsResult is the outcome of POST/DELETE on
// /_cluster/voting_config_exclusions. Neither endpoint returns a
// JSON body on success (HTTP 200 alone is the cluster's
// acknowledgement), so Acknowledged is set to true on a 2xx and false
// when the cluster refused the call. The capability layer surfaces
// the cluster's response body verbatim when the call fails — typical
// case is a 503 when the master can't reach quorum to commit the
// metadata change.
type VotingExclusionsResult struct {
	Acknowledged bool `json:"acknowledged" yaml:"acknowledged"`
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

// ILMPolicy is the neutral view of one Index Lifecycle Management policy
// from GET /_ilm/policy/<name> on Elasticsearch. ISM (the OpenSearch
// equivalent) keeps a separate type because the policy body shape is
// fundamentally different: ILM hangs a `phases` map off the policy, ISM
// uses a `states` array with explicit transitions. Surfacing one neutral
// "lifecycle policy" type would force a lossy translation in both
// directions and is exactly the kind of lowest-common-denominator trap
// the project's design rules warn against.
//
// Definition carries the inner `policy` block (phases + _meta) verbatim
// as a decoded map so renderers can pretty-print it without re-parsing.
// ModifiedDate / Version / InUseBy are populated when the cluster reports
// them — older ES 7.x omits some of these fields, in which case the
// zero value is what serialises out.
type ILMPolicy struct {
	Name         string         `json:"name"                       yaml:"name"`
	Version      int            `json:"version,omitempty"          yaml:"version,omitempty"`
	ModifiedDate string         `json:"modified_date,omitempty"    yaml:"modified_date,omitempty"`
	InUseBy      ILMInUseBy     `json:"in_use_by,omitempty"        yaml:"in_use_by,omitempty"`
	Definition   map[string]any `json:"definition,omitempty"       yaml:"definition,omitempty"`
}

// ILMInUseBy mirrors the cluster's `in_use_by` block: the indices,
// composable templates, and data-stream lifecycle definitions that
// reference the policy. Empty slices on policies the cluster reports as
// unattached.
type ILMInUseBy struct {
	Indices             []string `json:"indices,omitempty"             yaml:"indices,omitempty"`
	DataStreams         []string `json:"data_streams,omitempty"        yaml:"data_streams,omitempty"`
	ComposableTemplates []string `json:"composable_templates,omitempty" yaml:"composable_templates,omitempty"`
}

// ILMPolicyPutRequest is the input to a PUT /_ilm/policy/<name> call.
// Body is the raw JSON the operator handed in (the cluster expects
// {"policy":{"phases":{…}}}). The cluster validates the inner shape;
// esops only checks that the body is a JSON object.
type ILMPolicyPutRequest struct {
	Name string
	Body []byte
}

// ILMPolicyPutResult is the normalised view of a PUT /_ilm/policy/<name>
// response. The cluster only returns an acknowledged flag; Name is
// echoed so a captured JSON record stays self-describing.
type ILMPolicyPutResult struct {
	Action       string `json:"action"        yaml:"action"`
	DryRun       bool   `json:"dry_run"       yaml:"dry_run"`
	Name         string `json:"name"          yaml:"name"`
	Acknowledged bool   `json:"acknowledged"  yaml:"acknowledged"`
}

// ILMPolicyDeleteRequest is the input to a DELETE /_ilm/policy/<name>
// call. The cluster refuses with a 4xx when the policy is still attached
// to one or more indices; that error surfaces verbatim.
type ILMPolicyDeleteRequest struct {
	Name string
}

// ILMPolicyDeleteResult mirrors ILMPolicyPutResult for the delete path.
type ILMPolicyDeleteResult struct {
	Action       string `json:"action"        yaml:"action"`
	DryRun       bool   `json:"dry_run"       yaml:"dry_run"`
	Name         string `json:"name"          yaml:"name"`
	Acknowledged bool   `json:"acknowledged"  yaml:"acknowledged"`
}

// ILMExplainRequest narrows a GET /<index>/_ilm/explain call. Index is
// the comma-separated pattern the cluster accepts; OnlyErrors maps to
// ?only_errors=true (return only indices in the ERROR step) and
// OnlyManaged to ?only_managed=true (omit indices that have no policy
// attached). Both default off, matching the cluster default.
type ILMExplainRequest struct {
	Index       string
	OnlyErrors  bool
	OnlyManaged bool
}

// ILMExplainEntry is one row in /<index>/_ilm/explain. Managed=false
// means the cluster found the index but no policy is attached — surfaced
// rather than dropped so an operator scanning a pattern sees the gap.
//
// Phase / Action / Step are the operator-facing breadcrumbs of where the
// index is in its lifecycle right now. FailedStep / StepInfo carry the
// cluster's diagnosis when Step is "ERROR"; surfacing them verbatim
// keeps the message specific (e.g. "rollover_failed: alias not found").
type ILMExplainEntry struct {
	Index         string         `json:"index"                       yaml:"index"`
	Managed       bool           `json:"managed"                     yaml:"managed"`
	Policy        string         `json:"policy,omitempty"            yaml:"policy,omitempty"`
	LifecycleDate string         `json:"lifecycle_date,omitempty"    yaml:"lifecycle_date,omitempty"`
	Phase         string         `json:"phase,omitempty"             yaml:"phase,omitempty"`
	Action        string         `json:"action,omitempty"            yaml:"action,omitempty"`
	Step          string         `json:"step,omitempty"              yaml:"step,omitempty"`
	FailedStep    string         `json:"failed_step,omitempty"       yaml:"failed_step,omitempty"`
	StepInfo      map[string]any `json:"step_info,omitempty"         yaml:"step_info,omitempty"`
	Age           string         `json:"age,omitempty"               yaml:"age,omitempty"`
}

// ISMPolicy is the neutral view of one Index State Management policy
// from GET /_plugins/_ism/policies/<id> on OpenSearch. ISM is intentionally
// not unified with ILMPolicy — see ILMPolicy's docstring.
//
// Definition carries the inner `policy` block (states + transitions +
// ism_template) as a decoded map. SeqNo / PrimaryTerm are the OpenSearch
// optimistic-concurrency tokens the cluster returns and accepts back on
// PUTs; we preserve them on the read path so a future "round-trip edit"
// flow can hand them straight back to the cluster, but PutPolicy below
// uses the create-or-overwrite semantics by default.
type ISMPolicy struct {
	ID          string         `json:"id"                       yaml:"id"`
	SeqNo       int64          `json:"seq_no,omitempty"         yaml:"seq_no,omitempty"`
	PrimaryTerm int64          `json:"primary_term,omitempty"   yaml:"primary_term,omitempty"`
	Definition  map[string]any `json:"definition,omitempty"     yaml:"definition,omitempty"`
}

// ISMPolicyPutRequest is the input to a PUT /_plugins/_ism/policies/<id>
// call. Body is the raw JSON the operator handed in (the plugin expects
// {"policy":{"description":"…","default_state":"…","states":[…]}}).
//
// SeqNo/PrimaryTerm map to the cluster's ?if_seq_no=N&if_primary_term=M
// optimistic-concurrency parameters. When both are zero, the request
// posts unconditionally — the create-or-overwrite path. When set, the
// cluster refuses with a 409 if the policy has been concurrently
// modified.
type ISMPolicyPutRequest struct {
	ID          string
	Body        []byte
	SeqNo       int64
	PrimaryTerm int64
}

// ISMPolicyPutResult is the normalised view of a
// PUT /_plugins/_ism/policies/<id> response. The plugin returns the
// stored policy with fresh seq_no / primary_term — surfaced so a script
// chaining edits can pick up the new tokens without a follow-up GET.
type ISMPolicyPutResult struct {
	Action      string `json:"action"                yaml:"action"`
	DryRun      bool   `json:"dry_run"               yaml:"dry_run"`
	ID          string `json:"id"                    yaml:"id"`
	SeqNo       int64  `json:"seq_no,omitempty"      yaml:"seq_no,omitempty"`
	PrimaryTerm int64  `json:"primary_term,omitempty" yaml:"primary_term,omitempty"`
}

// ISMPolicyDeleteRequest is the input to a
// DELETE /_plugins/_ism/policies/<id> call. The plugin refuses when the
// policy is still attached to managed indices; that error surfaces
// verbatim.
type ISMPolicyDeleteRequest struct {
	ID string
}

// ISMPolicyDeleteResult mirrors ISMPolicyPutResult for the delete path.
// The plugin's response is essentially "ok"; we surface ID so a captured
// JSON record stays interpretable.
type ISMPolicyDeleteResult struct {
	Action string `json:"action"  yaml:"action"`
	DryRun bool   `json:"dry_run" yaml:"dry_run"`
	ID     string `json:"id"      yaml:"id"`
	OK     bool   `json:"ok"      yaml:"ok"`
}

// ISMExplainRequest narrows a GET /_plugins/_ism/explain[/<index>] call.
// Empty Index hits the no-index form (every managed index in the
// cluster). ShowPolicy maps to ?show_policy=true — when set, the plugin
// embeds each managed index's full policy body in the response, which
// can be megabytes on a large fleet.
type ISMExplainRequest struct {
	Index      string
	ShowPolicy bool
}

// ISMExplainEntry is one row in /_plugins/_ism/explain. Mirrors the
// shape of ILMExplainEntry where it can — same operator-facing question
// ("what is this index doing right now?") even though the underlying
// state machine differs. Managed=false means the index was found but
// is not under ISM control.
//
// PolicyID is the policy currently attached. State / Action are the
// plugin's view of where the index is in the state machine. StepInfo
// carries any error/diagnostic block the plugin reports — surfaced
// verbatim because the plugin's error messages are the canonical
// signal an operator needs.
type ISMExplainEntry struct {
	Index    string         `json:"index"                yaml:"index"`
	Managed  bool           `json:"managed"              yaml:"managed"`
	PolicyID string         `json:"policy_id,omitempty"  yaml:"policy_id,omitempty"`
	State    string         `json:"state,omitempty"      yaml:"state,omitempty"`
	Action   string         `json:"action,omitempty"     yaml:"action,omitempty"`
	Step     string         `json:"step,omitempty"       yaml:"step,omitempty"`
	StepInfo map[string]any `json:"step_info,omitempty"  yaml:"step_info,omitempty"`
	Enabled  bool           `json:"enabled,omitempty"    yaml:"enabled,omitempty"`
	Policy   map[string]any `json:"policy,omitempty"     yaml:"policy,omitempty"`
}

// SnapshotRepository is the neutral view of a single repository entry
// from GET /_snapshot[/<repos>] on either dialect. Type is the cluster's
// repository plugin name ("fs", "s3", "azure", "gcs", …); Settings holds
// the type-specific configuration the cluster echoed back. Both products
// expose the same envelope shape — but the keys inside Settings diverge
// (e.g. ES "bucket"/"base_path" vs OS "bucket"/"base_path" with
// occasional plugin-specific extras). We surface the cluster's keys
// verbatim so an operator reading the JSON sees what is actually
// configured rather than a normalised view that drops nuance.
type SnapshotRepository struct {
	Name     string         `json:"name"                yaml:"name"`
	Type     string         `json:"type"                yaml:"type"`
	Settings map[string]any `json:"settings,omitempty"  yaml:"settings,omitempty"`
}

// Snapshot is the neutral view of a single snapshot from
// GET /_snapshot/<repo>/<snapshot>. Both ES and OS share the wire shape
// for the fields below; product-specific extras (SLM policy on ES,
// remote_store_index_shallow_copy on OS) are intentionally dropped from
// the neutral view — `--output json/yaml` consumers who want them can
// fall back to the cluster's raw API. Repository is populated by the
// adapter so a flat snapshot list keeps "which repo is this in?"
// answerable without joining client-side.
type Snapshot struct {
	Repository         string   `json:"repository"                yaml:"repository"`
	Name               string   `json:"name"                      yaml:"name"`
	UUID               string   `json:"uuid,omitempty"            yaml:"uuid,omitempty"`
	State              string   `json:"state"                     yaml:"state"`
	Indices            []string `json:"indices,omitempty"         yaml:"indices,omitempty"`
	IncludeGlobalState bool     `json:"include_global_state"      yaml:"include_global_state"`
	StartTimeMillis    int64    `json:"start_time_millis,omitempty"      yaml:"start_time_millis,omitempty"`
	EndTimeMillis      int64    `json:"end_time_millis,omitempty"        yaml:"end_time_millis,omitempty"`
	DurationMillis     int64    `json:"duration_millis,omitempty"        yaml:"duration_millis,omitempty"`
	ShardsTotal        int      `json:"shards_total"              yaml:"shards_total"`
	ShardsSuccessful   int      `json:"shards_successful"         yaml:"shards_successful"`
	ShardsFailed       int      `json:"shards_failed"             yaml:"shards_failed"`
	Version            string   `json:"version,omitempty"         yaml:"version,omitempty"`
	// Failures carries the cluster's per-shard failure objects verbatim
	// so `--output json` consumers can drill in with jq (e.g.
	// `.failures[].reason`) instead of receiving JSON-encoded strings
	// they have to re-parse. Each entry is the raw JSON object the
	// cluster returned in the snapshot's `failures` array.
	Failures []json.RawMessage `json:"failures,omitempty"        yaml:"failures,omitempty"`
}

// SnapshotSort is the sort key applied to SnapshotFilter results. Snapshot
// listing endpoints honour server-side sort on ES but not on OS, so we
// always sort client-side for identical behaviour across dialects.
type SnapshotSort int

const (
	SnapshotSortStartTime SnapshotSort = iota // most recent first (default)
	SnapshotSortName                          // alphabetical by snapshot name
	SnapshotSortDuration                      // longest-running first
)

// SnapshotFilter narrows a SnapshotInspector.Snapshots call. Zero value
// = "every snapshot in every repository, most recent first".
//
// Repository / Snapshot are forwarded to the cluster as path segments;
// the empty string means "all" on each. State is enforced client-side
// (case-insensitive match against the cluster's State field) so behaviour
// is identical across dialects — neither cluster's raw endpoint accepts
// a state filter directly. IgnoreUnavailable maps to
// ?ignore_unavailable=true so a snapshot pattern that legitimately
// matches nothing in a repo is a no-op rather than a 404.
type SnapshotFilter struct {
	Repository        string       // comma-separated repo names; "" = all repos
	Snapshot          string       // comma-separated snapshot patterns; "" = "*"
	State             string       // e.g. "SUCCESS"|"FAILED"|"IN_PROGRESS"; case-insensitive; "" = any
	IgnoreUnavailable bool         // pass-through to the cluster
	Sort              SnapshotSort // ordering applied after retrieval
	// Slim asks the cluster for the lightweight per-snapshot envelope:
	// `?verbose=false` on ES 7.15+ and OS 2.x. The cluster drops indices,
	// per-shard counters, failures, and start/end times — saves an order
	// of magnitude of bandwidth on repositories with thousands of
	// snapshots, where the default table view doesn't render those fields
	// anyway. Default false (cluster default = verbose) so existing
	// callers don't silently lose fields. On older clusters the flag is
	// silently ignored — adapters may downgrade to verbose without
	// erroring.
	Slim bool
}

// SnapshotRepositoryFilter narrows a SnapshotInspector.Repositories call.
// Repository is comma-separated and forwarded as a path segment; empty
// returns every registered repository.
type SnapshotRepositoryFilter struct {
	Repository string
}

// SnapshotCreateRequest is the input to PUT /_snapshot/<repo>/<snapshot>.
// Repository and Snapshot are literal names — patterns are not honoured
// by the cluster on the create path. Indices is a comma-separated pattern
// the cluster receives as the body's `indices` field; empty means "every
// open index" (the cluster default). IncludeGlobalState mirrors the body
// field of the same name; defaults to the cluster's own default when nil.
//
// Partial maps to the body's `partial` field — when true, the cluster
// proceeds even if some shards are unavailable. Off by default because a
// silent partial backup is the worst kind of regret.
//
// IgnoreUnavailable maps to the body's `ignore_unavailable` field;
// useful when Indices includes a pattern that may not match anything.
//
// WaitForCompletion mirrors ?wait_for_completion. true blocks the HTTP
// call until the snapshot finishes (good for `--wait`); false returns
// immediately and the operator polls via `snapshot list`. Default false
// (the cluster's default) — long-running snapshots should not hold an
// HTTP connection open by default.
//
// Metadata is forwarded as the body's `metadata` block, an opaque map of
// labels the operator wants attached to the snapshot for later auditing.
type SnapshotCreateRequest struct {
	Repository         string
	Snapshot           string
	Indices            string
	IncludeGlobalState *bool
	Partial            *bool
	IgnoreUnavailable  *bool
	WaitForCompletion  bool
	Metadata           map[string]any
	// MasterTimeout maps to ?master_timeout on the create endpoint.
	// Empty leaves the cluster default (30s) alone; long-running master
	// state operations on busy clusters routinely need 1m+.
	MasterTimeout time.Duration
}

// SnapshotCreateResult is the normalised view of a snapshot creation. The
// shape mirrors what both ES and OS return on PUT /_snapshot/<repo>/<snap>.
//
// When WaitForCompletion=true, the cluster echoes a full Snapshot record
// inside `snapshot:{...}` — Snapshot is populated and Accepted=true. When
// WaitForCompletion=false (the default), the cluster only returns
// `{"accepted":true}` and Snapshot is the zero value with State="STARTED";
// the operator polls `snapshot list` to learn the final state.
//
// DryRun mirrors the operator's --dry-run flag — the cluster has no
// native dry-run for snapshot creation, so dry-run is implemented client-
// side as "validate + report what would have been sent".
type SnapshotCreateResult struct {
	Action            string   `json:"action"                       yaml:"action"`
	DryRun            bool     `json:"dry_run"                      yaml:"dry_run"`
	Repository        string   `json:"repository"                   yaml:"repository"`
	Name              string   `json:"name"                         yaml:"name"`
	Accepted          bool     `json:"accepted"                     yaml:"accepted"`
	WaitForCompletion bool     `json:"wait_for_completion"          yaml:"wait_for_completion"`
	Snapshot          Snapshot `json:"snapshot,omitempty"           yaml:"snapshot,omitempty"`
}

// SnapshotVerifyRequest is the input to POST /_snapshot/<repo>/_verify
// — the cluster's repository connectivity probe. Repository is a literal
// repo name; verify is single-repo by design (operators typically run
// it just before a manual snapshot). Empty Repository is rejected by the
// business layer up-front so the cluster doesn't see a request that would
// 400 anyway.
type SnapshotVerifyRequest struct {
	Repository string
}

// SnapshotVerifyResult is the normalised view of a verify response. The
// cluster returns the list of nodes that successfully connected to the
// repository; an empty list with no error means "verified, but no nodes
// answered" (rare and worth surfacing). When the repository is
// misconfigured, the cluster returns a 4xx/5xx with a precise reason —
// the adapter surfaces that as a wrapped error rather than encoding it
// in this struct, mirroring how the rest of the codebase handles cluster
// refusals.
//
// VerifiedNodes is a flat list of node identifiers (whatever the cluster
// returned as the map key, typically the node UUID). Names mirrors the
// `name` field nested under each node entry — kept as a parallel slice
// so a JSON record stays small but operators can still read which
// human-readable hostnames responded.
type SnapshotVerifyResult struct {
	Action        string   `json:"action"                  yaml:"action"`
	Repository    string   `json:"repository"              yaml:"repository"`
	VerifiedNodes []string `json:"verified_nodes"          yaml:"verified_nodes"`
	Names         []string `json:"node_names,omitempty"    yaml:"node_names,omitempty"`
}

// SnapshotRestoreRequest is the input to POST /_snapshot/<repo>/<snap>/_restore.
// Repository and Snapshot are literal names — patterns are not honoured by the
// cluster on the restore path. Indices is forwarded as the body's `indices`
// field; empty restores every index in the snapshot. RenamePattern /
// RenameReplacement map to the body's regex rename pair so an operator can
// restore a side-by-side copy without clobbering the live index — the standard
// "restore-into-staging" workflow.
//
// IncludeGlobalState / Partial / IgnoreUnavailable are *bool so the CLI can
// distinguish "leave the cluster default" from "explicitly false".
// IndexSettings overrides settings on the restored index (most commonly
// number_of_replicas to 0 during a quick restore-and-promote). IgnoreIndexSettings
// is the inverse — drop these settings from the source, falling back to the
// cluster's defaults on the restored index.
//
// WaitForCompletion mirrors ?wait_for_completion=true. Default false; long
// restores should not hold an HTTP connection open, and `snapshot list` /
// `_recovery` is the canonical post-hoc view.
type SnapshotRestoreRequest struct {
	Repository          string
	Snapshot            string
	Indices             string
	IncludeGlobalState  *bool
	Partial             *bool
	IgnoreUnavailable   *bool
	RenamePattern       string
	RenameReplacement   string
	IndexSettings       map[string]any
	IgnoreIndexSettings []string
	WaitForCompletion   bool
	MasterTimeout       time.Duration // ?master_timeout
}

// SnapshotRestoreInfo is the per-snapshot record echoed by the cluster on
// POST /_snapshot/<repo>/<snap>/_restore. Both dialects return the same shape:
// `{snapshot:{snapshot, indices, shards:{total,successful,failed}}}` when the
// cluster acted synchronously (wait_for_completion=true) and `{accepted:true}`
// when it queued the restore. Indices is the post-rename list — i.e. the
// indices the cluster *will* materialise — which makes a restore record
// directly comparable to a `snapshot list` row when verifying.
type SnapshotRestoreInfo struct {
	Snapshot         string   `json:"snapshot,omitempty"          yaml:"snapshot,omitempty"`
	Indices          []string `json:"indices,omitempty"           yaml:"indices,omitempty"`
	ShardsTotal      int      `json:"shards_total,omitempty"      yaml:"shards_total,omitempty"`
	ShardsSuccessful int      `json:"shards_successful,omitempty" yaml:"shards_successful,omitempty"`
	ShardsFailed     int      `json:"shards_failed,omitempty"     yaml:"shards_failed,omitempty"`
	// Collisions is the post-rename index list intersected with the
	// cluster's currently-live indices — populated only by the dry-run
	// pre-flight. A non-empty list is what trips the dry-run blocker;
	// the operator either picks a different rename pair or removes the
	// existing index (deliberately) before re-running live.
	Collisions []string `json:"collisions,omitempty"        yaml:"collisions,omitempty"`
	// PreflightWarning is set when the dry-run pre-flight couldn't
	// complete (the snapshot didn't decode cleanly, /_cat/indices was
	// unreachable, etc.). The dry-run still returns Accepted=true; this
	// field tells the operator the collision check is *unreliable* for
	// this run, not that the run itself is broken.
	PreflightWarning string `json:"preflight_warning,omitempty"  yaml:"preflight_warning,omitempty"`
}

// SnapshotRestoreResult is the normalised view of a snapshot restore. Action /
// DryRun / Repository / Name / Accepted / WaitForCompletion mirror the create
// shape so JSON/YAML consumers can treat the two operations symmetrically.
//
// RenamePattern / RenameReplacement echo the operator's intent so a captured
// JSON record describes the side-by-side restore without having to back-derive
// it from the resulting indices.
//
// DryRun is the global flag — when true the cluster is not contacted and the
// returned record reports what would have been sent. The cluster has no native
// dry-run for restore, so dry-run is implemented client-side.
type SnapshotRestoreResult struct {
	Action            string              `json:"action"                       yaml:"action"`
	DryRun            bool                `json:"dry_run"                      yaml:"dry_run"`
	Repository        string              `json:"repository"                   yaml:"repository"`
	Name              string              `json:"name"                         yaml:"name"`
	Accepted          bool                `json:"accepted"                     yaml:"accepted"`
	WaitForCompletion bool                `json:"wait_for_completion"          yaml:"wait_for_completion"`
	RenamePattern     string              `json:"rename_pattern,omitempty"     yaml:"rename_pattern,omitempty"`
	RenameReplacement string              `json:"rename_replacement,omitempty" yaml:"rename_replacement,omitempty"`
	Snapshot          SnapshotRestoreInfo `json:"snapshot,omitempty"           yaml:"snapshot,omitempty"`
}

// SnapshotDeleteRequest is the input to DELETE /_snapshot/<repo>/<snap[,snap…]>.
// Repository is a literal repo name; Snapshots is the explicit list to delete.
// Patterns are not accepted at this layer — the business `Prune` resolves them
// up-front, presents a deterministic list, and only then asks the cluster to
// delete. Surfacing the resolved set in `--dry-run` is the whole point.
type SnapshotDeleteRequest struct {
	Repository string
	Snapshots  []string
}

// SnapshotDeleteResult is the normalised view of a delete acknowledgement.
// The cluster returns `{acknowledged:true}` on success on both dialects;
// per-snapshot failures (a missing snapshot mid-list) are surfaced as a
// wrapped error rather than a partial success, mirroring how the rest of the
// codebase handles destructive cluster refusals.
type SnapshotDeleteResult struct {
	Action       string   `json:"action"        yaml:"action"`
	Repository   string   `json:"repository"    yaml:"repository"`
	Snapshots    []string `json:"snapshots"     yaml:"snapshots"`
	Acknowledged bool     `json:"acknowledged"  yaml:"acknowledged"`
}

// SnapshotPruneResult is the normalised view of `esops snapshot prune`. The
// retention plan is computed client-side (neither cluster has a native prune
// endpoint), so the result enumerates which snapshots were kept and which were
// deleted. Considered is the total candidate set after the snapshot-pattern
// filter; Kept and Deleted partition that set.
//
// On `--dry-run` the cluster is not contacted; Deleted lists what *would* be
// deleted and Acknowledged stays false. On a live run Acknowledged is true
// once the cluster confirms the bulk delete.
type SnapshotPruneResult struct {
	Action          string   `json:"action"                       yaml:"action"`
	DryRun          bool     `json:"dry_run"                      yaml:"dry_run"`
	Repository      string   `json:"repository"                   yaml:"repository"`
	Considered      int      `json:"considered"                   yaml:"considered"`
	Kept            []string `json:"kept"                         yaml:"kept"`
	Deleted         []string `json:"deleted"                      yaml:"deleted"`
	Acknowledged    bool     `json:"acknowledged"                 yaml:"acknowledged"`
	KeepLast        int      `json:"keep_last,omitempty"          yaml:"keep_last,omitempty"`
	OlderThan       string   `json:"older_than,omitempty"         yaml:"older_than,omitempty"`
	StateFilter     string   `json:"state_filter,omitempty"       yaml:"state_filter,omitempty"`
	SnapshotPattern string   `json:"snapshot_pattern,omitempty"   yaml:"snapshot_pattern,omitempty"`
	// MissingSkipped is non-nil when --continue-on-missing was set and
	// the cluster's bulk-delete returned 404 for some of the names. The
	// remaining names are retried individually and the missing ones land
	// here so the operator's audit log records what was already gone.
	MissingSkipped []string `json:"missing_skipped,omitempty"     yaml:"missing_skipped,omitempty"`
	// OnError is populated when the operator passed --on-error skip and
	// the business layer dispatched per-snapshot. Rollback is not
	// supported for prune (irreversible); the CLI refuses it with a
	// usage error before dispatch.
	OnError *OnErrorSummary `json:"on_error,omitempty" yaml:"on_error,omitempty"`
}

// RecoveryRequest narrows a RecoveryInspector.Recovery call. Indices is a
// comma-separated list (or empty for cluster-wide). ActiveOnly maps to
// `?active_only=true` so the cluster only echoes shards that are
// currently recovering — what the restore poller wants. Setting it to
// false yields the full history (DONE shards included), useful for
// post-hoc inspection.
type RecoveryRequest struct {
	Indices    string
	ActiveOnly bool
}

// RecoveryReport is the neutral view of GET /<indices>/_recovery. The
// cluster keys the response by index name; we surface a flat list of
// IndexRecovery entries with the index name lifted into the struct so
// the caller doesn't have to walk a map. AllDone is a derived flag —
// true when every shard across every index reaches stage DONE — for
// the polling loop's "stop polling" decision.
type RecoveryReport struct {
	Indices []IndexRecovery `json:"indices"   yaml:"indices"`
	AllDone bool            `json:"all_done"  yaml:"all_done"`
}

// IndexRecovery is the per-index recovery view. Shards is the slice of
// per-shard records the cluster echoes; for snapshot restores the
// `type` field is "SNAPSHOT" and the `source` block carries the
// (repository, snapshot, index) tuple the shard is reading from.
type IndexRecovery struct {
	Index  string          `json:"index"   yaml:"index"`
	Shards []ShardRecovery `json:"shards"  yaml:"shards"`
}

// ShardRecovery is one row inside `<index>.shards` in the cluster's
// /_recovery response. Stage walks INIT → INDEX → TRANSLOG →
// FINALIZE → DONE on a shard being restored from a snapshot. Bytes
// and Files percentages drive the operator-facing progress display;
// Source identifies which snapshot the shard is reading from so an
// operator polling a multi-snapshot recovery can see which of N
// concurrent restores each shard belongs to.
type ShardRecovery struct {
	ID             int    `json:"id"                          yaml:"id"`
	Type           string `json:"type"                        yaml:"type"`
	Stage          string `json:"stage"                       yaml:"stage"`
	Primary        bool   `json:"primary"                     yaml:"primary"`
	Source         string `json:"source,omitempty"            yaml:"source,omitempty"`
	BytesPercent   string `json:"bytes_percent,omitempty"     yaml:"bytes_percent,omitempty"`
	FilesPercent   string `json:"files_percent,omitempty"     yaml:"files_percent,omitempty"`
	BytesTotal     int64  `json:"bytes_total,omitempty"       yaml:"bytes_total,omitempty"`
	BytesRecovered int64  `json:"bytes_recovered,omitempty"   yaml:"bytes_recovered,omitempty"`
	FilesTotal     int    `json:"files_total,omitempty"       yaml:"files_total,omitempty"`
	FilesRecovered int    `json:"files_recovered,omitempty"   yaml:"files_recovered,omitempty"`
}

// SnapshotStatusRequest is the input to GET /_snapshot/<repo>/<snap>/_status.
// Both Repository and Snapshot are literal names — patterns are not
// honoured by /_status. Used by the wait/resume machinery on `snapshot
// create` so progress reporting and SIGINT-then-cancel share one data
// path.
type SnapshotStatusRequest struct {
	Repository string
	Snapshot   string
}

// SnapshotStatus is the normalised view of /_snapshot/<repo>/<snap>/_status.
// State is the cluster's enum (STARTED, IN_PROGRESS, SUCCESS, FAILED,
// ABORTED, MISSING). The Shards counters come from `shards_stats` —
// finer-grained than the regular Snapshot record's per-shard total/
// successful/failed because they include init/started/done splits the
// progress UI wants.
//
// MissingFromCluster is true when the cluster has no record of the
// snapshot (404 on /_status). Distinguished from the zero value so a
// resume can tell "still pending" apart from "the cluster forgot".
type SnapshotStatus struct {
	Repository         string `json:"repository"           yaml:"repository"`
	Snapshot           string `json:"snapshot"             yaml:"snapshot"`
	State              string `json:"state"                yaml:"state"`
	IncludeGlobalState bool   `json:"include_global_state" yaml:"include_global_state"`
	StartTimeMillis    int64  `json:"start_time_millis,omitempty"  yaml:"start_time_millis,omitempty"`
	DurationMillis     int64  `json:"duration_millis,omitempty"    yaml:"duration_millis,omitempty"`
	ShardsTotal        int    `json:"shards_total"         yaml:"shards_total"`
	ShardsInit         int    `json:"shards_init"          yaml:"shards_init"`
	ShardsStarted      int    `json:"shards_started"       yaml:"shards_started"`
	ShardsDone         int    `json:"shards_done"          yaml:"shards_done"`
	ShardsFailed       int    `json:"shards_failed"        yaml:"shards_failed"`
	MissingFromCluster bool   `json:"missing_from_cluster,omitempty" yaml:"missing_from_cluster,omitempty"`
}

// SnapshotAnalyzeRequest is the input to POST /_snapshot/<repo>/_analyze.
// Repository is a literal repo name. The remaining knobs map to the
// cluster's query params and let an operator scale the probe up for a
// production-sized repository (default settings target a small fs repo
// and complete in seconds; an S3 repo wants higher BlobCount to exercise
// the consistency surface).
//
// Both dialects expose the same query-param names; ES 7.4+ and OS 2.x
// have stable support. Older clusters return 405 — the adapter
// translates that to ErrUnsupported so the operator sees "your cluster
// doesn't support _analyze" rather than the cluster's terse 405.
type SnapshotAnalyzeRequest struct {
	Repository    string
	BlobCount     int           // ?blob_count, default 100 server-side
	Concurrency   int           // ?concurrency, default 10
	ReadNodeCount int           // ?read_node_count, default 10
	EarlyReadNode int           // ?early_read_node_count, default 2
	MaxBlobSize   string        // ?max_blob_size, e.g. "10MB"
	MaxTotalSize  string        // ?max_total_data_size, e.g. "1GB"
	Timeout       time.Duration // ?timeout
}

// SnapshotAnalyzeResult is the normalised view of a deep verify. Surfaces
// the cluster's per-blob report verbatim — operators triage repository
// problems by comparing the test counts against a known-good baseline,
// and rephrasing the cluster's vocabulary would obscure the signal.
type SnapshotAnalyzeResult struct {
	Action            string `json:"action"                          yaml:"action"`
	Repository        string `json:"repository"                      yaml:"repository"`
	CoordinatingNode  string `json:"coordinating_node,omitempty"     yaml:"coordinating_node,omitempty"`
	BlobCount         int    `json:"blob_count"                      yaml:"blob_count"`
	ConcurrencyLevels int    `json:"concurrency"                     yaml:"concurrency"`
	ReadNodeCount     int    `json:"read_node_count"                 yaml:"read_node_count"`
	EarlyReadNodes    int    `json:"early_read_node_count"           yaml:"early_read_node_count"`
	Status            string `json:"status,omitempty"                yaml:"status,omitempty"`
}

// SnapshotRepositoryRegisterRequest is the input to PUT /_snapshot/<repo>.
// Type is the cluster-side repository plugin (`fs`, `s3`, `azure`, `gcs`,
// …). Settings is the type-specific configuration map; we forward it
// verbatim so each plugin's full surface stays available without a
// translation table that drifts with new versions. Verify (default true
// at the cluster) maps to ?verify=false to skip the post-register node
// connectivity check — useful when registering a repository whose
// backing store isn't yet online and the operator wants the registration
// itself to land first.
type SnapshotRepositoryRegisterRequest struct {
	Repository string
	Type       string
	Settings   map[string]any
	Verify     *bool
}

// SnapshotRepositoryRegisterResult mirrors the cluster's
// `{acknowledged:true}` echo. Action is "register" so a captured JSON
// record is symmetric with the create/restore/delete results.
type SnapshotRepositoryRegisterResult struct {
	Action       string `json:"action"        yaml:"action"`
	DryRun       bool   `json:"dry_run"       yaml:"dry_run"`
	Repository   string `json:"repository"    yaml:"repository"`
	Type         string `json:"type"          yaml:"type"`
	Acknowledged bool   `json:"acknowledged"  yaml:"acknowledged"`
}

// SnapshotRepositoryDeregisterRequest is the input to DELETE /_snapshot/<repo>.
// Repository is a literal repo name; deregister does not accept patterns —
// the cluster's own behaviour here is to refuse a wildcard, mirrored at
// the business layer so the operator gets a clear error up-front.
type SnapshotRepositoryDeregisterRequest struct {
	Repository string
}

// SnapshotRepositoryDeregisterResult mirrors the cluster's
// `{acknowledged:true}` echo. Action is "deregister".
type SnapshotRepositoryDeregisterResult struct {
	Action       string `json:"action"        yaml:"action"`
	DryRun       bool   `json:"dry_run"       yaml:"dry_run"`
	Repository   string `json:"repository"    yaml:"repository"`
	Acknowledged bool   `json:"acknowledged"  yaml:"acknowledged"`
}

// SnapshotPolicy is the neutral view of one scheduled snapshot policy.
// On Elasticsearch this maps to GET /_slm/policy/<name> (Snapshot
// Lifecycle Management); on OpenSearch to
// GET /_plugins/_sm/policies/<name> (Snapshot Management plugin).
//
// The body shapes between the two products differ enough that the
// neutral type focuses on metadata that always comes back, while
// Definition carries the cluster's policy block verbatim so renderers
// can pretty-print it without re-parsing. An ES "snapshot create
// schedule" and an OS "creation+deletion schedule" are different
// concepts and we don't translate them — `--output json/yaml` is the
// faithful view, and the table renders the operator-scannable summary.
//
// Repository is the snapshot repository the policy targets — populated
// from the policy body when present (top-level `repository` on ES,
// `snapshot_config.repository` on OS) so a captured table tells the
// operator where snapshots will land without drilling into JSON.
//
// Schedule is a human-readable rendering of the policy's primary cron
// (ES: top-level `schedule`; OS: `creation.schedule.cron.expression`).
// Empty when the cluster doesn't surface one.
//
// LastSuccessMillis / LastFailureMillis / NextExecutionMillis are
// populated when the cluster reports them — ES SLM reports last_success
// and next_execution natively, OS SM exposes them on per-policy GET
// inside `last_updated_time` and the metadata block. Missing values
// surface as zero.
type SnapshotPolicy struct {
	Name                string         `json:"name"                            yaml:"name"`
	Repository          string         `json:"repository,omitempty"            yaml:"repository,omitempty"`
	Schedule            string         `json:"schedule,omitempty"              yaml:"schedule,omitempty"`
	Enabled             bool           `json:"enabled"                         yaml:"enabled"`
	Version             int64          `json:"version,omitempty"               yaml:"version,omitempty"`
	ModifiedDateMillis  int64          `json:"modified_date_millis,omitempty"  yaml:"modified_date_millis,omitempty"`
	LastSuccessMillis   int64          `json:"last_success_millis,omitempty"   yaml:"last_success_millis,omitempty"`
	LastFailureMillis   int64          `json:"last_failure_millis,omitempty"   yaml:"last_failure_millis,omitempty"`
	NextExecutionMillis int64          `json:"next_execution_millis,omitempty" yaml:"next_execution_millis,omitempty"`
	Definition          map[string]any `json:"definition,omitempty"            yaml:"definition,omitempty"`
}

// SnapshotPolicyPutRequest is the input to PUT /_slm/policy/<name> on ES
// or PUT /_plugins/_sm/policies/<name> on OS. Body is the raw JSON the
// operator handed in — the cluster validates the inner shape, esops
// only checks that the body is a JSON object so the cluster's reasoning
// stays the source of truth.
type SnapshotPolicyPutRequest struct {
	Name string
	Body []byte
}

// SnapshotPolicyPutResult mirrors the cluster's create/update echo.
// Both dialects return an acknowledged-style response; OS additionally
// returns the stored policy with fresh sequence tokens (dropped here —
// `policy get` retrieves them).
type SnapshotPolicyPutResult struct {
	Action       string `json:"action"        yaml:"action"`
	DryRun       bool   `json:"dry_run"       yaml:"dry_run"`
	Name         string `json:"name"          yaml:"name"`
	Acknowledged bool   `json:"acknowledged"  yaml:"acknowledged"`
}

// SnapshotPolicyDeleteRequest is the input to DELETE /_slm/policy/<name>
// (ES) or DELETE /_plugins/_sm/policies/<name> (OS). Name is a literal
// policy name — neither product accepts patterns on the delete path.
type SnapshotPolicyDeleteRequest struct {
	Name string
}

// SnapshotPolicyDeleteResult mirrors the cluster's delete echo.
type SnapshotPolicyDeleteResult struct {
	Action       string `json:"action"        yaml:"action"`
	DryRun       bool   `json:"dry_run"       yaml:"dry_run"`
	Name         string `json:"name"          yaml:"name"`
	Acknowledged bool   `json:"acknowledged"  yaml:"acknowledged"`
}

// SnapshotPolicyExecuteRequest is the input to a "run-now" call:
// POST /_slm/policy/<name>/_execute on ES, POST
// /_plugins/_sm/policies/<name>/_start on OS. The two dialects differ
// in semantics — ES synthesises a one-shot snapshot from the policy
// template; OS toggles the policy's enabled flag — but both serve the
// same operator intent: "kick this policy now".
type SnapshotPolicyExecuteRequest struct {
	Name string
}

// SnapshotPolicyExecuteResult is the normalised result of an execute
// call. SnapshotName carries the cluster's synthesised snapshot name
// when ES returns one; on OS it is empty (the plugin returns no
// snapshot name on _start). Action is "policy-execute" so a captured
// JSON record stays self-describing.
type SnapshotPolicyExecuteResult struct {
	Action       string `json:"action"                 yaml:"action"`
	DryRun       bool   `json:"dry_run"                yaml:"dry_run"`
	Name         string `json:"name"                   yaml:"name"`
	SnapshotName string `json:"snapshot_name,omitempty" yaml:"snapshot_name,omitempty"`
	Acknowledged bool   `json:"acknowledged"           yaml:"acknowledged"`
}

// Task is the neutral view of one in-flight task as reported by GET
// /_tasks?group_by=none. ID is the cluster's composite "<node_id>:
// <task_number>" identifier; the cancel API requires this exact form.
// Both ES and OS expose the same fields, so one shape covers both.
//
// Description is only populated when the request asked for detailed=true
// — the cluster otherwise returns an empty string. RunningTimeNanos
// counts wall-clock time since the task started; the renderer formats
// it as a short duration so saved JSON keeps the raw value (scriptable)
// while the table view stays readable.
//
// ParentTaskID is "" when the cluster reports the task as a root.
// Cancellable says whether the task accepts a cancel signal at all
// (force_merge tasks are not cancellable, for instance) — surfaced so
// `ops tasks cancel` can refuse with a clear message instead of issuing
// a POST the cluster will silently ignore.
type Task struct {
	ID               string `json:"id"                          yaml:"id"`
	Node             string `json:"node,omitempty"              yaml:"node,omitempty"`
	Type             string `json:"type,omitempty"              yaml:"type,omitempty"`
	Action           string `json:"action"                      yaml:"action"`
	Description      string `json:"description,omitempty"       yaml:"description,omitempty"`
	StartTimeMillis  int64  `json:"start_time_millis,omitempty" yaml:"start_time_millis,omitempty"`
	RunningTimeNanos int64  `json:"running_time_nanos"          yaml:"running_time_nanos"`
	Cancellable      bool   `json:"cancellable"                 yaml:"cancellable"`
	Cancelled        bool   `json:"cancelled,omitempty"         yaml:"cancelled,omitempty"`
	ParentTaskID     string `json:"parent_task_id,omitempty"    yaml:"parent_task_id,omitempty"`
}

// TasksFilter narrows a TasksInspector.Tasks call. Zero value lists every
// in-flight task on the cluster. Actions, Nodes and ParentTaskID are
// forwarded to the cluster as query params (the cluster does the
// filtering — no client-side scan); Detailed flips on the cluster's
// `?detailed=true` so the Description field on each Task is populated.
//
// Actions accept the cluster's dotted action names and shell-style
// wildcards: "indices:data/write/reindex", "*search*", etc. The cluster
// short-circuits the unfiltered "no actions" path so listing without a
// filter is the same call shape as a filtered list.
type TasksFilter struct {
	Actions      []string
	Nodes        []string
	ParentTaskID string
	Detailed     bool
}

// TasksCancelRequest is the input to a TasksCanceller.CancelTasks call.
// Either TaskIDs (each literal "<node_id>:<task_number>") or Filter
// must be set; mutually exclusive at the validation layer to keep
// "cancel one/many by ID" and "cancel by filter" calls visually
// distinct in shell history. An empty request is rejected — the
// cluster's POST /_tasks/_cancel with no filter would cancel every
// cancellable task in the cluster, which is a foot-gun we deliberately
// do not expose.
//
// At the capability layer, at most one TaskIDs entry is honoured per
// call (the cluster's POST /_tasks/<id>/_cancel endpoint takes a
// single ID). Multi-ID requests are fanned out by the business layer,
// which invokes the capability once per ID and aggregates per-call
// results — there is no batched cluster endpoint to fold this back
// into one request.
//
// WaitForCompletion forwards `?wait_for_completion=true` so the call
// blocks until the cancel signal lands; useful when the operator wants
// proof that the task is gone before continuing the runbook.
type TasksCancelRequest struct {
	TaskIDs           []string
	Filter            TasksFilter
	WaitForCompletion bool
}

// TasksCancelResult is the cluster's view of a successful cancel call.
// Cancelled is the slice of tasks the cluster reports as having received
// the signal — the cluster echoes the set so the operator sees what was
// actually targeted (a wildcard `--actions` match that hits zero tasks
// returns an empty Cancelled slice, which is information, not an error).
//
// NodeFailures captures the cluster's `node_failures` array verbatim so
// a partial cancel (one node refused) is still surfaced rather than
// silently dropped. DryRun mirrors the request's --dry-run flag (the
// cancel endpoints have no native dry-run; we list-then-report
// client-side). For multi-ID requests, TaskIDs echoes the full set the
// business layer fanned out across; Cancelled and NodeFailures are
// unions across every per-ID call so a partial fan-out (one ID
// succeeded, one failed) is faithfully surfaced.
type TasksCancelResult struct {
	Action            string   `json:"action"                  yaml:"action"`
	DryRun            bool     `json:"dry_run"                 yaml:"dry_run"`
	TaskIDs           []string `json:"task_ids,omitempty"      yaml:"task_ids,omitempty"`
	WaitForCompletion bool     `json:"wait_for_completion"     yaml:"wait_for_completion"`
	Cancelled         []Task   `json:"cancelled"               yaml:"cancelled"`
	NodeFailures      []string `json:"node_failures,omitempty" yaml:"node_failures,omitempty"`
}

// PendingTask is a normalised entry from /_cluster/pending_tasks — work
// the master has accepted but not yet committed to cluster state.
// ClusterHealth.NumberOfPendingTasks gives the count; this is the
// per-task breakdown a rule needs to tell "1 pending task that's been
// queued for 4 minutes" (master overload) from "30 pending tasks that
// were enqueued in the last second" (a healthy burst).
//
// InsertOrder is the cluster's monotonic counter — useful only for
// pairing with a follow-up read to detect "the same task is still
// stuck". Priority is one of "URGENT", "HIGH", "NORMAL", "LOW",
// "LANGUID" — surfaced verbatim so an operator who knows the cluster's
// vocabulary doesn't have to translate. Source is the cluster's free-
// text task description (e.g. "create-index [foo]"). Executing
// distinguishes "currently being applied by the master" from "queued
// behind something".
//
// TimeInQueueMillis is the cluster's own measurement; it does not
// include execution time. The renderer formats it as a short duration
// for the table view; the JSON form keeps the raw value scriptable.
type PendingTask struct {
	InsertOrder       int64  `json:"insert_order"         yaml:"insert_order"`
	Priority          string `json:"priority"             yaml:"priority"`
	Source            string `json:"source"               yaml:"source"`
	TimeInQueueMillis int64  `json:"time_in_queue_millis" yaml:"time_in_queue_millis"`
	Executing         bool   `json:"executing,omitempty"  yaml:"executing,omitempty"`
}

// DeprecationsRequest narrows a DeprecationInspector.Deprecations call.
// Zero value = "report on every category the cluster knows about".
//
// Index, when set, scopes the index_settings category to the named
// pattern (the cluster's own /_migration/deprecations/<index> endpoint
// shape) — the cluster_settings/node_settings/ml_settings categories
// are still returned because they are global. Patterns follow the same
// convention as elsewhere: a literal name, a comma list, or the cluster
// wildcard syntax.
type DeprecationsRequest struct {
	Index string
}

// DeprecationIssue is one finding from /_migration/deprecations,
// flattened across the cluster's per-category groupings. The cluster
// returns issues bucketed under cluster_settings, node_settings,
// index_settings (keyed by index name), ml_settings, data_streams,
// templates, ilm_policies — the bucket name is preserved on Category
// and the per-bucket key (where one exists, e.g. an index name for
// index_settings) on Target. A flat slice is the right shape for a
// rule consumer that wants "every critical issue across the cluster"
// without having to switch on category first.
//
// Level is "critical" / "warning" / "info" / "none" verbatim from the
// cluster — surfaced rather than mapped because the cluster's own
// vocabulary is what the operator-facing remediation docs use.
// Message is the short human description; URL points at the long-form
// docs; Details carries any cluster-supplied extra context (often the
// specific setting key or value that triggered the warning).
//
// ResolveDuringRollingUpgrade flags issues that the cluster will fix
// itself as nodes are restarted — useful so a hygiene rule can grade
// "you need to act before upgrading" against "the upgrade itself
// resolves this". Meta carries the cluster's free-form `_meta` block
// verbatim as a JSON-encoded string when present; it is a recent
// addition and not every issue carries one.
type DeprecationIssue struct {
	Category                    string `json:"category"                                 yaml:"category"`
	Target                      string `json:"target,omitempty"                         yaml:"target,omitempty"`
	Level                       string `json:"level"                                    yaml:"level"`
	Message                     string `json:"message"                                  yaml:"message"`
	URL                         string `json:"url,omitempty"                            yaml:"url,omitempty"`
	Details                     string `json:"details,omitempty"                        yaml:"details,omitempty"`
	ResolveDuringRollingUpgrade bool   `json:"resolve_during_rolling_upgrade,omitempty" yaml:"resolve_during_rolling_upgrade,omitempty"`
	Meta                        string `json:"meta,omitempty"                           yaml:"meta,omitempty"`
}

// DocumentListRequest narrows a DocumentInspector.Documents call. Index
// is the literal index name (no patterns — DocumentInspector is the
// "read operator-managed metadata from a known index" capability, not
// a general search client). IDs optionally restricts to the given
// document IDs; an empty slice returns every document in the index.
//
// The shape is deliberately small. A full DSL surface would push the
// capability towards "general-purpose search" — outside esops's
// scope, and the kind of lowest-common-denominator the project's
// design rules warn against. Operator-managed metadata indices
// (esops-doctor waivers, annotations, config) are the target use
// case; they fit in a single 10k-document _search and do not need
// scrolling.
type DocumentListRequest struct {
	Index string   // required; literal index name, no patterns
	IDs   []string // optional; empty = every document in the index
}

// Document is the neutral view of one cluster-side document. Source is
// the raw `_source` body the cluster returned, kept verbatim — the
// neutral domain types stop at the document boundary because the
// schema of operator-managed bodies (waivers, annotations, config) is
// the consumer's contract, not esops's. Callers decode Source into
// whatever shape they own.
//
// Index is echoed so a captured slice is interpretable on its own
// (the same way IndexSettings carries Index). Version and
// SequenceNumber are surfaced because consumers that write back into
// the same index need them for optimistic concurrency control via
// `if_seq_no`/`if_primary_term` — DocumentInspector itself never
// writes, but exposing the values keeps a future writer capability
// from forcing a re-read.
type Document struct {
	Index          string          `json:"index"            yaml:"index"`
	ID             string          `json:"id"               yaml:"id"`
	Source         json.RawMessage `json:"source,omitempty" yaml:"source,omitempty"`
	Version        int64           `json:"version,omitempty" yaml:"version,omitempty"`
	SequenceNumber int64           `json:"sequence_number,omitempty" yaml:"sequence_number,omitempty"`
}

// IndexMapping is the neutral view of a single index's GET /<index>/_mapping
// response. Properties holds the cluster's raw `properties` tree as an
// opaque map — esops's mapping translator walks it directly rather than
// imposing a typed shape on a structure that mostly varies. Meta carries
// the user-defined `_meta` block when present (operators stash schema
// versions there); Dynamic mirrors the top-level `dynamic` setting.
//
// Used by `migrate plan` to inspect source mappings before a reindex.
type IndexMapping struct {
	Name       string         `json:"name"                yaml:"name"`
	Properties map[string]any `json:"properties"          yaml:"properties"`
	Meta       map[string]any `json:"meta,omitempty"      yaml:"meta,omitempty"`
	Dynamic    string         `json:"dynamic,omitempty"   yaml:"dynamic,omitempty"`
}

// MappingFilter narrows a MappingsInspector.GetMappings call. Index
// accepts the cluster's standard pattern syntax (comma-separated names,
// '*' wildcards). IgnoreUnavailable maps to ?ignore_unavailable=true so
// a pattern that legitimately matches nothing returns an empty slice
// rather than a 404.
type MappingFilter struct {
	Index             string
	IgnoreUnavailable bool
}

// RemoteReindexWhitelist mirrors the cluster's
// `reindex.remote.whitelist` setting from /_cluster/settings — the list
// of host[:port] entries the cluster will accept as a remote source for
// /_reindex. Both ES and OS use the same setting name.
//
// Persistent and Transient are reported separately so an operator can
// see which scope set the value (transient settings vanish on a full
// cluster restart — a real foot-gun for migration plans built around
// them). Defaults captures any value the cluster reports under the
// "defaults" scope (rare; usually empty).
type RemoteReindexWhitelist struct {
	Persistent []string `json:"persistent" yaml:"persistent"`
	Transient  []string `json:"transient"  yaml:"transient"`
	Defaults   []string `json:"defaults,omitempty"  yaml:"defaults,omitempty"`
}

// All returns the de-duplicated union of every scope's entries. Order
// is persistent → transient → defaults. Used by the migrate-check
// reachability test, which only cares whether the source's host:port
// is whitelisted somewhere — not in which scope.
func (w RemoteReindexWhitelist) All() []string {
	total := len(w.Persistent) + len(w.Transient) + len(w.Defaults)
	seen := make(map[string]struct{}, total)
	out := make([]string, 0, total)
	for _, group := range [][]string{w.Persistent, w.Transient, w.Defaults} {
		for _, e := range group {
			if _, ok := seen[e]; ok {
				continue
			}
			seen[e] = struct{}{}
			out = append(out, e)
		}
	}
	return out
}

// MigrationCheckRequest controls a `migrate check` invocation. The
// command resolves both contexts up-front; a missing or unreachable
// endpoint surfaces as a single failed check rather than aborting the
// whole report.
type MigrationCheckRequest struct {
	SourceContext string
	TargetContext string
	Indices       string
}

// MigrationPlanRequest controls a `migrate plan` invocation.
// ThroughputDocsPerSec is an optional override for the duration
// estimate (zero = the package default — see internal/migrate). Indices
// narrows the source-side scan to a subset; empty = every non-hidden
// index.
type MigrationPlanRequest struct {
	SourceContext        string
	TargetContext        string
	Indices              string
	ThroughputDocsPerSec int
}

// MigrationEndpoint is the per-cluster summary block embedded in both
// the check and plan results so a captured JSON record is interpretable
// without re-reading the operator's config.
type MigrationEndpoint struct {
	Context string `json:"context"           yaml:"context"`
	Dialect string `json:"dialect,omitempty" yaml:"dialect,omitempty"`
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
	URL     string `json:"url"               yaml:"url"`
}

// MigrationStatus is the per-check / per-plan severity. The progression
// is ok → warn → block; the report's overall status is the worst seen.
const (
	MigrationStatusOK    = "ok"
	MigrationStatusWarn  = "warn"
	MigrationStatusBlock = "block"
)

// MigrationCheck is one row in MigrationCheckResult.Checks. Name is a
// short stable identifier (e.g. "source.connectivity",
// "target.remote_whitelist"); operators script around it. Detail is the
// human-readable line shown in table output.
type MigrationCheck struct {
	Name   string `json:"name"   yaml:"name"`
	Status string `json:"status" yaml:"status"`
	Detail string `json:"detail" yaml:"detail"`
}

// MigrationCheckResult is the structured outcome of `migrate check`.
type MigrationCheckResult struct {
	Source   MigrationEndpoint `json:"source"   yaml:"source"`
	Target   MigrationEndpoint `json:"target"   yaml:"target"`
	Checks   []MigrationCheck  `json:"checks"   yaml:"checks"`
	Status   string            `json:"status"   yaml:"status"`
	Blockers int               `json:"blockers" yaml:"blockers"`
	Warnings int               `json:"warnings" yaml:"warnings"`
}

// MappingIssue records a single mapping translation finding. Field is
// the dotted JSON path inside `properties` (e.g. "user.address.city").
// FromType is the source type; ToType is empty when the issue is purely
// diagnostic (e.g. an _all/_type usage that needs operator action).
// Severity is one of MigrationStatusOK / Warn / Block.
type MappingIssue struct {
	Field    string `json:"field"           yaml:"field"`
	FromType string `json:"from_type"       yaml:"from_type"`
	ToType   string `json:"to_type,omitempty" yaml:"to_type,omitempty"`
	Severity string `json:"severity"        yaml:"severity"`
	Note     string `json:"note"            yaml:"note"`
}

// MigrationIndexPlan is the per-index entry in MigrationPlan.Indices.
// Status is the worst severity across Issues. EstimatedSeconds is the
// duration estimate at the resolved throughput (0 when DocsCount is 0).
type MigrationIndexPlan struct {
	Index             string         `json:"index"                  yaml:"index"`
	DocsCount         int64          `json:"docs_count"             yaml:"docs_count"`
	PrimaryStoreBytes int64          `json:"primary_store_bytes"    yaml:"primary_store_bytes"`
	Issues            []MappingIssue `json:"issues,omitempty"       yaml:"issues,omitempty"`
	EstimatedSeconds  float64        `json:"estimated_seconds"      yaml:"estimated_seconds"`
	Status            string         `json:"status"                 yaml:"status"`
}

// MigrationPlanTotals is the aggregate footer.
type MigrationPlanTotals struct {
	Indices           int     `json:"indices"             yaml:"indices"`
	DocsCount         int64   `json:"docs_count"          yaml:"docs_count"`
	PrimaryStoreBytes int64   `json:"primary_store_bytes" yaml:"primary_store_bytes"`
	EstimatedSeconds  float64 `json:"estimated_seconds"   yaml:"estimated_seconds"`
}

// MigrationPlan is the structured outcome of `migrate plan`.
type MigrationPlan struct {
	Source        MigrationEndpoint    `json:"source"             yaml:"source"`
	Target        MigrationEndpoint    `json:"target"             yaml:"target"`
	ThroughputDPS int                  `json:"throughput_dps"     yaml:"throughput_dps"`
	Indices       []MigrationIndexPlan `json:"indices"            yaml:"indices"`
	Totals        MigrationPlanTotals  `json:"totals"             yaml:"totals"`
	Blockers      int                  `json:"blockers"           yaml:"blockers"`
	Warnings      int                  `json:"warnings"           yaml:"warnings"`
	Status        string               `json:"status"             yaml:"status"`
}

// CountRequest scopes a DocCounter.Count call to one or more indices.
// IgnoreUnavailable maps to ?ignore_unavailable=true so a list with one
// missing index returns a count for the rest rather than a 404. Used by
// `migrate verify` and `migrate cutover` for fresh per-index doc counts —
// /_cat/indices' `docs.count` lags refreshes by tens of seconds, which
// is the exact window cutover/verify run in.
type CountRequest struct {
	Indices           []string
	IgnoreUnavailable bool
}

// CountResult is the cluster's view of /<index>/_count. Both ES and OS
// return the same envelope ({count, _shards: {total, successful, failed,
// skipped}}); one shape covers both adapters.
type CountResult struct {
	Count  int64           `json:"count"             yaml:"count"`
	Shards CountShardsInfo `json:"shards,omitempty"  yaml:"shards,omitempty"`
}

// CountShardsInfo mirrors the cluster's `_shards` block on the count
// response. Surfaced because a verify reading a partial shard set
// (Failed > 0 or Skipped > 0) is not authoritative — the renderer
// flags it.
type CountShardsInfo struct {
	Total      int `json:"total"      yaml:"total"`
	Successful int `json:"successful" yaml:"successful"`
	Failed     int `json:"failed"     yaml:"failed"`
	Skipped    int `json:"skipped,omitempty" yaml:"skipped,omitempty"`
}

// ReindexSubmitRequest is the input to a remote /_reindex submission.
// The reindex runs on the *target* cluster — Source* describes the
// remote fetch the target performs, the rest of the fields shape the
// reindex body itself. Adapters submit with ?wait_for_completion=false
// and return the cluster's task ID; polling is the business layer's
// job.
//
// The capability deliberately does not own the source-side credentials
// indirection (env / file / keyring). Those are resolved in the
// business layer from the source context's auth config; the adapter
// receives a literal username / password / headers map.
type ReindexSubmitRequest struct {
	// SourceIndex is the index pattern on the source cluster. The
	// cluster's /_reindex source.index honours the same comma /
	// wildcard syntax /_search does.
	SourceIndex string

	// SourceRemoteHost is the URL the *target* will use to fetch from
	// the source. Includes scheme and port — "https://es-src:9200".
	// This must match an entry in the target's reindex.remote.whitelist
	// (see migrate check).
	SourceRemoteHost string

	// Optional source-side auth carried in the request body to the
	// target. Headers covers bearer/api-key auth as raw header pairs
	// (e.g. "Authorization": "ApiKey ...").
	SourceUsername string
	SourcePassword string
	SourceHeaders  map[string]string

	// TargetIndex is the destination on the target cluster. Reindex
	// will create the index lazily; pre-creating with the correct
	// settings/mappings is the operator's job (the translator from
	// `migrate plan` is the input to that work).
	TargetIndex string

	// Conflicts maps to the cluster's `conflicts` field — "abort"
	// (default) stops on the first version_conflict, "proceed"
	// continues and counts the failures.
	Conflicts string

	// SizeLimit caps total docs copied. 0 = no cap.
	SizeLimit int64

	// RequestsPerSecond throttles the reindex. -1 = unbounded; 0 =
	// cluster default; positive = explicit cap.
	RequestsPerSecond float64

	// Slices controls source-side slicing. "auto" lets the cluster
	// pick (recommended); a positive integer pins it; "" = unsliced.
	Slices string

	// WaitForActiveShards forwards the cluster's
	// ?wait_for_active_shards param. "" = cluster default ("1"),
	// "all" / a positive integer otherwise.
	WaitForActiveShards string
}

// ReindexSubmit captures the cluster's response to a deferred /_reindex
// submission. The cluster returns {"task": "<node_id>:<task_number>"};
// the adapter normalises that into TaskID. Action is "migrate-reindex"
// for log/output consistency.
type ReindexSubmit struct {
	Action      string `json:"action"        yaml:"action"`
	TaskID      string `json:"task_id"       yaml:"task_id"`
	SourceIndex string `json:"source_index"  yaml:"source_index"`
	TargetIndex string `json:"target_index"  yaml:"target_index"`
}

// ReindexTaskStatus is the polling view of a deferred /_reindex via
// /_tasks/<task_id>. The composed `task.status` block carries the
// reindex-specific progress fields; the surrounding envelope adds
// completion, cancellable/cancelled flags, and any failures[] entries.
//
// Failures are surfaced as a slice of preformatted "<index>[<id>]:
// <reason>" strings — the cluster's failure objects vary in shape
// across versions and dialects, and a flat string list is the form
// the renderer + log lines need.
type ReindexTaskStatus struct {
	TaskID            string   `json:"task_id"                       yaml:"task_id"`
	Completed         bool     `json:"completed"                     yaml:"completed"`
	Total             int64    `json:"total"                         yaml:"total"`
	Created           int64    `json:"created"                       yaml:"created"`
	Updated           int64    `json:"updated"                       yaml:"updated"`
	Deleted           int64    `json:"deleted"                       yaml:"deleted"`
	Batches           int64    `json:"batches"                       yaml:"batches"`
	VersionConflicts  int64    `json:"version_conflicts"             yaml:"version_conflicts"`
	Noops             int64    `json:"noops"                         yaml:"noops"`
	BulkRetries       int64    `json:"retries_bulk"                  yaml:"retries_bulk"`
	SearchRetries     int64    `json:"retries_search"                yaml:"retries_search"`
	ThrottledMillis   int64    `json:"throttled_millis,omitempty"    yaml:"throttled_millis,omitempty"`
	RequestsPerSecond float64  `json:"requests_per_second,omitempty" yaml:"requests_per_second,omitempty"`
	StartTimeMillis   int64    `json:"start_time_millis,omitempty"   yaml:"start_time_millis,omitempty"`
	RunningTimeNanos  int64    `json:"running_time_nanos,omitempty"  yaml:"running_time_nanos,omitempty"`
	Cancellable       bool     `json:"cancellable,omitempty"         yaml:"cancellable,omitempty"`
	Cancelled         bool     `json:"cancelled,omitempty"           yaml:"cancelled,omitempty"`
	Failures          []string `json:"failures,omitempty"            yaml:"failures,omitempty"`
}

// MigrationReindexRequest is the operator-facing migrate-layer request:
// which indices to copy, throughput knobs, and dry-run support.
// Multi-index requests fan out as one ReindexSubmit per source index;
// each task is polled to completion before the next is submitted so a
// shared cluster's reindex slot stays bounded by the operator's
// expectation, not multiplied by --indices' resolved cardinality.
//
// Resume is set by `esops migrate reindex --resume`. When true, the
// orchestrator looks up persisted state under $XDG_STATE_HOME/esops/
// keyed by (source-context, target-context, target-suffix-or-literal)
// and continues polling any task IDs that did not complete in the prior
// run. The new fan-out is skipped — resume is "finish what was started",
// not "start over with the same flags".
type MigrationReindexRequest struct {
	SourceContext       string
	TargetContext       string
	Indices             string  // pattern: comma list / wildcards
	TargetSuffix        string  // appended to source name when --to-suffix is set
	TargetIndexLiteral  string  // explicit target name; mutually exclusive with TargetSuffix
	Conflicts           string  // "abort" / "proceed"
	RequestsPerSecond   float64 // -1 = unbounded; 0 = cluster default
	Slices              string  // "auto" or a positive integer; "" = default
	WaitForActiveShards string  // "" / "all" / "<N>"
	SizeLimit           int64
	DryRun              bool
	Resume              bool
	PollInterval        time.Duration
}

// MigrationReindexEntry is the per-source-index outcome line in a
// MigrationReindexResult. Status is one of MigrationStatusOK / Warn /
// Block; Error carries the cluster-supplied reason when the entry is
// not OK so a saved JSON record stays self-contained.
type MigrationReindexEntry struct {
	SourceIndex string            `json:"source_index"          yaml:"source_index"`
	TargetIndex string            `json:"target_index"          yaml:"target_index"`
	TaskID      string            `json:"task_id,omitempty"     yaml:"task_id,omitempty"`
	DryRun      bool              `json:"dry_run,omitempty"     yaml:"dry_run,omitempty"`
	Status      string            `json:"status"                yaml:"status"`
	Detail      string            `json:"detail,omitempty"      yaml:"detail,omitempty"`
	Stats       ReindexTaskStatus `json:"stats"                 yaml:"stats"`
}

// MigrationReindexResult is the structured outcome of `migrate reindex`.
// The footer mirrors the check / plan reports — Blockers and Warnings
// are the count of per-index entries at each level so a CI script can
// flag a partial run without parsing the entries[] array.
type MigrationReindexResult struct {
	Action   string                  `json:"action"            yaml:"action"`
	Source   MigrationEndpoint       `json:"source"            yaml:"source"`
	Target   MigrationEndpoint       `json:"target"            yaml:"target"`
	DryRun   bool                    `json:"dry_run"           yaml:"dry_run"`
	Indices  []MigrationReindexEntry `json:"indices"           yaml:"indices"`
	Status   string                  `json:"status"            yaml:"status"`
	Blockers int                     `json:"blockers"          yaml:"blockers"`
	Warnings int                     `json:"warnings"          yaml:"warnings"`
}

// MigrationVerifyRequest is the input to `migrate verify`. The pairs
// are resolved up-front by the business layer from --indices so the
// cluster-side calls hit literal names — verify on a 20k-index pattern
// would otherwise issue 20k _count calls in the worst case, with no
// way to distinguish "not yet copied" from "doesn't exist on source".
type MigrationVerifyRequest struct {
	SourceContext string
	TargetContext string
	Indices       string // pattern resolved against source
	TargetSuffix  string // applied to source name to derive target
	// TargetIndexLiteral pins the target side to a single literal name —
	// only valid when the source-side resolution returns one index. The
	// flag mirrors `migrate reindex --to-index`; without it, an operator
	// who reindexed `logs` → `logs-fixed` via --to-index has no way to
	// verify the pair (suffix-based pairing produces the wrong target
	// name).
	TargetIndexLiteral string
}

// MigrationVerifyEntry is one per-index row of a verify report. Diff
// is signed (Source - Target): positive means the target is behind,
// negative is the rare "target has more" case (manual writes during
// the reindex window) — both are surfaced rather than abs'd so a
// captured JSON record reads unambiguously.
type MigrationVerifyEntry struct {
	SourceIndex string `json:"source_index"      yaml:"source_index"`
	TargetIndex string `json:"target_index"      yaml:"target_index"`
	SourceCount int64  `json:"source_count"      yaml:"source_count"`
	TargetCount int64  `json:"target_count"      yaml:"target_count"`
	Diff        int64  `json:"diff"              yaml:"diff"`
	Status      string `json:"status"            yaml:"status"`
	Detail      string `json:"detail,omitempty"  yaml:"detail,omitempty"`
}

// MigrationVerifyResult is the structured outcome of `migrate verify`.
type MigrationVerifyResult struct {
	Action   string                 `json:"action"   yaml:"action"`
	Source   MigrationEndpoint      `json:"source"   yaml:"source"`
	Target   MigrationEndpoint      `json:"target"   yaml:"target"`
	Indices  []MigrationVerifyEntry `json:"indices"  yaml:"indices"`
	Status   string                 `json:"status"   yaml:"status"`
	Blockers int                    `json:"blockers" yaml:"blockers"`
	Warnings int                    `json:"warnings" yaml:"warnings"`
}

// MigrationCutoverRequest is the input to a single-cluster atomic
// alias swap. Alias, FromIndex, and ToIndex are literal names on the
// target cluster — `migrate cutover` rejects patterns at the CLI
// layer so the swap touches exactly what the operator typed.
//
// BlockWrites flips index.blocks.write=true on FromIndex *before* the
// swap so any in-flight writer on the alias gets a clear refusal
// rather than silently appending to the soon-to-be-orphaned old
// index. KeepWriteBlock leaves the block on after the swap; the
// default is to remove it again so the old index stays read-only-but-
// not-write-blocked (operators clean up later with `index delete`).
type MigrationCutoverRequest struct {
	Context        string
	Alias          string
	FromIndex      string
	ToIndex        string
	BlockWrites    bool
	KeepWriteBlock bool
	DryRun         bool
	// DrainDelay is a short pause inserted between setting
	// index.blocks.write=true on FromIndex and performing the alias
	// swap. The block only refuses *new* writes; bulk requests already
	// in flight on individual shards may still complete after the
	// settings call returns. A small delay lets those drain so the
	// orphan index doesn't receive a write the operator thought was
	// already cut over. Zero = no pause (the original behaviour).
	DrainDelay time.Duration
}

// MigrationCutoverStep is one row in MigrationCutoverResult.Steps —
// the per-step outcome of the cutover sequence so a captured JSON
// record explains *why* a swap stopped where it did.
type MigrationCutoverStep struct {
	Name   string `json:"name"             yaml:"name"`
	Status string `json:"status"           yaml:"status"`
	Detail string `json:"detail,omitempty" yaml:"detail,omitempty"`
}

// MigrationCutoverResult is the structured outcome of `migrate cutover`.
// Acknowledged is the cluster's own ack on the alias-swap call; on a
// dry-run it is always false and the steps[] log carries the
// "would-do" detail.
type MigrationCutoverResult struct {
	Action       string                 `json:"action"               yaml:"action"`
	Context      string                 `json:"context"              yaml:"context"`
	Alias        string                 `json:"alias"                yaml:"alias"`
	FromIndex    string                 `json:"from_index"           yaml:"from_index"`
	ToIndex      string                 `json:"to_index"             yaml:"to_index"`
	DryRun       bool                   `json:"dry_run"              yaml:"dry_run"`
	Acknowledged bool                   `json:"acknowledged"         yaml:"acknowledged"`
	Steps        []MigrationCutoverStep `json:"steps"                yaml:"steps"`
	Status       string                 `json:"status"               yaml:"status"`
}

// NodeBootstrap is the per-node view of bootstrap-check posture the
// cluster exposes through its /_nodes/{jvm,process,os} endpoints. Used
// by esops-doctor's bootstrap_memory_lock / file-descriptor /
// max_map_count rules — these are the operator-tuned kernel and JVM
// limits whose absence the cluster either refuses to start with (in
// production mode) or warns loudly about (in development). Surfacing
// them lets a hygiene rule flag the same misconfiguration the cluster
// would have rejected at startup but didn't because the operator
// flipped to single-node mode.
//
// Numeric fields are zero when the cluster did not report a value.
// MaxMapCount in particular is only present on some cluster versions
// (it's an OS-side reading the cluster does not always relay through
// /_nodes); a zero value is "the cluster did not tell us", not "the
// kernel says zero". BootstrapWarnings is a verbatim list of warning
// strings the cluster surfaces (when it does — newer ES tucks them
// behind /_migration/deprecations rather than /_nodes); empty when
// the cluster reports a clean bootstrap or omits the field.
//
// GCCollectors and JVMInputArguments come from the same
// /_nodes/process,jvm body — `jvm.gc_collectors[]` (e.g. ["G1 Young
// Generation","G1 Old Generation"]) and `jvm.input_arguments[]` (the
// raw JVM CLI flags as the running process sees them). Doctor's
// jvm_gc_ergonomics rule reads both: the collector list to flag CMS
// (deprecated since JDK 9, removed in 14) and the argument list to
// flag manual overrides of the cluster's own G1 heuristics
// (-XX:MaxGCPauseMillis, -XX:G1HeapRegionSize, …). Empty when the
// cluster did not surface the field — never inferred from defaults.
type NodeBootstrap struct {
	Name               string       `json:"name"                            yaml:"name"`
	MlockallEnabled    bool         `json:"mlockall_enabled"                yaml:"mlockall_enabled"`
	MaxFileDescriptors int64        `json:"max_file_descriptors,omitempty"  yaml:"max_file_descriptors,omitempty"`
	MaxMapCount        int64        `json:"max_map_count,omitempty"         yaml:"max_map_count,omitempty"`
	BootstrapWarnings  []string     `json:"bootstrap_warnings,omitempty"    yaml:"bootstrap_warnings,omitempty"`
	GCCollectors       []string     `json:"gc_collectors,omitempty"         yaml:"gc_collectors,omitempty"`
	JVMInputArguments  []string     `json:"jvm_input_arguments,omitempty"   yaml:"jvm_input_arguments,omitempty"`
	Plugins            []NodePlugin `json:"plugins,omitempty"               yaml:"plugins,omitempty"`
}

// NodePlugin is the per-plugin row reported by /_nodes/<id>/plugins on
// both Elasticsearch and OpenSearch. Name is the cluster's registered
// plugin name (e.g. "repository-s3", "opensearch-security",
// "analysis-icu"); Version is the plugin's version string, blank when
// the cluster did not report one. Description is intentionally omitted
// — the cluster's free-text description is long, version-specific,
// and never the bit operators are looking up.
type NodePlugin struct {
	Name    string `json:"name"              yaml:"name"`
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
}

// NodeSettings is the per-node static-settings view from
// /_nodes/settings — the operator-set keys the cluster doesn't surface
// through /_cluster/settings (network.host, node.store.allow_mmap,
// discovery.seed_hosts, path.data, action.auto_create_index, …).
//
// Settings is the flat-key map of dotted-path keys → cluster-reported
// value, exactly as the cluster returned it with flat_settings=true;
// consumers reach in directly without walking nested objects. Keys
// the operator did not set are absent (no cluster defaults are
// merged in).
//
// Name is echoed so a captured slice is interpretable on its own —
// rules iterate per node and need to attribute the value back to the
// reporting node when the fleet disagrees.
type NodeSettings struct {
	Name     string         `json:"name"     yaml:"name"`
	Settings map[string]any `json:"settings" yaml:"settings"`
}

// ClusterSettingsFull is the un-narrowed view of /_cluster/settings —
// the persistent and transient scopes plus the cluster's own defaults
// when the caller asked for them. Distinct from ClusterSettingsView,
// which is shaped narrowly for the drain/uncordon path's single
// allocation-exclude key; rules that need to inspect any other key
// (cluster.routing.allocation.awareness.attributes,
// action.destructive_requires_name, script.allowed_types,
// reindex.remote.whitelist, cluster.max_shards_per_node, …) need the
// full envelope so they can drill in without each one demanding its
// own typed view.
//
// Maps are flat-key strings → cluster-reported value when the caller
// requested flat_settings=true (the default at the adapter layer).
// Defaults is empty unless the caller asked for include_defaults; the
// cluster's default tree is large and rarely interesting outside of
// drift/awareness rules that explicitly want to compare against it.
type ClusterSettingsFull struct {
	Persistent map[string]any `json:"persistent"          yaml:"persistent"`
	Transient  map[string]any `json:"transient"           yaml:"transient"`
	Defaults   map[string]any `json:"defaults,omitempty"  yaml:"defaults,omitempty"`
}

// NodeAllocation is the per-node row /_cat/allocation reports — shard
// count and disk breakdown keyed by node name. Distinct from Node
// (which wraps /_cat/nodes) because /_cat/allocation also reports rows
// for the cluster's "UNASSIGNED" pseudo-node, and because adding the
// shard count to /_cat/nodes would force every Nodes() caller to pay
// for a second cluster call they don't need.
//
// Used by esops-doctor's shard_count_per_node rule (the ~600-shard-
// per-node ceiling). Numeric fields are bytes; zero when the cluster
// did not report a value (coordinating-only nodes legitimately omit
// the disk columns).
type NodeAllocation struct {
	Name             string `json:"name"                          yaml:"name"`
	Shards           int    `json:"shards"                        yaml:"shards"`
	DiskIndicesBytes int64  `json:"disk_indices_bytes,omitempty"  yaml:"disk_indices_bytes,omitempty"`
	DiskUsedBytes    int64  `json:"disk_used_bytes,omitempty"     yaml:"disk_used_bytes,omitempty"`
	DiskAvailBytes   int64  `json:"disk_avail_bytes,omitempty"    yaml:"disk_avail_bytes,omitempty"`
	DiskTotalBytes   int64  `json:"disk_total_bytes,omitempty"    yaml:"disk_total_bytes,omitempty"`
	DiskPercent      int    `json:"disk_percent,omitempty"        yaml:"disk_percent,omitempty"`
	Host             string `json:"host,omitempty"                yaml:"host,omitempty"`
	IP               string `json:"ip,omitempty"                  yaml:"ip,omitempty"`
}

// ClusterSettingsPutRequest is the input to a generic PUT
// /_cluster/settings call. Persistent and Transient carry the keys
// the operator wants written into each scope; the cluster honours
// either map being absent (no scope key in the body), or being
// present but empty (no-op write to that scope). The adapter sends
// flat_settings=true so dotted keys are accepted as-is — no nested
// {"cluster":{"routing":{"allocation":{"exclude":{"_name":...}}}}}
// dance from the consumer's side.
//
// Values are forwarded verbatim. The cluster validates known keys and
// returns a typed 400 with its own reason when a key is unknown,
// immutable, or a value is rejected — surfaced verbatim.
//
// Distinct from ClusterAllocationExcludeUpdate, which is the narrow
// allocation-exclude path bound to drain/uncordon. This is the general
// "set whatever cluster-level key the operator typed" path behind
// `ops settings set`.
type ClusterSettingsPutRequest struct {
	Persistent map[string]any
	Transient  map[string]any
}

// ClusterSettingsPutResult is the cluster's `acknowledged` flag plus
// the post-update persistent/transient maps the cluster echoed back.
// The cluster always returns the resulting state on a 200, so the
// renderer can show "before / after" without a separate GET.
type ClusterSettingsPutResult struct {
	Acknowledged bool           `json:"acknowledged"        yaml:"acknowledged"`
	Persistent   map[string]any `json:"persistent"          yaml:"persistent"`
	Transient    map[string]any `json:"transient"           yaml:"transient"`
}

// ClusterSettingsScopedView is the result of `ops settings get`,
// surfaced through the renderer. The cluster reports persistent and
// transient as flat-key maps when flat_settings=true; we forward both
// alongside Defaults (only populated when --include-defaults was set).
//
// IncludeDefaults is recorded so a captured JSON record stays
// interpretable on its own — the consumer reading the saved blob
// doesn't have to remember which CLI flags were passed.
type ClusterSettingsScopedView struct {
	Persistent      map[string]any `json:"persistent"          yaml:"persistent"`
	Transient       map[string]any `json:"transient"           yaml:"transient"`
	Defaults        map[string]any `json:"defaults,omitempty"  yaml:"defaults,omitempty"`
	IncludeDefaults bool           `json:"include_defaults"    yaml:"include_defaults"`
}

// ClusterSettingsDiff is the result of `ops settings diff`: the keys
// where persistent and transient disagree (including keys set in only
// one scope). A row whose persistent and transient values are equal is
// omitted on purpose — the diff is for finding drift, and "set the
// same in both scopes" is not drift.
type ClusterSettingsDiff struct {
	Rows []ClusterSettingsDiffRow `json:"rows" yaml:"rows"`
}

// ClusterSettingsDiffRow is one entry in a ClusterSettingsDiff: the
// flat key plus the value from each scope. A nil value means "this
// scope did not have the key set".
type ClusterSettingsDiffRow struct {
	Key        string `json:"key"                  yaml:"key"`
	Persistent any    `json:"persistent,omitempty" yaml:"persistent,omitempty"`
	Transient  any    `json:"transient,omitempty"  yaml:"transient,omitempty"`
}

// IndexUnblockRequest narrows the input to ops.UnblockIndices to the
// shape `ops unblock` actually needs: a comma-separated index pattern
// and the optional knobs the cluster's _settings endpoint accepts.
//
// Block targets the named cluster index block. Empty string means
// "the canonical disk-watermark block": index.blocks.read_only_allow_delete
// — the one disk-pressure auto-applies and does not auto-clear when
// pressure subsides. Other blocks (read_only, write, metadata, read)
// are accepted so the same command can clear an operator-applied
// freeze without forcing them to remember the underlying _settings
// shape.
type IndexUnblockRequest struct {
	Indices           []string
	Block             string
	IgnoreUnavailable bool
	DryRun            bool
}

// IndexUnblockResult is the normalised view of the PUT
// /<indices>/_settings call that ops.UnblockIndices issues. Mirrors
// IndexSettingsUpdateResult deliberately — `unblock` is a narrow
// preset on top of the same settings endpoint, and the captured JSON
// shape stays interpretable next to other settings-mutation records.
type IndexUnblockResult struct {
	Action       string   `json:"action"        yaml:"action"`
	DryRun       bool     `json:"dry_run"       yaml:"dry_run"`
	Indices      []string `json:"indices"       yaml:"indices"`
	Block        string   `json:"block"         yaml:"block"`
	Acknowledged bool     `json:"acknowledged"  yaml:"acknowledged"`
}

// IndexCacheClearRequest is the input to the POST /<indices>/_cache/clear
// endpoint. Indices is the comma-separated pattern the cluster receives
// as the path segment (empty means cluster-wide — the business layer
// refuses that shape so a wildcard "clear everything" is never the
// implicit outcome).
//
// Fielddata, Query, and Request map to the cluster's per-cache flags.
// When all three are false, the cluster's default behaviour is to
// clear every cache. The business layer surfaces this as a flat
// "clear everything" mode when the operator passes none of the
// per-cache flags, so the operator gets the cluster's default without
// having to type --fielddata --query --request.
//
// Fields narrows fielddata clearing to specific field names — only
// honoured when Fielddata is true (the cluster ignores it otherwise).
type IndexCacheClearRequest struct {
	Indices           []string
	Fielddata         bool
	Query             bool
	Request           bool
	Fields            []string
	IgnoreUnavailable bool
}

// IndexCacheClearResult is the normalised view of POST /<indices>/_cache/clear.
// Both ES and OS report the {_shards: {total, successful, failed,
// failures: []}} envelope; we surface those four numbers so a
// non-zero failed count is grep-able from the table view.
//
// Caches lists the labels of every cache the operator asked to clear
// (or {"all"} when none of the per-cache flags were set), so a
// captured JSON record stays interpretable on its own without having
// to know what the CLI default expanded to.
type IndexCacheClearResult struct {
	Action     string   `json:"action"               yaml:"action"`
	DryRun     bool     `json:"dry_run"              yaml:"dry_run"`
	Indices    []string `json:"indices"              yaml:"indices"`
	Caches     []string `json:"caches"               yaml:"caches"`
	Fields     []string `json:"fields,omitempty"     yaml:"fields,omitempty"`
	Total      int      `json:"shards_total"         yaml:"shards_total"`
	Successful int      `json:"shards_successful"    yaml:"shards_successful"`
	Failed     int      `json:"shards_failed"        yaml:"shards_failed"`
}

// AuditConfig is the neutral view of the cluster's audit-logging
// configuration — whether audit is enabled, which sinks the events
// land in, and the include/exclude/ignore-users filters that shape
// what gets recorded. Fed by the audit-logging-enabled hygiene rule
// (does the cluster even capture an audit trail?) and the
// recent-audit-log-warnings rule (is anything failing in the trail?).
//
// Sources differ per dialect:
//   - ES reads /_cluster/settings filtered to xpack.security.audit.*
//   - OS reads /_plugins/_security/audit/config (security plugin)
//
// so the adapter normalises both into this shape.
//
// Outputs is the set of sinks the cluster ships records to. The
// values "index" and "logfile" mirror the ES/OS naming and are kept
// stable across dialects: an empty slice means the cluster did not
// report any sink (most commonly because audit is disabled). Empty
// EventsInclude/EventsExclude/IgnoreUsers slices mean the cluster
// reported nothing for that filter — distinct from "an empty list
// was explicitly set" only at the wire level, which the audit rule
// does not need to distinguish.
type AuditConfig struct {
	Enabled       bool     `json:"enabled"                  yaml:"enabled"`
	Outputs       []string `json:"outputs,omitempty"        yaml:"outputs,omitempty"`
	EventsInclude []string `json:"events_include,omitempty" yaml:"events_include,omitempty"`
	EventsExclude []string `json:"events_exclude,omitempty" yaml:"events_exclude,omitempty"`
	IgnoreUsers   []string `json:"ignore_users,omitempty"   yaml:"ignore_users,omitempty"`
}

// AuditWarningsRequest narrows a recent-audit-log-warnings tail to
// the time window and result count the consumer can usefully digest.
// Since trims to entries with @timestamp >= Since (zero = no lower
// bound, the cluster decides what "recent" means by index lifetime).
// Limit caps the number of returned rows so a busy cluster does not
// drown the rule; zero falls through to the adapter's safe default.
type AuditWarningsRequest struct {
	Since time.Time
	Limit int
}

// AuditWarning is one entry from the audit-log tail, normalised
// across ES and OS. Only metadata fields are surfaced — Timestamp,
// Layer, Type, Count — by design: the audit log records request
// bodies and document IDs that callers must not see through this
// capability.
//
// Layer is the wire layer the event came from ("rest", "transport",
// "ip_filter", …) lowercased so consumer matches stay stable across
// dialects (OS reports "REST"/"TRANSPORT" upper-case, ES uses
// lowercase). Type is the event type (e.g., "authentication_failed"
// on ES, "FAILED_LOGIN" on OS, also lowercased) — the rule decides
// which types qualify as warnings.
//
// Count is 1 for a single entry; non-1 when the adapter ever
// summarises into buckets (a future extension — the field is in the
// type now so the consumer doesn't have to migrate later).
type AuditWarning struct {
	Timestamp time.Time `json:"timestamp"        yaml:"timestamp"`
	Layer     string    `json:"layer,omitempty"  yaml:"layer,omitempty"`
	Type      string    `json:"type,omitempty"   yaml:"type,omitempty"`
	Count     int       `json:"count"            yaml:"count"`
}

// Realm is one configured authentication realm — the dialect-neutral
// view of "how does this cluster identify a request?". Fed by the
// deprecated-realms hygiene rule (does this cluster still rely on a
// realm type the next major drops?) and any future identity-posture
// audit.
//
// Sources differ per dialect:
//   - ES walks /_cluster/settings filtered to xpack.security.authc.realms.*
//     across persistent → transient → defaults, where each realm is
//     keyed as `xpack.security.authc.realms.<type>.<name>` with order
//     and enabled fields inside.
//   - OS reads /_plugins/_security/api/securityconfig and walks the
//     dynamic.authc map: each authc domain carries an http_authenticator
//     and authentication_backend block plus order/http_enabled.
//
// Type is the cluster's own realm/backend label — "native", "file",
// "ldap", "saml", "oidc", "kerberos" on ES; "internal", "ldap",
// "kerberos", "noop" plus an http authenticator hint ("basic",
// "openid", "saml", "jwt", …) on OS. Order is the cluster's evaluation
// order (lower wins). Enabled is whether the cluster will actually
// consult the realm; a disabled-but-present realm is information, not
// a failure.
//
// Deprecated is true when the realm type is marked deprecated for the
// cluster's reported version. The flag is computed against the cluster's
// own version rather than a static catalog so a future deprecation
// (say, removing a realm type in ES 10) lands without esops bookkeeping
// in every consumer.
type Realm struct {
	Name       string `json:"name"                  yaml:"name"`
	Type       string `json:"type"                  yaml:"type"`
	Order      int    `json:"order"                 yaml:"order"`
	Enabled    bool   `json:"enabled"               yaml:"enabled"`
	Deprecated bool   `json:"deprecated,omitempty"  yaml:"deprecated,omitempty"`
}

// APIKey is the richer ES-only view of one API key from
// /_security/api_key, returned by APIKeyInspector. Distinct from
// SecurityAPIKey (which the audit pipeline produces with the older
// string-shaped Creation/Expiration) so callers wanting the age-based
// hygiene rule can reach for typed time.Time values without re-parsing.
//
// Creation is the cluster-reported creation timestamp — always set when
// the cluster surfaced a key. Expiration is nil when the key has no
// expiry set (the operator-relevant signal "this key never expires").
// LastAuth is nil when the cluster does not report it: ES did not
// surface last-authentication on every supported version, and a
// missing value is "the cluster did not tell us", not "never used".
//
// RoleTemplates carries the configured role-template names attached to
// the key (the request shape ES accepts at creation time). Empty when
// the key was created without a role-templates block — i.e. the key
// inherits its owner's roles directly.
type APIKey struct {
	ID            string     `json:"id"                       yaml:"id"`
	Name          string     `json:"name"                     yaml:"name"`
	Username      string     `json:"username,omitempty"       yaml:"username,omitempty"`
	Realm         string     `json:"realm,omitempty"          yaml:"realm,omitempty"`
	Creation      time.Time  `json:"creation"                 yaml:"creation"`
	Expiration    *time.Time `json:"expiration,omitempty"     yaml:"expiration,omitempty"`
	LastAuth      *time.Time `json:"last_auth,omitempty"      yaml:"last_auth,omitempty"`
	Invalidated   bool       `json:"invalidated,omitempty"    yaml:"invalidated,omitempty"`
	RoleTemplates []string   `json:"role_templates,omitempty" yaml:"role_templates,omitempty"`
}

// ServiceToken is the ES-only view of one credential issued for a
// service account, returned by ServiceTokenInspector. ES lets a service
// account hold both index-stored tokens (mutable, listed via
// /_security/service/<ns>/<svc>/credential) and file-realm tokens
// (declared on disk in service_tokens). The capability surfaces both;
// Source distinguishes them so the operator can tell which credential
// is rotated through the API and which is bound to a node's
// configuration.
//
// Creation is the cluster-reported creation timestamp for index-stored
// tokens; the zero time for file-realm tokens, which the cluster does
// not timestamp. Namespace and Service together form the parent
// service account ("elastic/fleet-server"); Name is the per-credential
// identifier.
type ServiceToken struct {
	Name      string    `json:"name"                yaml:"name"`
	Namespace string    `json:"namespace"           yaml:"namespace"`
	Service   string    `json:"service"             yaml:"service"`
	Creation  time.Time `json:"creation,omitempty"  yaml:"creation,omitempty"`
	Source    string    `json:"source"              yaml:"source"`
}

// FieldCapsRequest narrows a FieldCapsInspector.FieldCaps call. Index
// is forwarded to the cluster as the path's <index> segment
// (comma-joined; empty or "*" lists every visible index). Fields
// narrows the cluster-side response to a specific field set; empty
// requests "*" — every mapped field — which is what the deprecated-
// field-types and mapping-drift rules want.
//
// IncludeUnmapped surfaces fields that exist in some indices of the
// matched pattern but not others; off by default because the cluster
// then includes "unmapped" entries in `Types` which most consumers
// have to filter out.
type FieldCapsRequest struct {
	Index           string
	Fields          []string
	IncludeUnmapped bool
}

// FieldCaps is the neutral view of one field's metadata across the
// matched index pattern. The cluster's /_field_caps response groups
// the data by field name; this type is the per-field row.
//
// Types is the union of all field types seen across the matched
// indices. A homogeneous mapping has one entry; a pattern that
// straddles types (e.g. one index with `keyword`, another with `text`)
// has multiple — the canonical signal mapping-drift rules look for.
//
// MetadataField mirrors the cluster's `metadata_field` flag: true for
// `_id`, `_routing`, `_source`, etc. Searchable/Aggregatable are the
// per-field capabilities the cluster reports.
//
// Indices is populated only when the field is heterogeneous across the
// pattern — the cluster lists which indices contributed each type
// variant. For homogeneous fields this is empty (the cluster omits it).
type FieldCaps struct {
	Field         string   `json:"field"                    yaml:"field"`
	Types         []string `json:"types"                    yaml:"types"`
	Searchable    bool     `json:"searchable"               yaml:"searchable"`
	Aggregatable  bool     `json:"aggregatable"             yaml:"aggregatable"`
	MetadataField bool     `json:"metadata_field,omitempty" yaml:"metadata_field,omitempty"`
	Indices       []string `json:"indices,omitempty"        yaml:"indices,omitempty"`
}

// FieldCapsSummary is the richer return shape from
// FieldCapsInspector.FieldCapsSummary — the per-field flattened slice
// plus the cluster's top-level "indices" list (every index that
// matched the request pattern). The slice form (`FieldCaps()`) is
// kept as the primary surface because most rules only want the
// per-field rows; consumers that need to render "23 indices analysed"
// or that want to know if a request narrowed past zero indices reach
// for the summary instead.
//
// MatchedIndices is the cluster's verbatim "indices" array — sorted
// and deduplicated by the cluster, surfaced as-is so a captured slice
// is stable across runs.
type FieldCapsSummary struct {
	Fields         []FieldCaps `json:"fields"          yaml:"fields"`
	MatchedIndices []string    `json:"matched_indices" yaml:"matched_indices"`
}

// FieldCardinalityRequest narrows a FieldCardinalityInspector.Cardinality
// call. Each (Index, Field) pair runs as a separate /_search with a
// single `cardinality` aggregation; the implementation runs them
// sequentially under MaxFieldsToScan and IndexLimit, so a runaway
// rule stays polite to the cluster.
//
// Indices is the comma-separated index pattern forwarded as the path
// segment. Fields is the literal list of fields the rule wants
// counted; cardinality is computed per (index, field) pair.
//
// Precision is the optional `precision_threshold` for the cluster's
// HyperLogLog estimator (the cluster's default is 3000). Zero leaves
// the cluster's default in place; values >40000 are silently capped by
// the cluster.
//
// MaxFieldsToScan caps the per-call total (len(Indices) × len(Fields))
// before the request fans out; 0 = no cap. IndexLimit caps the number
// of indices touched in one call (resolved client-side from the
// pattern); 0 = no cap. Both are belt-and-braces — the rule that
// invokes this should also pass a pre-narrowed Indices list.
type FieldCardinalityRequest struct {
	Indices         string
	Fields          []string
	Precision       int
	MaxFieldsToScan int
	IndexLimit      int
}

// FieldCardinality is the neutral view of one field's cardinality on
// one index — the result of one (index, field) /_search round trip.
//
// Cardinality is the cluster's aggregation result; for HyperLogLog
// this is an approximation, surfaced through Precision. "exact" is
// reserved for a future code path that runs `terms` rather than
// `cardinality` (we don't run that now — it's listed in the type so
// the rule's table column has a stable shape).
//
// Sampled is true when the implementation ran the agg against a
// random-sample subset of the index rather than the full document
// set. Today's adapter never samples — Sampled is always false — but
// the field is on the type so the rule consumer can rely on the
// shape and a future sampling path can land without a type bump.
type FieldCardinality struct {
	Field       string `json:"field"                yaml:"field"`
	Index       string `json:"index"                yaml:"index"`
	Cardinality int64  `json:"cardinality"          yaml:"cardinality"`
	Sampled     bool   `json:"sampled,omitempty"    yaml:"sampled,omitempty"`
	Precision   string `json:"precision,omitempty"  yaml:"precision,omitempty"`
}

// RestoreEvent is the neutral view of one snapshot-restore observation,
// returned by SnapshotRestoreHistoryInspector. The capability has two
// sources: the cluster's task history (in-flight + recently completed
// restores via /_tasks?actions=cluster:admin/snapshot/restore*) and
// the convention-based `.esops-doctor-restore-tests` index that
// operators or runbooks write to when they exercise a verified restore.
//
// Source distinguishes the two — "task_history" for the cluster-derived
// view (always best-effort: a cluster that hasn't restored anything
// recently reports an empty slice, which is a valid "no evidence"
// outcome) and "convention_index" for the runbook-driven path. The
// rule consumer treats an empty slice as "no evidence" rather than
// "never restored" — the difference matters when the lookback window
// is short.
//
// State is the cluster's reported state when known: "in_progress",
// "success", "failed". CompletedAt is nil for in-flight tasks.
type RestoreEvent struct {
	SnapshotID  string     `json:"snapshot_id"            yaml:"snapshot_id"`
	Repository  string     `json:"repository"             yaml:"repository"`
	StartedAt   time.Time  `json:"started_at"             yaml:"started_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty" yaml:"completed_at,omitempty"`
	State       string     `json:"state"                  yaml:"state"`
	Indices     []string   `json:"indices,omitempty"      yaml:"indices,omitempty"`
	Source      string     `json:"source"                 yaml:"source"`
}

// SnapshotRestoreHistoryRequest narrows a SnapshotRestoreHistoryInspector
// call. Lookback bounds how far back to look at task history (0 = "no
// limit / whatever the cluster still has"). ConventionIndex names the
// convention-based index when non-empty; empty falls back to the
// default `.esops-doctor-restore-tests`.
//
// IncludeTaskHistory and IncludeConventionIndex toggle the two
// sources independently. The default zero value (both false) is a
// usage error: the implementation requires at least one source —
// silently no-op'ing on a fully zero request would mask a coding
// mistake in the rule.
type SnapshotRestoreHistoryRequest struct {
	Lookback               time.Duration
	ConventionIndex        string
	IncludeTaskHistory     bool
	IncludeConventionIndex bool
}

// RemoteCluster is the neutral view of one cross-cluster remote, returned
// by RemoteClusterInspector.RemoteClusters. The cluster's /_remote/info
// endpoint is shared across ES and OS — both dialects return the same
// envelope keyed by remote-cluster name.
//
// Mode is "sniff" (the cluster connects to seed hosts and discovers the
// rest of the remote) or "proxy" (the cluster connects through a single
// gateway and lets the gateway route). NumNodesConnected is the live
// count the cluster reports — zero on a configured-but-unreachable
// remote, which is the operator-facing signal CCS readiness rules look
// for. SkipUnavailable mirrors the cluster's `skip_unavailable` flag:
// true means a remote outage does not fail searches that target it,
// false means it does.
type RemoteCluster struct {
	Name              string   `json:"name"                yaml:"name"`
	Mode              string   `json:"mode"                yaml:"mode"`
	Connected         bool     `json:"connected"           yaml:"connected"`
	NumNodesConnected int      `json:"num_nodes_connected" yaml:"num_nodes_connected"`
	SeedHosts         []string `json:"seed_hosts,omitempty" yaml:"seed_hosts,omitempty"`
	SkipUnavailable   bool     `json:"skip_unavailable"    yaml:"skip_unavailable"`
}

// FollowerStats is the neutral view of one cross-cluster-replication
// follower index, returned by RemoteClusterInspector.FollowerStats.
// CCR is Elasticsearch-only — the OS adapter returns ErrUnsupported on
// FollowerStats so the matching doctor rule auto-skips with a stable
// "not applicable on this dialect" reason.
//
// FollowerLagOps is the cluster's reported `operations_written_lag`
// (number of ops the follower is behind the leader). LastReadFailure
// is the cluster's last-read-error string when present, empty when the
// follower is healthy. Paused is true when the operator has paused
// replication via /_ccr/follow/_pause.
//
// RetentionLeaseAgeMillis is the cluster's reported max
// `retention_lease_age_in_millis` across the follower's shards.
// Retention leases keep the leader cluster's operations available for
// the follower to consume; once a lease expires (default lease
// retention period: 12 hours, configured via
// `index.soft_deletes.retention_lease.period`), the leader is free to
// discard the unconsumed ops and the follower falls irrecoverably
// behind. Aggregated as max-across-shards because one expired-lease
// shard makes the whole follower index unrecoverable. Zero when the
// cluster did not report a value (older ES versions, or a brand-new
// follower whose shards have not yet acquired a lease).
type FollowerStats struct {
	Index                   string `json:"index"                                yaml:"index"`
	LeaderIndex             string `json:"leader_index"                         yaml:"leader_index"`
	LeaderCluster           string `json:"leader_cluster"                       yaml:"leader_cluster"`
	FollowerLagOps          int64  `json:"follower_lag_ops"                     yaml:"follower_lag_ops"`
	LastReadFailure         string `json:"last_read_failure,omitempty"          yaml:"last_read_failure,omitempty"`
	Paused                  bool   `json:"paused,omitempty"                     yaml:"paused,omitempty"`
	RetentionLeaseAgeMillis int64  `json:"retention_lease_age_millis,omitempty" yaml:"retention_lease_age_millis,omitempty"`
}

// AutoFollowPattern is the neutral view of one Elasticsearch CCR
// auto-follow pattern, returned by RemoteClusterInspector.AutoFollowPatterns.
// Auto-follow patterns instruct the cluster to create a follower
// index automatically every time the leader cluster creates a new
// index matching one of LeaderIndexPatterns.
//
// Active mirrors the cluster's `active` flag — true when the pattern
// is currently consulted, false when paused (typically during a
// maintenance window). FollowIndexPattern is the cluster's template
// for the follower-side index name; an empty string means the
// cluster's default ("the leader's name") applies.
//
// CCR is Elasticsearch-only; the OpenSearch adapter returns
// ErrUnsupported on AutoFollowPatterns so the matching doctor rule
// auto-skips with a stable "not applicable on this dialect" reason.
type AutoFollowPattern struct {
	Name                string   `json:"name"                              yaml:"name"`
	RemoteCluster       string   `json:"remote_cluster"                    yaml:"remote_cluster"`
	LeaderIndexPatterns []string `json:"leader_index_patterns"             yaml:"leader_index_patterns"`
	FollowIndexPattern  string   `json:"follow_index_pattern,omitempty"    yaml:"follow_index_pattern,omitempty"`
	Active              bool     `json:"active"                            yaml:"active"`
}

// LicenseStatus is the neutral view of an Elasticsearch license,
// returned by LicenseInspector.License. ES-only: OpenSearch has no
// equivalent commercial-license surface, so the OS adapter returns
// ErrUnsupported and the matching doctor rule is dialect-tagged
// `[elasticsearch]`.
//
// Status is the cluster's reported license state: "active", "expired",
// or "invalid". Type is the licence tier ("basic", "standard", "gold",
// "platinum", "enterprise", "trial"). ExpiresAt is nil for licences
// that never expire (the basic tier is the canonical case), populated
// for time-bound licences. MaxNodes is the licence's node cap; zero
// when the licence is not node-bound.
type LicenseStatus struct {
	Status    string     `json:"status"               yaml:"status"`
	Type      string     `json:"type"                 yaml:"type"`
	IssuedTo  string     `json:"issued_to,omitempty"  yaml:"issued_to,omitempty"`
	Issuer    string     `json:"issuer,omitempty"     yaml:"issuer,omitempty"`
	IssuedAt  time.Time  `json:"issued_at"            yaml:"issued_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
	MaxNodes  int        `json:"max_nodes,omitempty"  yaml:"max_nodes,omitempty"`
	UID       string     `json:"uid,omitempty"        yaml:"uid,omitempty"`
}

// SegmentsRequest narrows a SegmentsInspector.Segments call. Indices is
// a comma-separated list (or empty for cluster-wide). Both ES and OS
// share the same `GET /<indices>/_segments` endpoint and response
// shape so one request type covers both dialects.
type SegmentsRequest struct {
	Indices string
}

// SegmentsReport is the neutral view of GET /<indices>/_segments
// aggregated to the index level — what an operator triaging segment
// counts and deleted-doc ratios actually wants. The cluster's response
// nests segments under `indices.<name>.shards.<shardN>[].segments`;
// SegmentsReport rolls those up so a 200-segment, 30-shard index
// renders as a single row (with primary/replica counts kept distinct
// for the wide view).
type SegmentsReport struct {
	Indices []IndexSegments `json:"indices" yaml:"indices"`
}

// IndexSegments is the per-index aggregate. SegmentsTotal counts every
// segment record the cluster echoed (across both primary and replica
// copies), which is what /_segments natively reports. SegmentsPrimary
// drops replica copies — closer to "logical" segment count and the
// number that matters for force-merge planning. DeletedDocs is the
// sum of per-segment `deleted_docs` across primaries (matches what
// /_cat/indices reports under `docs.deleted`). Bytes is the on-disk
// size summed across primaries.
type IndexSegments struct {
	Index            string `json:"index"               yaml:"index"`
	Shards           int    `json:"shards"              yaml:"shards"`
	SegmentsTotal    int    `json:"segments_total"      yaml:"segments_total"`
	SegmentsPrimary  int    `json:"segments_primary"    yaml:"segments_primary"`
	DocsTotal        int64  `json:"docs_total"          yaml:"docs_total"`
	DocsDeleted      int64  `json:"docs_deleted"        yaml:"docs_deleted"`
	Bytes            int64  `json:"bytes"               yaml:"bytes"`
	MaxSegmentsShard int    `json:"max_segments_shard"  yaml:"max_segments_shard"`
	Committed        int    `json:"committed,omitempty" yaml:"committed,omitempty"`
	Searchable       int    `json:"searchable,omitempty" yaml:"searchable,omitempty"`
}

// IngestPipeline is the neutral view of an ingest pipeline as reported
// by GET /_ingest/pipeline[/<id>]. Both Elasticsearch and OpenSearch
// share the same envelope ({"<id>": {"description":..., "processors":
// [...], "version":..., "_meta":..., "on_failure":[...]}}), so one
// shape covers both dialects.
//
// Processors and OnFailure are preserved as map[string]any slices: the
// processor set differs across products and versions (ES has
// `enrich`, OS has `text_embedding`, both add new ones in minor
// versions), so freezing a typed schema for them would force a lossy
// translation. The renderer pretty-prints the slice; structured
// consumers (--output json/yaml) get the same shape the cluster
// returned.
type IngestPipeline struct {
	Name string `json:"name" yaml:"name"`

	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	Version int64 `json:"version,omitempty" yaml:"version,omitempty"`

	Meta map[string]any `json:"meta,omitempty" yaml:"meta,omitempty"`

	Processors []map[string]any `json:"processors,omitempty" yaml:"processors,omitempty"`

	OnFailure []map[string]any `json:"on_failure,omitempty" yaml:"on_failure,omitempty"`
}

// DataStreamBackingIndex is one entry under data_streams[].indices in
// GET /_data_stream. Both Elasticsearch and OpenSearch report the
// backing index name and uuid; ES additionally surfaces the prefer_ilm
// flag and managed_by string ("Index Lifecycle Management",
// "Data stream lifecycle", "Unmanaged"), which we preserve when the
// cluster reports them and leave zero otherwise.
//
// Health, StoreBytes, and DocsCount are *not* in the /_data_stream
// envelope itself — the business layer enriches them from a single
// /_cat/indices call against the backing-index names. They stay
// zero when the enrichment is skipped or fails.
type DataStreamBackingIndex struct {
	Name       string `json:"name"                   yaml:"name"`
	UUID       string `json:"uuid,omitempty"         yaml:"uuid,omitempty"`
	PreferILM  *bool  `json:"prefer_ilm,omitempty"   yaml:"prefer_ilm,omitempty"`
	ManagedBy  string `json:"managed_by,omitempty"   yaml:"managed_by,omitempty"`
	Health     string `json:"health,omitempty"       yaml:"health,omitempty"`
	StoreBytes int64  `json:"store_bytes,omitempty"  yaml:"store_bytes,omitempty"`
	DocsCount  int64  `json:"docs_count,omitempty"   yaml:"docs_count,omitempty"`
}

// DataStream is the neutral view of a data stream as reported by
// GET /_data_stream[/<name>]. Data streams sit *next to* indices —
// each one is backed by a generation-numbered chain of hidden
// `.ds-<stream>-<date>-<gen>` indices that the cluster manages on
// rollover. Both Elasticsearch and OpenSearch share the endpoint shape
// (top-level {"data_streams": [...]}) so one type covers both
// dialects, but a handful of fields are dialect-specific:
//
//   - ILMPolicy: Elasticsearch only. The data stream's associated ILM
//     policy when one is bound via the matching index template;
//     empty on OS because ILM is ES-native.
//   - ISMTemplate: OpenSearch only. The ISM template name resolved for
//     the stream when one is bound; empty on ES.
//   - Lifecycle: ES-only data-stream lifecycle (DSL) block — the
//     newer per-stream retention model that landed in 8.11. We
//     preserve it as a free-form map because the field set is still
//     evolving across minor versions.
//   - Hidden / System / Replicated / AllowCustomRouting: cluster-
//     reported flags. AllowCustomRouting is ES-only; the others apply
//     to both dialects.
//
// Indices is ordered by generation as the cluster returned it (oldest
// first, write index last). Generation, IndexCount, BackingIndexNewest
// and BackingIndexOldest are derived for table rendering so an
// operator can see the lifecycle shape without expanding the full
// index list.
type DataStream struct {
	Name               string                   `json:"name"                            yaml:"name"`
	Status             string                   `json:"status,omitempty"                yaml:"status,omitempty"`
	Generation         int64                    `json:"generation,omitempty"            yaml:"generation,omitempty"`
	Template           string                   `json:"template,omitempty"              yaml:"template,omitempty"`
	ILMPolicy          string                   `json:"ilm_policy,omitempty"            yaml:"ilm_policy,omitempty"`
	ISMTemplate        string                   `json:"ism_template,omitempty"          yaml:"ism_template,omitempty"`
	Lifecycle          map[string]any           `json:"lifecycle,omitempty"             yaml:"lifecycle,omitempty"`
	Hidden             bool                     `json:"hidden,omitempty"                yaml:"hidden,omitempty"`
	System             bool                     `json:"system,omitempty"                yaml:"system,omitempty"`
	Replicated         bool                     `json:"replicated,omitempty"            yaml:"replicated,omitempty"`
	AllowCustomRouting bool                     `json:"allow_custom_routing,omitempty"  yaml:"allow_custom_routing,omitempty"`
	WriteIndex         string                   `json:"write_index,omitempty"           yaml:"write_index,omitempty"`
	IndexCount         int                      `json:"index_count,omitempty"           yaml:"index_count,omitempty"`
	BackingIndexOldest string                   `json:"backing_index_oldest,omitempty"  yaml:"backing_index_oldest,omitempty"`
	BackingIndexNewest string                   `json:"backing_index_newest,omitempty"  yaml:"backing_index_newest,omitempty"`
	Indices            []DataStreamBackingIndex `json:"indices,omitempty"               yaml:"indices,omitempty"`

	// StoreBytes and DocsCount aggregate the per-backing-index store
	// size and doc count after the business layer enriches the stream
	// with /_cat/indices. They stay zero when enrichment is skipped
	// or fails — the cluster's /_data_stream envelope alone does not
	// carry these numbers.
	StoreBytes int64 `json:"store_bytes,omitempty"  yaml:"store_bytes,omitempty"`
	DocsCount  int64 `json:"docs_count,omitempty"   yaml:"docs_count,omitempty"`
}

// DataStreamFilter narrows a DataStreamInspector.DataStreams call. Names
// is forwarded to the cluster as a comma-joined path segment (both
// dialects honour patterns there). Empty Names returns every
// configured data stream — both ES and OS surface hidden streams by
// default on the bare endpoint; the business layer filters them out
// client-side using each DataStream.Hidden flag when the operator
// did not pass --hidden, so the capability stays a thin wire wrapper.
type DataStreamFilter struct {
	Names []string
}

// DataStreamStatsRequest narrows a DataStreamInspector.DataStreamStats
// call. Names is forwarded to the cluster as a comma-joined path
// segment on GET /_data_stream/<names>/_stats (both dialects honour
// patterns there). Empty Names returns aggregate stats for every
// configured stream.
type DataStreamStatsRequest struct {
	Names []string
}

// DataStreamStatsReport is the neutral view of
// GET /_data_stream[/<names>]/_stats. The endpoint is the fast path
// for "how big are my streams" — the cluster returns aggregate counts
// and per-stream rows without expanding the full backing-index list
// or joining /_cat/indices.
//
// TotalStoreBytes is the sum across every backing index of every
// matched stream; BackingIndicesCount is the parallel index-cardinality
// number. DataStreamCount is the number of streams the response
// covered. Both products report the same shape, so one type covers ES
// and OS.
type DataStreamStatsReport struct {
	DataStreamCount     int               `json:"data_stream_count"     yaml:"data_stream_count"`
	BackingIndicesCount int               `json:"backing_indices_count" yaml:"backing_indices_count"`
	TotalStoreBytes     int64             `json:"total_store_bytes"     yaml:"total_store_bytes"`
	DataStreams         []DataStreamStats `json:"data_streams"          yaml:"data_streams"`
}

// DataStreamStats is one per-stream row inside DataStreamStatsReport.
// MaximumTimestampMillis is the epoch-millis of the newest @timestamp
// the cluster could resolve across the stream's backing indices —
// zero when the cluster has no docs yet, or did not report a value.
// StoreBytes and BackingIndicesCount mirror the per-stream aggregates
// the endpoint returns.
type DataStreamStats struct {
	DataStream             string `json:"data_stream"                      yaml:"data_stream"`
	BackingIndicesCount    int    `json:"backing_indices_count"            yaml:"backing_indices_count"`
	StoreBytes             int64  `json:"store_bytes"                      yaml:"store_bytes"`
	MaximumTimestampMillis int64  `json:"maximum_timestamp_millis,omitempty" yaml:"maximum_timestamp_millis,omitempty"`
}

// DataStreamRolloverRequest is the input to a DataStreamManager.RolloverDataStream
// call. Name is the literal data stream name (no patterns — rollover
// targets one stream). Conditions reuses the index-rollover shape;
// both ES and OS accept the same {conditions:{...}} body on
// /<stream>/_rollover. DryRun maps to ?dry_run=true (cluster-native on
// ES; OS supports it from 2.x).
type DataStreamRolloverRequest struct {
	Name                string
	Conditions          IndexRolloverConditions
	DryRun              bool
	WaitForActiveShards string
}

// DataStreamRolloverResult is the normalised view of a data stream
// rollover. Mirrors IndexRolloverResult so renderers can reuse the
// same shape; "alias" is replaced by "data_stream" in JSON/YAML
// output to keep captured records self-describing.
type DataStreamRolloverResult struct {
	DataStream         string          `json:"data_stream"          yaml:"data_stream"`
	OldIndex           string          `json:"old_index,omitempty"  yaml:"old_index,omitempty"`
	NewIndex           string          `json:"new_index,omitempty"  yaml:"new_index,omitempty"`
	RolledOver         bool            `json:"rolled_over"          yaml:"rolled_over"`
	DryRun             bool            `json:"dry_run"              yaml:"dry_run"`
	Acknowledged       bool            `json:"acknowledged"         yaml:"acknowledged"`
	ShardsAcknowledged bool            `json:"shards_acknowledged"  yaml:"shards_acknowledged"`
	ConditionsMet      map[string]bool `json:"conditions,omitempty" yaml:"conditions,omitempty"`
}

// DataStreamModifyAction names a single _data_stream/_modify action.
// Values match the wire field names that wrap the per-action object
// in the cluster's request body ("add_backing_index",
// "remove_backing_index").
type DataStreamModifyAction string

const (
	DataStreamModifyAdd    DataStreamModifyAction = "add_backing_index"
	DataStreamModifyRemove DataStreamModifyAction = "remove_backing_index"
)

// DataStreamModifyRequest is the input to a DataStreamManager.ModifyDataStream
// call. POSTs to /_data_stream/_modify with a {"actions": [...]} body
// of add_backing_index / remove_backing_index entries. Modify is
// advanced usage — primarily during migrations to splice an existing
// index into a stream — so the request shape preserves the order the
// operator submitted, and the response is a simple acknowledged flag
// from the cluster.
type DataStreamModifyRequest struct {
	Actions []DataStreamModifyEntry
	DryRun  bool
}

// DataStreamModifyEntry is one action in a DataStreamModifyRequest.
// Action is the verb; DataStream is the stream name; Index is the
// backing index name on the cluster.
type DataStreamModifyEntry struct {
	Action     DataStreamModifyAction `json:"action"      yaml:"action"`
	DataStream string                 `json:"data_stream" yaml:"data_stream"`
	Index      string                 `json:"index"       yaml:"index"`
}

// DataStreamModifyResult is the normalised view of a
// POST /_data_stream/_modify response. The cluster returns a single
// acknowledged flag; Actions echoes what the operator submitted so a
// captured JSON record is self-describing.
type DataStreamModifyResult struct {
	Action       string                  `json:"action"             yaml:"action"`
	DryRun       bool                    `json:"dry_run"            yaml:"dry_run"`
	Acknowledged bool                    `json:"acknowledged"       yaml:"acknowledged"`
	Actions      []DataStreamModifyEntry `json:"actions,omitempty"  yaml:"actions,omitempty"`
}

// DataStreamDeleteRequest is the input to a
// DataStreamManager.DeleteDataStream call. Names is the resolved
// literal-name list (already partitioned by the business layer's
// pre-flight, with patterns expanded); the adapter joins them for the
// URL path. Deleting a data stream removes the stream object **and**
// all of its backing indices, so the business layer enforces a
// blast-radius cap and the CLI requires --allow-data-loss.
type DataStreamDeleteRequest struct {
	Names []string
}

// DataStreamDeleteResult is the normalised view of a
// DELETE /_data_stream/<names> response. Both dialects return only an
// acknowledged flag; Names echoes the resolved targets so captured
// records are self-describing.
type DataStreamDeleteResult struct {
	Action       string   `json:"action"            yaml:"action"`
	DryRun       bool     `json:"dry_run"           yaml:"dry_run"`
	Names        []string `json:"names,omitempty"   yaml:"names,omitempty"`
	Acknowledged bool     `json:"acknowledged"      yaml:"acknowledged"`
}

// Transform is the neutral view of a pivot/aggregation transform as
// reported by GET /_transform[/<id>] (Elasticsearch) and
// GET /_plugins/_transform[/<id>] (OpenSearch).
//
// The two products share the same operator-facing concepts (a job that
// reads from one or more source indices, writes to a destination index,
// runs as a one-shot batch or continuously) but ship genuinely
// different wire shapes for the body. The neutral fields below cover
// what a `transform list` row needs (id, source pattern, dest,
// continuous flag); Definition carries the raw cluster body so
// `transform get` can pretty-print the per-dialect detail without a
// lossy translation.
type Transform struct {
	Name string `json:"name"                 yaml:"name"`

	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Source is the comma-joined source-index pattern the cluster
	// reports. Both dialects accept multiple sources; the adapter
	// flattens the list for display.
	Source string `json:"source,omitempty"      yaml:"source,omitempty"`

	// Dest is the destination index name.
	Dest string `json:"dest,omitempty"        yaml:"dest,omitempty"`

	// Continuous is true when the transform is configured to re-run on
	// a schedule (ES: presence of `sync`, OS: `continuous: true`).
	// Operators care about this because stopping a continuous transform
	// halts ongoing materialisation.
	Continuous bool `json:"continuous,omitempty"  yaml:"continuous,omitempty"`

	// Version is the cluster's reported product version stamped on the
	// transform at create time. ES surfaces it; OS does not.
	Version string `json:"version,omitempty"     yaml:"version,omitempty"`

	// CreatedAtMillis is the create-time epoch millisecond the cluster
	// recorded. ES surfaces it on /_transform; OS does not — OS only
	// reports UpdatedAtMillis. Empty when the cluster did not report
	// one.
	CreatedAtMillis int64 `json:"created_at_millis,omitempty" yaml:"created_at_millis,omitempty"`

	// UpdatedAtMillis is the last-modified epoch millisecond the cluster
	// recorded. OS surfaces it on every GET; ES does not. The renderer
	// prefers whichever the cluster reports — both products report
	// *some* "when this transform last changed" timestamp, just under
	// different names.
	UpdatedAtMillis int64 `json:"updated_at_millis,omitempty" yaml:"updated_at_millis,omitempty"`

	// Definition is the raw cluster body — preserved verbatim because
	// the source/dest/pivot/sync shapes are dialect-specific and
	// evolving. The renderer pretty-prints this; structured consumers
	// (--output json/yaml) get the shape the cluster returned.
	Definition map[string]any `json:"definition,omitempty"  yaml:"definition,omitempty"`

	// State is the runtime state the cluster reports on /_stats or
	// /_explain. Populated by the list view's stats join and stays
	// zero when no stats were fetched (e.g. `transform get`, which
	// answers from a single GET /_transform/<id>). Same pattern
	// DataStream uses to surface enrichment results.
	State string `json:"state,omitempty"               yaml:"state,omitempty"`

	// DocsProcessed is the cluster's running total of source documents
	// processed by the transform. Joined from /_stats on the list view.
	DocsProcessed int64 `json:"docs_processed,omitempty" yaml:"docs_processed,omitempty"`

	// Failures is the sum of index + search failures the cluster
	// reports, surfaced in the list table's wide mode so an operator
	// scanning a fleet spots a misbehaving transform without paging
	// into per-id stats.
	Failures int64 `json:"failures,omitempty"            yaml:"failures,omitempty"`

	// CheckpointLastTimeMillis is the wall-clock time the most recent
	// checkpoint completed. ES reports it on `checkpoint.last`; OS
	// reports it on `metadata.last_updated_at`. Joined from /_stats on
	// the list view.
	CheckpointLastTimeMillis int64 `json:"checkpoint_last_time_millis,omitempty" yaml:"checkpoint_last_time_millis,omitempty"`

	// CheckpointAgeMillis is derived (now - CheckpointLastTimeMillis)
	// in the business layer so renderers don't have to do arithmetic
	// against time.Now.
	CheckpointAgeMillis int64 `json:"checkpoint_age_millis,omitempty"     yaml:"checkpoint_age_millis,omitempty"`
}

// TransformStats normalises the stats overlap between
// GET /_transform/<id>/_stats (Elasticsearch) and
// GET /_plugins/_transform/<id>/_explain (OpenSearch). The two
// products report different envelopes — ES nests counters under
// `stats`, OS exposes them as a flat `metadata` block — so the
// adapter is responsible for flattening to this neutral shape.
//
// State strings come straight from the cluster — both dialects emit
// values like "started", "stopped", "indexing", "aborting", "failed".
// The renderer treats them as opaque.
//
// CheckpointLastTimeMillis is the wall-clock time the most recent
// checkpoint completed (ES: `checkpoint.last.timestamp_millis`, OS:
// `metadata.last_updated_at`). CheckpointAgeMillis is derived by the
// business layer (now - last) so the renderer doesn't have to do
// arithmetic against `time.Now`. Both stay zero when the cluster has
// not reported a completed checkpoint yet.
type TransformStats struct {
	Name string `json:"name"                          yaml:"name"`

	State string `json:"state,omitempty"               yaml:"state,omitempty"`

	Continuous bool `json:"continuous,omitempty"          yaml:"continuous,omitempty"`

	DocsProcessed  int64 `json:"docs_processed,omitempty"   yaml:"docs_processed,omitempty"`
	DocsIndexed    int64 `json:"docs_indexed,omitempty"     yaml:"docs_indexed,omitempty"`
	PagesProcessed int64 `json:"pages_processed,omitempty"  yaml:"pages_processed,omitempty"`
	TriggerCount   int64 `json:"trigger_count,omitempty"    yaml:"trigger_count,omitempty"`

	SearchTimeMillis int64 `json:"search_time_millis,omitempty" yaml:"search_time_millis,omitempty"`
	IndexTimeMillis  int64 `json:"index_time_millis,omitempty"  yaml:"index_time_millis,omitempty"`

	IndexFailures  int64 `json:"index_failures,omitempty"    yaml:"index_failures,omitempty"`
	SearchFailures int64 `json:"search_failures,omitempty"   yaml:"search_failures,omitempty"`

	// CheckpointLastTimeMillis is the wall-clock time the most recent
	// checkpoint finished. ES reports it on `checkpoint.last`; OS
	// reports it on `metadata.last_updated_at`.
	CheckpointLastTimeMillis int64 `json:"checkpoint_last_time_millis,omitempty" yaml:"checkpoint_last_time_millis,omitempty"`

	// CheckpointAgeMillis is derived (now - CheckpointLastTimeMillis)
	// in the business layer, so renderers can show "checkpoint age"
	// without doing arithmetic against time.Now themselves.
	CheckpointAgeMillis int64 `json:"checkpoint_age_millis,omitempty"      yaml:"checkpoint_age_millis,omitempty"`

	// Reason carries the cluster's failure message when State is
	// "failed". Empty otherwise.
	Reason string `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// TransformApplyRequest is the input to a TransformManager.ApplyTransform
// call. Body is the raw transform definition the operator submitted
// (file or stdin); the adapter forwards it verbatim and lets the
// cluster validate the shape. Update=true switches the adapter from
// PUT-create to the cluster's update path when one exists.
type TransformApplyRequest struct {
	Name   string
	Body   []byte
	Update bool
}

// TransformApplyResult is the normalised view of an apply response.
// Created is true when the transform did not previously exist;
// Updated is true when the apply overwrote an existing definition;
// Unchanged is true when the submitted body matched the existing
// definition byte-for-byte and the adapter short-circuited the call.
type TransformApplyResult struct {
	Action       string `json:"action"                  yaml:"action"`
	Name         string `json:"name"                    yaml:"name"`
	Created      bool   `json:"created,omitempty"       yaml:"created,omitempty"`
	Updated      bool   `json:"updated,omitempty"       yaml:"updated,omitempty"`
	Unchanged    bool   `json:"unchanged,omitempty"     yaml:"unchanged,omitempty"`
	DryRun       bool   `json:"dry_run,omitempty"       yaml:"dry_run,omitempty"`
	Acknowledged bool   `json:"acknowledged,omitempty"  yaml:"acknowledged,omitempty"`
}

// TransformDeleteRequest is the input to a TransformManager.DeleteTransform
// call. Force=true tells the adapter to stop the transform first when
// the cluster otherwise refuses to delete a running one.
type TransformDeleteRequest struct {
	Name      string
	ForceStop bool
}

// TransformDeleteResult is the normalised view of a delete response.
// Stopped reports whether the adapter had to stop the transform first
// to satisfy a --force-stop flow.
type TransformDeleteResult struct {
	Action       string `json:"action"                 yaml:"action"`
	Name         string `json:"name"                   yaml:"name"`
	Stopped      bool   `json:"stopped,omitempty"      yaml:"stopped,omitempty"`
	DryRun       bool   `json:"dry_run,omitempty"      yaml:"dry_run,omitempty"`
	Acknowledged bool   `json:"acknowledged,omitempty" yaml:"acknowledged,omitempty"`
}

// TransformStartRequest is the input to a TransformManager.StartTransform
// call. Single literal name; no other knobs.
type TransformStartRequest struct {
	Name string
}

// TransformStartResult is the normalised view of a start response.
type TransformStartResult struct {
	Action       string `json:"action"                 yaml:"action"`
	Name         string `json:"name"                   yaml:"name"`
	DryRun       bool   `json:"dry_run,omitempty"      yaml:"dry_run,omitempty"`
	Acknowledged bool   `json:"acknowledged,omitempty" yaml:"acknowledged,omitempty"`
}

// TransformStopRequest is the input to a TransformManager.StopTransform
// call. WaitForCheckpoint asks the cluster to let the in-flight
// checkpoint finish before halting, so the destination index isn't
// left mid-batch — critical for continuous transforms.
type TransformStopRequest struct {
	Name              string
	WaitForCheckpoint bool
}

// TransformStopResult is the normalised view of a stop response.
type TransformStopResult struct {
	Action            string `json:"action"                          yaml:"action"`
	Name              string `json:"name"                            yaml:"name"`
	WaitForCheckpoint bool   `json:"wait_for_checkpoint,omitempty"   yaml:"wait_for_checkpoint,omitempty"`
	DryRun            bool   `json:"dry_run,omitempty"               yaml:"dry_run,omitempty"`
	Acknowledged      bool   `json:"acknowledged,omitempty"          yaml:"acknowledged,omitempty"`
}

// TransformPreviewRequest is the input to a TransformInspector.PreviewTransform
// call. Body is the raw transform definition the operator submitted
// (file or stdin); the adapter forwards it to the cluster's preview
// endpoint and returns a bounded sample of materialised rows.
type TransformPreviewRequest struct {
	Body []byte
}

// TransformPreviewResult is the normalised view of a preview response.
// Documents is the bounded sample of materialised rows the cluster
// returned; Mappings is the dest-index mapping the cluster would
// generate. Both products surface the same conceptual shape; we
// preserve the cluster's payload as map[string]any so the renderer
// can pretty-print it without a typed schema.
type TransformPreviewResult struct {
	Documents []map[string]any `json:"documents,omitempty" yaml:"documents,omitempty"`
	Mappings  map[string]any   `json:"mappings,omitempty"  yaml:"mappings,omitempty"`
}

// IndexDescription is the aggregated single-index view returned by
// `esops index describe`. The shape composes existing neutral types
// (Index, IndexSettings, IndexMapping, Alias, Shard, IndexRecovery,
// IndexSegments, ILMExplainEntry, ISMExplainEntry, Task) rather than
// inventing new ones — describe is pure aggregation over the
// read-only inspector capabilities, not a new endpoint.
//
// Sections that the cluster does not (or cannot) report are simply
// absent: ILM is always nil on OpenSearch, ISM always nil on
// Elasticsearch, Recovery only populated when the index has shards
// still recovering, Aliases empty when nothing points at the index.
// SectionWarnings carries per-section collector failures (e.g.
// /_segments returning a 4xx on a corner-case index state) so the
// JSON/YAML form stays faithful — a working section never emits an
// entry there.
//
// ResolvedFrom is set when the operator named an alias on the CLI
// and the aggregator resolved it to a concrete index name. Keeps the
// captured JSON self-describing without relying on the operator to
// remember which alias they typed.
//
// Dialect is echoed so a structured consumer (e.g. esops-doctor or a
// future commercial layer) can tell "ILM empty because OpenSearch"
// apart from "ILM empty because no policy attached" without
// re-probing.
type IndexDescription struct {
	Name         string `json:"name"                    yaml:"name"`
	ResolvedFrom string `json:"resolved_from,omitempty" yaml:"resolved_from,omitempty"`
	Dialect      string `json:"dialect,omitempty"       yaml:"dialect,omitempty"`

	Info     Index         `json:"info"               yaml:"info"`
	Settings IndexSettings `json:"settings"           yaml:"settings"`
	Mappings *IndexMapping `json:"mappings,omitempty" yaml:"mappings,omitempty"`

	Aliases  []Alias        `json:"aliases,omitempty"  yaml:"aliases,omitempty"`
	Shards   []Shard        `json:"shards,omitempty"   yaml:"shards,omitempty"`
	Recovery *IndexRecovery `json:"recovery,omitempty" yaml:"recovery,omitempty"`
	Segments *IndexSegments `json:"segments,omitempty" yaml:"segments,omitempty"`

	ILM *ILMExplainEntry `json:"ilm,omitempty" yaml:"ilm,omitempty"`
	ISM *ISMExplainEntry `json:"ism,omitempty" yaml:"ism,omitempty"`

	Tasks []Task `json:"tasks,omitempty" yaml:"tasks,omitempty"`

	// MatchedTemplate is the highest-priority composable index
	// template whose `index_patterns` glob the index name. Best-effort
	// — the cluster does not record which template was applied at
	// creation, so this reports the template the cluster *would* pick
	// for a new index of this name today. A template added after the
	// index will show up here even though it did not shape the index;
	// surfaced loudly enough for an operator to spot the divergence.
	MatchedTemplate *IndexTemplate `json:"matched_template,omitempty" yaml:"matched_template,omitempty"`

	SectionWarnings map[string]string `json:"section_warnings,omitempty" yaml:"section_warnings,omitempty"`
}

// NodeDescription is the aggregated single-node view returned by
// `esops ops node describe`. Composes existing neutral types (Node,
// NodeStats, NodeBootstrap, NodeSettings, Shard) plus a small
// drain/voting summary so a downstream consumer can answer "what is
// this node doing right now, and is anything preventing it from
// hosting shards" without re-querying.
//
// Fields absent from the cluster's response are nil/zero rather than
// fabricated. SectionWarnings carries any per-collector failures —
// partial-but-loud rather than aborting the whole view.
type NodeDescription struct {
	Name    string `json:"name"              yaml:"name"`
	Dialect string `json:"dialect,omitempty" yaml:"dialect,omitempty"`

	Info      Node           `json:"info"                yaml:"info"`
	Stats     *NodeStats     `json:"stats,omitempty"     yaml:"stats,omitempty"`
	Bootstrap *NodeBootstrap `json:"bootstrap,omitempty" yaml:"bootstrap,omitempty"`
	Settings  *NodeSettings  `json:"settings,omitempty"  yaml:"settings,omitempty"`

	// Drain summarises whether the node is currently excluded from
	// shard allocation (persistent or transient) and whether it sits
	// in the cluster's voting-config exclusion list. Both signals
	// matter at incident time: a "missing" data node is often a node
	// the operator drained yesterday and forgot to uncordon.
	Drain NodeDrainStatus `json:"drain" yaml:"drain"`

	// Shards is the list of shard copies currently allocated to this
	// node, sourced from /_cat/shards filtered by the node name.
	// ShardSummary aggregates primary/replica counts and total store
	// bytes for the kubectl-style header line.
	Shards       []Shard          `json:"shards,omitempty"  yaml:"shards,omitempty"`
	ShardSummary NodeShardSummary `json:"shard_summary"     yaml:"shard_summary"`

	SectionWarnings map[string]string `json:"section_warnings,omitempty" yaml:"section_warnings,omitempty"`
}

// NodeDrainStatus carries the operator-facing drain state of one node.
// AllocationExcluded is true when the node's name appears in
// cluster.routing.allocation.exclude._name (persistent or transient).
// VotingExcluded is true when the node's name is in the cluster's
// voting_config_exclusions list. Scopes is a short slice naming where
// the exclusion was set ("persistent", "transient", or both) — useful
// for an operator deciding whether `ops uncordon` will clear it.
type NodeDrainStatus struct {
	AllocationExcluded bool     `json:"allocation_excluded"           yaml:"allocation_excluded"`
	VotingExcluded     bool     `json:"voting_excluded"               yaml:"voting_excluded"`
	Scopes             []string `json:"scopes,omitempty"              yaml:"scopes,omitempty"`
}

// NodeShardSummary is the per-node shard aggregate that drives the
// describe header. Primaries and Replicas count shard copies (not
// distinct shard IDs); StoreBytes sums the on-disk size across both.
// A node holding zero shards reports zeros, not absent.
type NodeShardSummary struct {
	Primaries  int   `json:"primaries"   yaml:"primaries"`
	Replicas   int   `json:"replicas"    yaml:"replicas"`
	StoreBytes int64 `json:"store_bytes" yaml:"store_bytes"`
}

// DataStreamDescription is the aggregated single-data-stream view
// returned by `esops datastream describe`. Composes the cluster's
// DataStream envelope (already enriched with backing-index health
// and size by the business layer's Get helper) with the matched
// composable template, any in-flight recovery on the backing indices,
// and tasks touching the stream.
//
// Stream is the load-bearing payload; sub-sections are populated
// when the cluster has something to report. SectionWarnings captures
// non-fatal collector failures.
type DataStreamDescription struct {
	Name    string `json:"name"              yaml:"name"`
	Dialect string `json:"dialect,omitempty" yaml:"dialect,omitempty"`

	Stream DataStream `json:"stream" yaml:"stream"`

	MatchedTemplate *IndexTemplate  `json:"matched_template,omitempty" yaml:"matched_template,omitempty"`
	Recoveries      []IndexRecovery `json:"recoveries,omitempty"       yaml:"recoveries,omitempty"`
	Tasks           []Task          `json:"tasks,omitempty"            yaml:"tasks,omitempty"`

	SectionWarnings map[string]string `json:"section_warnings,omitempty" yaml:"section_warnings,omitempty"`
}

// ILMPolicyDescription is the aggregated view of a single ILM policy
// returned by `esops index ilm describe`. The policy body itself comes
// from /_ilm/policy/<name>; ManagedIndices is the per-index explain
// view (phase, action, step, age) for every index the policy is
// currently attached to, which is what an operator wants when
// triaging "is anything stuck on this policy".
//
// ErrorCount and ManagedCount are summary counters surfaced in the
// header so the renderer doesn't have to walk ManagedIndices to
// answer "how many indices are managed, and are any of them stuck".
type ILMPolicyDescription struct {
	Name    string `json:"name"              yaml:"name"`
	Dialect string `json:"dialect,omitempty" yaml:"dialect,omitempty"`

	Policy ILMPolicy `json:"policy" yaml:"policy"`

	ManagedIndices []ILMExplainEntry `json:"managed_indices,omitempty" yaml:"managed_indices,omitempty"`
	ManagedCount   int               `json:"managed_count"             yaml:"managed_count"`
	ErrorCount     int               `json:"error_count"               yaml:"error_count"`

	SectionWarnings map[string]string `json:"section_warnings,omitempty" yaml:"section_warnings,omitempty"`
}

// ISMPolicyDescription is the OpenSearch sibling of
// ILMPolicyDescription. Mirrors the shape so a single-source-of-truth
// renderer can switch on a top-level key, while keeping the
// dialect-specific policy bodies (ISM has states/transitions; ILM has
// phases/actions) as opaque maps the renderer treats as a JSON dump.
//
// ISM has no `in_use_by` analogue, so ManagedIndices is sourced from
// /_plugins/_ism/explain rather than a reverse-reference structure
// embedded in the policy.
type ISMPolicyDescription struct {
	ID      string `json:"id"                yaml:"id"`
	Dialect string `json:"dialect,omitempty" yaml:"dialect,omitempty"`

	Policy ISMPolicy `json:"policy" yaml:"policy"`

	ManagedIndices []ISMExplainEntry `json:"managed_indices,omitempty" yaml:"managed_indices,omitempty"`
	ManagedCount   int               `json:"managed_count"             yaml:"managed_count"`
	ErrorCount     int               `json:"error_count"               yaml:"error_count"`

	SectionWarnings map[string]string `json:"section_warnings,omitempty" yaml:"section_warnings,omitempty"`
}

// DisplayName returns the operator-facing identifier of an ILM
// policy description. ILM keys policies by `name`; the helper exists
// purely so renderers and downstream consumers (e.g. esops-doctor)
// can stay dialect-agnostic alongside ISMPolicyDescription.DisplayName,
// whose underlying field is `id`. The asymmetry is inherited from
// the upstream cluster APIs, not invented here.
func (d ILMPolicyDescription) DisplayName() string { return d.Name }

// DisplayName returns the operator-facing identifier of an ISM
// policy description. The OpenSearch plugin keys policies by `id`
// rather than `name`; see ILMPolicyDescription.DisplayName for the
// motivation behind the helper pair.
func (d ISMPolicyDescription) DisplayName() string { return d.ID }

// SnapshotDescription is the aggregated single-snapshot view returned
// by `esops snapshot describe REPO/NAME`. Composes the Snapshot
// record (state, indices, duration, shard totals) with the live
// SnapshotStatus block (per-shard init/started/done/failed). Status
// is a pointer so a captured JSON shape can distinguish "the cluster
// didn't have a status to report" (nil) from "status is all-zero
// because the snapshot is brand-new" (non-nil, all-zero counters).
type SnapshotDescription struct {
	Repository string `json:"repository"        yaml:"repository"`
	Name       string `json:"name"              yaml:"name"`
	Dialect    string `json:"dialect,omitempty" yaml:"dialect,omitempty"`

	Info   Snapshot        `json:"info"             yaml:"info"`
	Status *SnapshotStatus `json:"status,omitempty" yaml:"status,omitempty"`

	SectionWarnings map[string]string `json:"section_warnings,omitempty" yaml:"section_warnings,omitempty"`
}

// SnapshotPolicyDescription is the aggregated single-policy view
// returned by `esops snapshot policy describe`. Composes the
// SnapshotPolicy record (schedule, next/last run, definition) with
// recent snapshots produced by that policy. SnapshotInspector
// reports no back-reference to the policy that created a snapshot,
// so RecentSnapshots is sourced by listing the policy's target
// repository and trimming to the most recent N — best-effort and
// renderer-honest about that.
type SnapshotPolicyDescription struct {
	Name    string `json:"name"              yaml:"name"`
	Dialect string `json:"dialect,omitempty" yaml:"dialect,omitempty"`

	Policy SnapshotPolicy `json:"policy" yaml:"policy"`

	RecentSnapshots []Snapshot `json:"recent_snapshots,omitempty" yaml:"recent_snapshots,omitempty"`
	RecentLimit     int        `json:"recent_limit,omitempty"     yaml:"recent_limit,omitempty"`

	SectionWarnings map[string]string `json:"section_warnings,omitempty" yaml:"section_warnings,omitempty"`
}

// SnapshotRepositoryDescription is the aggregated single-repo view
// returned by `esops snapshot repository describe`. Composes the
// SnapshotRepository config with the cluster's verify result (which
// nodes can talk to the backing store), a summary count of stored
// snapshots, and the SLM/SM policies that target the repo.
//
// Verify is a pointer so callers can tell "verify was not run" from
// "verify succeeded with an empty node list" (the latter is a real
// state on a fresh repo nobody has snapshotted against yet).
type SnapshotRepositoryDescription struct {
	Name    string `json:"name"              yaml:"name"`
	Dialect string `json:"dialect,omitempty" yaml:"dialect,omitempty"`

	Info     SnapshotRepository        `json:"info"             yaml:"info"`
	Verify   *SnapshotVerifyResult     `json:"verify,omitempty" yaml:"verify,omitempty"`
	Summary  SnapshotRepositorySummary `json:"summary"         yaml:"summary"`
	Policies []SnapshotPolicy          `json:"policies,omitempty" yaml:"policies,omitempty"`

	SectionWarnings map[string]string `json:"section_warnings,omitempty" yaml:"section_warnings,omitempty"`
}

// SnapshotRepositorySummary is the per-repo aggregate the describe
// header carries: how many snapshots are stored, how many succeeded,
// how many are still in progress / failed. The cluster does not
// publish a single summary endpoint — this is computed client-side
// from the snapshot listing for the repo.
//
// SuccessCount + FailedCount + InProgressCount + OtherCount is
// guaranteed to equal SnapshotCount — every snapshot lands in
// exactly one bucket. OtherCount catches states the explicit
// buckets don't (ABORTED, MISSING, future cluster-introduced
// states) so the math an operator does on the header line always
// adds up.
type SnapshotRepositorySummary struct {
	SnapshotCount   int `json:"snapshot_count"    yaml:"snapshot_count"`
	SuccessCount    int `json:"success_count"     yaml:"success_count"`
	FailedCount     int `json:"failed_count"      yaml:"failed_count"`
	InProgressCount int `json:"in_progress_count" yaml:"in_progress_count"`
	OtherCount      int `json:"other_count"       yaml:"other_count"`
}

// TransformDescription is the aggregated single-transform view
// returned by `esops transform describe`. Composes the configured
// definition (Transform.Definition: source/dest/pivot/sync) with
// runtime Stats (state, doc counts, checkpoint age, failure reason)
// so an operator triaging a stalled transform sees both halves in
// one screen.
//
// Stats is a pointer so the captured JSON distinguishes "stats fetch
// failed or was skipped" (nil — see SectionWarnings) from "stats
// returned an all-zero record" (non-nil) — the latter is a real
// transient state on a freshly-created transform that hasn't ticked
// yet.
type TransformDescription struct {
	Name    string `json:"name"              yaml:"name"`
	Dialect string `json:"dialect,omitempty" yaml:"dialect,omitempty"`

	Info  Transform       `json:"info"             yaml:"info"`
	Stats *TransformStats `json:"stats,omitempty"  yaml:"stats,omitempty"`

	SectionWarnings map[string]string `json:"section_warnings,omitempty" yaml:"section_warnings,omitempty"`
}
