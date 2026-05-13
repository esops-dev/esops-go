package client

import (
	"context"

	"github.com/esops-dev/esops-go/pkg/types"
)

// HealthInspector exposes read-only cluster-health operations.
type HealthInspector interface {
	Health(ctx context.Context) (types.ClusterHealth, error)
}

// ShardInspector exposes read-only shard-allocation operations. Kept
// separate from HealthInspector so a future capability (e.g. a cached
// allocation summariser) can implement just this one.
type ShardInspector interface {
	Shards(ctx context.Context, filter types.ShardFilter) ([]types.Shard, error)
}

// NodeInspector exposes /_cat/nodes — the cluster-side source of truth
// for node names and roles. Used by ops shards analyze to identify the
// real set of data nodes for skew analysis: a "shard count per node"
// derived from /_cat/shards alone misses freshly-added or drained data
// nodes (zero shards = invisible) which is the most interesting case
// to surface.
type NodeInspector interface {
	Nodes(ctx context.Context) ([]types.Node, error)
}

// NodeStatsInspector exposes the JVM/OS introspection surface that
// /_cat/nodes leaves out: configured -Xms/-Xmx (jvm.mem.heap_init_in_bytes,
// jvm.mem.heap_max_in_bytes) and the host's kernel-reported total
// physical memory (os.mem.total_in_bytes). NodeInspector reports current
// heap usage and disk via /_cat/nodes, which is the right shape for
// `ops nodes` but the wrong shape for "is the configured heap floor
// safe given how much RAM the host has" — that question needs the
// init/max pair plus OS memory, none of which /_cat/nodes carries.
//
// Implementations call /_nodes/jvm (info — heap_init/heap_max are
// configuration, not stats) and /_nodes/stats/os (memory totals — the
// info endpoint's os block omits them on both ES and OS) and merge by
// node. Kept separate from NodeInspector so consumers that only want
// the cat-nodes columns aren't paying the larger payload cost; the
// info+stats responses include cgroup, GC, thread-pool, and pool
// breakdowns that the doctor's heap_size rule does not need.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx/5xx with the cluster's body in the
//     message
type NodeStatsInspector interface {
	NodeStats(ctx context.Context) ([]types.NodeStats, error)
}

// IndexInspector exposes read-only index-listing operations backed by
// /_cat/indices. The filter's Index field is forwarded to the cluster;
// Health/Status/Hidden/Sort are applied in the business layer so the
// same filter produces the same view on ES and OS.
type IndexInspector interface {
	Indices(ctx context.Context, filter types.IndexFilter) ([]types.Index, error)
	// IndicesNamesStatus is the slim companion: just (name, status)
	// pairs from /_cat/indices?h=index,status. The state-change and
	// optimize pre-flights only need the open/closed split — pulling
	// the full 12-column row to throw 10 of them away is wasted
	// network on busy clusters.
	IndicesNamesStatus(ctx context.Context, filter types.IndexFilter) ([]types.IndexNameStatus, error)
	// IndicesStream decodes /_cat/indices one row at a time and invokes
	// yield for each parsed Index. yield returning a non-nil error
	// stops iteration and surfaces the error from the call. Used by
	// `index list --output ndjson` so a 20k-index cluster doesn't have
	// to be buffered into one slice before the first row is printed.
	//
	// Implementations must apply the same server-side filtering as
	// Indices (filter.Index, filter.Hidden via expand_wildcards) so the
	// streaming and slice paths produce identical row sets.
	IndicesStream(ctx context.Context, filter types.IndexFilter, yield func(types.Index) error) error
}

// IndexStateChanger exposes /{index}/_open and /{index}/_close. Both
// endpoints share the same request shape (a comma-separated index
// pattern plus a small set of query params) and the same response
// envelope (acknowledged + shards_acknowledged) on both dialects, so
// one capability covers the pair.
//
// The cluster has no dry_run query flag for these endpoints; the
// business layer implements --dry-run client-side by resolving the
// pattern via IndexInspector and reporting the would-be transition
// without firing the call. Implementations therefore only need to
// execute the live transition.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message (the typical case is a missing index when
//     IgnoreUnavailable is false)
type IndexStateChanger interface {
	OpenIndex(ctx context.Context, req types.IndexStateChangeRequest) (types.IndexStateChange, error)
	CloseIndex(ctx context.Context, req types.IndexStateChangeRequest) (types.IndexStateChange, error)
}

// IndexOptimizer wraps /{index}/_forcemerge — the segment-merge
// endpoint both dialects expose with identical query params and
// response shape. The operator-facing verb is "optimize" because
// "force_merge" is the cluster's spelling of an operation that the
// real-world ops vocabulary calls "compact this index". Same name on
// both dialects keeps the CLI surface neutral.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message (typical case: a missing index when IgnoreUnavailable
//     is false, or a 400 from an invalid max_num_segments).
type IndexOptimizer interface {
	ForceMerge(ctx context.Context, req types.IndexOptimizeRequest) (types.IndexOptimizeResult, error)
}

// IndexRollover wraps /<alias>/_rollover — the data-stream/alias
// rollover endpoint. The cluster evaluates the submitted conditions
// against the alias's current write index and either creates the next
// generation (rolling the alias forward) or refuses with the per-
// condition outcome.
//
// The endpoint shape is identical on both dialects (POST a JSON body
// of {conditions:{…}}, plus dry_run/wait_for_active_shards query
// params and an optional new-index path segment), so one capability
// covers ES and OS.
//
// Errors mirror IndexOptimizer; a 404 on the alias means the operator
// pointed at something that doesn't exist, surfaced verbatim so the
// table cell lines up with what they typed.
type IndexRollover interface {
	Rollover(ctx context.Context, req types.IndexRolloverRequest) (types.IndexRolloverResult, error)
}

// IndexShrinker wraps /<source>/_shrink/<target> — the cluster
// endpoint that hard-links a source index's segments into a new index
// with fewer primary shards. The shape (POST a JSON body of
// {settings, aliases}, plus copy_settings/wait_for_active_shards query
// flags) is identical on ES and OS, so one capability covers both.
//
// Implementations execute the live shrink only; the cluster has no
// native dry-run for this endpoint and the business layer simulates
// one client-side ("validate + report"). They must surface the
// cluster's own pre-flight refusals verbatim — the source-not-read-
// only error and shard-co-location errors are common operator-facing
// cases and rephrasing them would lose signal.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message (typical case: source not blocked-for-writes, or
//     primaries spread across multiple nodes)
type IndexShrinker interface {
	Shrink(ctx context.Context, req types.IndexShrinkRequest) (types.IndexShrinkResult, error)
}

// IndexSettingsInspector wraps GET /<indices>/_settings — the read-only
// settings surface. Both dialects expose the same response envelope
// ({"<index>":{"settings":{…}}}) so one capability covers ES and OS.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message (typical case: a missing index when IgnoreUnavailable is
//     false)
type IndexSettingsInspector interface {
	GetSettings(ctx context.Context, req types.IndexSettingsGetRequest) ([]types.IndexSettings, error)
	// GetSettingsStream streams /<indices>/_settings — one IndexSettings
	// per cluster-keyed entry — and yields each. Lets `settings get
	// --include-defaults -o ndjson` stream on a busy cluster where the
	// full nested-defaults payload is multiple MB.
	GetSettingsStream(ctx context.Context, req types.IndexSettingsGetRequest, yield func(types.IndexSettings) error) error
}

// IndexSettingsUpdater wraps PUT /<indices>/_settings — the destructive
// half of the settings surface. The cluster's body shape
// ({"index":{"<key>":<value>, ...}}) is identical on both dialects.
//
// The cluster has no native dry-run for /_settings; the business layer
// implements --dry-run client-side as "validate + report" so the
// adapter only executes live calls.
//
// Errors mirror IndexSettingsInspector; a 400 from the cluster usually
// carries a precise reason (immutable setting, unknown key) which is
// surfaced verbatim.
type IndexSettingsUpdater interface {
	PutSettings(ctx context.Context, req types.IndexSettingsUpdateRequest) (types.IndexSettingsUpdateResult, error)
}

// IndexTemplateInspector wraps GET /_index_template/<names> — the
// read-only listing/get surface for v2 (composable) index templates.
// Both ES and OS expose the same response envelope
// ({"index_templates":[{"name":"…","index_template":{…}}, ...]}). An
// empty Names list lists every template.
//
// Legacy /_template entries are deliberately out of scope; both
// products consider them deprecated and surfacing both sets would
// muddy the CLI story.
type IndexTemplateInspector interface {
	GetTemplates(ctx context.Context, names []string) ([]types.IndexTemplate, error)
	// GetTemplate returns one template by literal name, or a wrapped
	// error including a not-found case the caller can match on. Avoids
	// the GetTemplates([]string{name}) → linear-scan-1-element pattern
	// the business layer used to do.
	GetTemplate(ctx context.Context, name string) (types.IndexTemplate, error)
}

