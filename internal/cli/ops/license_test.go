package ops

import (
	"strings"
	"testing"
	"time"
)

func TestCheckLicenseExpiry_disabledWhenWarnDaysZero(t *testing.T) {
	expiry := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 7, 23, 59, 0, 0, time.UTC)
	// One day to expiry — but warn-days=0 means "feature disabled",
	// even when an alarm would otherwise fire.
	if err := checkLicenseExpiry(&expiry, 0, now); err != nil {
		t.Errorf("warn-days=0 should never warn, got %v", err)
	}
}

func TestCheckLicenseExpiry_basicLicenceNeverWarns(t *testing.T) {
	now := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	// Basic licence — ExpiresAt nil. Even an aggressive warn-days
	// must not fabricate a warning.
	if err := checkLicenseExpiry(nil, 365, now); err != nil {
		t.Errorf("basic licence (nil ExpiresAt) should never warn, got %v", err)
	}
}

func TestCheckLicenseExpiry_withinThresholdReturnsError(t *testing.T) {
	now := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(5 * 24 * time.Hour)
	err := checkLicenseExpiry(&expiry, 7, now)
	if err == nil {
		t.Fatalf("expected error when expiry is 5d out and warn-days=7, got nil")
	}
	if !strings.Contains(err.Error(), "expires in") || !strings.Contains(err.Error(), "warn-days=7") {
		t.Errorf("error message missing expected fragments: %v", err)
	}
}

func TestCheckLicenseExpiry_outsideThresholdReturnsNil(t *testing.T) {
	now := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(60 * 24 * time.Hour)
	if err := checkLicenseExpiry(&expiry, 30, now); err != nil {
		t.Errorf("expiry 60d out / warn-days=30 should not warn, got %v", err)
	}
}

func TestCheckLicenseExpiry_alreadyExpiredReportsAge(t *testing.T) {
	now := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(-3 * 24 * time.Hour)
	err := checkLicenseExpiry(&expiry, 7, now)
	if err == nil {
		t.Fatalf("expected error for already-expired licence")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error should report expiry age, got: %v", err)
	}
	if strings.Contains(err.Error(), "expires in") {
		t.Errorf("expired licence should not say 'expires in': %v", err)
	}
}

func TestCheckLicenseExpiry_exactBoundaryWarns(t *testing.T) {
	// Boundary case — expiry exactly at the threshold. The
	// `until <= threshold` comparison fires here, which is the
	// CI-friendly choice: a job scheduled to run "30 days before
	// expiry" at the same time of day should trigger on day 30,
	// not silently miss the day before.
	now := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(30 * 24 * time.Hour)
	if err := checkLicenseExpiry(&expiry, 30, now); err == nil {
		t.Errorf("boundary case (expiry == threshold) should warn, got nil")
	}
}
