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
	"sort"
	"strconv"
	"strings"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// Document inspector: a deliberately small read-only window onto a
// named index — see the doc on client.DocumentInspector for the
// rationale behind the narrow shape.
//
// Wire path mirrors the ES adapter exactly: POST /<index>/_search
// with size=10000 and track_total_hits=false. Implementation goes
// through c.api.Client.Perform because the typed opensearch-go
// SearchHit drops _version, and surfacing it on types.Document is
// part of the contract.

const documentsMaxHits = 10000

// searchHitsWire mirrors the slice of /_search response we consume.
// Same shape as the ES adapter's wire type — duplicated rather than
// shared because pkg/internal/client/opensearch and ES are
// peer-internal packages with no shared dependency they can cross.
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
// beats asking the cluster to sort on `_id`.
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
// parsed Document so the caller never has to allocate the full slice.
// Iteration order is the cluster's hits[] order, mirroring the
// streaming siblings.
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

	// _source=true is the cluster default but spelled out so the wire
	// request matches what the capability promises ("Source is the
	// cluster-side body verbatim"). A future default change — or a
	// query-rewriting proxy stripping it — does not silently empty
	// out the Source field.
	path := "/" + url.PathEscape(req.Index) + "/_search?size=" + strconv.Itoa(documentsMaxHits) +
		"&track_total_hits=false&_source=true&version=true&seq_no_primary_term=true"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, op))
	if err != nil {
		return nil, errors.Join(errors.New("reading "+op+" response"), err)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, esopsclient.ErrAuth
	case http.StatusForbidden:
		return nil, esopsclient.ErrForbidden
	}
	if resp.StatusCode >= 400 {
		if isSearchHandlerMissing(resp.StatusCode, respBody) {
			return nil, fmt.Errorf("%w: /_search not exposed by this cluster build", esopsclient.ErrUnsupported)
		}
		return nil, clientutil.ClassifyStatus(op, resp.StatusCode, respBody)
	}

	var wire searchHitsWire
	if err := json.Unmarshal(respBody, &wire); err != nil {
		return nil, errors.Join(errors.New("decoding "+op+" response"), err)
	}
	return wire.Hits.Hits, nil
}

// isSearchHandlerMissing detects the "the cluster build no longer
// registers /_search" 404. Both ES and OS surface that as a 404 with
// "no handler found" in the body. Translating to ErrUnsupported lets
// downstream consumers (esops-doctor) map the failure to
// ErrProbeNotApplicable instead of treating an absent endpoint as a
// hard error.
//
// This does NOT match an index_not_found_exception 404 — that one
// means "the cluster works fine, you asked for a missing index" and
// the operator wants the cluster's verbatim reason.
func isSearchHandlerMissing(status int, body []byte) bool {
	if status != http.StatusNotFound {
		return false
	}
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "no handler found") && strings.Contains(lower, "_search")
}

// documentFromHit projects a wire hit into the neutral domain type.
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
// match-all the index; with IDs we use the cluster's `ids` query.
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
