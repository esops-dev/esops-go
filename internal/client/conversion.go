package client

import (
	"strconv"
	"strings"
)

// Helpers shared by the Elasticsearch and OpenSearch adapters for the
// small ceremony around _cat API responses: quoted numerics, null-able
// string columns, comma-separated index patterns. Kept in the `client`
// package (rather than a util/ sibling) so both adapters import the same
// primitives from one place.

// SplitCSV splits a comma-separated list of patterns, trimming whitespace
// and dropping empty entries. Returns nil (not []string{}) for the empty
// input — distinguishable from "user passed an empty pattern".
func SplitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// AtoiOrZero parses a string as int; returns 0 on error. The _cat APIs
// quote every numeric column, and cells can be empty for system indices
// or unassigned shards — zero is the sentinel across both dialects.
func AtoiOrZero(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// Atoi64Ptr parses a *string (the null-able shape both _cat APIs use)
// as int64; returns 0 for nil or parse error.
func Atoi64Ptr(p *string) int64 {
	if p == nil {
		return 0
	}
	n, _ := strconv.ParseInt(*p, 10, 64)
	return n
}

// DerefString returns *p, or "" when p is nil. The _cat JSON payloads
// emit null for optional columns (node, unassigned.*) — the neutral
// domain types use "" instead.
func DerefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// IntPtrOrZero returns *p, or 0 when p is nil. The opensearch-go/v4
// typed client already decodes numeric columns for us but still exposes
// them as *int so the "not present" case round-trips.
func IntPtrOrZero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// Int64PtrOrZero returns int64(*p), or 0 when p is nil.
func Int64PtrOrZero(p *int) int64 {
	if p == nil {
		return 0
	}
	return int64(*p)
}

// UniqueStrings returns the input in its original order with duplicates
// removed. Empty entries are dropped. Returns nil (not []string{}) for
// nil or all-empty input so callers that serialise with omitempty stay
// tidy.
func UniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
