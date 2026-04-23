package config

import (
	"fmt"
	"os"
	"regexp"
)

// currentContextLine matches a top-level `current-context:` key on its own
// line. We deliberately rewrite only that line rather than round-tripping
// the whole YAML so comments, ordering, and formatting survive intact.
var currentContextLine = regexp.MustCompile(`(?m)^current-context:[^\n]*$`)

// SetCurrentContext rewrites the `current-context` value in the config
// file at path. If the key is present it is replaced in place; otherwise
// a new line is inserted at the top of the file. File permissions are
// preserved by os.WriteFile.
func SetCurrentContext(path, name string) error {
	raw, err := os.ReadFile(path) // #nosec G304 -- path is the user-selected config file

	if err != nil {
		return fmt.Errorf("reading config %q: %w", path, err)
	}
	newLine := fmt.Sprintf("current-context: %s", name)
	var out []byte
	if currentContextLine.Match(raw) {
		out = currentContextLine.ReplaceAll(raw, []byte(newLine))
	} else {
		out = []byte(newLine + "\n")
		if len(raw) > 0 && raw[0] != '\n' {
			out = append(out, '\n')
		}
		out = append(out, raw...)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("writing config %q: %w", path, err)
	}
	return nil
}
