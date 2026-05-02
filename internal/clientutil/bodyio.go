package clientutil

import (
	"errors"
	"fmt"
	"io"

	"github.com/esops-dev/esops-go/pkg/client"
)

// DefaultMaxResponseBytes is the default cap for cluster response
// bodies. Set generously: a /_cat/indices on a 100k-index cluster is a
// few MB, an `ism explain --show-policy` on a multi-thousand-index
// fleet can run into tens of MB. The cap is here to fail clean on a
// runaway response (a misconfigured plugin returning gigabytes) rather
// than to constrain normal traffic.
const DefaultMaxResponseBytes int64 = 128 * 1024 * 1024 // 128 MiB

// BoundedReader wraps r and refuses to read past max bytes. It returns
// ErrResponseTooLarge (wrapped) when the underlying stream emits more
// data than the cap. Used by adapters around resp.Body so the JSON
// decoder doesn't try to buffer a runaway response into the operator's
// heap.
//
// The standard io.LimitReader truncates silently — the decoder would
// then see a clipped JSON blob and fail with a generic parse error.
// BoundedReader fails with a typed error the caller can surface
// distinctly (and that the exit-code mapper picks up).
type BoundedReader struct {
	r       io.Reader
	max     int64
	read    int64
	op      string // short label e.g. "cat indices" — appears in error
	tripped bool
}

// NewBoundedReader returns a reader that fails with ErrResponseTooLarge
// once the underlying stream has emitted more than max bytes. op is a
// short label used in the error (e.g. "cat indices", "ism explain") so
// the operator can identify which read tripped. A non-positive max is
// treated as "no cap" — the reader is returned as-is.
func NewBoundedReader(r io.Reader, max int64, op string) io.Reader {
	if max <= 0 {
		return r
	}
	return &BoundedReader{r: r, max: max, op: op}
}

func (b *BoundedReader) Read(p []byte) (int, error) {
	if b.tripped {
		return 0, b.tooLargeErr()
	}
	remaining := b.max - b.read
	if remaining <= 0 {
		// Try one more byte to confirm the stream is actually over the
		// cap (vs. exactly at it with a clean EOF coming next).
		var probe [1]byte
		n, err := b.r.Read(probe[:])
		if n > 0 {
			b.tripped = true
			return 0, b.tooLargeErr()
		}
		if errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
		return 0, err
	}
	if int64(len(p)) > remaining+1 {
		// Allow one byte past the cap so we can detect "exactly at the
		// limit" vs "over the limit" without an extra read on the next
		// call. The trip is consumed (and error returned) on the next
		// Read invocation if data continues.
		p = p[:remaining+1]
	}
	n, err := b.r.Read(p)
	b.read += int64(n)
	if b.read > b.max {
		b.tripped = true
		return int(remaining), b.tooLargeErr()
	}
	return n, err
}

func (b *BoundedReader) tooLargeErr() error {
	return fmt.Errorf("%w: %s response exceeded %d bytes", client.ErrResponseTooLarge, b.op, b.max)
}