// IndexTemplateUpdater wraps PUT /_index_template/<name> and
// DELETE /_index_template/<name> — the destructive surface. Templates
// are updated one at a time; bulk operations are out of scope and the
// cluster's raw API is the right place for them.
//
// Neither endpoint has a native dry-run; --dry-run is implemented
// client-side as "validate + report" so the adapter only executes live
// calls. PutTemplate honours request.Create (which maps to
// ?create=true) — a 400 from the cluster on Create=true means the
// template already exists, surfaced verbatim.
type IndexTemplateUpdater interface {
	PutTemplate(ctx context.Context, req types.IndexTemplatePutRequest) (types.IndexTemplatePutResult, error)
	DeleteTemplate(ctx context.Context, req types.IndexTemplateDeleteRequest) (types.IndexTemplateDeleteResult, error)
}

// AliasInspector wraps /_cat/aliases — the read-only listing surface
// for alias→index bindings. ES and OS both expose the same column
// layout; the AliasFilter's Alias/Index fields are forwarded to the
// cluster as path segments. Behaviour is identical across dialects.
type AliasInspector interface {
	Aliases(ctx context.Context, filter types.AliasFilter) ([]types.Alias, error)
	// AliasesStream decodes /_cat/aliases?format=json one row at a
	// time and invokes yield for each parsed Alias. Honours the same
	// streaming contract as IndicesStream — a 5k-alias cluster emits
	// the first row before the cluster has finished sending.
	AliasesStream(ctx context.Context, filter types.AliasFilter, yield func(types.Alias) error) error
}

// AliasUpdater wraps POST /_aliases — the bulk add/remove endpoint.
// Both ES and OS apply the actions as a single cluster-state update
// (atomic from the cluster's perspective), so callers get
// "everything happened or nothing did" semantics for free.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message (typical case: a missing index named in an action, or
//     a malformed filter expression)
type AliasUpdater interface {
	UpdateAliases(ctx context.Context, req types.AliasUpdateRequest) (types.AliasUpdateResult, error)
}

// AllocationExplainer exposes /_cluster/allocation/explain. An empty
// request.Index lets the cluster pick any unassigned shard it finds —
// matching the raw endpoint's default behaviour — so on-call operators
// can drop straight into "why is anything unassigned" without having to
// name a specific shard first.
type AllocationExplainer interface {
	AllocationExplain(ctx context.Context, req types.AllocationExplainRequest) (types.AllocationExplanation, error)
}

// SecurityAuditor exposes the user/role surface of the cluster's
// security model. The implementation is dialect-aware: ES reads the
// native-realm endpoints under /_security/*; OS reads the security
// plugin's admin API under /_plugins/_security/api/*. When the cluster
// has no security in play at all (ES with xpack.security.enabled=false,
// OS without the plugin), implementations return Status.Enabled=false
// with an empty Users/Roles slice rather than an error — "no security"
// is a valid audit outcome that operators want surfaced, not hidden
// behind a failed command.
type SecurityAuditor interface {
	Audit(ctx context.Context) (types.SecurityReport, error)
}

// HotThreadsInspector wraps /_nodes/hot_threads — the JVM introspection
// endpoint both dialects expose in the same text format. Response bodies
// are not JSON and the schema is an implementation detail of the
// cluster's code; the capability keeps it as an opaque Text blob so the
// upstream format can change without touching esops.
type HotThreadsInspector interface {
	HotThreads(ctx context.Context, req types.HotThreadsRequest) (types.HotThreads, error)
}

// RerouteExecutor wraps /_cluster/reroute — the cluster-state mutation
// behind `ops rebalance`. The endpoint shape is identical on both
// dialects (POST a body of {commands: [...]}, plus dry_run/explain/
// retry_failed query flags) so one capability covers ES and OS.
//
// Implementations must surface the cluster's own acknowledged flag and
// any explanations[] block verbatim — operators rely on the cluster's
// per-decider prose to triage a refused move; rephrasing it would lose
// signal. Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message (typical case: a bad command shape that the cluster
//     rejects with a 400 + reason)
type RerouteExecutor interface {
	Reroute(ctx context.Context, req types.RerouteRequest) (types.RerouteResult, error)
}

// ClusterDiagnoser performs raw HTTP GETs against cluster endpoints for
// the `ops diag` bundle. Purposely minimal — a generic raw-HTTP escape
// hatch is a power tool we only want the diag collector using, so the
// rest of the codebase keeps talking through dialect-aware capabilities.
//
// Errors:
//   - transport failures (DNS, TCP, TLS): wrapped ErrUnreachable
//   - HTTP 401: ErrAuth
//   - HTTP 403: ErrForbidden
//   - any other non-2xx: returned as (body, status, nil) so the caller
//     can record the body in the bundle. A missing-endpoint 404 on a
//     dialect-specific path (ILM on OS, ISM on ES) is expected data,
//     not a bundle-breaking error.
type ClusterDiagnoser interface {
	DoGET(ctx context.Context, path string) (body []byte, status int, err error)
}

// ILMManager wraps the Elasticsearch ILM admin surface — list/get/put/
// delete policies (/_ilm/policy[/<name>]) and per-index explain
// (/<index>/_ilm/explain). ILM is ES-only; the OpenSearch adapter
// returns ErrUnsupported on every method so the CLI can surface the
// dialect mismatch loudly rather than silently no-op.
//
// The capability is not unified with ISMManager (the OpenSearch
// equivalent) on purpose: the policy body shapes are fundamentally
// different, and a neutral "lifecycle" interface would force a lossy
// translation in both directions — exactly the lowest-common-denominator
// trap the project's design rules warn against.
//
// Errors mirror IndexTemplateUpdater: ErrUnreachable on transport
// failure, ErrAuth/ErrForbidden on 401/403, ErrUnsupported when the
// adapter is the OS one, and a wrapped error on 4xx with the cluster's
// body in the message (typical case: a 4xx on Delete when the policy is
// still attached to a managed index).
type ILMManager interface {
	ListPolicies(ctx context.Context, names []string) ([]types.ILMPolicy, error)
	GetPolicy(ctx context.Context, name string) (types.ILMPolicy, error)
	PutPolicy(ctx context.Context, req types.ILMPolicyPutRequest) (types.ILMPolicyPutResult, error)
	DeletePolicy(ctx context.Context, req types.ILMPolicyDeleteRequest) (types.ILMPolicyDeleteResult, error)
	ExplainPolicy(ctx context.Context, req types.ILMExplainRequest) ([]types.ILMExplainEntry, error)
	// ExplainPolicyStream streams /<index>/_ilm/explain — one entry per
	// managed index — and yields each. Cluster-wide explain on a
	// multi-thousand-index fleet is the canonical case where buffering
	// the whole response is wasted memory.
	ExplainPolicyStream(ctx context.Context, req types.ILMExplainRequest, yield func(types.ILMExplainEntry) error) error
}

// ISMManager wraps the OpenSearch ISM plugin admin surface —
// list/get/put/delete policies (/_plugins/_ism/policies[/<id>]) and
// explain (/_plugins/_ism/explain[/<index>]). ISM is OpenSearch-only;
// the Elasticsearch adapter returns ErrUnsupported on every method.
//
// See ILMManager for the rationale behind keeping ILM and ISM as two
// separate capabilities rather than a unified "lifecycle" interface.
type ISMManager interface {
	ListPolicies(ctx context.Context, ids []string) ([]types.ISMPolicy, error)
	GetPolicy(ctx context.Context, id string) (types.ISMPolicy, error)
	PutPolicy(ctx context.Context, req types.ISMPolicyPutRequest) (types.ISMPolicyPutResult, error)
	DeletePolicy(ctx context.Context, req types.ISMPolicyDeleteRequest) (types.ISMPolicyDeleteResult, error)
	ExplainPolicy(ctx context.Context, req types.ISMExplainRequest) ([]types.ISMExplainEntry, error)
	// ExplainPolicyStream is the streaming companion. Same rationale as
	// the ILM equivalent — `--show-policy` on a multi-thousand-index
	// fleet can run into tens of MB.
	ExplainPolicyStream(ctx context.Context, req types.ISMExplainRequest, yield func(types.ISMExplainEntry) error) error
}

