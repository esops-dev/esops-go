package cluster_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/cluster"
	"github.com/dj-wasabi/esops-go/internal/config"
	"github.com/dj-wasabi/esops-go/internal/types"
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
