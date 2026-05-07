package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fieldCapsWire mirrors the cluster's /_field_caps response. Same shape
// as the ES adapter's wire type — duplicated rather than shared because
// pkg/internal/client/opensearch and ES are peer-internal packages with
// no shared dependency they can cross.
type fieldCapsWire struct {
	Indices []string                                 `json:"indices"`
	Fields  map[string]map[string]fieldCapsTypeEntry `json:"fields"`
}

type fieldCapsTypeEntry struct {
	Type          string   `json:"type"`
	Searchable    bool     `json:"searchable"`
	Aggregatable  bool     `json:"aggregatable"`
	MetadataField bool     `json:"metadata_field,omitempty"`
	Indices       []string `json:"indices,omitempty"`
}

// FieldCaps implements client.FieldCapsInspector via POST /<index>/_field_caps.
// Implementation goes through the underlying transport because the
// upstream typed FieldCaps response surfaces fewer fields than the
// neutral type promises (indices[] for heterogeneous mappings is
// omitted from the typed view), and the wire shape is small enough
// that decoding it directly is the lowest-risk option.
func (c *Client) FieldCaps(ctx context.Context, req types.FieldCapsRequest) ([]types.FieldCaps, error) {
	wire, err := c.fetchFieldCaps(ctx, req)
	if err != nil {
		return nil, err
	}
	return fieldCapsFromWire(wire), nil
}

// FieldCapsSummary implements client.FieldCapsInspector. Same wire
// call as FieldCaps; surfaces the cluster's top-level matched-indices
// list alongside the per-field rows. See the matching ES adapter for
// the rationale.
func (c *Client) FieldCapsSummary(ctx context.Context, req types.FieldCapsRequest) (types.FieldCapsSummary, error) {
	wire, err := c.fetchFieldCaps(ctx, req)
	if err != nil {
		return types.FieldCapsSummary{}, err
	}
	return types.FieldCapsSummary{
		Fields:         fieldCapsFromWire(wire),
		MatchedIndices: wire.Indices,
	}, nil
}

func (c *Client) fetchFieldCaps(ctx context.Context, req types.FieldCapsRequest) (fieldCapsWire, error) {
	indices := esopsclient.SplitCSV(req.Index)
	pathBuilder := strings.Builder{}
	if len(indices) > 0 {
		// PathEscape each segment so an index name with reserved
		// characters travels intact; the cluster joins on commas.
		escaped := make([]string, 0, len(indices))
		for _, n := range indices {
			escaped = append(escaped, url.PathEscape(n))
		}
		pathBuilder.WriteString("/")
		pathBuilder.WriteString(strings.Join(escaped, ","))
	}
	pathBuilder.WriteString("/_field_caps?")
	fields := req.Fields
	if len(fields) == 0 {
		fields = []string{"*"}
	}
	q := url.Values{}
	q.Set("fields", strings.Join(fields, ","))
	if req.IncludeUnmapped {
		q.Set("include_unmapped", "true")
	}
	pathBuilder.WriteString(q.Encode())

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, pathBuilder.String(), nil)
	if err != nil {
		return fieldCapsWire{}, err
	}

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return fieldCapsWire{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "field_caps"))
	if err != nil {
		return fieldCapsWire{}, errors.Join(errors.New("reading field_caps response"), err)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fieldCapsWire{}, esopsclient.ErrAuth
	case http.StatusForbidden:
		return fieldCapsWire{}, esopsclient.ErrForbidden
	}
	if resp.StatusCode >= 400 {
		return fieldCapsWire{}, clientutil.ClassifyStatus("field_caps", resp.StatusCode, respBody)
	}

	var wire fieldCapsWire
	if err := json.Unmarshal(respBody, &wire); err != nil {
		return fieldCapsWire{}, errors.Join(errors.New("decoding field_caps response"), err)
	}
	return wire, nil
}

