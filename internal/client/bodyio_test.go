package client

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestBoundedReader_belowCap_passesThrough(t *testing.T) {
	r := NewBoundedReader(strings.NewReader("hello"), 100, "test")
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestBoundedReader_atCap_passesThrough(t *testing.T) {
	r := NewBoundedReader(strings.NewReader("hello"), 5, "test")
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestBoundedReader_overCap_returnsErrResponseTooLarge(t *testing.T) {
	r := NewBoundedReader(strings.NewReader("hello world"), 5, "test")
	_, err := io.ReadAll(r)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("err = %v, want ErrResponseTooLarge", err)
	}
	if !strings.Contains(err.Error(), "test") {
		t.Errorf("err missing op label: %v", err)
	}
}

func TestBoundedReader_overCap_chunkedReads(t *testing.T) {
	// Simulate a stream that returns one byte at a time so the
	// trip-detection path inside BoundedReader gets exercised.
	r := NewBoundedReader(&oneByteReader{src: bytes.NewReader([]byte("abcdefgh"))}, 4, "test")
	_, err := io.ReadAll(r)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("err = %v, want ErrResponseTooLarge", err)
	}
}

func TestBoundedReader_zeroCap_isUncapped(t *testing.T) {
	r := NewBoundedReader(strings.NewReader("anything"), 0, "test")
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "anything" {
		t.Errorf("got %q, want %q", got, "anything")
	}
}

// oneByteReader emits one byte per Read call so test cases can exercise
// the "exactly at limit, next call brings more" path without depending
// on bytes.Reader's internal chunking.
type oneByteReader struct {
	src io.Reader
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return r.src.Read(p[:1])
}
