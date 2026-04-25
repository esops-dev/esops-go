package ops

import (
	"fmt"
	"strconv"
	"strings"
)

// parseBytesFlag turns a CLI byte-size string into bytes. Accepts:
//
//   - bare number: "5368709120"  → bytes
//   - SI decimal:  "1KB" / "1MB" / "1GB" / "1TB" / "1PB"  (×1000)
//   - IEC binary:  "1KiB" / "1MiB" / "1GiB" / "1TiB" / "1PiB"  (×1024)
//   - bare "B" / "b": bytes
//
// Empty string returns 0 with no error so AnalyzeShards's "0 = use the
// default" convention works directly. Suffixes are case-insensitive
// only on the unit letter — "1Kib" and "1kib" both parse as 1024.
//
// Errors are formatted for direct surfacing to the operator; the value
// in the message is the input as received so they can see what went wrong.
func parseBytesFlag(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}

	num, suffix := splitBytesSuffix(s)
	if num == "" {
		return 0, fmt.Errorf("invalid byte value %q: expected a number with optional unit (e.g. 50GiB, 1024)", s)
	}

	mult, err := bytesMultiplier(suffix)
	if err != nil {
		return 0, fmt.Errorf("invalid byte value %q: %w", s, err)
	}

	// Float parse handles fractional inputs ("1.5GiB"); we collapse to
	// int64 at the end. Fractional bytes round toward zero — which is
	// what an operator typing "0.5GiB" actually means at the threshold.
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid byte value %q: %w", s, err)
	}
	if v < 0 {
		return 0, fmt.Errorf("invalid byte value %q: must be non-negative", s)
	}
	return int64(v * float64(mult)), nil
}

// splitBytesSuffix peels the trailing alpha suffix off a byte-value
// string. "50GiB" → ("50", "GiB"); "1024" → ("1024", ""); "1.5G" →
// ("1.5", "G"). Whitespace between the number and the suffix is
// tolerated ("50 GiB").
func splitBytesSuffix(s string) (number, suffix string) {
	i := len(s)
	for i > 0 {
		c := s[i-1]
		if (c >= '0' && c <= '9') || c == '.' {
			break
		}
		i--
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i:])
}

// bytesMultiplier maps a unit suffix to its byte count. SI suffixes
// (KB / MB / GB / TB / PB) use 1000; IEC suffixes (KiB / MiB / GiB /
// TiB / PiB) and the bare single-char K / M / G / T / P forms use 1024
// — the K=1024 default matches what `du -h` and most ops tools surface.
func bytesMultiplier(suffix string) (int64, error) {
	const (
		kb = int64(1024)
		mb = kb * 1024
		gb = mb * 1024
		tb = gb * 1024
		pb = tb * 1024
	)
	switch strings.ToLower(suffix) {
	case "", "b":
		return 1, nil
	case "k", "kib":
		return kb, nil
	case "m", "mib":
		return mb, nil
	case "g", "gib":
		return gb, nil
	case "t", "tib":
		return tb, nil
	case "p", "pib":
		return pb, nil
	case "kb":
		return 1000, nil
	case "mb":
		return 1000 * 1000, nil
	case "gb":
		return 1000 * 1000 * 1000, nil
	case "tb":
		return 1000 * 1000 * 1000 * 1000, nil
	case "pb":
		return 1000 * 1000 * 1000 * 1000 * 1000, nil
	default:
		return 0, fmt.Errorf("unknown unit %q (expected: B, K/KiB/KB, M/MiB/MB, G/GiB/GB, T/TiB/TB, P/PiB/PB)", suffix)
	}
}
