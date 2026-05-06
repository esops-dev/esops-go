// Package mapping is the table-driven translator behind `esops migrate
// plan`. It walks an index's mapping tree and emits one issue per
// field whose type either changes shape across dialects (warn) or
// blocks a reindex outright (block) — see CLAUDE.md §9.1.
//
// Adding a new translation is a data change: append a row to the
// rules table. Code does not branch on dialect names anywhere outside
// this file, so a new ES/OS pair lands without surgery.
package mapping

import (
	"sort"

	"github.com/esops-dev/esops-go/pkg/types"
)

// Rule is one row in the translation table. FromDialect/ToDialect
// scope the rule to a directional pair; FromType is the cluster's
// type string at the source. ToType is the target spelling — empty
// when the issue is purely diagnostic ("you must hand-translate").
// Severity is one of MigrationStatusOK / Warn / Block.
type Rule struct {
	From     types.Dialect
	To       types.Dialect
	FromType string
	ToType   string
	Severity string
	Note     string
}

// rules is the canonical table. Keep the directions explicit — same-
// dialect upgrades (ES 7 → ES 8 string removal, OS 1 → OS 2) and
// cross-dialect entries live side by side, and a missing entry is the
// signal that no translation is needed.
var rules = []Rule{
	// ─── ES → OS (cross-dialect) ──────────────────────────────────────
	{
		From:     types.DialectElasticsearch,
		To:       types.DialectOpenSearch,
		FromType: "flattened",
		ToType:   "flat_object",
		Severity: types.MigrationStatusWarn,
		Note:     "ES 'flattened' maps to OS 'flat_object'; subfield queries differ — review consumers",
	},
	{
		From:     types.DialectElasticsearch,
		To:       types.DialectOpenSearch,
		FromType: "constant_keyword",
		ToType:   "keyword",
		Severity: types.MigrationStatusWarn,
		Note:     "OpenSearch has no constant_keyword; downgrades to keyword (storage cost increases)",
	},
	{
		From:     types.DialectElasticsearch,
		To:       types.DialectOpenSearch,
		FromType: "match_only_text",
		ToType:   "text",
		Severity: types.MigrationStatusWarn,
		Note:     "OpenSearch has no match_only_text; falls back to text (positions retained — larger index)",
	},
	{
		From:     types.DialectElasticsearch,
		To:       types.DialectOpenSearch,
		FromType: "version",
		ToType:   "keyword",
		Severity: types.MigrationStatusWarn,
		Note:     "OpenSearch has no version type; downgrades to keyword — semver-aware sorting is lost",
	},
	{
		From:     types.DialectElasticsearch,
		To:       types.DialectOpenSearch,
		FromType: "rank_features",
		ToType:   "",
		Severity: types.MigrationStatusBlock,
		Note:     "OpenSearch has no rank_features; reindex will fail without a hand-translated mapping",
	},

	// ─── OS → ES (cross-dialect) ──────────────────────────────────────
	{
		From:     types.DialectOpenSearch,
		To:       types.DialectElasticsearch,
		FromType: "flat_object",
		ToType:   "flattened",
		Severity: types.MigrationStatusWarn,
		Note:     "OS 'flat_object' maps to ES 'flattened'; subfield queries differ — review consumers",
	},
	{
		From:     types.DialectOpenSearch,
		To:       types.DialectElasticsearch,
		FromType: "knn_vector",
		ToType:   "dense_vector",
		Severity: types.MigrationStatusWarn,
		Note:     "OS 'knn_vector' maps to ES 'dense_vector'; index params (m, ef_construction) need re-derivation",
	},

	// ─── Same-dialect upgrades / removals (ES 6+) ─────────────────────
	{
		From:     types.DialectElasticsearch,
		To:       types.DialectElasticsearch,
		FromType: "string",
		ToType:   "",
		Severity: types.MigrationStatusBlock,
		Note:     "ES 6+ removed legacy 'string'; hand-translate to text or keyword before reindex",
	},
	{
		From:     types.DialectOpenSearch,
		To:       types.DialectOpenSearch,
		FromType: "string",
		ToType:   "",
		Severity: types.MigrationStatusBlock,
		Note:     "OpenSearch has never accepted legacy 'string'; hand-translate to text or keyword",
	},
	{
		From:     types.DialectOpenSearch,
		To:       types.DialectElasticsearch,
		FromType: "string",
		ToType:   "",
		Severity: types.MigrationStatusBlock,
		Note:     "ES has never accepted legacy 'string'; hand-translate to text or keyword",
	},
	{
		From:     types.DialectElasticsearch,
		To:       types.DialectOpenSearch,
		FromType: "string",
		ToType:   "",
		Severity: types.MigrationStatusBlock,
		Note:     "OpenSearch has never accepted legacy 'string'; hand-translate to text or keyword",
	},
}

// Analyze walks the mapping's properties tree and returns issues
// raised by the rule table. Issues are sorted by field path so the
// per-index report is stable across runs (the cluster's
// json.RawMessage iteration order is not).
func Analyze(from, to types.Dialect, m types.IndexMapping) []types.MappingIssue {
	if m.Properties == nil {
		return nil
	}
	out := make([]types.MappingIssue, 0)
	walk(from, to, "", m.Properties, &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out
}

// walk recurses through `properties`, emitting issues for any field
// whose `type` matches a rule in the table. Multi-fields nested under
// a parent's `fields` block are walked too — they declare their own
// `type` and the cluster validates them just like top-level fields.
//
// Object-typed parents have a nested `properties` block; those are
// recursed without raising an issue against the parent (object/nested
// are universal).
func walk(from, to types.Dialect, prefix string, props map[string]any, out *[]types.MappingIssue) {
	for name, raw := range props {
		field, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}

		if t, ok := field["type"].(string); ok && t != "" {
			if rule, found := match(from, to, t); found {
				*out = append(*out, types.MappingIssue{
					Field:    path,
					FromType: t,
					ToType:   rule.ToType,
					Severity: rule.Severity,
					Note:     rule.Note,
				})
			}
		}

		if children, ok := field["properties"].(map[string]any); ok {
			walk(from, to, path, children, out)
		}
		if multi, ok := field["fields"].(map[string]any); ok {
			walk(from, to, path, multi, out)
		}
	}
}

// match looks up a rule for the given (from, to, fromType) triple.
// Same-dialect lookups land on a (X, X, type) row; cross-dialect on
// (ES, OS, type) etc. The first matching rule wins; the table is
// curated so no (from, to, type) triple has more than one entry.
func match(from, to types.Dialect, fromType string) (Rule, bool) {
	for _, r := range rules {
		if r.From == from && r.To == to && r.FromType == fromType {
			return r, true
		}
	}
	return Rule{}, false
}

// WorstSeverity returns the highest severity across the issue set, or
// MigrationStatusOK on an empty slice. Block dominates Warn dominates
// OK. Used by the per-index plan to summarise its row's status.
func WorstSeverity(issues []types.MappingIssue) string {
	worst := types.MigrationStatusOK
	for _, i := range issues {
		switch i.Severity {
		case types.MigrationStatusBlock:
			return types.MigrationStatusBlock
		case types.MigrationStatusWarn:
			if worst == types.MigrationStatusOK {
				worst = types.MigrationStatusWarn
			}
		}
	}
	return worst
}
