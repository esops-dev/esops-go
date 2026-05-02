package index_test

import (
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/index"
	"github.com/esops-dev/esops-go/pkg/types"
)

func TestParseSort_knownValues(t *testing.T) {
	cases := []struct {
		in   string
		want types.IndexSort
	}{
		{"", types.IndexSortName},
		{"name", types.IndexSortName},
		{"NAME", types.IndexSortName},
		{" size ", types.IndexSortSize},
		{"docs", types.IndexSortDocs},
	}
	for _, c := range cases {
		got, err := index.ParseSort(c.in)
		if err != nil {
			t.Errorf("ParseSort(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseSort(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseSort_rejectsUnknown(t *testing.T) {
	_, err := index.ParseSort("age")
	if err == nil || !strings.Contains(err.Error(), "age") {
		t.Errorf("expected error naming the rejected value, got: %v", err)
	}
}

func TestValidateHealth_colours(t *testing.T) {
	for _, ok := range []string{"green", "YELLOW", " red "} {
		if err := index.ValidateHealth(ok); err != nil {
			t.Errorf("ValidateHealth(%q) error: %v", ok, err)
		}
	}
}

func TestValidateHealth_rejectsOther(t *testing.T) {
	if err := index.ValidateHealth("purple"); err == nil {
		t.Error("expected error for unknown health")
	}
}

func TestValidateStatus_openClose(t *testing.T) {
	for _, ok := range []string{"open", "CLOSE"} {
		if err := index.ValidateStatus(ok); err != nil {
			t.Errorf("ValidateStatus(%q) error: %v", ok, err)
		}
	}
}

func TestValidateStatus_rejectsOther(t *testing.T) {
	if err := index.ValidateStatus("quarantined"); err == nil {
		t.Error("expected error for unknown status")
	}
}
