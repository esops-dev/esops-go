package cluster_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/cluster"
	"github.com/esops-dev/esops-go/pkg/config"
	"github.com/esops-dev/esops-go/pkg/types"
)

// esRootBody mimics a real Elasticsearch 9.x GET / response. The probe
// distinguishes ES from OS via the tagline + absence of a distribution field.
const esRootBody = `{
  "name": "es01",
  "cluster_name": "esops-dev-es",
  "cluster_uuid": "abc",
  "version": {
    "number": "9.3.3",
    "build_flavor": "default",
    "build_type": "docker"
  },
  "tagline": "You Know, for Search"
}`

// osRootBody mimics OpenSearch 3.x GET /. The distribution field is the
// authoritative signal — OS is always "opensearch" regardless of tagline.
const osRootBody = `{
  "name": "os01",
  "cluster_name": "esops-dev-os",
  "version": {
    "distribution": "opensearch",
    "number": "3.6.0"
  },
  "tagline": "The OpenSearch Project: https://opensearch.org/"
}`

func newServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func ctxFor(url string) config.Context {
	return config.Context{URL: url}
}

func TestProbe_identifiesElasticsearch(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(esRootBody))
	})

	info, err := cluster.Probe(context.Background(), ctxFor(s.URL))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Dialect != types.DialectElasticsearch {
		t.Errorf("dialect = %q, want %q", info.Dialect, types.DialectElasticsearch)
	}
	if info.Version != "9.3.3" {
		t.Errorf("version = %q, want 9.3.3", info.Version)
	}
	if info.ClusterName != "esops-dev-es" {
		t.Errorf("cluster_name = %q, want esops-dev-es", info.ClusterName)
	}
}

func TestProbe_identifiesOpenSearch(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(osRootBody))
	})

	info, err := cluster.Probe(context.Background(), ctxFor(s.URL))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Dialect != types.DialectOpenSearch {
		t.Errorf("dialect = %q, want %q", info.Dialect, types.DialectOpenSearch)
	}
	if info.Distribution != "opensearch" {
		t.Errorf("distribution = %q, want opensearch", info.Distribution)
	}
}

func TestProbe_unknownProductBody(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":{"number":"1.0.0"},"tagline":"Something Else"}`))
	})

	_, err := cluster.Probe(context.Background(), ctxFor(s.URL))
	if !errors.Is(err, client.ErrUnknownProduct) {
		t.Errorf("expected ErrUnknownProduct, got %v", err)
	}
}

