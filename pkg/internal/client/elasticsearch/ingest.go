package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// ingestPipelineWire mirrors one entry in the GET /_ingest/pipeline
// response. The cluster keys the response by pipeline id; we decode
// the value into this struct and surface Name from the map key.
//
// _meta is a free-form map both products preserve verbatim. Processors
// and OnFailure stay as []map[string]any: the processor catalogue
// differs across versions and products, so freezing a typed schema
// would force a lossy translation.
type ingestPipelineWire struct {
	Description string           `json:"description,omitempty"`
	Version     int64            `json:"version,omitempty"`
	Meta        map[string]any   `json:"_meta,omitempty"`
	Processors  []map[string]any `json:"processors,omitempty"`
	OnFailure   []map[string]any `json:"on_failure,omitempty"`
}

// ListPipelines implements client.IngestPipelineInspector via
// GET /_ingest/pipeline[/<ids>]. Both Elasticsearch and OpenSearch
// share the same path and envelope; this adapter forwards a
// comma-joined name list at the path segment when one is given.
//
// Implementation note: we drop down to the raw transport rather than
// going through doRawGET so the response body can be wrapped in a
// BoundedReader. A cluster with hundreds of pipelines × complex
// processor chains × large _meta blobs is a realistic shape (the
// `audit-tagger` pattern alone has produced ~200 KB responses in
// the wild), and an unbounded io.ReadAll on a misbehaving cluster
// would peg the CLI's memory.
//
// A 404 against the bare endpoint normalises to an empty slice — a
// cluster with no pipelines configured returns `{}` 200 on modern
// versions and 404 on some older builds; treating both as "nothing
// configured" keeps the CLI's empty-table semantics consistent. A
// 404 against an explicit id list is genuinely not-found.
func (c *Client) ListPipelines(ctx context.Context, names []string) ([]types.IngestPipeline, error) {
	path := "/_ingest/pipeline"
	if len(names) > 0 {
		path += "/" + strings.Join(names, ",")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.Perform(req)
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
	if err := classifyResponse("ingest get_pipeline", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "ingest get_pipeline")
	wire := map[string]ingestPipelineWire{}
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding ingest get_pipeline response"), err)
	}
	out := make([]types.IngestPipeline, 0, len(wire))
	for name, p := range wire {
		out = append(out, ingestPipelineFromWire(name, p))
	}
	return out, nil
}

// GetPipeline implements client.IngestPipelineInspector for the
// single-id lookup. Returns ErrNotFound (wrapped) when the cluster
// 404s — both ES and OS 404 a missing pipeline id rather than
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

// ingestPipelineFromWire flattens the wire entry to the neutral
// shape. Kept as a file-local helper so the OS adapter, which
// re-decodes the same wire shape, doesn't pull in cross-adapter
// imports — same convention slm.go / sm.go follow.
func ingestPipelineFromWire(name string, w ingestPipelineWire) types.IngestPipeline {
	return types.IngestPipeline{
		Name:        name,
		Description: w.Description,
		Version:     w.Version,
		Meta:        w.Meta,
		Processors:  w.Processors,
		OnFailure:   w.OnFailure,
	}
}

// Compile-time guarantee that this Client satisfies the inspector
// surface. The OS adapter implements the same interface against the
// same path; the factory wires whichever the dialect resolves to.
var _ esopsclient.IngestPipelineInspector = (*Client)(nil)
