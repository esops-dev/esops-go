package opensearch

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
// GET /<indices>/_segments. Same shape as Elasticsearch; the OS typed
// client doesn't expose the endpoint cleanly so we go through Perform.
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
	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.SegmentsReport{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == 404 {
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