func TestProbe_garbageBody(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>not json</html>`))
	})

	_, err := cluster.Probe(context.Background(), ctxFor(s.URL))
	if !errors.Is(err, client.ErrUnknownProduct) {
		t.Errorf("expected ErrUnknownProduct for undecodable body, got %v", err)
	}
}

func TestProbe_401(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	_, err := cluster.Probe(context.Background(), ctxFor(s.URL))
	if !errors.Is(err, client.ErrAuth) {
		t.Errorf("expected ErrAuth, got %v", err)
	}
}

func TestProbe_403(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	_, err := cluster.Probe(context.Background(), ctxFor(s.URL))
	if !errors.Is(err, client.ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestProbe_500(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	_, err := cluster.Probe(context.Background(), ctxFor(s.URL))
	if !errors.Is(err, client.ErrUnreachable) {
		t.Errorf("expected ErrUnreachable for 500, got %v", err)
	}
}

func TestProbe_connectionRefused(t *testing.T) {
	// Listen on a random port, then close immediately so the port is
	// guaranteed-unused on this machine without racing for one.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := s.URL
	s.Close()

	_, err := cluster.Probe(context.Background(), ctxFor(url))
	if !errors.Is(err, client.ErrUnreachable) {
		t.Errorf("expected ErrUnreachable for connection refused, got %v", err)
	}
}

func TestProbe_noAddressSet(t *testing.T) {
	_, err := cluster.Probe(context.Background(), config.Context{})
	if err == nil {
		t.Fatal("expected error for empty context")
	}
}

func TestProbe_basicAuthSendsHeader(t *testing.T) {
	var gotUser, gotPass string
	var hadAuth bool
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, hadAuth = r.BasicAuth()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(osRootBody))
	})

	cc := config.Context{
		URL: s.URL,
		Auth: config.Auth{
			Type:     "basic",
			Username: "admin",
			Password: "hunter2",
		},
	}
	if _, err := cluster.Probe(context.Background(), cc); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !hadAuth {
		t.Fatal("expected Authorization header, got none")
	}
	if gotUser != "admin" || gotPass != "hunter2" {
		t.Errorf("creds = (%q, %q), want (admin, hunter2)", gotUser, gotPass)
	}
}

func TestProbe_unsupportedAuthType(t *testing.T) {
	cc := config.Context{
		URL:  "http://127.0.0.1:0",
		Auth: config.Auth{Type: "sigv4"},
	}
	_, err := cluster.Probe(context.Background(), cc)
	if err == nil {
		t.Fatal("expected error for unsupported auth type")
	}
}

func TestProbe_mtlsMissingKeypair(t *testing.T) {
	cc := config.Context{
		URL:  "https://127.0.0.1:0",
		Auth: config.Auth{Type: "mtls"},
	}
	_, err := cluster.Probe(context.Background(), cc)
	if err == nil {
		t.Fatal("expected error when mtls auth lacks client_cert/client_key")
	}
	if !strings.Contains(err.Error(), "client_cert") {
		t.Errorf("error should mention the missing keypair fields, got: %v", err)
	}
}

// TestProbe_mtlsPresentsClientCert proves the whole TLS path: the cert
// and key referenced in Auth get loaded into the http.Transport, the
// server sees the expected client CN, and the probe identifies ES as
// normal. Any regression in buildTLSConfig (forgetting to load the
// keypair, forgetting to pass Auth through, etc.) breaks this test.
func TestProbe_mtlsPresentsClientCert(t *testing.T) {
	caPath, certPath, keyPath, serverTLS := genMTLSMaterial(t)

	var gotCN string
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			gotCN = r.TLS.PeerCertificates[0].Subject.CommonName
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(esRootBody))
	}))
	s.TLS = serverTLS
	s.StartTLS()
	t.Cleanup(s.Close)

	cc := config.Context{
		URL: s.URL,
		Auth: config.Auth{
			Type:       "mtls",
			ClientCert: certPath,
			ClientKey:  keyPath,
		},
		TLS: config.TLS{CACert: caPath},
	}
	info, err := cluster.Probe(context.Background(), cc)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Dialect != types.DialectElasticsearch {
		t.Errorf("dialect = %q, want %q", info.Dialect, types.DialectElasticsearch)
	}
	if gotCN != "test-client" {
		t.Errorf("server saw client CN = %q, want test-client", gotCN)
	}
}

// genMTLSMaterial builds a throw-away CA, server cert (SAN 127.0.0.1 +
// localhost), and client cert (CN=test-client), writes them to t.TempDir,
// and returns a server *tls.Config that requires and verifies the client
// cert against the CA. The returned paths match the shape that
// config.Context Auth/TLS fields expect.
func genMTLSMaterial(t *testing.T) (caPath, clientCertPath, clientKeyPath string, serverTLS *tls.Config) {
	t.Helper()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	caTpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	caCert, _ := x509.ParseCertificate(caDER)

	srvKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("server key: %v", err)
	}
	srvTpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTpl, caTpl, &srvKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("server cert: %v", err)
	}

	cliKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("client key: %v", err)
	}
	cliTpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	cliDER, err := x509.CreateCertificate(rand.Reader, cliTpl, caTpl, &cliKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("client cert: %v", err)
	}

	dir := t.TempDir()
	writeCert := func(name string, der []byte) string {
		p := filepath.Join(dir, name+".crt")
		if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		return p
	}
	writeKey := func(name string, k *rsa.PrivateKey) string {
		p := filepath.Join(dir, name+".key")
		der := x509.MarshalPKCS1PrivateKey(k)
		if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		return p
	}

	caPath = writeCert("ca", caDER)
	srvCertPath := writeCert("server", srvDER)
	srvKeyPath := writeKey("server", srvKey)
	clientCertPath = writeCert("client", cliDER)
	clientKeyPath = writeKey("client", cliKey)

	srvPair, err := tls.LoadX509KeyPair(srvCertPath, srvKeyPath)
	if err != nil {
		t.Fatalf("load server keypair: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	serverTLS = &tls.Config{
		Certificates: []tls.Certificate{srvPair},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	}
	return caPath, clientCertPath, clientKeyPath, serverTLS
}
