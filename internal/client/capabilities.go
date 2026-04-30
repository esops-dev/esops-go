package client

import (
	"context"

	"github.com/dj-wasabi/esops-go/internal/types"
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
	// --include-defaults -o ndjson` honour CLAUDE.md §6 on a busy cluster
	// where the full nested-defaults payload is multiple MB.
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
	// CLAUDE.md §6 contract as IndicesStream — a 5k-alias cluster
	// emits the first row before the cluster has finished sending.
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
// CLAUDE.md §3 warns against.
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

// Client is the composite surface returned by the cluster factory: the
// probed cluster metadata plus one capability per feature area.
// Business-logic callers should depend on the capability interfaces, not
// on *Client directly — composing (rather than embedding) the interfaces
// is what makes that a compile-time constraint instead of a convention.
type Client struct {
	Info              types.ClusterInfo
	Health            HealthInspector
	Shards            ShardInspector
	Nodes             NodeInspector
	Indices           IndexInspector
	IndexState        IndexStateChanger
	IndexOptimize     IndexOptimizer
	IndexRollover     IndexRollover
	IndexShrink       IndexShrinker
	IndexSettings     IndexSettingsInspector
	IndexSettingsSet  IndexSettingsUpdater
	IndexTemplateGet  IndexTemplateInspector
	IndexTemplateSet  IndexTemplateUpdater
	AliasInspect      AliasInspector
	AliasUpdate       AliasUpdater
	AllocationExplain AllocationExplainer
	Security          SecurityAuditor
	Diag              ClusterDiagnoser
	HotThreads        HotThreadsInspector
	Reroute           RerouteExecutor
	ILM               ILMManager
	ISM               ISMManager
}
