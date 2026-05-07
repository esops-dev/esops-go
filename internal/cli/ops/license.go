package ops

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	esopsops "github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/internal/output"
)

func licenseCommand() *cli.Command {
	return &cli.Command{
		Name:  "license",
		Usage: "Show the cluster's installed licence (ES only)",
		Description: `Read-only listing over GET /_license — the cluster's currently
installed commercial licence. Reports status, type, expiry, and
node cap.

Default columns: status, type, issued_to, expires, max_nodes. A
basic licence (the default Elasticsearch ships with) lands as
"active / basic / (no expiry)". A trial or paid licence reports its
expiry timestamp.

-o wide adds the issuer, issue date, and UID for support tickets.

Pass --warn-days N to make the command CI-pluggable: it still
renders the licence to stdout, but exits non-zero (code 1) when
ExpiresAt is within N days of now (or already past). Basic licences
have no expiry and never trigger the warning. Default 0 (disabled).

Elasticsearch only. OpenSearch has no equivalent commercial-licence
surface; the command exits with code 6 (operation unsupported on
this cluster dialect) when targeted at an OS context.`,
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:  "warn-days",
				Usage: "Exit non-zero when ExpiresAt is within N days of now (0 disables; basic licences are never flagged)",
			},
		},
		Action: runLicense,
	}
}

func runLicense(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	lic, err := esopsops.GetLicense(ctx, s.Client.License)
	if err != nil {
		return err
	}

	expiresIn := ""
	if lic.ExpiresAt != nil {
		expiresIn = lic.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	warnDays := cmd.Int("warn-days")
	s.Log("ops.license",
		"status", lic.Status,
		"type", lic.Type,
		"expires_at", expiresIn,
		"max_nodes", lic.MaxNodes,
		"warn_days", warnDays,
	)
	if err := output.License(os.Stdout, lic, s.Format); err != nil {
		return err
	}
	// Render first, then evaluate the warning. Operators piping to
	// jq still get the licence document; the non-zero exit signals
	// the alarm without losing the data.
	return checkLicenseExpiry(lic.ExpiresAt, warnDays, time.Now())
}

// checkLicenseExpiry compares ExpiresAt against now+warnDays and
// returns a non-nil error when the licence is within the threshold
// (already expired counts). A nil ExpiresAt — basic licences are
// the canonical case — never triggers, regardless of warnDays.
//
// Pulled out for testability: time.Now is injected so the warn
// boundary can be exercised deterministically.
func checkLicenseExpiry(expiresAt *time.Time, warnDays int, now time.Time) error {
	if warnDays <= 0 || expiresAt == nil {
		return nil
	}
	until := expiresAt.Sub(now)
	threshold := time.Duration(warnDays) * 24 * time.Hour
	if until > threshold {
		return nil
	}
	formatted := expiresAt.UTC().Format(time.RFC3339)
	if until < 0 {
		return fmt.Errorf("license expired %d days ago (%s)",
			int(-until.Hours()/24), formatted)
	}
	return fmt.Errorf("license expires in %d days (%s) — within --warn-days=%d threshold",
		int(until.Hours()/24), formatted, warnDays)
}
