package cmdsetup

import (
	"io"
	"os"
)

// IsTTY reports whether w is a *os.File wrapping a character device.
// A non-File writer (bytes.Buffer in tests, anything custom) is
// treated as non-TTY, which is the safe default — never emit ANSI
// sequences or refuse-to-print-secrets gates against something we
// can't confirm is a terminal.
//
// Shared here so the `watch` and `ops apikey create` paths use the
// same check; a third caller (interactive prompter, progress bar)
// drops in without copying the helper a third time.
func IsTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