// SnapshotInspector exposes the read-only repository/snapshot listing
// surface — GET /_snapshot[/<repos>] for repositories and
// GET /_snapshot/<repo>/<snapshot> for snapshots themselves. The
// envelope shapes are identical across dialects (per-repo `{type,
// settings}` for repositories; a `snapshots: [...]` array of objects
// with `snapshot/state/indices/start_time_in_millis/...` for the
// snapshot listing), so one capability covers ES and OS.
//
// The cluster has no native multi-repo snapshot endpoint that returns
// stable cross-product semantics: ES 8+ accepts a comma-separated repo
// list on /_snapshot/<repos>/<pattern> while OS 1.x rejects it. The
// adapter resolves an empty/wildcard Repository client-side by listing
// repositories first and fanning out per repo — the same fan-out a
// hand-rolled curl loop would do, just behind a single capability call.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message (typical case: a 404 on a missing repo, or a 500 from a
//     repository whose backing storage is unreachable)
type SnapshotInspector interface {
	Repositories(ctx context.Context, filter types.SnapshotRepositoryFilter) ([]types.SnapshotRepository, error)
	Snapshots(ctx context.Context, filter types.SnapshotFilter) ([]types.Snapshot, error)
	// SnapshotStatus reads /_snapshot/<repo>/<name>/_status and returns
	// the cluster's live progress view. Distinct from Snapshots()
	// because /_status carries shard-level progress (started, init,
	// done, failed) that the regular GET endpoint omits — exactly what
	// a polling loop wants. Used by the wait/resume machinery on
	// `snapshot create` so SIGINT and progress reporting share the same
	// data path.
	SnapshotStatus(ctx context.Context, req types.SnapshotStatusRequest) (types.SnapshotStatus, error)
	// SnapshotsStream fans out across the resolved repository set and
	// invokes yield for each parsed snapshot as it arrives. yield
	// returning a non-nil error stops iteration and surfaces the error
	// from the call. Honours the same streaming contract as
	// IndicesStream — `snapshot list --output ndjson` against a fleet
	// with thousands of snapshots emits the first row before the cluster
	// has finished sending.
	//
	// Per-repo errors (storage offline, auth denied on one repo) are
	// reported through repoErr; iteration continues for every other
	// resolved repository. The caller decides whether a per-repo error
	// is fatal — partial-but-loud is the right default for a 20-repo
	// fleet where one S3 bucket is having an outage.
	SnapshotsStream(ctx context.Context, filter types.SnapshotFilter, yield func(types.Snapshot) error, repoErr func(repo string, err error)) error
}

// SnapshotCreator wraps PUT /_snapshot/<repo>/<snapshot> — the destructive
// half of the snapshot surface. The body shape (`indices`,
// `include_global_state`, `partial`, `ignore_unavailable`, `metadata`)
// is identical on both dialects, so one capability covers ES and OS.
//
// The cluster has no native dry-run for snapshot creation; the business
// layer implements --dry-run client-side as "validate + report what
// would be sent". Implementations therefore only execute live calls.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx/5xx with the cluster's body in the
//     message (typical case: a 404 when the repo doesn't exist, or a
//     400 when an existing snapshot name is reused)
type SnapshotCreator interface {
	CreateSnapshot(ctx context.Context, req types.SnapshotCreateRequest) (types.SnapshotCreateResult, error)
}

// SnapshotVerifier wraps POST /_snapshot/<repo>/_verify — the repository
// connectivity probe both dialects expose with the same shape (no body,
// returns `{nodes:{<id>:{name:...}}}`). Read-only from the operator's
// perspective: the cluster talks to the repository's backing store but
// no snapshot data is written.
//
// AnalyzeRepository wraps POST /_snapshot/<repo>/_analyze — the deeper
// integrity check ES 7.4+ and OS 2.x expose. Writes a small set of test
// blobs, reads them back from every node, then deletes them; surfaces
// the cluster's per-blob report verbatim. Still read-from-the-operator's-
// view in the sense that no user data is touched, but it does write
// scratch blobs to the repository — labelled as a probe in the docs and
// `--deep` in the CLI to mark the difference.
type SnapshotVerifier interface {
	VerifyRepository(ctx context.Context, req types.SnapshotVerifyRequest) (types.SnapshotVerifyResult, error)
	AnalyzeRepository(ctx context.Context, req types.SnapshotAnalyzeRequest) (types.SnapshotAnalyzeResult, error)
}

// RecoveryInspector wraps GET /<indices>/_recovery — the cluster's
// per-shard recovery progress endpoint. Both ES and OS expose the same
// envelope (`{"<index>":{"shards":[...]}}`) and the per-shard fields
// (`type`, `stage`, `primary`, `source`, `index.size.{...}`,
// `index.files.{...}`) line up; one capability covers both.
//
// Used by `snapshot restore --wait` to surface per-shard progress
// during a long restore. `_recovery` is also the right answer for
// `index shrink` and routing-driven recoveries; we keep the capability
// general so a future `index recovery` command lands without churning
// the interface.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message (typical case: a missing index when the operator narrowed
//     to a literal name that no longer exists)
type RecoveryInspector interface {
	Recovery(ctx context.Context, req types.RecoveryRequest) (types.RecoveryReport, error)
}

// SegmentsInspector wraps GET /<indices>/_segments — the per-shard
// segment census the cluster maintains for force-merge planning and
// deleted-doc accounting. Both ES and OS expose the same envelope
// (`indices.<name>.shards.<shardN>[].segments.<id>`) and the per-segment
// fields (`size_in_bytes`, `num_docs`, `deleted_docs`, `committed`,
// `search`) line up; one capability covers both dialects.
//
// The capability returns an aggregated, per-index view rather than the
// raw three-level nesting: an operator deciding whether to force-merge
// `logs-2024-12` reaches for "how many segments does it have, and what
// fraction of its docs are deleted" — both of which are sums over the
// per-segment records. Adapters keep responsibility for parsing the
// cluster's nested envelope, business code stays out of it.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message (typical case: a missing index when the operator
//     narrowed to a literal name that no longer exists)
type SegmentsInspector interface {
	Segments(ctx context.Context, req types.SegmentsRequest) (types.SegmentsReport, error)
}

// SnapshotRepositoryManager wraps PUT/DELETE /_snapshot/<repo> — the
// register/deregister surface for snapshot repositories. The body shape
// (`type`, `settings`, `verify`) is identical on both dialects, so one
// capability covers ES and OS.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx/5xx with the cluster's body in the
//     message (typical case: a 500 from the repository plugin when the
//     backing store is unreachable, or a 400 on a malformed type)
type SnapshotRepositoryManager interface {
	RegisterRepository(ctx context.Context, req types.SnapshotRepositoryRegisterRequest) (types.SnapshotRepositoryRegisterResult, error)
	DeregisterRepository(ctx context.Context, req types.SnapshotRepositoryDeregisterRequest) (types.SnapshotRepositoryDeregisterResult, error)
}

// SnapshotRestorer wraps POST /_snapshot/<repo>/<snap>/_restore — the
// destructive surface that materialises a snapshot's indices back into the
// cluster. The body shape (`indices`, `include_global_state`, `partial`,
// `ignore_unavailable`, `rename_pattern`, `rename_replacement`,
// `index_settings`, `ignore_index_settings`) is identical on both dialects,
// so one capability covers ES and OS.
//
// The cluster has no native dry-run for restore; the business layer
// implements --dry-run client-side as "validate + report what would be sent".
// Implementations therefore only execute live calls.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx/5xx with the cluster's body in the
//     message (typical case: a 500 when the target index already exists
//     and would be clobbered, or a 404 on a missing repo/snapshot)
type SnapshotRestorer interface {
	RestoreSnapshot(ctx context.Context, req types.SnapshotRestoreRequest) (types.SnapshotRestoreResult, error)
}

// SnapshotPruner wraps DELETE /_snapshot/<repo>/<snap[,snap…]> — the
// bulk-delete surface behind `snapshot prune`. The endpoint shape is identical
// on both dialects (the cluster acks with `{acknowledged:true}`); the prune
// retention math runs client-side in internal/snapshot, so the capability is
// the small mechanical bit at the bottom.
//
// Implementations forward the literal snapshot list to the cluster as a
// comma-separated path segment. Patterns are not accepted at this layer —
// `snapshot prune` resolves them up-front so a captured `--dry-run` listing
// is the same set the live run would delete.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx/5xx with the cluster's body in the
//     message (typical case: a 404 when a named snapshot has already been
//     deleted, or a 500 when the repository's backing storage is offline)
type SnapshotPruner interface {
	DeleteSnapshots(ctx context.Context, req types.SnapshotDeleteRequest) (types.SnapshotDeleteResult, error)
}

// SnapshotPolicyInspector exposes the read-only policy listing surface
// for the cluster's scheduled-snapshot system: ES SLM
// (/_slm/policy[/<name>]) and OS Snapshot Management
// (/_plugins/_sm/policies[/<name>]). Split from SnapshotPolicyManager
// so read-only consumers (esops-doctor's "is anything actually backing
// this cluster up?" rule) can depend on the read surface alone without
// pulling in a type whose method set lets them mutate.
//
// The two products surface fundamentally different policy bodies — ES
// SLM's flat schedule + retention vs OS SM's creation/deletion split —
// so the neutral SnapshotPolicy view focuses on the metadata that
// always comes back and carries Definition verbatim. Renderers diff
// the two shapes loudly (`-o json`/`-o yaml`) instead of forcing a
// lossy translation.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - ErrNotFound on a missing-policy 404
//   - generic wrapped error on other 4xx with the cluster's body in
//     the message (typical case: a 404 from `/_plugins/_sm/policies`
//     on an OpenSearch cluster where the SM plugin has not been
//     installed — surfaced verbatim so the operator sees the gap)
type SnapshotPolicyInspector interface {
	ListSnapshotPolicies(ctx context.Context, names []string) ([]types.SnapshotPolicy, error)
	GetSnapshotPolicy(ctx context.Context, name string) (types.SnapshotPolicy, error)
}

