// Package cluster wires a config.Context to a connected esops client.
//
// This is the public construction surface consumed by esops-doctor and
// any future commercial product. The ES/OS adapters, transport
// plumbing, and auth providers live under pkg/internal/... so they
// stay invisible to external consumers — Go's internal-directory rule
// makes pkg/cluster the only path to a connected *client.Client.
package cluster

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/config"
	"github.com/esops-dev/esops-go/pkg/internal/auth"
	esadapter "github.com/esops-dev/esops-go/pkg/internal/client/elasticsearch"
	osadapter "github.com/esops-dev/esops-go/pkg/internal/client/opensearch"
	"github.com/esops-dev/esops-go/pkg/types"
)

// New probes the target cluster, determines its dialect, and returns
// a *client.Client composing the right capability implementations.
func New(ctx context.Context, cc config.Context) (*client.Client, error) {
	info, err := Probe(ctx, cc)
	if err != nil {
		return nil, err
	}

	provider, err := auth.From(cc.Auth)
	if err != nil {
		return nil, err
	}

	transport, err := buildTransport(cc)
	if err != nil {
		return nil, err
	}

	addrs := cc.Addresses()
	switch info.Dialect {
	case types.DialectElasticsearch:
		impl, err := esadapter.New(addrs, provider, transport, esadapter.Options{
			MaxRetries:          cc.MaxRetries,
			CompressRequestBody: cc.Compress,
			Version:             info.Version,
		})
		if err != nil {
			return nil, err
		}
		// ISM is OpenSearch-only — wire the unsupported-dialect stub
		// so business code can call s.Client.ISM.* without nil-checks
		// and get ErrUnsupported with a clear message when the
		// operator targeted an ES context by mistake.
		return &client.Client{Info: info, Health: impl, Shards: impl, Nodes: impl, NodeStats: impl, Indices: impl, IndexState: impl, IndexOptimize: impl, IndexRollover: impl, IndexShrink: impl, IndexSettings: impl, IndexSettingsSet: impl, IndexTemplateGet: impl, IndexTemplateSet: impl, AliasInspect: impl, AliasUpdate: impl, AllocationExplain: impl, Security: impl, Diag: impl, HotThreads: impl, Reroute: impl, ILM: impl, ISM: unsupportedISM{}, Snapshot: impl, SnapshotCreate: impl, SnapshotVerify: impl, SnapshotRestore: impl, SnapshotPrune: impl, SnapshotRepoMgr: impl, SnapshotPolicy: impl, SnapshotPolicyRead: impl, Recovery: impl, Segments: impl, ClusterSettings: impl, ClusterSettingsRead: impl, VotingExclusion: impl, Tasks: impl, TasksCancel: impl, PendingTasks: impl, Deprecations: impl, Documents: impl, Search: impl, Mappings: impl, RemoteReindex: impl, DocCount: impl, Reindex: impl, NodeBootstrap: impl, NodeSettings: impl, ClusterSettingsAll: impl, ClusterSettingsPut: impl, Allocation: impl, TransportTLS: impl, HTTPTLS: impl, IndexCacheClear: impl, AuditLog: impl, Realms: impl, APIKeys: impl, APIKeysManage: impl, ServiceTokens: impl, FieldCaps: impl, FieldCardinality: impl, RestoreHistory: impl, RemoteClusters: impl, License: impl, IngestPipeline: impl, DataStream: impl, DataStreamManage: impl, Transform: impl, TransformManage: impl}, nil
	case types.DialectOpenSearch:
		impl, err := osadapter.New(addrs, provider, transport, osadapter.Options{
			MaxRetries:          cc.MaxRetries,
			CompressRequestBody: cc.Compress,
			Version:             info.Version,
		})
		if err != nil {
			return nil, err
		}
		// ILM is Elasticsearch-only — same stubbing pattern as the ES
		// branch above for ISM.
		return &client.Client{Info: info, Health: impl, Shards: impl, Nodes: impl, NodeStats: impl, Indices: impl, IndexState: impl, IndexOptimize: impl, IndexRollover: impl, IndexShrink: impl, IndexSettings: impl, IndexSettingsSet: impl, IndexTemplateGet: impl, IndexTemplateSet: impl, AliasInspect: impl, AliasUpdate: impl, AllocationExplain: impl, Security: impl, Diag: impl, HotThreads: impl, Reroute: impl, ILM: unsupportedILM{}, ISM: impl, Snapshot: impl, SnapshotCreate: impl, SnapshotVerify: impl, SnapshotRestore: impl, SnapshotPrune: impl, SnapshotRepoMgr: impl, SnapshotPolicy: impl, SnapshotPolicyRead: impl, Recovery: impl, Segments: impl, ClusterSettings: impl, ClusterSettingsRead: impl, VotingExclusion: impl, Tasks: impl, TasksCancel: impl, PendingTasks: impl, Deprecations: unsupportedDeprecations{}, Documents: impl, Search: impl, Mappings: impl, RemoteReindex: impl, DocCount: impl, Reindex: impl, NodeBootstrap: impl, NodeSettings: impl, ClusterSettingsAll: impl, ClusterSettingsPut: impl, Allocation: impl, TransportTLS: impl, HTTPTLS: impl, IndexCacheClear: impl, AuditLog: impl, Realms: impl, APIKeys: unsupportedAPIKeys{}, APIKeysManage: unsupportedAPIKeyManager{}, ServiceTokens: unsupportedServiceTokens{}, FieldCaps: impl, FieldCardinality: impl, RestoreHistory: impl, RemoteClusters: impl, License: unsupportedLicense{}, IngestPipeline: impl, DataStream: impl, DataStreamManage: impl, Transform: impl, TransformManage: impl}, nil
	default:
		return nil, fmt.Errorf("%w: dialect=%q", client.ErrUnknownProduct, info.Dialect)
	}
}

