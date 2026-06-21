package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// SearchExecutor: replays an arbitrary operator-supplied search body for
// shadow-read verification. Mirrors the ES adapter exactly; see the doc
// on client.SearchExecutor for why this is a separate capability from
// the deliberately narrow DocumentInspector.
//
// Wire path: POST /<indices>/_search through c.api.Client.Perform. The
// index pattern is joined with commas without per-segment escaping so a
// wildcard like `logs-*` reaches the cluster intact — the same join the
// upstream opensearch-go SearchReq performs. Size, _source=false and
// track_total_hits=true are merged into the body (not the query string)
// so a body-level size does not collide with a duplicate query param.

var _ esopsclient.SearchExecutor = (*Client)(nil)

// searchExecWire mirrors the slice of /_search response shadow reads
// consume. Same shape as the ES adapter's wire type — duplicated rather
// than shared because the two adapters are peer-internal packages with
// no shared dependency they can cross.
type searchExecWire struct {
	Hits struct {
		Total struct {
			Value int64 `json:"value"`
		} `json:"total"`
		Hits []struct {
			Index string   `json:"_index"`
			ID    string   `json:"_id"`
			Score *float64 `json:"_score"`
		} `json:"hits"`
	} `json:"hits"`
}

// Search implements client.SearchExecutor.
func (c *Client) Search(ctx context.Context, req types.SearchRequest) (types.SearchResult, error) {
	const op = "search"

	body, err := buildExecSearchBody(req.Body, req.Size)
	if err != nil {
		return types.SearchResult{}, esopsclient.UsageError("%s: %v", op, err)
	}

	path := "/_search"
	if len(req.Indices) > 0 {
		// Escape each index segment (matching the OS documents adapter)
		// but join with a literal comma — the comma is the multi-index
		// separator and a wildcard like `logs-*` must reach the cluster
		// intact, so neither is escaped.
		escaped := make([]string, len(req.Indices))
		for i, ix := range req.Indices {
			escaped[i] = url.PathEscape(ix)
		}
		path = "/" + strings.Join(escaped, ",") + "/_search"
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return types.SearchResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if req.IgnoreUnavailable {
		q := httpReq.URL.Query()
		q.Set("ignore_unavailable", "true")
		httpReq.URL.RawQuery = q.Encode()
	}

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.SearchResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, op))
	if err != nil {
		return types.SearchResult{}, errors.Join(errors.New("reading "+op+" response"), err)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return types.SearchResult{}, esopsclient.ErrAuth
	case http.StatusForbidden:
		return types.SearchResult{}, esopsclient.ErrForbidden
	}
	if resp.StatusCode >= 400 {
		if isSearchHandlerMissing(resp.StatusCode, respBody) {
			return types.SearchResult{}, fmt.Errorf("%w: /_search not exposed by this cluster build", esopsclient.ErrUnsupported)
		}
		return types.SearchResult{}, clientutil.ClassifyStatus(op, resp.StatusCode, respBody)
	}

	var wire searchExecWire
	if err := json.Unmarshal(respBody, &wire); err != nil {
		return types.SearchResult{}, errors.Join(errors.New("decoding "+op+" response"), err)
	}
	return searchResultFromWire(wire), nil
}

func searchResultFromWire(wire searchExecWire) types.SearchResult {
	out := types.SearchResult{
		TotalHits: wire.Hits.Total.Value,
		Hits:      make([]types.SearchResultHit, 0, len(wire.Hits.Hits)),
	}
	for _, h := range wire.Hits.Hits {
		var score float64
		if h.Score != nil {
			score = *h.Score
		}
		out.Hits = append(out.Hits, types.SearchResultHit{Index: h.Index, ID: h.ID, Score: score})
	}
	return out
}

// buildExecSearchBody merges the shadow-read comparison window onto the
// operator-supplied body. An empty body becomes `{}` (a cluster-side
// match_all). size, _source and track_total_hits are forced, and `from`
// is reset to 0, regardless of what the operator set: shadow reads
// compare a fixed top-K window of hit identities starting at the first
// hit, never document bodies. Sort is left untouched so the operator's
// intended ordering drives the order diff.
func buildExecSearchBody(raw json.RawMessage, size int) ([]byte, error) {
	body := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("query body is not a JSON object: %w", err)
		}
	}
	if size < 0 {
		size = 0
	}
	body["size"] = size
	body["from"] = 0
	body["_source"] = false
	body["track_total_hits"] = true
	// Aggregations never affect which hits come back, and shadow reads
	// diff hits, not aggregation buckets — drop them so an agg-heavy
	// slowlog query doesn't burn cluster CPU on both sides for output we
	// discard during a migration window.
	delete(body, "aggs")
	delete(body, "aggregations")
	return json.Marshal(body)
}
