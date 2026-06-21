package migrate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// shadowScannerBuffer is the max single-line size the NDJSON / slowlog
// scanners accept. Search bodies (and slowlog source[] blocks) can be
// large once filters and aggregations pile up; 8 MiB is generous
// headroom over any realistic single query while still bounding memory.
const shadowScannerBuffer = 8 << 20

// ParseShadowQueriesNDJSON reads an NDJSON stream of {name, body}
// records, one per line, into the shadow query set. Each `body` must be
// a JSON object (a valid search request). Blank lines are skipped; a
// record with no `name` is auto-named q1, q2, … in input order. A
// malformed line is a usage error (with the line number) rather than a
// silent skip — a typo'd query file should fail loudly, not quietly
// verify fewer queries than the operator wrote.
func ParseShadowQueriesNDJSON(r io.Reader) ([]types.ShadowQuery, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), shadowScannerBuffer)

	var out []types.ShadowQuery
	line := 0
	for sc.Scan() {
		line++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		var rec struct {
			Name string          `json:"name"`
			Body json.RawMessage `json:"body"`
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, client.UsageError("queries line %d: not valid JSON: %v", line, err)
		}
		if len(bytes.TrimSpace(rec.Body)) == 0 {
			return nil, client.UsageError("queries line %d: missing %q field", line, "body")
		}
		if !isJSONObject(rec.Body) {
			return nil, client.UsageError("queries line %d: %q must be a JSON object", line, "body")
		}
		name := strings.TrimSpace(rec.Name)
		if name == "" {
			name = fmt.Sprintf("q%d", len(out)+1)
		}
		out = append(out, types.ShadowQuery{Name: name, Body: rec.Body})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading queries: %w", err)
	}
	if len(out) == 0 {
		return nil, client.UsageError("no queries found in --queries input")
	}
	return out, nil
}

// ParseShadowSlowlog extracts distinct search bodies from an
// Elasticsearch/OpenSearch search-slowlog export and returns the top-N
// by frequency. Both the legacy plain-text log format (`… source[{…}]
// …`) and the structured JSON slowlog (a per-line object with a
// `source` string field) are recognised. Bodies are canonicalised
// (keys sorted) before counting so the same query logged with different
// key ordering collapses to one entry. topN <= 0 keeps every distinct
// query. Lines without a recognisable search body are skipped — a
// slowlog interleaves index and fetch phases and other components.
func ParseShadowSlowlog(r io.Reader, topN int) ([]types.ShadowQuery, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), shadowScannerBuffer)

	type agg struct {
		body  json.RawMessage
		count int
		first int
	}
	seen := map[string]*agg{}
	order := 0
	for sc.Scan() {
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		body, ok := extractSlowlogSource(raw)
		if !ok {
			continue
		}
		// The canonical form (keys sorted, numbers preserved via
		// UseNumber) is the dedup key only — the *original* body bytes
		// are what we replay, so a large integer literal in the query
		// (epoch-nanos, a long id) isn't lossily reformatted through a
		// float64 round-trip.
		key, err := canonicalizeKey(body)
		if err != nil {
			continue
		}
		a := seen[key]
		if a == nil {
			order++
			a = &agg{body: body, first: order}
			seen[key] = a
		}
		a.count++
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading slowlog: %w", err)
	}
	if len(seen) == 0 {
		return nil, client.UsageError("no search queries found in --from-slowlog input")
	}

	aggs := make([]*agg, 0, len(seen))
	for _, a := range seen {
		aggs = append(aggs, a)
	}
	sort.Slice(aggs, func(i, j int) bool {
		if aggs[i].count != aggs[j].count {
			return aggs[i].count > aggs[j].count
		}
		return aggs[i].first < aggs[j].first
	})
	if topN > 0 && len(aggs) > topN {
		aggs = aggs[:topN]
	}

	out := make([]types.ShadowQuery, len(aggs))
	for i, a := range aggs {
		out[i] = types.ShadowQuery{Name: fmt.Sprintf("slowlog-%d", i+1), Body: a.body}
	}
	return out, nil
}