// unsupportedILM is the ILM capability stub installed on OpenSearch
// clients. Every method returns ErrUnsupported so the CLI surfaces a
// stable exit code 6 (and message "ILM is Elasticsearch-only…") instead
// of a confusing 404 from /_ilm/policy on a cluster that simply doesn't
// have the feature.
type unsupportedILM struct{}

func (unsupportedILM) ListPolicies(context.Context, []string) ([]types.ILMPolicy, error) {
	return nil, fmt.Errorf("%w: ILM is Elasticsearch-only; use 'esops index ism' for OpenSearch", client.ErrUnsupported)
}
func (unsupportedILM) GetPolicy(context.Context, string) (types.ILMPolicy, error) {
	return types.ILMPolicy{}, fmt.Errorf("%w: ILM is Elasticsearch-only; use 'esops index ism' for OpenSearch", client.ErrUnsupported)
}
func (unsupportedILM) PutPolicy(context.Context, types.ILMPolicyPutRequest) (types.ILMPolicyPutResult, error) {
	return types.ILMPolicyPutResult{}, fmt.Errorf("%w: ILM is Elasticsearch-only; use 'esops index ism' for OpenSearch", client.ErrUnsupported)
}
func (unsupportedILM) DeletePolicy(context.Context, types.ILMPolicyDeleteRequest) (types.ILMPolicyDeleteResult, error) {
	return types.ILMPolicyDeleteResult{}, fmt.Errorf("%w: ILM is Elasticsearch-only; use 'esops index ism' for OpenSearch", client.ErrUnsupported)
}
func (unsupportedILM) ExplainPolicy(context.Context, types.ILMExplainRequest) ([]types.ILMExplainEntry, error) {
	return nil, fmt.Errorf("%w: ILM is Elasticsearch-only; use 'esops index ism' for OpenSearch", client.ErrUnsupported)
}
func (unsupportedILM) ExplainPolicyStream(context.Context, types.ILMExplainRequest, func(types.ILMExplainEntry) error) error {
	return fmt.Errorf("%w: ILM is Elasticsearch-only; use 'esops index ism' for OpenSearch", client.ErrUnsupported)
}

// unsupportedISM is the mirror of unsupportedILM for the ES branch.
type unsupportedISM struct{}

