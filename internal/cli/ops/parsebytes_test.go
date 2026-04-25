package ops

import "testing"

func TestParseBytesFlag(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"1024", 1024, false},
		{"1B", 1, false},
		{"1b", 1, false},
		{"1K", 1024, false},
		{"1KiB", 1024, false},
		{"1kib", 1024, false},
		{"1KB", 1000, false},
		{"50GiB", 50 * 1024 * 1024 * 1024, false},
		{"50G", 50 * 1024 * 1024 * 1024, false},
		{"1GB", 1000 * 1000 * 1000, false},
		{"1MiB", 1024 * 1024, false},
		{"1.5GiB", int64(1.5 * 1024 * 1024 * 1024), false},
		{"  50 GiB  ", 50 * 1024 * 1024 * 1024, false}, // whitespace tolerated

		{"abc", 0, true},
		{"-5", 0, true},
		{"5XB", 0, true},
		{"GiB", 0, true}, // no number
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := parseBytesFlag(c.in)
			if c.wantErr {
				if err == nil {
					t.Errorf("parseBytesFlag(%q) = %d, want error", c.in, got)
				}
				return
			}
			if err != nil {
				t.Errorf("parseBytesFlag(%q): unexpected error %v", c.in, err)
				return
			}
			if got != c.want {
				t.Errorf("parseBytesFlag(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}
