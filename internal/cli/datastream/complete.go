package datastream

import (
	"github.com/esops-dev/esops-go/internal/cli/completion"
)

// dsNameSuggesters drives tab-completion for `datastream *` subcommands.
// Every verb here keys off --name → data-stream IDs, so the map is
// hoisted to a single declaration.
var dsNameSuggesters = map[string]completion.Suggester{
	"name": completion.SuggestDataStreams,
}
