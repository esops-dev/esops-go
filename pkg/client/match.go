package client

import (
	"path"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/pkg/types"
)

// DescriptionMentions reports whether desc references `name` as a
// complete index/stream/policy-name token — not as a substring of a
// longer name. Used by the `describe` aggregators to filter the
// cluster's /_tasks descriptions down to tasks that genuinely touch
// the resource, without `users` matching a reindex of `users-v2`.
//
// Token boundaries are anything outside the index-name byte class
// (lowercase ASCII, digits, `-`, `_`, `.`). Brackets, spaces, commas,
// colons, quotes all count as boundaries — which is exactly the
// punctuation /_tasks descriptions use to delimit names. Uppercase
// ASCII is accepted in the byte class so a description that quotes
// an operator-typed name (which may include capitals even though
// real index names are lowercase) still matches the boundary rule
// symmetrically on both sides.
func DescriptionMentions(desc, name string) bool {
	if desc == "" || name == "" {
		return false
	}
	rest := desc
	offset := 0
	for {
		idx := strings.Index(rest, name)
		if idx < 0 {
			return false
		}
		absIdx := offset + idx
		leftOK := absIdx == 0 || !isIndexNameByte(desc[absIdx-1])
		rightOK := absIdx+len(name) == len(desc) || !isIndexNameByte(desc[absIdx+len(name)])
		if leftOK && rightOK {
			return true
		}
		rest = rest[idx+1:]
		offset = absIdx + 1
	}
}

func isIndexNameByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z':
		return true
	case b >= 'A' && b <= 'Z':
		return true
	case b >= '0' && b <= '9':
		return true
	case b == '-', b == '_', b == '.':
		return true
	}
	return false
}

// MatchHighestPriorityTemplate finds the highest-priority composable
// index template whose `index_patterns` glob the supplied resource
// name. pred narrows the candidate set — pass nil to consider every
// template, or a predicate like `func(t) bool { return len(t.DataStream) > 0 }`
// to restrict matching to data-stream templates.
//
// The cluster doesn't record which template was applied at index or
// data-stream creation; this returns the template the cluster
// *would* pick for a new resource of this name today. A template
// added after the resource will show up here even though it didn't
// shape it; downstream callers surface the result honestly as
// "matched template" rather than "applied template".
//
// Returns ok=false when no candidate matches.
func MatchHighestPriorityTemplate(
	name string,
	tmpls []types.IndexTemplate,
	pred func(types.IndexTemplate) bool,
) (types.IndexTemplate, bool) {
	candidates := make([]types.IndexTemplate, 0)
	for _, t := range tmpls {
		if pred != nil && !pred(t) {
			continue
		}
		for _, pat := range t.IndexPatterns {
			ok, err := path.Match(pat, name)
			if err == nil && ok {
				candidates = append(candidates, t)
				break
			}
		}
	}
	if len(candidates) == 0 {
		return types.IndexTemplate{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Priority > candidates[j].Priority
	})
	return candidates[0], true
}
