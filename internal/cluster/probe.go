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

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/config"
	"github.com/dj-wasabi/esops-go/internal/types"
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

	httpc, err := probeHTTPClient(cc)
	if err != nil {
		return types.ClusterInfo{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr+"/", nil)
	if err != nil {
		return types.ClusterInfo{}, err
	}
	if err := applyAuth(req, cc.Auth); err != nil {
		return types.ClusterInfo{}, err
	}

	resp, err := httpc.Do(req)
	if err != nil {
		return types.ClusterInfo{}, fmt.Errorf("%w: %v", client.ErrUnreachable, err)
	}
	defer resp.Body.Close()

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

func probeHTTPClient(cc config.Context) (*http.Client, error) {
	tlsConf, err := buildTLSConfig(cc.TLS)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		TLSClientConfig: tlsConf,
	}
	timeout := cc.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

func buildTLSConfig(t config.TLS) (*tls.Config, error) {
	if t.CACert == "" && !t.Insecure {
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
	return conf, nil
}

func applyAuth(req *http.Request, a config.Auth) error {
	switch strings.ToLower(a.Type) {
	case "", "none":
		return nil
	case "basic":
		if a.Username == "" {
			return errors.New("auth.type=basic requires a username")
		}
		req.SetBasicAuth(a.Username, a.Password)
		return nil
	default:
		return fmt.Errorf("auth type %q is not implemented yet (v0.1.0 supports: basic)", a.Type)
	}
}
