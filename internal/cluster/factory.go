// Package cluster wires a config.Context to a connected esops client.
package cluster

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/dj-wasabi/esops-go/internal/auth"
	"github.com/dj-wasabi/esops-go/internal/client"
	esadapter "github.com/dj-wasabi/esops-go/internal/client/elasticsearch"
	osadapter "github.com/dj-wasabi/esops-go/internal/client/opensearch"
	"github.com/dj-wasabi/esops-go/internal/config"
	"github.com/dj-wasabi/esops-go/internal/types"
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
		})
		if err != nil {
			return nil, err
		}
		return &client.Client{Info: info, Health: impl, Shards: impl, Nodes: impl, Indices: impl, IndexState: impl, IndexOptimize: impl, IndexRollover: impl, IndexShrink: impl, IndexSettings: impl, IndexSettingsSet: impl, IndexTemplateGet: impl, IndexTemplateSet: impl, AliasInspect: impl, AliasUpdate: impl, AllocationExplain: impl, Security: impl, Diag: impl, HotThreads: impl, Reroute: impl}, nil
	case types.DialectOpenSearch:
		impl, err := osadapter.New(addrs, provider, transport, osadapter.Options{
			MaxRetries:          cc.MaxRetries,
			CompressRequestBody: cc.Compress,
		})
		if err != nil {
			return nil, err
		}
		return &client.Client{Info: info, Health: impl, Shards: impl, Nodes: impl, Indices: impl, IndexState: impl, IndexOptimize: impl, IndexRollover: impl, IndexShrink: impl, IndexSettings: impl, IndexSettingsSet: impl, IndexTemplateGet: impl, IndexTemplateSet: impl, AliasInspect: impl, AliasUpdate: impl, AllocationExplain: impl, Security: impl, Diag: impl, HotThreads: impl, Reroute: impl}, nil
	default:
		return nil, fmt.Errorf("%w: dialect=%q", client.ErrUnknownProduct, info.Dialect)
	}
}

// buildTransport returns the http.RoundTripper handed to the upstream
// clients. It honours TLS config (including an mTLS client keypair
// carried on cc.Auth) and an overall per-request timeout. Auth
// wrapping is applied separately by the adapter so dialect-specific
// rejections (sigv4-on-ES, api_key-on-OS) can fail loudly before the
// transport is ever exercised.
func buildTransport(cc config.Context) (http.RoundTripper, error) {
	tlsConf, err := buildTLSConfig(cc.Auth, cc.TLS)
	if err != nil {
		return nil, err
	}
	t := &http.Transport{
		TLSClientConfig:       tlsConf,
		ResponseHeaderTimeout: effectiveTimeout(cc),
	}
	return t, nil
}

func effectiveTimeout(cc config.Context) time.Duration {
	if cc.Timeout > 0 {
		return cc.Timeout
	}
	return 30 * time.Second
}
