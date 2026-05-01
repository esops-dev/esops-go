package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// unmarshalSettings parses raw bytes from path into out, choosing the
// codec by extension. JSON for `.json`, YAML for `.yaml` / `.yml`. An
// unknown extension is rejected up-front so a typo doesn't produce a
// confusing decoder error half-way through parsing.
func unmarshalSettings(data []byte, path string, out *map[string]any) error {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".json"):
		return json.Unmarshal(data, out)
	case strings.HasSuffix(lower, ".yaml"), strings.HasSuffix(lower, ".yml"):
		return yaml.Unmarshal(data, out)
	default:
		return fmt.Errorf("expected a .json/.yaml/.yml file (got %q)", path)
	}
}

// loadKVMapFile parses a YAML or JSON file into a map[string]any. Used
// by --metadata-file (snapshot create) and --index-settings-file
// (snapshot restore). Returns the empty map for an empty file so the
// caller can still detect "operator passed the flag but the file had
// nothing in it" by inspecting len.
func loadKVMapFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is the operator-supplied --metadata-file / --index-settings-file flag
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	out := make(map[string]any)
	if err := unmarshalSettings(data, path, &out); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", path, err)
	}
	return out, nil
}
