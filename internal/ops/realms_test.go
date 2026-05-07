package ops_test

import (
	"context"
	"errors"
	"testing"

	"github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/pkg/types"
)

type fakeRealmsInspector struct {
	realms []types.Realm
	err    error
}

func (f *fakeRealmsInspector) Realms(_ context.Context) ([]types.Realm, error) {
	return f.realms, f.err
}

func TestListRealms_propagatesError(t *testing.T) {
	want := errors.New("boom")
	fake := &fakeRealmsInspector{err: want}
	_, err := ops.ListRealms(context.Background(), fake)
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

func TestListRealms_sortsByOrderThenTypeThenName(t *testing.T) {
	fake := &fakeRealmsInspector{realms: []types.Realm{
		// Adapters already sort, but the business helper is the
		// single source of truth for ordering, so we re-apply.
		{Name: "b_ldap", Type: "ldap", Order: 10},
		{Name: "a_saml", Type: "saml", Order: 10},
		{Name: "native", Type: "native", Order: 1},
	}}
	got, err := ops.ListRealms(context.Background(), fake)
	if err != nil {
		t.Fatalf("ListRealms: %v", err)
	}
	want := []string{"native", "b_ldap", "a_saml"}
	for i, r := range got {
		if r.Name != want[i] {
			t.Errorf("realms[%d] = %q, want %q", i, r.Name, want[i])
		}
	}
}

func TestListRealms_emptySliceWhenNoneConfigured(t *testing.T) {
	fake := &fakeRealmsInspector{realms: nil}
	got, err := ops.ListRealms(context.Background(), fake)
	if err != nil {
		t.Fatalf("ListRealms: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %d", len(got))
	}
}