func (unsupportedISM) ListPolicies(context.Context, []string) ([]types.ISMPolicy, error) {
	return nil, fmt.Errorf("%w: ISM is OpenSearch-only; use 'esops index ilm' for Elasticsearch", client.ErrUnsupported)
}
func (unsupportedISM) GetPolicy(context.Context, string) (types.ISMPolicy, error) {
	return types.ISMPolicy{}, fmt.Errorf("%w: ISM is OpenSearch-only; use 'esops index ilm' for Elasticsearch", client.ErrUnsupported)
}
func (unsupportedISM) PutPolicy(context.Context, types.ISMPolicyPutRequest) (types.ISMPolicyPutResult, error) {
	return types.ISMPolicyPutResult{}, fmt.Errorf("%w: ISM is OpenSearch-only; use 'esops index ilm' for Elasticsearch", client.ErrUnsupported)
}
func (unsupportedISM) DeletePolicy(context.Context, types.ISMPolicyDeleteRequest) (types.ISMPolicyDeleteResult, error) {
	return types.ISMPolicyDeleteResult{}, fmt.Errorf("%w: ISM is OpenSearch-only; use 'esops index ilm' for Elasticsearch", client.ErrUnsupported)
}
func (unsupportedISM) ExplainPolicy(context.Context, types.ISMExplainRequest) ([]types.ISMExplainEntry, error) {
	return nil, fmt.Errorf("%w: ISM is OpenSearch-only; use 'esops index ilm' for Elasticsearch", client.ErrUnsupported)
}
func (unsupportedISM) ExplainPolicyStream(context.Context, types.ISMExplainRequest, func(types.ISMExplainEntry) error) error {
	return fmt.Errorf("%w: ISM is OpenSearch-only; use 'esops index ilm' for Elasticsearch", client.ErrUnsupported)
}

// unsupportedDeprecations is the deprecation-inspector stub installed
// on OpenSearch clients. /_migration/deprecations is an Elasticsearch-
// only endpoint (the migration plugin); OS has no equivalent. Returning
// ErrUnsupported here keeps the dialect mismatch loud and stable
// (exit code 6) rather than leaking through as a 404 from a path the
// cluster never advertised.
type unsupportedDeprecations struct{}

func (unsupportedDeprecations) Deprecations(context.Context, types.DeprecationsRequest) ([]types.DeprecationIssue, error) {
	return nil, fmt.Errorf("%w: /_migration/deprecations is Elasticsearch-only; OpenSearch has no equivalent endpoint", client.ErrUnsupported)
}

// unsupportedAPIKeys is the API-key-inspector stub installed on
// OpenSearch clients. /_security/api_key is an Elasticsearch-native
// concept; the OS security plugin has no equivalent surface, so the
// capability returns ErrUnsupported rather than fabricating an empty
// result that downstream rules would mistake for "no keys".
type unsupportedAPIKeys struct{}

func (unsupportedAPIKeys) APIKeys(context.Context) ([]types.APIKey, error) {
	return nil, fmt.Errorf("%w: API keys are Elasticsearch-only; OpenSearch has no equivalent first-class surface", client.ErrUnsupported)
}

func (unsupportedAPIKeys) GetAPIKey(context.Context, string) (types.APIKey, error) {
	return types.APIKey{}, fmt.Errorf("%w: API keys are Elasticsearch-only; OpenSearch has no equivalent first-class surface", client.ErrUnsupported)
}

// unsupportedAPIKeyManager is the mutating-side stub installed on
// OpenSearch clients. Same dialect-only rationale as unsupportedAPIKeys —
// /_security/api_key is Elasticsearch-native; OS has no equivalent.
type unsupportedAPIKeyManager struct{}

func (unsupportedAPIKeyManager) CreateAPIKey(context.Context, types.APIKeyCreateRequest) (types.APIKeyCreated, error) {
	return types.APIKeyCreated{}, fmt.Errorf("%w: API keys are Elasticsearch-only; OpenSearch has no equivalent first-class surface", client.ErrUnsupported)
}

