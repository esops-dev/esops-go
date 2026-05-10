package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/esops-dev/esops-go/pkg/internal/client/segmentswire"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// Segments implements client.SegmentsInspector via
// GET /<indices>/_segments. Goes through Perform rather than the typed
// client because the typed wrapper decodes into a shape that flattens
// `segments.<id>` into a slice (losing the segment id) and skips the
// per-shard `num_committed_segments` / `num_search_segments` fields the
// wide table renders.
func (c *Client) Segments(ctx context.Context, req types.SegmentsRequest) (types.SegmentsReport, error) {
	indices := strings.TrimSpace(req.Indices)
	if indices == "" {
		indices = "_all"
	}
	path := "/" + url.PathEscape(indices) + "/_segments"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return types.SegmentsReport{}, err
	}
	resp, err := c.api.Perform(httpReq)
	if err != nil {
		return types.SegmentsReport{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == 404 {
		// No matching index: surface as an empty report rather than an
		// error. A pattern that resolves to nothing is a normal
		// "nothing to merge" outcome, not a cluster-side failure.
		return types.SegmentsReport{Indices: []types.IndexSegments{}}, nil
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(clientutil.NewBoundedReader(resp.Body, 64*1024, "segments error"))
		return types.SegmentsReport{}, clientutil.ClassifyStatus("segments indices="+indices, resp.StatusCode, body)
	}

	var wire segmentswire.Envelope
	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "segments")
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return types.SegmentsReport{}, errors.Join(errors.New("decoding segments response"), err)
	}
	return segmentswire.ReportFromWire(wire), nil
}