// SnapshotPolicyManager wraps the create/update/delete/execute mutations
// on the same scheduled-snapshot surface. ES backs the methods with
// /_slm/policy/<name> and /_slm/policy/<name>/_execute; OS backs them
// with /_plugins/_sm/policies/<name> and /_plugins/_sm/policies/<name>/_start.
//
// The read half lives on SnapshotPolicyInspector and is embedded here
// so the manager IS-A inspector — esops calls a single capability for
// `snapshot policy <verb>`, while esops-doctor depends on the inspector
// alone.
//
// ExecuteSnapshotPolicy semantics differ between dialects:
//   - ES SLM `_execute` synthesises a one-shot snapshot named after the
//     policy template and returns that name. Useful for ad-hoc
//     "back this up right now" without changing the schedule.
//   - OS SM `_start` toggles the policy's enabled flag on; the plugin
//     does not return a snapshot name (the snapshot lands on the next
//     scheduled cron tick). Adapters surface what the plugin returns
//     verbatim and esops keeps the divergence visible in the result.
//
// Errors mirror SnapshotPolicyInspector. Both adapters surface the
// cluster's reason on a 4xx so a "policy already running" or
// "repository missing" rejection lands intact.
type SnapshotPolicyManager interface {
	SnapshotPolicyInspector
	PutSnapshotPolicy(ctx context.Context, req types.SnapshotPolicyPutRequest) (types.SnapshotPolicyPutResult, error)
	DeleteSnapshotPolicy(ctx context.Context, req types.SnapshotPolicyDeleteRequest) (types.SnapshotPolicyDeleteResult, error)
	ExecuteSnapshotPolicy(ctx context.Context, req types.SnapshotPolicyExecuteRequest) (types.SnapshotPolicyExecuteResult, error)
}

// ClusterSettingsInspector wraps GET /_cluster/settings — the read-only
// half of the cluster-level settings surface. Split out from
// ClusterSettingsManager so read-only consumers (esops-doctor's hygiene
// rules detecting cluster-settings drift) can depend on the read
// surface alone, without pulling in a type whose name carries
// PutAllocationExclude.
//
// GetClusterSettings reads the persistent and transient exclude._name
// lists; a missing key returns an empty slice rather than an error
// (the cluster did not exclude any node — that is a valid state, not a
// failure). Defaults are not surfaced: the exclude._name default is the
// empty list and a default-only response is indistinguishable from
// "nothing set", which is exactly what the empty-slice convention models.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message
type ClusterSettingsInspector interface {
	GetClusterSettings(ctx context.Context) (types.ClusterSettingsView, error)
}

