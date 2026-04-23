// Package elasticsearch adapts the upstream go-elasticsearch/v9 client
// to esops capability interfaces. Nothing in this package leaks out
// into the rest of the codebase: callers get neutral types only.
package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	es "github.com/elastic/go-elasticsearch/v9"

	esopsclient "github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// Client is an ES-backed implementation of the esops client capabilities.
type Client struct {
	api *es.Client
}

// New builds a client against the given addresses + basic-auth credentials.
// TLS and transport tweaks come in via httpTransport.
func New(addrs []string, username, password string, httpTransport http.RoundTripper) (*Client, error) {
	c, err := es.NewClient(es.Config{
		Addresses: addrs,
		Username:  username,
		Password:  password,
		Transport: httpTransport,
	})
	if err != nil {
		return nil, fmt.Errorf("elasticsearch client: %w", err)
	}
	return &Client{api: c}, nil
}

// Health implements client.HealthInspector.
func (c *Client) Health(ctx context.Context) (types.ClusterHealth, error) {
	resp, err := c.api.Cluster.Health(c.api.Cluster.Health.WithContext(ctx))
	if err != nil {
		return types.ClusterHealth{}, fmt.Errorf("%w: %v", esopsclient.ErrUnreachable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return types.ClusterHealth{}, esopsclient.ErrAuth
	}
	if resp.StatusCode == http.StatusForbidden {
		return types.ClusterHealth{}, esopsclient.ErrForbidden
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return types.ClusterHealth{}, fmt.Errorf("cluster health returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var h types.ClusterHealth
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return types.ClusterHealth{}, errors.Join(errors.New("decoding cluster health response"), err)
	}
	return h, nil
}