// extractSlowlogSource pulls the search source out of one slowlog line.
// A line starting with `{` is treated as a structured JSON slowlog
// record: any field named `source` (or `*.source` / `*_source`) holds
// the body, either as a JSON string or an inline object; failing that,
// a `message` string is scanned for the bracket form. Everything else
// is treated as a plain-text log line and scanned for `source[{…}]`.
func extractSlowlogSource(line []byte) (json.RawMessage, bool) {
	if len(line) > 0 && line[0] == '{' {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(line, &obj); err == nil {
			for k, v := range obj {
				lk := strings.ToLower(k)
				if lk != "source" && !strings.HasSuffix(lk, ".source") && !strings.HasSuffix(lk, "_source") {
					continue
				}
				if body, ok := jsonObjectFromStringOrInline(v); ok {
					return body, true
				}
			}
			if msg, ok := obj["message"]; ok {
				var s string
				if json.Unmarshal(msg, &s) == nil {
					if body, ok := extractBracketSource([]byte(s)); ok {
						return body, true
					}
				}
			}
			return nil, false
		}
	}
	return extractBracketSource(line)
}

// jsonObjectFromStringOrInline accepts either a JSON-string-encoded
// object (the structured slowlog's usual `"source":"{…}"`) or an inline
// object value, returning the object body when present.
func jsonObjectFromStringOrInline(v json.RawMessage) (json.RawMessage, bool) {
	var s string
	if json.Unmarshal(v, &s) == nil {
		if body, ok := asJSONObject([]byte(s)); ok {
			return body, true
		}
		return nil, false
	}
	return asJSONObject(v)
}

// extractBracketSource finds the `source[…]` field in a plain-text log
// line and decodes the JSON object that follows. A json.Decoder reads
// exactly one balanced value, so brackets and braces inside the query
// body don't confuse the scan and the trailing `]` is ignored.
//
// The `source[` marker must start a field — it has to be at the start of
// the line or preceded by a non-identifier byte — so a decoy field like
// `datasource[…]` or `note_source[…]` is not mistaken for the query
// source. Successive `source[` occurrences are tried in order, returning
// the first that yields a balanced JSON object, so a decoy that fails to
// parse doesn't drop a real query later on the same line.
func extractBracketSource(line []byte) (json.RawMessage, bool) {
	marker := []byte("source[")
	for offset := 0; ; {
		idx := bytes.Index(line[offset:], marker)
		if idx < 0 {
			return nil, false
		}
		abs := offset + idx
		offset = abs + len(marker)
		if abs != 0 && isIdentByte(line[abs-1]) {
			continue // part of a longer field name, not `source[`
		}
		rest := line[abs+len(marker):]
		brace := bytes.IndexByte(rest, '{')
		if brace < 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(rest[brace:]))
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == nil && isJSONObject(raw) {
			return raw, true
		}
	}
}

// isIdentByte reports whether b can be part of a log-field identifier,
// used to anchor the `source[` marker so it isn't matched mid-word.
func isIdentByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}

// asJSONObject returns a copy of b when it is a JSON object, else false.
func asJSONObject(b []byte) (json.RawMessage, bool) {
	t := bytes.TrimSpace(b)
	if !isJSONObject(t) {
		return nil, false
	}
	out := make(json.RawMessage, len(t))
	copy(out, t)
	return out, true
}

// isJSONObject reports whether b parses as a JSON object.
func isJSONObject(b []byte) bool {
	t := bytes.TrimSpace(b)
	if len(t) == 0 || t[0] != '{' {
		return false
	}
	var probe map[string]json.RawMessage
	return json.Unmarshal(t, &probe) == nil
}

// canonicalizeKey returns a stable dedup key for a query body,
// re-marshalling through a generic value so map keys are sorted at every
// depth. UseNumber keeps integer literals exact (json.Unmarshal would
// coerce them to float64 and round large values), so two genuinely
// different large-integer queries don't collide on the key. The key is
// never replayed — only the original body bytes are.
func canonicalizeKey(body json.RawMessage) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
