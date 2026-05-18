package transform

import "time"

// Test-only hooks so package-internal helpers (nowFn) can be swapped
// out from the *_test package without exporting them.

// SetNowFn replaces the clock used to derive CheckpointAgeMillis.
// The matching ResetNowFn restores the default.
func SetNowFn(fn func() time.Time) { nowFn = fn }

// ResetNowFn restores nowFn to time.Now in UTC.
func ResetNowFn() { nowFn = func() time.Time { return time.Now().UTC() } }
