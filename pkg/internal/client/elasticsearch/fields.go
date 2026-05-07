package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"

	"github.com/elastic/go-elasticsearch/v9/esapi"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fieldCapsWire mirrors the cluster's /_field_caps response. The
// envelope is a top-level `fields` map keyed by field name; each entry
// is itself a map keyed by field type. The cluster repeats this shape
// on ES and OS so one wire definition serves both adapters (the
// opensearch.go counterpart duplicates it — peer-internal packages
// share no common dependency they can cross).
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
// The endpoint is metadata-only — cheap enough that the deprecated-
// field-types and mapping-drift rules can run on every audit. The
// upstream typed client supports the call directly; we still bound the
// response read so a misconfigured cluster returning a multi-MB body
// fails loudly rather than silently exhausts memory.
//
// `fields=*` is the cluster default but we forward whatever the rule
// asked for so a narrow-scoped rule pays a smaller payload. The
// "metadata field" sentinel _id/_routing/_source is preserved on the
// neutral type — the rule consumer needs to ignore those when scanning
// for deprecated user-defined types.
func (c *Client) FieldCaps(ctx context.Context, req types.FieldCapsRequest) ([]types.FieldCaps, error) {
	wire, err := c.fetchFieldCaps(ctx, req)
	if err != nil {
		return nil, err
	}
	return fieldCapsFromWire(wire), nil
}

// FieldCapsSummary implements client.FieldCapsInspector. Same wire
// call as FieldCaps; surfaces the cluster's top-level matched-indices
// list alongside the per-field rows so a rule consumer can render
// "<n> indices analysed" without an extra round trip.
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
	fc := c.api.FieldCaps
	opts := []func(*esapi.FieldCapsRequest){fc.WithContext(ctx)}
	if req.Index != "" {
		opts = append(opts, fc.WithIndex(esopsclient.SplitCSV(req.Index)...))
	}
	fields := req.Fields
	if len(fields) == 0 {
		fields = []string{"*"}
	}
	opts = append(opts, fc.WithFields(fields...))
	if req.IncludeUnmapped {
		opts = append(opts, fc.WithIncludeUnmapped(true))
	}

	resp, err := c.api.FieldCaps(opts...)
	if err != nil {
		return fieldCapsWire{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("field_caps", resp.StatusCode, resp.Body); err != nil {
		return fieldCapsWire{}, err
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "field_caps")
	var wire fieldCapsWire
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return fieldCapsWire{}, errors.Join(errors.New("decoding field_caps response"), err)
	}
	return wire, nil
}

// fieldCapsFromWire flattens the cluster's nested {field:{type:{...}}}
// map into the row-per-field neutral slice. Output is sorted by field
// name so a captured slice is stable across runs.
//
// When a field is heterogeneous (multiple types across the matched
// indices), the cluster lists each type with its own per-type
// `Indices` slice. We merge: Types collects every type variant; the
// other booleans collapse with OR-semantics (a field is searchable if
// any contributing type makes it so), which matches the rule
// consumer's "can I search this anywhere" question.
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
			// Deduplicate while preserving sort order so the table cell
			// is stable. Cluster only populates `indices` when a field
			// is heterogeneous; for homogeneous fields we leave it nil.
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

// cardinalitySearchBody mirrors the {size:0, track_total_hits:false,
// aggs:{c:{cardinality:{field:..., precision_threshold:...}}}} body
// shape. Held as a typed builder so the test path can assert on the
// wire shape without re-implementing it.
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

// cardinalitySearchWire mirrors the slice of /_search we read.
type cardinalitySearchWire struct {
	Aggregations struct {
		C struct {
			Value int64 `json:"value"`
		} `json:"c"`
	} `json:"aggregations"`
}

// Cardinality implements client.FieldCardinalityInspector. Runs one
// /_search per (index, field) pair sequentially: the rule consumer
// passes the list it actually wants counted, and the bounded fan-out
// stays polite to the cluster.
//
// Implementation goes through the underlying transport rather than
// the typed Search call so the body shape and query flags are spelled
// out — the test path asserts on the wire shape, and the upstream
// typed Search has accumulated a long list of helpers (track_scores,
// _source_excludes, …) that this capability does not need.
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

// CardinalityStream implements client.FieldCardinalityInspector.
// Iteration order is the request's (index, field) order — stable so
// a yield function that aggregates by index can rely on contiguous
// rows per index.
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

// validateCardinalityRequest is the shared usage-validation block for
// the slice and stream paths. Pulled out so the two cannot drift in
// what they refuse — the cap semantics are part of the capability
// contract.
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

	resp, err := c.api.Perform(httpReq)
	if err != nil {
		return types.FieldCardinality{}, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("cardinality search", resp.StatusCode, resp.Body); err != nil {
		return types.FieldCardinality{}, err
	}
	bodyR := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "cardinality search")
	var wire cardinalitySearchWire
	if err := json.NewDecoder(bodyR).Decode(&wire); err != nil {
		return types.FieldCardinality{}, errors.Join(errors.New("decoding cardinality search response"), err)
	}
	return types.FieldCardinality{
		Field:       field,
		Index:       index,
		Cardinality: wire.Aggregations.C.Value,
		Precision:   "approximate",
	}, nil
}