func (unsupportedAPIKeyManager) InvalidateAPIKey(context.Context, types.APIKeyInvalidateRequest) (types.APIKeyInvalidated, error) {
	return types.APIKeyInvalidated{}, fmt.Errorf("%w: API keys are Elasticsearch-only; OpenSearch has no equivalent first-class surface", client.ErrUnsupported)
}

// unsupportedServiceTokens mirrors unsupportedAPIKeys for service
// account tokens: an Elasticsearch-only concept under /_security/service.
type unsupportedServiceTokens struct{}

func (unsupportedServiceTokens) ServiceTokens(context.Context) ([]types.ServiceToken, error) {
	return nil, fmt.Errorf("%w: service tokens are Elasticsearch-only; OpenSearch has no equivalent first-class surface", client.ErrUnsupported)
}

// unsupportedLicense is the LicenseInspector stub installed on
// OpenSearch clients. /_license is an Elasticsearch-only commercial-
// licence surface; OS has no equivalent. Returning ErrUnsupported keeps
// the dialect mismatch loud and stable (exit code 6) rather than
// leaking a 404 from a path the cluster never advertised.
type unsupportedLicense struct{}

func (unsupportedLicense) License(context.Context) (types.LicenseStatus, error) {
	return types.LicenseStatus{}, fmt.Errorf("%w: /_license is Elasticsearch-only; OpenSearch has no equivalent commercial-licence surface", client.ErrUnsupported)
}

// buildTransport returns the http.RoundTripper handed to the upstream
// clients. It honours TLS config (including an mTLS client keypair
// carried on cc.Auth) and an overall per-request timeout. Auth
// wrapping is applied separately by the adapter so dialect-specific
// rejections (sigv4-on-ES, api_key-on-OS) can fail loudly before the
// transport is ever exercised.
//
// When the context sets max_in_flight > 0, the transport is wrapped
// with a semaphore so concurrent in-flight requests for that context
// stay bounded — prevents one runaway esops invocation from starving
// a shared cluster. The cap applies across the whole process since
// one config.Context produces one client.
func buildTransport(cc config.Context) (http.RoundTripper, error) {
	tlsConf, err := buildTLSConfig(cc.Auth, cc.TLS)
	if err != nil {
		return nil, err
	}
	var rt http.RoundTripper = &http.Transport{
		TLSClientConfig:       tlsConf,
		ResponseHeaderTimeout: effectiveTimeout(cc),
	}
	if cc.MaxInFlight > 0 {
		rt = newBoundedTransport(rt, cc.MaxInFlight)
	}
	return rt, nil
}

// boundedTransport caps concurrent in-flight HTTP requests via a
// counting semaphore. Acquired before delegating to the wrapped
// RoundTripper, released when the response body is closed (or when
// the wrapped call returns an error and there is no body to close).
//
// The semaphore is process-wide for the cluster client it backs: one
// config.Context produces one client, and that client's transport is
// the only path requests take to the cluster. Operators who want
// per-host caps should split the cluster into multiple contexts.
type boundedTransport struct {
	inner http.RoundTripper
	sem   chan struct{}
}

func newBoundedTransport(inner http.RoundTripper, max int) *boundedTransport {
	return &boundedTransport{
		inner: inner,
		sem:   make(chan struct{}, max),
	}
}

func (b *boundedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	select {
	case b.sem <- struct{}{}:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	resp, err := b.inner.RoundTrip(req)
	if err != nil {
		<-b.sem
		return nil, err
	}
	// Release on body close so the slot is held for the duration of
	// the read, not just the headers. This is the contract operators
	// expect from a "max in flight" knob: a slow consumer reading a
	// large response counts against the cap.
	resp.Body = &releaseOnClose{ReadCloser: resp.Body, sem: b.sem}
	return resp, nil
}

type releaseOnClose struct {
	io.ReadCloser
	sem      chan struct{}
	released bool
}

func (r *releaseOnClose) Close() error {
	err := r.ReadCloser.Close()
	if !r.released {
		<-r.sem
		r.released = true
	}
	return err
}

func effectiveTimeout(cc config.Context) time.Duration {
	if cc.Timeout > 0 {
		return cc.Timeout
	}
	return 30 * time.Second
}