// ClusterSettingsManager wraps GET/PUT /_cluster/settings — the
// cluster-level settings surface behind `ops drain` and `ops uncordon`.
// The capability is intentionally narrow: `ops drain` and `ops uncordon`
// only need to read and modify `cluster.routing.allocation.exclude._name`,
// so the surface here is a typed view over that single setting rather
// than the full nested-vs-flat dance every other key would require.
//
// The read half lives on ClusterSettingsInspector and is embedded here
// so the manager IS-A inspector for callers that need both, while
// read-only consumers can take a dependency on the inspector alone.
//
// PutAllocationExclude installs the requested name list. An empty
// Names slice clears the setting by sending JSON null on the wire —
// the cluster removes the key rather than leaving an empty-string value
// in /_cluster/settings (which it would honour as "exclude no nodes"
// but keep visible on every subsequent GET).
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message (typical case: a 400 from the cluster when the setting
//     name is misspelled — surfaced verbatim so the operator sees the
//     cluster's own reason)
type ClusterSettingsManager interface {
	ClusterSettingsInspector
	PutAllocationExclude(ctx context.Context, req types.ClusterAllocationExcludeUpdate) (types.ClusterSettingsUpdateResult, error)
}

// VotingExclusionManager wraps /_cluster/voting_config_exclusions —
// the cluster's "safely retire a master-eligible node" surface. The
// cluster API has three operations and one notable shape constraint:
//
//   - POST /_cluster/voting_config_exclusions?node_names=a,b appends
//     to the exclusion list. Idempotent on re-add.
//   - DELETE /_cluster/voting_config_exclusions clears the entire
//     list. There is no "remove one entry" endpoint; the only choice
//     is keep-all or wipe-all, by design (the cluster expects the
//     list to be transient cleanup state).
//   - GET (via /_cluster/state/metadata/voting_config_exclusions)
//     reads the list back as {node_id, node_name} pairs.
//
// The capability surfaces all three. `ops drain --also-exclude-voting`
// uses Add; `ops uncordon --clear-voting` uses Clear; `ops drain
// --list` uses Get.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx/5xx with the cluster's body in the
//     message (typical case: a 503 when the master can't reach quorum
//     to commit the coordination metadata change)
type VotingExclusionManager interface {
	AddVotingExclusions(ctx context.Context, names []string) (types.VotingExclusionsResult, error)
	ClearVotingExclusions(ctx context.Context) (types.VotingExclusionsResult, error)
	GetVotingExclusions(ctx context.Context) ([]string, error)
}

// TasksInspector wraps GET /_tasks — the read-only listing surface for
// in-flight transport tasks (reindex, force-merge, search, etc.). The
// adapter normalises the cluster's `nodes.<id>.tasks.<task-id>{...}`
// envelope into one []Task slice keyed by the cluster's composite
// "<node_id>:<task_number>" task IDs.
//
// Filtering is server-side (the cluster does the actions/nodes/
// parent-task narrowing); Detailed flips on the cluster's
// ?detailed=true so the per-task Description field is populated.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the message
type TasksInspector interface {
	Tasks(ctx context.Context, filter types.TasksFilter) ([]types.Task, error)
	// TasksStream walks the cluster's response and yields each task as
	// it is decoded. yield returning a non-nil error stops iteration
	// and surfaces the error from the call. Used by `ops tasks list
	// --output ndjson` so a fleet running thousands of concurrent
	// tasks doesn't have to buffer the full nested envelope before
	// printing the first row.
	//
	// Implementations apply the same server-side filter as Tasks. Sort
	// order is *not* normalised — the streaming path leaves rows in
	// the cluster's iteration order; consumers that want a stable
	// listing use Tasks (which buffers and sorts).
	TasksStream(ctx context.Context, filter types.TasksFilter, yield func(types.Task) error) error
}

// TasksCanceller wraps POST /_tasks/<task_id>/_cancel and the bulk
// /_tasks/_cancel?actions=… endpoint behind a single capability. The
// adapter forwards either a literal TaskID or the filter set the
// business layer assembled — the cluster's own refusal-to-cancel
// (force_merge, snapshot, anything not flagged cancellable) surfaces
// verbatim because the operator needs the cluster's own reason, not
// our paraphrase.
//
// Errors mirror TasksInspector.
type TasksCanceller interface {
	CancelTasks(ctx context.Context, req types.TasksCancelRequest) (types.TasksCancelResult, error)
}

// PendingTasksInspector wraps GET /_cluster/pending_tasks — the master
// queue of cluster-state changes that have been accepted but not yet
// applied. ClusterHealth.NumberOfPendingTasks gives the count; this
// capability returns the per-task breakdown (priority, source,
// time-in-queue, executing-vs-queued) a hygiene rule needs to tell a
// transient burst from a stuck master.
//
// Endpoint shape is identical on ES and OS (`tasks: [...]` array of
// objects with insert_order/priority/source/time_in_queue_millis/
// executing), so one capability covers both dialects.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message
type PendingTasksInspector interface {
	PendingTasks(ctx context.Context) ([]types.PendingTask, error)
}

// DeprecationInspector wraps GET /_migration/deprecations — the
// pre-upgrade lint that reports cluster, node, index, ML, data-stream,
// and template settings that will not survive the next major version.
// ES-only: OpenSearch has no equivalent endpoint, so the OS adapter
// returns ErrUnsupported on every method, mirroring the ILM/ISM split.
//
// The cluster groups issues by category (cluster_settings,
// node_settings, index_settings keyed by index name, ml_settings,
// data_streams, templates, ilm_policies); the adapter flattens those
// into a single []DeprecationIssue tagged with Category and Target so
// a rule consumer can scan for "every critical issue cluster-wide"
// without switching on category first.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - ErrUnsupported when the adapter is the OS one
//   - generic wrapped error on 4xx with the cluster's body in the
//     message (typical case: a 404 on /_migration/deprecations against
//     an ES build that omits the migration plugin)
type DeprecationInspector interface {
	Deprecations(ctx context.Context, req types.DeprecationsRequest) ([]types.DeprecationIssue, error)
}

// DocumentInspector wraps GET /<index>/_search — restricted to the
// "list every document, optionally filtered by ID" shape so the
// capability stays a read-only window onto operator-managed metadata
// indices (esops-doctor waivers, annotations, config) rather than a
// general-purpose search client. The endpoint envelope
// (`{"hits":{"hits":[{_id, _source, _version, _seq_no}, ...]}}`) is
// identical on ES and OS, so one capability covers both dialects.
//
// Read-only by construction: there is no DocumentWriter or
// DocumentDeleter sibling on this interface, and we do not plan to
// add one. If a write surface ever lands it goes on a separate
// capability so a downstream "no mutating capability imports" guard
// (esops-doctor's CI) stays a single grep.
//
// Documents returns the matching set in deterministic id-sorted order
// so a captured slice is stable across runs. DocumentsStream walks
// the cluster's hits[] one record at a time and yields each — for
// the typical operator-managed index this barely matters, but the
// streaming contract mirrors IndicesStream/AliasesStream so a
// thousand-waiver index does not have to be buffered.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - ErrUnsupported when the cluster surface ever stops exposing
//     /_search at the shape we use (so doctor's translation to
//     ErrProbeNotApplicable keeps working unchanged)
//   - generic wrapped error on 4xx/5xx with the cluster's body in
//     the message (typical case: a 404 when the requested index
//     does not exist)
type DocumentInspector interface {
	Documents(ctx context.Context, req types.DocumentListRequest) ([]types.Document, error)
	DocumentsStream(ctx context.Context, req types.DocumentListRequest, yield func(types.Document) error) error
}

// MappingsInspector wraps GET /<indices>/_mapping — the read-only
// mapping surface. Both ES and OS expose the same envelope
// (`{"<index>":{"mappings":{"properties":{...},"_meta":{...},"dynamic":"..."}}}`)
// so one capability covers both dialects.
//
// Used by `migrate plan` to inspect source mappings before a reindex.
// The capability is intentionally read-only: there is no MappingsUpdater
// sibling and we do not plan to add one — mapping rewrites belong on the
// target index's settings/template path, not as an in-place mutation.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message (typical case: a 404 when a literal index name does not
//     exist and IgnoreUnavailable is false)
type MappingsInspector interface {
	GetMappings(ctx context.Context, filter types.MappingFilter) ([]types.IndexMapping, error)
}

// DocCounter wraps GET /<index>/_count — the cluster's authoritative
// per-index doc count. /_cat/indices' docs.count column lags refreshes
// by tens of seconds (it's read off the local node's segment cache, not
// recomputed per call), and `migrate verify` / `migrate cutover` run in
// the exact window where that lag matters. Both ES and OS expose the
// same envelope ({count, _shards: {...}}) so one capability covers both.
//
// Implementations forward request.Indices as the path's <index> segment
// (comma-joined) and IgnoreUnavailable as ?ignore_unavailable=true so a
// missing source index in a multi-pair verify lands as a 0/0 row rather
// than a hard 404.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - ErrNotFound on a 404 with a literal index name and
//     IgnoreUnavailable=false
//   - generic wrapped error on other 4xx/5xx with the cluster's body in
//     the message
type DocCounter interface {
	Count(ctx context.Context, req types.CountRequest) (types.CountResult, error)
}

// Reindexer wraps POST /_reindex?wait_for_completion=false — the
// task-based reindex submission. Lives on the *target* cluster: the
// target reads from the source via `source.remote`, so the capability
// holds on the connection that does the writing. The cluster's response
// is a task ID; polling /_tasks/<id> is delegated to TaskInspector
// (the existing capability covers it) and to the business layer's
// reindex poller, which folds the task envelope into the typed
// ReindexTaskStatus.
//
// SubmitReindex returns a ReindexSubmit with the cluster-side TaskID;
// it does not block on completion — that would force a single
// long-running request to hold a connection open for hours. The
// capability is small: submission and per-task polling. CancelReindex
// is intentionally NOT a method here — the existing TasksCanceller
// surface covers /_tasks/<id>/_cancel and the reindex action is one
// of the actions that capability already cancels.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx/5xx with the cluster's body in the
//     message (typical case: a 400 from the cluster when the source
//     URL is not in reindex.remote.whitelist — the same condition
//     migrate check reports as a blocker)
type Reindexer interface {
	SubmitReindex(ctx context.Context, req types.ReindexSubmitRequest) (types.ReindexSubmit, error)
	// ReindexTaskStatus reads /_tasks/<task_id> and returns the
	// reindex-shaped status block. The cluster reports the same
	// envelope on ES and OS; the adapter walks `task.status` plus the
	// outer `completed` / `cancellable` / `cancelled` flags.
	ReindexTaskStatus(ctx context.Context, taskID string) (types.ReindexTaskStatus, error)
}

// RemoteReindexInspector reads the cluster's `reindex.remote.whitelist`
// configuration via GET /_cluster/settings?include_defaults=true. Both
// ES and OS use the same setting name — without it, a /_reindex call
// using `source.remote` is rejected by the cluster regardless of
// network reachability or credentials. `migrate check` uses it to
// surface the pre-flight that catches the most common "the reindex
// just won't start" foot-gun.
//
// The capability is split out from ClusterSettingsInspector — whose
// scope is documented as the drain/uncordon path's narrow view of
// allocation excludes — so a future hygiene rule that only needs the
// whitelist doesn't have to depend on the broader settings surface.
//
// Errors mirror ClusterSettingsInspector.
type RemoteReindexInspector interface {
	RemoteReindexWhitelist(ctx context.Context) (types.RemoteReindexWhitelist, error)
}

// NodeBootstrapInspector wraps the per-node bootstrap-check posture the
// cluster reports through its /_nodes endpoints — the JVM mlockall
// state (jvm.mem.using_memory_lock), the process-level
// max_file_descriptors, and the OS-side vm.max_map_count when the
// cluster surfaces it. /_cat/nodes carries none of these, and the
// existing NodeStats wraps configured heap + host RAM rather than
// kernel/process limits, so a fresh capability is the cleanest fit.
//
// Used by esops-doctor's bootstrap_memory_lock and bootstrap-check
// parity rules (file descriptors, mlockall, max_map_count). Adapters
// hit /_nodes/process,jvm in one round trip and merge by node ID —
// both endpoints share the same envelope shape on ES and OS, so one
// capability covers both dialects.
//
// The same /_nodes/process,jvm body also carries `jvm.gc_collectors[]`
// and `jvm.input_arguments[]`, which the doctor catalog's
// jvm_gc_ergonomics rule consumes. Surfacing them on
// types.NodeBootstrap rather than introducing a second inspector keeps
// the on-the-wire cost identical (the data is in the response body
// already) and keeps the capability footprint small.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message
type NodeBootstrapInspector interface {
	NodeBootstrap(ctx context.Context) ([]types.NodeBootstrap, error)
}

// NodeSettingsInspector wraps GET /_nodes/settings?flat_settings=true
// — the static, operator-set node-level settings that don't surface
// through /_cluster/settings. Sibling of NodeBootstrapInspector
// (which only reads process and JVM blocks); this one carries the
// whole `nodes.*.settings` map so consumers can reach for any
// statically-configured key (network.host, node.store.allow_mmap,
// discovery.*, path.data, action.auto_create_index, …).
//
// flat_settings=true is always on so the returned maps are keyed by
// dotted-path names ("network.host") rather than the nested-object
// form. include_defaults is deliberately omitted — the cluster's
// default tree is large and per-key default lookups are not what
// rules need; if a future caller wants defaults, add a second method
// rather than overloading this one.
//
// Returns one NodeSettings per cluster node. ES and OS expose the
// same envelope shape; a single capability covers both dialects.
//
// Used by esops-doctor's network_host_wildcard,
// node_store_allow_mmap, and suspect_discovery_settings rules — each
// reaches into the per-node Settings map for a different dotted-path
// key. Sharing the underlying /_nodes/settings fetch with the TLS
// inspectors keeps the round-trip cost flat regardless of how many
// rules consume it.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message
type NodeSettingsInspector interface {
	NodeSettings(ctx context.Context) ([]types.NodeSettings, error)
}

// ClusterSettingsFullInspector wraps GET /_cluster/settings without the
// narrow shape ClusterSettingsInspector imposes. Returns the full
// persistent/transient (and optionally defaults) envelopes as flat-key
// maps so a hygiene rule can reach for any key the cluster reports —
// awareness attributes, destructive_requires_name, script policy,
// reindex remote whitelist, etc. — without each one demanding its own
// typed view.
//
// IncludeDefaults flips the cluster's ?include_defaults=true query
// param. Off by default: the defaults tree is large (tens of KB on a
// minimal cluster) and most rules don't need it. Adapters always send
// flat_settings=true so the returned maps are keyed by the cluster's
// dotted-path names ("cluster.routing.allocation.awareness.attributes")
// rather than the nested-object form, which would force every consumer
// to walk into Persistent["cluster"].(map[string]any)["routing"].….
//
// Errors mirror ClusterSettingsInspector.
type ClusterSettingsFullInspector interface {
	GetClusterSettingsFull(ctx context.Context, includeDefaults bool) (types.ClusterSettingsFull, error)
}

// AllocationInspector wraps /_cat/allocation — the per-node shard
// count and disk breakdown the cluster keeps separate from
// /_cat/nodes. Used by esops-doctor's shard_count_per_node rule (the
// ~600-shard-per-node recommendation) and by any future hygiene rule
// that wants the same per-node summary without summing /_cat/shards
// rows in the consumer.
//
// Distinct capability from NodeInspector so the existing Nodes() call
// stays cheap — a /_cat/nodes consumer doesn't pay for a second
// /_cat/allocation request it didn't ask for. Both ES and OS expose
// the same column set; one capability covers both dialects.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message
type AllocationInspector interface {
	AllocationByNode(ctx context.Context) ([]types.NodeAllocation, error)
}

// TransportTLSInspector wraps the cluster-side view of node-to-node
// transport TLS posture. Distinct from the existing TLSPosture surface
// (which mixes client-side connection facts the operator's config
// owns) because what the cluster reports about *its own* transport
// security has to come from the cluster, not from inferring on the
// HTTP-client side.
//
// Sources differ per dialect — ES's /_nodes/settings exposes
// `xpack.security.transport.ssl.enabled` and `verification_mode`; OS
// exposes `plugins.security.ssl.transport.enforce_hostname_verification`
// and the per-node SSL config — so the adapter is the right place to
// normalise the difference. Both endpoints are read-only; this is the
// strict /_nodes/settings GET, not a probe.
//
// Aggregation is all-or-nothing across the reachable node set:
// Enabled is true only when every node reports transport TLS as
// enabled. One misconfigured node in a fleet of ten is the exact
// case esops-doctor's node_to_node_encryption rule has to flag, so
// surfacing a majority vote as "enabled" would defeat the rule's
// purpose.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message
type TransportTLSInspector interface {
	TransportTLS(ctx context.Context) (types.TransportTLS, error)
}

// HTTPTLSInspector wraps the cluster-side view of HTTP-layer TLS
// posture — the layer external clients (esops itself, dashboards,
// application traffic) connect to. Sibling of TransportTLSInspector
// rather than an extension because the two layers fail and pass
// independently: a cluster can encrypt node-to-node while accepting
// plaintext on 9200, or vice versa, so each gets its own rule and its
// own capability.
//
// Sources differ per dialect — ES exposes
// `xpack.security.http.ssl.*` on /_nodes/settings (with
// `client_authentication`, `supported_protocols`,
// `cipher_suites`); OS exposes
// `plugins.security.ssl.http.*` (with `clientauth_mode`,
// `enabled_protocols`, `enabled_ciphers`) — so the adapter is the
// right place to normalise the difference. Both endpoints are
// read-only; this is the strict /_nodes/settings GET, not a probe.
//
// Aggregation is all-or-nothing for Enabled and the homogeneous-or-
// "mixed" rule for ClientAuth; Protocols and CipherSuites are the
// union across nodes (the attacker picks the weakest accepted by any
// node, so the rule evaluates the union, not the intersection). One
// misconfigured node in a fleet of ten is the exact case the
// http-tls rule has to flag, so neither aggregation hides
// disagreement.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message
type HTTPTLSInspector interface {
	HTTPTLS(ctx context.Context) (types.HTTPTLSPosture, error)
}

// ClusterSettingsPutter wraps PUT /_cluster/settings as a general
// key/value writer — the destructive companion to
// ClusterSettingsFullInspector. Distinct from ClusterSettingsManager
// (which is bound to the narrow allocation-exclude path drain/uncordon
// uses) so a downstream tool that only wants to read every cluster
// setting doesn't transitively pull in a write surface; symmetrically,
// `ops settings set` doesn't have to embed the read view it doesn't
// need to operate. Adapter implementations send flat_settings=true so
// dotted keys arrive on the wire as the operator typed them.
//
// The cluster has no native dry-run; --dry-run is implemented client-
// side as "validate + report what would be sent". Implementations only
// execute live calls.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message (typical case: a 400 from the cluster when the setting
//     name is unknown or the value is rejected — surfaced verbatim so
//     the operator sees the cluster's own reason)
type ClusterSettingsPutter interface {
	PutClusterSettings(ctx context.Context, req types.ClusterSettingsPutRequest) (types.ClusterSettingsPutResult, error)
}

// IndexCacheClearer wraps POST /<indices>/_cache/clear — the per-index
// cache flush the cluster exposes for fielddata, query, and request
// caches. Both ES and OS expose the same path and the same response
// envelope ({_shards:{total,successful,failed,failures:[…]}}) so one
// capability covers both dialects.
//
// Implementations forward request.Indices as the path's <index>
// segment (comma-joined). When none of Fielddata/Query/Request is
// true, the cluster's default is to clear every cache; the adapter
// passes the request through unchanged so the cluster's own default
// is what the operator gets. Fielddata-only narrowing via Fields
// requires Fielddata=true on the wire — the adapter forwards Fields
// only in that mode so a future cluster version that tightens the
// validation doesn't break the call.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx/5xx with the cluster's body in the
//     message (typical case: a 404 when a literal index name doesn't
//     exist and IgnoreUnavailable is false)
type IndexCacheClearer interface {
	ClearCache(ctx context.Context, req types.IndexCacheClearRequest) (types.IndexCacheClearResult, error)
}

// AuditLogInspector reports the cluster's audit-logging configuration
// and a recent-events tail — the two reads esops-doctor's
// audit-logging-enabled and recent-audit-log-warnings rules need.
// Split into two methods so the "is audit even on" check (one cheap
// settings read) doesn't pay for the tail's _search round trip when
// the rule short-circuits on a disabled config.
//
// Sources differ per dialect:
//   - ES reads /_cluster/settings filtered to xpack.security.audit.*
//     for the config and the .security_audit_log-* index pattern for
//     the tail.
//   - OS reads /_plugins/_security/audit/config for the config and
//     the audit_log index for the tail.
//
// The tail reader returns only metadata (timestamp, layer, type,
// count) by design — the audit log records request bodies and
// document IDs which the capability deliberately does not surface.
// Consumers that want more detail must read the underlying index
// themselves, where the cluster's existing access controls apply.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - ErrUnsupported when the cluster lacks the underlying surface
//     (ES with xpack.security.audit unavailable; OS without the
//     security plugin's audit config endpoint registered)
//   - generic wrapped error on other 4xx/5xx with the cluster's body
//     in the message
//
// AuditWarnings returns an empty slice (not an error) when the audit
// log index does not exist on the cluster yet — a fresh deployment
// with audit just-enabled hasn't populated it, and that is a valid
// "nothing to report" outcome rather than a failure.
type AuditLogInspector interface {
	AuditConfig(ctx context.Context) (types.AuditConfig, error)
	AuditWarnings(ctx context.Context, req types.AuditWarningsRequest) ([]types.AuditWarning, error)
}

// RealmsInspector reports the cluster's configured authentication
// realms — the dialect-neutral view of "how does this cluster identify
// a request?". Used by the deprecated-realms hygiene rule and any
// future identity-posture audit.
//
// Sources differ per dialect:
//   - ES merges /_xpack/usage?filter_path=security.realms (the
//     authoritative list of loaded realms, including yaml-only ones)
//     with /_cluster/settings filtered to xpack.security.authc.realms.*
//     for any per-realm runtime overrides. Either source alone misses
//     cases — settings alone misses elasticsearch.yml-declared realms,
//     usage alone misses runtime overrides.
//   - OS reads /_plugins/_security/api/securityconfig and walks the
//     dynamic.authc map (authc/authz domains).
//
// Important caveat for OpenSearch consumers: the security plugin gates
// /_plugins/_security/api/securityconfig behind admin TLS certs (the
// plugin's restapi role / admin DN list — see the OS security plugin
// docs). Operators authenticated with basic auth, an API token, or a
// non-admin client cert hit a 403 here and the capability returns
// ErrForbidden. ErrForbidden is not "no realms configured" — consumers
// (notably the deprecated-realms rule) should treat it as
// inconclusive, not as a green-light absence.
//
// Implementations sort the result deterministically by Order, then
// Type, then Name, so a captured slice is stable across runs. A
// cluster with security disabled or no realm configuration returns an
// empty slice rather than an error — "nothing configured" is a valid
// posture, not a failure mode.
//
// The Deprecated field on each Realm is computed against the cluster's
// reported version. Adapters do not maintain a static deprecation list
// in the public API; the version-aware mapping lives behind the
// adapter so future deprecations land without consumer bookkeeping.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403 (a forbidden read is reported
//     as an auth error rather than an empty slice — the operator
//     wants the difference between "cluster has no realms" and "I
//     can't see them")
//   - ErrUnsupported when the cluster surface is unavailable (OS
//     without the security plugin)
//   - generic wrapped error on other 4xx/5xx with the cluster's body
//     in the message
type RealmsInspector interface {
	Realms(ctx context.Context) ([]types.Realm, error)
}

// APIKeyInspector reports active API keys from the cluster's native
// security store. ES-only: the OpenSearch security plugin has no
// equivalent first-class concept, and the OS adapter returns
// ErrUnsupported on every method so the dialect mismatch surfaces as
// exit code 6 rather than a silent empty slice.
//
// Distinct from SecurityAuditor.Audit's APIKeys block: that one
// flattens the cluster's response into the older SecurityAPIKey shape
// (string-typed Creation/Expiration) for the audit table. APIKeyInspector
// is the typed-time companion the age-based hygiene rule needs —
// stale-key detection wants time.Time, not RFC3339 strings to re-parse.
//
// Source endpoint: GET /_security/api_key?owner=false. Requires the
// cluster privilege manage_api_key (or read_security on newer ES); a
// 403 returns ErrForbidden. Implementations sort the result
// deterministically by Creation desc, then ID, so a captured slice is
// stable across runs.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - ErrUnsupported when the adapter is the OS one
//   - generic wrapped error on other 4xx/5xx with the cluster's body
//     in the message
type APIKeyInspector interface {
	APIKeys(ctx context.Context) ([]types.APIKey, error)
}

// ServiceTokenInspector reports the credentials issued for the
// cluster's service accounts — the ES-only "service token" surface
// (sibling of API keys, used by Fleet, APM Server, and similar
// integrations). The OS adapter returns ErrUnsupported on every method.
//
// The cluster's API is per-service-account: GET /_security/service
// lists every (namespace, service) pair; GET /_security/service/<ns>/<svc>/credential
// lists the credentials of one. Implementations fan out across the
// listed accounts and merge into a single slice — the typical fleet
// has a handful of service accounts, so the fan-out is cheap and the
// caller gets a flat, cluster-wide view rather than nested per-account
// maps.
//
// Source distinguishes index-stored tokens (mutable, rotated via the
// API — Source="index") from file-realm tokens declared on disk in
// service_tokens (Source="file"). The credential's name is the only
// piece operators can act on without root access to the node, so the
// capability surfaces nothing else from the file form.
//
// Errors mirror APIKeyInspector. A per-service forbidden read does not
// abort the whole call: the implementation skips that account and
// continues; the caller receives the credentials it could enumerate.
// A blanket 403 on /_security/service (no read_security at all)
// returns ErrForbidden.
type ServiceTokenInspector interface {
	ServiceTokens(ctx context.Context) ([]types.ServiceToken, error)
}

// FieldCapsInspector wraps GET /<indices>/_field_caps?fields=… — the
// cheap metadata-only view of which fields exist across an index
// pattern, what types they have, and which are searchable/aggregatable.
// Both ES and OS expose the same envelope
// (`{"indices":[...],"fields":{"<name>":{"<type>":{"type":"…",
// "searchable":…,"aggregatable":…,"metadata_field":…,"indices":[...]}
// }}}`) so one capability covers both dialects.
//
// Used by esops-doctor's deprecated-field-types and mapping-drift
// rules — both want "tell me every field's type across this pattern"
// without paying for the full /_mapping body. /_field_caps is the
// right shape: cheap, summarises across indices, and surfaces
// heterogeneous types in one row rather than forcing the rule to
// diff per-index mapping trees.
//
// Deliberately split from FieldCardinalityInspector so the cheap rule
// (deprecated-field-types) doesn't drag in the expensive cardinality
// path. A rule importing FieldCapsInspector alone never accidentally
// triggers a cardinality aggregation against a busy cluster.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx/5xx with the cluster's body in the
//     message (typical case: a 404 when a literal index name does not
//     exist in the path)
type FieldCapsInspector interface {
	FieldCaps(ctx context.Context, req types.FieldCapsRequest) ([]types.FieldCaps, error)
	// FieldCapsSummary is the richer companion that returns the
	// per-field flattened rows plus the cluster's top-level matched-
	// indices list. The two methods share the same wire call; a
	// consumer that only wants the slice continues to use FieldCaps
	// and pays no extra cost. Consumers that need to know which
	// indices contributed (or just the count for a "23 indices
	// analysed" header) reach for FieldCapsSummary.
	FieldCapsSummary(ctx context.Context, req types.FieldCapsRequest) (types.FieldCapsSummary, error)
}

// FieldCardinalityInspector wraps POST /<index>/_search with a
// `cardinality` aggregation — the expensive companion to
// FieldCapsInspector. Both ES and OS expose the same aggregation
// shape; one capability covers both dialects.
//
// Used by esops-doctor's unbounded-keyword-cardinality rule: it
// answers "how many distinct values does this keyword field hold on
// this index" without the rule having to compose a search body
// itself. Implementation runs one /_search per (index, field) pair
// with `size:0` and `track_total_hits:false` so the cluster does not
// materialise hits.
//
// Cardinality runs the cluster's HyperLogLog estimator — the result
// is approximate by construction. The rule consumer treats
// `Precision="approximate"` as the canonical state for now; a future
// "exact" mode would re-run with a `terms` agg, but that's not built.
//
// Bounded by request.MaxFieldsToScan and request.IndexLimit so a
// runaway rule stays polite to the cluster — the capability refuses
// to fan out past those caps with ErrUsage rather than silently
// truncating.
//
// Errors mirror FieldCapsInspector. ErrUsage when neither Indices
// nor Fields is set (a no-op call that almost certainly means the
// rule is mis-wired).
type FieldCardinalityInspector interface {
	Cardinality(ctx context.Context, req types.FieldCardinalityRequest) ([]types.FieldCardinality, error)
	// CardinalityStream is the streaming companion: yields each
	// (index, field) result as soon as the per-pair /_search returns,
	// rather than buffering the full slice. The contract mirrors
	// IndicesStream/AliasesStream — yield returning a non-nil error
	// stops iteration and surfaces the error from the call.
	//
	// Useful when a rule wants progress reporting on a multi-field
	// scan (e.g. 50 fields × 10 indices), or when the caller wants to
	// short-circuit on the first cardinality crossing some threshold
	// rather than waiting for every pair to finish. The slice path is
	// still the primary surface for the typical "give me the
	// cardinality of these three fields" use case.
	CardinalityStream(ctx context.Context, req types.FieldCardinalityRequest, yield func(types.FieldCardinality) error) error
}

// SnapshotRestoreHistoryInspector reports recent or in-flight restore
// observations from two sources, configured per-call:
//
//   - The cluster's task history via
//     GET /_tasks?actions=cluster:admin/snapshot/restore*&detailed=true,
//     a best-effort view that surfaces in-flight restores plus what
//     the cluster still has in its task index. A cluster that hasn't
//     restored anything recently reports an empty slice, which is a
//     valid "no evidence in the lookback window" outcome rather than
//     a failure.
//   - A convention-based index (default `.esops-doctor-restore-tests`)
//     where operators or runbooks record verified restore events. Same
//     read shape as DocumentInspector — one document per recorded
//     restore — so the implementation reuses the existing read
//     window rather than introducing a third search shape.
//
// The capability has no canonical cluster API to reach for: ES and OS
// neither expose a "restore audit log" nor a "verified restore" flag,
// so the convention-based path is the only honest answer to the
// untested-restore rule's central question. The task-history path is
// a nice-to-have for short-lookback "did anything restore in the last
// hour" checks.
//
// Both sources are off by default — request.IncludeTaskHistory and
// request.IncludeConventionIndex must be set explicitly. A request
// with both false returns ErrUsage rather than silently no-op'ing, so
// a mis-wired rule fails loud.
//
// Both ES and OS expose the same task and search envelope shapes; one
// capability covers both dialects.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - ErrUsage when both source toggles are false
//   - generic wrapped error on 4xx/5xx with the cluster's body in the
//     message
type SnapshotRestoreHistoryInspector interface {
	RestoreHistory(ctx context.Context, req types.SnapshotRestoreHistoryRequest) ([]types.RestoreEvent, error)
}

// RemoteClusterInspector reports the cluster's view of every configured
// remote — the read surface esops-doctor's CCR / CCS readiness rules
// build on. The CCS half (RemoteClusters) is shared across ES and OS:
// both dialects expose GET /_remote/info with the same envelope keyed
// by remote-cluster name. The CCR half (FollowerStats,
// AutoFollowPatterns) is Elasticsearch-only: /_ccr/stats and
// /_ccr/auto_follow have no OpenSearch equivalent, so the OS adapter
// returns ErrUnsupported on those methods and the doctor rule auto-
// skips with a stable "not applicable on this dialect" reason rather
// than failing.
//
// Read-only by construction: there is no Updater sibling. Configuring a
// remote, pausing replication, or unfollowing a leader is operator
// surface that lives elsewhere — this capability only reports.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - ErrUnsupported from FollowerStats / AutoFollowPatterns on the OS
//     adapter; on basic-licence ES when the endpoint 404s
//   - generic wrapped error on other 4xx/5xx with the cluster's body in
//     the message
type RemoteClusterInspector interface {
	RemoteClusters(ctx context.Context) ([]types.RemoteCluster, error)
	FollowerStats(ctx context.Context) ([]types.FollowerStats, error)
	AutoFollowPatterns(ctx context.Context) ([]types.AutoFollowPattern, error)
}

// LicenseInspector reports the cluster's license posture from
// GET /_license — the read surface esops-doctor's license-expiration
// rule depends on. ES-only: OpenSearch has no equivalent commercial-
// license surface, so the OS adapter returns ErrUnsupported on every
// method and the matching rule is dialect-tagged `[elasticsearch]`.
//
// Returns one LicenseStatus per call — the cluster's currently
// installed license. The cluster reports a basic license (always
// active, never expiring) by default; the capability surfaces that as
// Status="active", Type="basic", ExpiresAt=nil so the rule consumer
// can render "no expiry concern" without a special-case branch.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - ErrUnsupported when the adapter is the OS one
//   - generic wrapped error on other 4xx/5xx with the cluster's body in
//     the message (typical case: a 404 on /_license against an ES build
//     with the licensing module disabled)
type LicenseInspector interface {
	License(ctx context.Context) (types.LicenseStatus, error)
}

// IngestPipelineInspector exposes the read-only ingest-pipeline surface
// — GET /_ingest/pipeline[/<id>]. Both Elasticsearch and OpenSearch
// share the endpoint path and the response envelope
// (`{"<id>": {"description":..., "processors":[...], ...}}`), so one
// capability covers both dialects with the same adapter shape.
//
// ListPipelines returns every configured pipeline (sorted by name in
// the business layer for stable output). Names is a comma-joined
// filter forwarded to the cluster at the path segment — both dialects
// honour it. An empty list returns every pipeline; a 404 from the
// cluster (no pipelines configured at all) is normalised to an empty
// slice rather than a not-found error.
//
// GetPipeline returns one pipeline by literal id, or a wrapped
// ErrNotFound the caller can match on. Patterns are rejected in the
// business layer — same convention as policy get.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - ErrNotFound (wrapped) from GetPipeline on a 404
//   - generic wrapped error on other 4xx/5xx with the cluster's body in
//     the message
type IngestPipelineInspector interface {
	ListPipelines(ctx context.Context, names []string) ([]types.IngestPipeline, error)
	GetPipeline(ctx context.Context, name string) (types.IngestPipeline, error)
}

// DataStreamInspector exposes the read-only data-stream surface —
// GET /_data_stream[/<names>]. Both Elasticsearch and OpenSearch share
// the endpoint path and the top-level
// {"data_streams": [...]} envelope, so one capability covers both
// dialects with the same adapter shape. Dialect-specific fields
// (ES `ilm_policy` and `lifecycle`; OS `ism_template`) are surfaced
// on types.DataStream when the cluster reports them and left zero
// otherwise.
//
// DataStreams returns every data stream the cluster reports, sorted
// by name in the business layer for stable output. Filter.Names is a
// comma-joined path segment both dialects honour (including
// patterns); an empty Names returns every stream. A 404 from the
// cluster against the bare endpoint normalises to an empty slice —
// some older builds 404 a cluster with no streams configured rather
// than returning `{"data_streams":[]}`.
//
// GetDataStream returns one stream by literal name, or a wrapped
// ErrNotFound. Patterns are rejected in the business layer to match
// the policy/pipeline get convention.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - ErrNotFound (wrapped) from GetDataStream on a 404
//   - generic wrapped error on other 4xx/5xx with the cluster's body
//     in the message
type DataStreamInspector interface {
	DataStreams(ctx context.Context, filter types.DataStreamFilter) ([]types.DataStream, error)
	GetDataStream(ctx context.Context, name string) (types.DataStream, error)
}

// DataStreamManager exposes the mutating data-stream surface —
// rollover, modify (add/remove backing index) and delete. Kept as a
// separate interface from DataStreamInspector so external read-only
// consumers (esops-doctor) can import the inspector without pulling
// in mutate methods.
//
// RolloverDataStream POSTs to /<name>/_rollover. Both dialects accept
// the same {conditions:{...}} body and honour ?dry_run=true.
//
// ModifyDataStream POSTs to /_data_stream/_modify with a body of
// {"actions": [...]}. Advanced — primarily used during migrations to
// splice an index into a stream's backing-index chain. Both products
// support the endpoint (OS added it in 2.x); the adapter passes the
// actions through verbatim.
//
// DeleteDataStream DELETEs /_data_stream/<names>. The cluster removes
// the stream object **and** all of its backing indices in one shot —
// the business layer enforces a blast-radius cap (default 5 streams)
// and the CLI requires --allow-data-loss, so this method assumes the
// caller has cleared both gates already.
//
// Errors mirror the inspector set, plus ErrDryRunBlocker when a
// dry-run rollover reports conditions that did not match and the
// caller treats that as a blocker.
type DataStreamManager interface {
	RolloverDataStream(ctx context.Context, req types.DataStreamRolloverRequest) (types.DataStreamRolloverResult, error)
	ModifyDataStream(ctx context.Context, req types.DataStreamModifyRequest) (types.DataStreamModifyResult, error)
	DeleteDataStream(ctx context.Context, req types.DataStreamDeleteRequest) (types.DataStreamDeleteResult, error)
}

// Client is the composite surface returned by the cluster factory: the
// probed cluster metadata plus one capability per feature area.
// Business-logic callers should depend on the capability interfaces, not
// on *Client directly — composing (rather than embedding) the interfaces
// is what makes that a compile-time constraint instead of a convention.
type Client struct {
	Info                types.ClusterInfo
	Health              HealthInspector
	Shards              ShardInspector
	Nodes               NodeInspector
	NodeStats           NodeStatsInspector
	Indices             IndexInspector
	IndexState          IndexStateChanger
	IndexOptimize       IndexOptimizer
	IndexRollover       IndexRollover
	IndexShrink         IndexShrinker
	IndexSettings       IndexSettingsInspector
	IndexSettingsSet    IndexSettingsUpdater
	IndexTemplateGet    IndexTemplateInspector
	IndexTemplateSet    IndexTemplateUpdater
	AliasInspect        AliasInspector
	AliasUpdate         AliasUpdater
	AllocationExplain   AllocationExplainer
	Security            SecurityAuditor
	Diag                ClusterDiagnoser
	HotThreads          HotThreadsInspector
	Reroute             RerouteExecutor
	ILM                 ILMManager
	ISM                 ISMManager
	Snapshot            SnapshotInspector
	SnapshotCreate      SnapshotCreator
	SnapshotVerify      SnapshotVerifier
	SnapshotRestore     SnapshotRestorer
	SnapshotPrune       SnapshotPruner
	SnapshotRepoMgr     SnapshotRepositoryManager
	SnapshotPolicy      SnapshotPolicyManager
	SnapshotPolicyRead  SnapshotPolicyInspector
	Recovery            RecoveryInspector
	Segments            SegmentsInspector
	ClusterSettings     ClusterSettingsManager
	ClusterSettingsRead ClusterSettingsInspector
	VotingExclusion     VotingExclusionManager
	Tasks               TasksInspector
	TasksCancel         TasksCanceller
	PendingTasks        PendingTasksInspector
	Deprecations        DeprecationInspector
	Documents           DocumentInspector
	Mappings            MappingsInspector
	RemoteReindex       RemoteReindexInspector
	DocCount            DocCounter
	Reindex             Reindexer
	NodeBootstrap       NodeBootstrapInspector
	NodeSettings        NodeSettingsInspector
	ClusterSettingsAll  ClusterSettingsFullInspector
	ClusterSettingsPut  ClusterSettingsPutter
	Allocation          AllocationInspector
	TransportTLS        TransportTLSInspector
	HTTPTLS             HTTPTLSInspector
	IndexCacheClear     IndexCacheClearer
	AuditLog            AuditLogInspector
	Realms              RealmsInspector
	APIKeys             APIKeyInspector
	ServiceTokens       ServiceTokenInspector
	FieldCaps           FieldCapsInspector
	FieldCardinality    FieldCardinalityInspector
	RestoreHistory      SnapshotRestoreHistoryInspector
	RemoteClusters      RemoteClusterInspector
	License             LicenseInspector
	IngestPipeline      IngestPipelineInspector
	DataStream          DataStreamInspector
	DataStreamManage    DataStreamManager
}
