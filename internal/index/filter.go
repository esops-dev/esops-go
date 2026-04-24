package index

import (
	"fmt"
	"strings"

	"github.com/dj-wasabi/esops-go/internal/types"
)

// ParseSort turns a --sort string into the typed IndexSort value used
// by IndexFilter. Accepts the empty string as an alias for "name" so
// callers don't need to special-case an unset flag.
func ParseSort(s string) (types.IndexSort, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "name":
		return types.IndexSortName, nil
	case "size":
		return types.IndexSortSize, nil
	case "docs":
		return types.IndexSortDocs, nil
	default:
		return 0, fmt.Errorf("unknown --sort value %q (supported: name, size, docs)", s)
	}
}

// ValidateHealth rejects any --health value that isn't a known cluster
// health colour. Cluster APIs return lowercase; we accept any case and
// the business layer compares case-insensitively.
func ValidateHealth(s string) error {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "green", "yellow", "red":
		return nil
	default:
		return fmt.Errorf("unknown --health value %q (supported: green, yellow, red)", s)
	}
}

// ValidateStatus rejects any --status value that isn't "open" or "close".
// Matches the values the cluster returns on /_cat/indices.
func ValidateStatus(s string) error {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "open", "close":
		return nil
	default:
		return fmt.Errorf("unknown --status value %q (supported: open, close)", s)
	}
}
