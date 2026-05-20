package types

import "testing"

func TestILMPolicyDescription_DisplayName(t *testing.T) {
	d := ILMPolicyDescription{Name: "logs-30d"}
	if got := d.DisplayName(); got != "logs-30d" {
		t.Errorf("ILMPolicyDescription.DisplayName() = %q, want %q", got, "logs-30d")
	}
}

func TestISMPolicyDescription_DisplayName(t *testing.T) {
	d := ISMPolicyDescription{ID: "logs-rollover"}
	if got := d.DisplayName(); got != "logs-rollover" {
		t.Errorf("ISMPolicyDescription.DisplayName() = %q, want %q", got, "logs-rollover")
	}
}
