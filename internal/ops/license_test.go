package ops_test

import (
	"context"
	"errors"
	"testing"

	"github.com/esops-dev/esops-go/internal/ops"
	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

type fakeLicenseInspector struct {
	lic types.LicenseStatus
	err error
}

func (f *fakeLicenseInspector) License(_ context.Context) (types.LicenseStatus, error) {
	return f.lic, f.err
}

func TestGetLicense_propagatesError(t *testing.T) {
	fake := &fakeLicenseInspector{err: esopsclient.ErrUnsupported}
	_, err := ops.GetLicense(context.Background(), fake)
	if !errors.Is(err, esopsclient.ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

func TestGetLicense_returnsStatus(t *testing.T) {
	want := types.LicenseStatus{Status: "active", Type: "basic"}
	fake := &fakeLicenseInspector{lic: want}
	got, err := ops.GetLicense(context.Background(), fake)
	if err != nil {
		t.Fatalf("GetLicense: %v", err)
	}
	if got != want {
		t.Errorf("got = %#v, want %#v", got, want)
	}
}
