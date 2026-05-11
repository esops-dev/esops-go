package opensearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// ingestPipelineWire mirrors one entry in the GET /_ingest/pipeline
// response. Both Elasticsearch and OpenSearch return the same
// envelope, but the OS v4 typed wrapper (IngestGetResp) drops fields
// we care about — `version`, `_meta`, `on_failure` — so we go through
// the raw transport here.
//
// _meta is preserved verbatim as map[string]any; Processors and
// OnFailure stay as []map[string]any so the processor catalogue can
// evolve per-version without freezing a typed schema.
type ingestPipelineWire struct {
	Description string           `json:"description,omitempty"`
	Version     int64            `json:"version,omitempty"`
	Meta        map[string]any   `json:"_meta,omitempty"`
	Processors  []map[string]any `json:"processors,omitempty"`
	OnFailure   []map[string]any `json:"on_failure,omitempty"`
}

// ListPipelines implements client.IngestPipelineInspector via
// GET /_ingest/pipeline[/<ids>]. The OS endpoint shape and response
// envelope are identical to ES, so the adapter is a near-mirror of
// the ES side — only the raw-transport helper differs.
//
// Implementation note: we drop down to the raw transport (rather
// than the typed osapi.IngestGetReq, which strips version/_meta/
// on_failure) and wrap the response body in a BoundedReader. A
// pipeline catalogue with hundreds of entries × complex processor
// chains × large _meta blobs is realistic enough that an unbounded
// io.ReadAll on a misbehaving cluster could peg the CLI's memory.
//
// A 404 against the bare endpoint normalises to an empty slice — a
// cluster with no pipelines configured returns `{}` 200 on modern
// versions and 404 on some older builds. A 404 against an explicit
// id list is genuinely not-found.
func (c *Client) ListPipelines(ctx context.Context, names []string) ([]types.IngestPipeline, error) {
	path := "/_ingest/pipeline"
	if len(names) > 0 {
		path += "/" + strings.Join(names, ",")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.Client.Perform(req)
	if err != nil {
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, esopsclient.ErrAuth
	case http.StatusForbidden:
		return nil, esopsclient.ErrForbidden
	case http.StatusNotFound:
		if len(names) == 0 {
			return []types.IngestPipeline{}, nil
		}
		return nil, fmt.Errorf("%w: ingest pipeline(s) %s", esopsclient.ErrNotFound, strings.Join(names, ","))
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, clientutil.ClassifyStatus("ingest get_pipeline", resp.StatusCode, raw)
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "ingest get_pipeline")
	wire := map[string]ingestPipelineWire{}
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding ingest get_pipeline response"), err)
	}
	out := make([]types.IngestPipeline, 0, len(wire))
	for name, p := range wire {
		out = append(out, types.IngestPipeline{
			Name:        name,
			Description: p.Description,
			Version:     p.Version,
			Meta:        p.Meta,
			Processors:  p.Processors,
			OnFailure:   p.OnFailure,
		})
	}
	return out, nil
}

// GetPipeline implements client.IngestPipelineInspector for the
// single-id lookup. Returns ErrNotFound (wrapped) when the cluster
// 404s — both products 404 a missing pipeline id rather than
// returning an empty map.
func (c *Client) GetPipeline(ctx context.Context, name string) (types.IngestPipeline, error) {
	rows, err := c.ListPipelines(ctx, []string{name})
	if err != nil {
		return types.IngestPipeline{}, err
	}
	for _, p := range rows {
		if p.Name == name {
			return p, nil
		}
	}
	return types.IngestPipeline{}, fmt.Errorf("%w: ingest pipeline %q", esopsclient.ErrNotFound, name)
}

// Compile-time guarantee that this Client satisfies the inspector
// surface.
var _ esopsclient.IngestPipelineInspector = (*Client)(nil)
