package opensearch_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/pkg/client"
)

// remoteOSTransport replays canned /_remote/info responses. The OS
// client doesn't issue a product-check probe, so unlike the ES side
// there's no special-case for the root path.
type remoteOSTransport struct {
	body   string
	status int
}

func (t *remoteOSTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.HasPrefix(req.URL.Path, "/_remote/info") {
		return &http.Response{
			StatusCode: 404,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":"unhandled path"}`)),
			Request:    req,
		}, nil
	}
	status := t.status
	if status == 0 {
		status = 200
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(t.body)),
		Request:    req,
	}, nil
}

func TestRemoteClusters_OS_emptyObjectIsEmptySlice(t *testing.T) {
	tr := &remoteOSTransport{body: `{}`}
	c := newProbedClient(t, tr)
	got, err := c.RemoteClusters(context.Background())
	if err != nil {
		t.Fatalf("RemoteClusters: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("got = %v, want empty slice", got)
	}
}

func TestRemoteClusters_OS_proxyModeUsesSocketCount(t *testing.T) {
	tr := &remoteOSTransport{body: `{
		"proxy-cluster": {
			"mode":"proxy",
			"connected":true,
			"num_proxy_sockets_connected":4,
			"skip_unavailable":true
		}
	}`}
	c := newProbedClient(t, tr)
	got, err := c.RemoteClusters(context.Background())
	if err != nil {
		t.Fatalf("RemoteClusters: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].NumNodesConnected != 4 {
		t.Errorf("NumNodesConnected = %d, want 4 (proxy socket count)", got[0].NumNodesConnected)
	}
	if !got[0].SkipUnavailable {
		t.Errorf("SkipUnavailable = false, want true")
	}
}

func TestRemoteClusters_OS_sniffModePopulatesSeedHosts(t *testing.T) {
	tr := &remoteOSTransport{body: `{
		"sniff-cluster": {
			"mode":"sniff",
			"connected":true,
			"num_nodes_connected":2,
			"seeds":["host1:9300","host2:9300"],
			"skip_unavailable":false
		}
	}`}
	c := newProbedClient(t, tr)
	got, err := c.RemoteClusters(context.Background())
	if err != nil {
		t.Fatalf("RemoteClusters: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	want := []string{"host1:9300", "host2:9300"}
	if len(got[0].SeedHosts) != 2 || got[0].SeedHosts[0] != want[0] || got[0].SeedHosts[1] != want[1] {
		t.Errorf("SeedHosts = %v, want %v", got[0].SeedHosts, want)
	}
}

func TestRemoteClusters_OS_sortedByName(t *testing.T) {
	tr := &remoteOSTransport{body: `{
		"zeta": {"mode":"sniff","connected":true,"num_nodes_connected":1,"seeds":["z:9300"]},
		"alpha": {"mode":"sniff","connected":true,"num_nodes_connected":1,"seeds":["a:9300"]}
	}`}
	c := newProbedClient(t, tr)
	got, err := c.RemoteClusters(context.Background())
	if err != nil {
		t.Fatalf("RemoteClusters: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Errorf("got = %#v, want sorted by name", got)
	}
}

func TestRemoteClusters_OS_unauthorizedIsErrAuth(t *testing.T) {
	tr := &remoteOSTransport{status: 401, body: `{"error":"unauthorized"}`}
	c := newProbedClient(t, tr)
	_, err := c.RemoteClusters(context.Background())
	if !errors.Is(err, client.ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

func TestRemoteClusters_OS_forbiddenIsErrForbidden(t *testing.T) {
	tr := &remoteOSTransport{status: 403, body: `{"error":"forbidden"}`}
	c := newProbedClient(t, tr)
	_, err := c.RemoteClusters(context.Background())
	if !errors.Is(err, client.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

func TestFollowerStats_OS_alwaysReturnsErrUnsupported(t *testing.T) {
	// CCR is Elasticsearch-only — the OS adapter short-circuits without
	// touching the wire. The stub must return ErrUnsupported regardless
	// of cluster state so a downstream rule auto-skips with a stable
	// "not applicable on this dialect" reason.
	tr := &remoteOSTransport{}
	c := newProbedClient(t, tr)
	_, err := c.FollowerStats(context.Background())
	if !errors.Is(err, client.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestAutoFollowPatterns_OS_alwaysReturnsErrUnsupported(t *testing.T) {
	// Same rationale as FollowerStats — auto-follow is Elasticsearch-
	// only on the same licence-gated terms.
	tr := &remoteOSTransport{}
	c := newProbedClient(t, tr)
	_, err := c.AutoFollowPatterns(context.Background())
	if !errors.Is(err, client.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}
