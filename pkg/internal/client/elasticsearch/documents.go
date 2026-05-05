package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/elastic/go-elasticsearch/v9/esapi"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// Document inspector: a deliberately small read-only window onto a
// named index. The capability targets operator-managed metadata
// (esops-doctor waivers, annotations, config) — see the doc on
// client.DocumentInspector for the rationale behind the narrow shape.
//
// Wire path: POST /<index>/_search with size=10000 and
// track_total_hits=false. _source is always requested verbatim;
// _version and _seq_no are surfaced on the neutral type so a future
// writer capability can do optimistic concurrency control without a
// re-read.

const documentsMaxHits = 10000

// searchHitsWire mirrors the slice form of the /_search response we
// actually consume. Fields outside hits[] (took, _shards, aggregations,
// suggest) are ignored — DocumentInspector is a read window, not a
// search client.
type searchHitsWire struct {
	Hits struct {
		Hits []searchHitWire `json:"hits"`
	} `json:"hits"`
}

type searchHitWire struct {
	Index   string          `json:"_index"`
	ID      string          `json:"_id"`
	Source  json.RawMessage `json:"_source"`
	Version int64           `json:"_version"`
	SeqNo   int64           `json:"_seq_no"`
}

// Documents implements client.DocumentInspector. The slice path
// collects every hit and sorts by ID so the returned slice is stable
// across calls — operator-managed metadata indices are small enough
// (waivers, annotations, config) that buffering and sorting client-side
// is cheaper and more portable than asking the cluster to sort on
// `_id` (which has a fielddata footgun on older ES builds).
func (c *Client) Documents(ctx context.Context, req types.DocumentListRequest) ([]types.Document, error) {
	hits, err := c.fetchDocumentHits(ctx, req, "documents search")
	if err != nil {
		return nil, err
	}
	out := make([]types.Document, 0, len(hits))
	for _, h := range hits {
		out = append(out, documentFromHit(h))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// DocumentsStream implements client.DocumentInspector. Yields each
// parsed Document so the caller never has to allocate the full slice
// — the contract IndicesStream/AliasesStream honour for the same
// reason. yield returning a non-nil error stops iteration and
// surfaces the error from the call.
//
// Iteration order is the cluster's hits[] order, mirroring the
// streaming siblings; consumers that need the deterministic id-sort
// the doc on Documents promises should call Documents.
func (c *Client) DocumentsStream(ctx context.Context, req types.DocumentListRequest, yield func(types.Document) error) error {
	if yield == nil {
		return esopsclient.UsageError("documents stream: yield is required")
	}
	hits, err := c.fetchDocumentHits(ctx, req, "documents search (stream)")
	if err != nil {
		return err
	}
	for _, h := range hits {
		if err := yield(documentFromHit(h)); err != nil {
			return err
		}
	}
	return nil
}

// fetchDocumentHits is the shared transport+decode body for the
// slice/stream paths. Pulled out so the two cannot drift in what they
// ask the cluster for or how they classify failures.
func (c *Client) fetchDocumentHits(ctx context.Context, req types.DocumentListRequest, op string) ([]searchHitWire, error) {
	if strings.TrimSpace(req.Index) == "" {
		return nil, esopsclient.UsageError("%s: index is required", op)
	}

	body, err := buildDocumentsSearchBody(req.IDs)
	if err != nil {
		return nil, err
	}

	resp, err := c.api.Search(documentsSearchOptions(ctx, c.api.Search, req.Index, body)...)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if isSearchHandlerMissing(resp.StatusCode, raw) {
			return nil, fmt.Errorf("%w: /_search not exposed by this cluster build", esopsclient.ErrUnsupported)
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return nil, esopsclient.ErrAuth
		case http.StatusForbidden:
			return nil, esopsclient.ErrForbidden
		}
		return nil, clientutil.ClassifyStatus(op, resp.StatusCode, raw)
	}

	var wire searchHitsWire
	bodyR := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, op)
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding "+op+" response"), err)
	}
	return wire.Hits.Hits, nil
}

// isSearchHandlerMissing detects the "the cluster build no longer
// registers /_search" 404. Both ES and OS surface that as a 404 with
// "no handler found" in the body — the same shape the security audit
// path uses to detect xpack.security.enabled=false in
// isSecurityDisabledBody. Translating to ErrUnsupported lets
// downstream consumers (esops-doctor) map the failure to
// ErrProbeNotApplicable instead of treating an absent endpoint as a
// hard error.
//
// Crucially this does NOT match an index_not_found_exception 404 —
// that one means "the cluster works fine, you asked for a missing
// index" and the operator wants the cluster's verbatim reason.
func isSearchHandlerMissing(status int, body []byte) bool {
	if status != http.StatusNotFound {
		return false
	}
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "no handler found") && strings.Contains(lower, "_search")
}

// documentFromHit projects a wire hit into the neutral domain type.
// _version / _seq_no come back as 0 when the cluster did not include
// them (older builds, or when version/seq_no_primary_term is not
// requested) — surfaced as the zero value via omitempty.
func documentFromHit(h searchHitWire) types.Document {
	return types.Document{
		Index:          h.Index,
		ID:             h.ID,
		Source:         h.Source,
		Version:        h.Version,
		SequenceNumber: h.SeqNo,
	}
}

// buildDocumentsSearchBody constructs the _search body. With no IDs we
// match-all the index; with IDs we use the cluster's `ids` query so
// the cluster does the filtering rather than us scanning client-side.
// The body is intentionally minimal — see DocumentInspector's doc for
// why we deliberately don't accept a DSL.
func buildDocumentsSearchBody(ids []string) ([]byte, error) {
	type bodyT struct {
		Query map[string]any `json:"query"`
	}
	var query map[string]any
	if len(ids) == 0 {
		query = map[string]any{"match_all": map[string]any{}}
	} else {
		query = map[string]any{"ids": map[string]any{"values": ids}}
	}
	return json.Marshal(bodyT{Query: query})
}

// documentsSearchOptions assembles the options shared by Documents and
// DocumentsStream. Kept in one place so the two paths cannot drift in
// what they ask the cluster for.
func documentsSearchOptions(ctx context.Context, s esapi.Search, index string, body []byte) []func(*esapi.SearchRequest) {
	return []func(*esapi.SearchRequest){
		s.WithContext(ctx),
		s.WithIndex(index),
		s.WithBody(bytes.NewReader(body)),
		s.WithSize(documentsMaxHits),
		s.WithTrackTotalHits(false),
		// _source=true is the cluster default but spelled out so the
		// wire request matches what the capability promises ("Source
		// is the cluster-side body verbatim"). A future change to the
		// default — or a query-rewriting proxy stripping it — does
		// not silently empty out the Source field.
		s.WithSource("true"),
		s.WithVersion(true),
		s.WithSeqNoPrimaryTerm(true),
	}
}
