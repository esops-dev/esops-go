package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/elastic/go-elasticsearch/v9/esapi"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// SearchExecutor: replays an arbitrary operator-supplied search body for
// shadow-read verification. See the doc on client.SearchExecutor for why
// this is a separate capability from the deliberately narrow
// DocumentInspector.
//
// Wire path: POST /<indices>/_search. Size, _source=false and
// track_total_hits=true are merged into the body (not sent as query
// params) so a caller-supplied `size`/`from` in the body does not collide
// with a duplicate query-string value — ES rejects `size` set in both
// places with a 400.

var _ esopsclient.SearchExecutor = (*Client)(nil)

// searchExecWire mirrors the part of the /_search response shadow reads
// consume: the total match count and the per-hit identity + score. The
// rest of the envelope (took, _shards, aggregations) is ignored.
//
// Total decodes as an object ({"value":N,"relation":"..."}). This holds
// because the adapter forces track_total_hits=true and never sets
// rest_total_hits_as_int, so ES 7.0+ and all OS versions return the
// object form — never the bare-number shape that would fail this decode.
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

	resp, err := c.api.Search(execSearchOptions(ctx, c.api.Search, req, body)...)
	if err != nil {
		return types.SearchResult{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if isSearchHandlerMissing(resp.StatusCode, raw) {
			return types.SearchResult{}, fmt.Errorf("%w: /_search not exposed by this cluster build", esopsclient.ErrUnsupported)
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return types.SearchResult{}, esopsclient.ErrAuth
		case http.StatusForbidden:
			return types.SearchResult{}, esopsclient.ErrForbidden
		}
		return types.SearchResult{}, clientutil.ClassifyStatus(op, resp.StatusCode, raw)
	}

	var wire searchExecWire
	bodyR := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, op)
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return types.SearchResult{}, errors.Join(errors.New("decoding "+op+" response"), err)
	}
	return searchResultFromWire(wire), nil
}

// execSearchOptions assembles the esapi.Search options. The index
// pattern is forwarded verbatim (wildcards preserved); an empty pattern
// omits WithIndex so the search is cluster-wide.
func execSearchOptions(ctx context.Context, s esapi.Search, req types.SearchRequest, body []byte) []func(*esapi.SearchRequest) {
	opts := []func(*esapi.SearchRequest){
		s.WithContext(ctx),
		s.WithBody(bytes.NewReader(body)),
	}
	if len(req.Indices) > 0 {
		opts = append(opts, s.WithIndex(req.Indices...))
	}
	if req.IgnoreUnavailable {
		opts = append(opts, s.WithIgnoreUnavailable(true))
	}
	return opts
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
// hit, never document bodies. A body-level size/from/_source is thus
// overwritten rather than honoured — the SearchExecutor contract
// promises a caller-supplied size/from cannot shift the window. Sort
// (and everything else) is left untouched so the operator's intended
// ordering drives the order diff.
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
