package index

import "strings"

// isPatternish reports whether s contains any glob glyph the cluster
// resolves at request time, or one of the match-everything sentinels.
// Used by every command that requires a literal name (shrink endpoints,
// alias add/remove, template/policy get-and-mutate paths) to refuse
// patterns up-front rather than letting a typo widen the blast radius.
func isPatternish(s string) bool {
	switch s {
	case "*", "_all":
		return true
	}
	return strings.ContainsAny(s, "*?")
}
