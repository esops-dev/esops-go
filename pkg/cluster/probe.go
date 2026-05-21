package cluster

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/config"
	"github.com/esops-dev/esops-go/pkg/internal/auth"
	"github.com/esops-dev/esops-go/pkg/types"
)

// Probe hits GET / on the first address in the context and identifies
// the dialect. We do this with plain net/http rather than through one
// of the upstream clients because both clients enforce product checks
// (X-Elastic-Product header, stricter OS parsing) that would reject
// responses from the other product — defeating the whole point of
// probing first.
func Probe(ctx context.Context, cc config.Context) (types.ClusterInfo, error) {
	addrs := cc.Addresses()
	if len(addrs) == 0 {
		return types.ClusterInfo{}, errors.New("context has no url or urls set")
	}
	addr := strings.TrimRight(addrs[0], "/")

	provider, err := auth.From(cc.Auth)
	if err != nil {
		return types.ClusterInfo{}, err
	}

	httpc, err := probeHTTPClient(cc, provider)
	if err != nil {
		return types.ClusterInfo{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr+"/", nil)
	if err != nil {
		return types.ClusterInfo{}, err
	}

	resp, err := httpc.Do(req)
	if err != nil {
		return types.ClusterInfo{}, fmt.Errorf("%w: %v", client.ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return types.ClusterInfo{}, fmt.Errorf("%w: GET / returned 401", client.ErrAuth)
	case http.StatusForbidden:
		return types.ClusterInfo{}, fmt.Errorf("%w: GET / returned 403", client.ErrForbidden)
	}
	if resp.StatusCode >= 400 {
		return types.ClusterInfo{}, fmt.Errorf("%w: GET / returned HTTP %d", client.ErrUnreachable, resp.StatusCode)
	}

	var body struct {
		ClusterName string `json:"cluster_name"`
		Tagline     string `json:"tagline"`
		Version     struct {
			Number       string `json:"number"`
			Distribution string `json:"distribution"`
			BuildFlavor  string `json:"build_flavor"`
		} `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return types.ClusterInfo{}, fmt.Errorf("%w: could not decode GET / response: %v", client.ErrUnknownProduct, err)
	}

	info := types.ClusterInfo{
		ClusterName:  body.ClusterName,
		Version:      body.Version.Number,
		Distribution: body.Version.Distribution,
	}
	switch {
	case strings.EqualFold(body.Version.Distribution, "opensearch"):
		info.Dialect = types.DialectOpenSearch
	case strings.Contains(body.Tagline, "You Know, for Search"):
		info.Dialect = types.DialectElasticsearch
	default:
		return info, fmt.Errorf("%w: tagline=%q distribution=%q",
			client.ErrUnknownProduct, body.Tagline, body.Version.Distribution)
	}
	return info, nil
}

func probeHTTPClient(cc config.Context, provider auth.Provider) (*http.Client, error) {
	tlsConf, err := buildTLSConfig(cc.Auth, cc.TLS)
	if err != nil {
		return nil, err
	}
	var base http.RoundTripper = &http.Transport{
		TLSClientConfig: tlsConf,
	}
	timeout := cc.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Transport: auth.WrapTransport(provider, base), Timeout: timeout}, nil
}

// buildTLSConfig returns a *tls.Config when any TLS-layer setting is in
// play: a CA override, insecure mode, or an mTLS client keypair carried
// on the Auth block. Returns nil when the Go default TLS behaviour is
// sufficient so the upstream client can fall back to its own transport.
func buildTLSConfig(a config.Auth, t config.TLS) (*tls.Config, error) {
	wantMTLS := strings.EqualFold(a.Type, "mtls")
	if t.CACert == "" && !t.Insecure && !wantMTLS {
		return nil, nil
	}
	conf := &tls.Config{MinVersion: tls.VersionTLS12}
	if t.Insecure {
		conf.InsecureSkipVerify = true
	}
	if t.CACert != "" {
		pem, err := os.ReadFile(t.CACert)
		if err != nil {
			return nil, fmt.Errorf("reading cacert %q: %w", t.CACert, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("cacert %q contains no valid certificates", t.CACert)
		}
		conf.RootCAs = pool
	}
	if wantMTLS {
		if a.ClientCert == "" || a.ClientKey == "" {
			return nil, errors.New("auth.type=mtls requires both auth.client_cert and auth.client_key")
		}
		pair, err := tls.LoadX509KeyPair(a.ClientCert, a.ClientKey)
		if err != nil {
			return nil, fmt.Errorf("loading client keypair (%s, %s): %w", a.ClientCert, a.ClientKey, err)
		}
		conf.Certificates = []tls.Certificate{pair}
	}
	return conf, nil
}
