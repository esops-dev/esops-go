// Package cluster wires a config.Context to a connected esops client.
//
// Placement note: CLAUDE.md sketches a single internal/client/ directory
// holding factory.go, capabilities.go, and the per-product adapters.
// That arrangement creates an import cycle because the adapters need to
// reference sentinel errors defined in internal/client. We keep the
// errors + capability interfaces in internal/client (the leaf) and put
// the probe + factory here so they can import both the adapter
// subpackages without cycling.
package cluster

import (
	"context"
	"fmt"
	"net/http"
	"time"

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

	transport, err := buildTransport(cc)
	if err != nil {
		return nil, err
	}

	addrs := cc.Addresses()
	switch info.Dialect {
	case types.DialectElasticsearch:
		impl, err := esadapter.New(addrs, cc.Auth.Username, cc.Auth.Password, transport)
		if err != nil {
			return nil, err
		}
		return &client.Client{Info: info, HealthInspector: impl}, nil
	case types.DialectOpenSearch:
		impl, err := osadapter.New(addrs, cc.Auth.Username, cc.Auth.Password, transport)
		if err != nil {
			return nil, err
		}
		return &client.Client{Info: info, HealthInspector: impl}, nil
	default:
		return nil, fmt.Errorf("%w: dialect=%q", client.ErrUnknownProduct, info.Dialect)
	}
}

// buildTransport returns the http.RoundTripper handed to the upstream
// clients. It honours TLS config and an overall per-request timeout.
func buildTransport(cc config.Context) (http.RoundTripper, error) {
	tlsConf, err := buildTLSConfig(cc.TLS)
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
