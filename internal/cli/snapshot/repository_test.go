package snapshot

import "testing"

func TestEffectiveVerify(t *testing.T) {
	cases := []struct {
		name            string
		verifyRequested bool
		dryRun          bool
		want            bool
	}{
		{name: "neither", verifyRequested: false, dryRun: false, want: false},
		{name: "verify only", verifyRequested: true, dryRun: false, want: true},
		{name: "dry-run only", verifyRequested: false, dryRun: true, want: false},
		{name: "verify suppressed under dry-run", verifyRequested: true, dryRun: true, want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := effectiveVerify(c.verifyRequested, c.dryRun); got != c.want {
				t.Errorf("effectiveVerify(%v, %v) = %v, want %v", c.verifyRequested, c.dryRun, got, c.want)
			}
		})
	}
}
