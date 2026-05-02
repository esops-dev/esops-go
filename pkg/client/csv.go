package client

import "strings"

// SplitCSV splits a comma-separated list of patterns, trimming whitespace
// and dropping empty entries. Returns nil (not []string{}) for the empty
// input — distinguishable from "user passed an empty pattern".
//
// Lives in the public client package because both adapter implementations
// and business-logic callers (e.g. settings get with a comma list) want
// the same one-line "do the right thing" behaviour.
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
