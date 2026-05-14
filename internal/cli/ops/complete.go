package ops

import (
	"github.com/esops-dev/esops-go/internal/cli/completion"
)

// Shared ShellComplete maps for `ops *` subcommands. The flag-name
// conventions are stable across the ops tree:
//
//   --index  → SuggestIndices (single or comma-joined patterns)
//   --node   → SuggestNodes   (a single node name)
//   --nodes  → SuggestNodes   (comma-joined; same source set)
//
// Hoisted because the same flags appear on a dozen call sites and
// drift between them would be a real maintenance trap.
var (
	indexFlagSuggesters = map[string]completion.Suggester{
		"index": completion.SuggestIndices,
	}
	nodeFlagSuggesters = map[string]completion.Suggester{
		"node":  completion.SuggestNodes,
		"nodes": completion.SuggestNodes,
	}
	indexAndNodeSuggesters = map[string]completion.Suggester{
		"index": completion.SuggestIndices,
		"node":  completion.SuggestNodes,
		"nodes": completion.SuggestNodes,
	}
)