// fieldCapsFromWire flattens the cluster's nested {field:{type:{...}}}
// map into the row-per-field neutral slice. See the ES adapter's
// counterpart for the rationale behind the merge semantics — kept
// identical so a contract test running both adapters against the same
// payload produces the same neutral view.
func fieldCapsFromWire(w fieldCapsWire) []types.FieldCaps {
	out := make([]types.FieldCaps, 0, len(w.Fields))
	for name, byType := range w.Fields {
		row := types.FieldCaps{Field: name}
		typeNames := make([]string, 0, len(byType))
		var indicesUnion []string
		for t, e := range byType {
			typeNames = append(typeNames, t)
			if e.Searchable {
				row.Searchable = true
			}
			if e.Aggregatable {
				row.Aggregatable = true
			}
			if e.MetadataField {
				row.MetadataField = true
			}
			indicesUnion = append(indicesUnion, e.Indices...)
		}
		sort.Strings(typeNames)
		row.Types = typeNames
		if len(byType) > 1 && len(indicesUnion) > 0 {
			row.Indices = uniqueSorted(indicesUnion)
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out
}

func uniqueSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// cardinalitySearchBody mirrors the same body the ES adapter sends —
// kept identical so a future contract test can run the same byte slice
// through both code paths and assert the cluster reports the same
// cardinality on a shared fixture.
func cardinalitySearchBody(field string, precision int) ([]byte, error) {
	agg := map[string]any{"field": field}
	if precision > 0 {
		agg["precision_threshold"] = precision
	}
	body := map[string]any{
		"size":             0,
		"track_total_hits": false,
		"aggs": map[string]any{
			"c": map[string]any{"cardinality": agg},
		},
	}
	return json.Marshal(body)
}

type cardinalitySearchWire struct {
	Aggregations struct {
		C struct {
			Value int64 `json:"value"`
		} `json:"c"`
	} `json:"aggregations"`
}

// Cardinality implements client.FieldCardinalityInspector. Mirrors the
// ES adapter's bounded fan-out: one /_search per (index, field) pair,
// run sequentially, with caller-supplied caps so a runaway rule cannot
// fan out past the rule consumer's expectations.
func (c *Client) Cardinality(ctx context.Context, req types.FieldCardinalityRequest) ([]types.FieldCardinality, error) {
	indices, err := validateCardinalityRequest(req)
	if err != nil {
		return nil, err
	}
	out := make([]types.FieldCardinality, 0, len(indices)*len(req.Fields))
	for _, idx := range indices {
		for _, field := range req.Fields {
			row, err := c.cardinalityOne(ctx, idx, field, req.Precision)
			if err != nil {
				return nil, err
			}
			out = append(out, row)
		}
	}
	return out, nil
}

// CardinalityStream implements client.FieldCardinalityInspector. Yields
// each (index, field) result as it returns. Iteration order is the
// request's (index, field) order — see the ES adapter for the
// rationale; kept identical so a contract test exercises both
// implementations the same way.
func (c *Client) CardinalityStream(ctx context.Context, req types.FieldCardinalityRequest, yield func(types.FieldCardinality) error) error {
	if yield == nil {
		return esopsclient.UsageError("cardinality stream: yield is required")
	}
	indices, err := validateCardinalityRequest(req)
	if err != nil {
		return err
	}
	for _, idx := range indices {
		for _, field := range req.Fields {
			row, err := c.cardinalityOne(ctx, idx, field, req.Precision)
			if err != nil {
				return err
			}
			if err := yield(row); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateCardinalityRequest is the shared usage-validation block —
// see the ES adapter's identical helper for the rationale.
func validateCardinalityRequest(req types.FieldCardinalityRequest) ([]string, error) {
	indices := esopsclient.SplitCSV(req.Indices)
	if len(indices) == 0 || len(req.Fields) == 0 {
		return nil, esopsclient.UsageError("cardinality: indices and fields are required")
	}
	if req.IndexLimit > 0 && len(indices) > req.IndexLimit {
		return nil, esopsclient.UsageError(
			"cardinality: %d indices exceed IndexLimit=%d", len(indices), req.IndexLimit,
		)
	}
	total := len(indices) * len(req.Fields)
	if req.MaxFieldsToScan > 0 && total > req.MaxFieldsToScan {
		return nil, esopsclient.UsageError(
			"cardinality: %d (index×field) pairs exceed MaxFieldsToScan=%d", total, req.MaxFieldsToScan,
		)
	}
	return indices, nil
}

func (c *Client) cardinalityOne(ctx context.Context, index, field string, precision int) (types.FieldCardinality, error) {
	body, err := cardinalitySearchBody(field, precision)
	if err != nil {
		return types.FieldCardinality{}, err
	}
	path := "/" + url.PathEscape(index) + "/_search?size=0&track_total_hits=false"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return types.FieldCardinality{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.api.Client.Perform(httpReq)
	if err != nil {
		return types.FieldCardinality{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "cardinality search"))
	if err != nil {
		return types.FieldCardinality{}, errors.Join(errors.New("reading cardinality search response"), err)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return types.FieldCardinality{}, esopsclient.ErrAuth
	case http.StatusForbidden:
		return types.FieldCardinality{}, esopsclient.ErrForbidden
	}
	if resp.StatusCode >= 400 {
		return types.FieldCardinality{}, clientutil.ClassifyStatus("cardinality search", resp.StatusCode, respBody)
	}

	var wire cardinalitySearchWire
	if err := json.Unmarshal(respBody, &wire); err != nil {
		return types.FieldCardinality{}, errors.Join(errors.New("decoding cardinality search response"), err)
	}
	return types.FieldCardinality{
		Field:       field,
		Index:       index,
		Cardinality: wire.Aggregations.C.Value,
		Precision:   "approximate",
	}, nil
}
