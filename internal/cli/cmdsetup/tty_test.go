package cmdsetup

import (
	"bytes"
	"os"
	"testing"
)

func TestIsTTY_nonFileWriter(t *testing.T) {
	if IsTTY(&bytes.Buffer{}) {
		t.Error("bytes.Buffer must not be reported as a TTY")
	}
}

func TestIsTTY_regularFile(t *testing.T) {
	// A plain temp file is the shape `esops ops health --watch > out.log`
	// or `ops apikey create > key.json` hits — the writer is an *os.File
	// but it's not a character device. IsTTY must return false so the
	// secret-printing path doesn't refuse on a redirected stdout.
	f, err := os.CreateTemp(t.TempDir(), "tty-check")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer func() { _ = f.Close() }()
	if IsTTY(f) {
		t.Error("regular file must not be reported as a TTY")
	}
}
