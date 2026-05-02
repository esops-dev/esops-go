package ops_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeHotThreadsInspector captures the request it received so parameter
// plumbing is testable end-to-end without a real cluster.
type fakeHotThreadsInspector struct {
	got types.HotThreadsRequest
	out types.HotThreads
	err error
}

func (f *fakeHotThreadsInspector) HotThreads(_ context.Context, req types.HotThreadsRequest) (types.HotThreads, error) {
	f.got = req
	return f.out, f.err
}

func intPtr(v int) *int { return &v }

func TestHotThreads_passesRequestThrough(t *testing.T) {
	fake := &fakeHotThreadsInspector{
		out: types.HotThreads{Text: "::: {es01}\n  3% cpu\n"},
	}
	got, err := ops.HotThreads(context.Background(), fake, types.HotThreadsRequest{
		Nodes:     []string{"es01"},
		Threads:   intPtr(5),
		Interval:  2 * time.Second,
		Snapshots: intPtr(20),
		Type:      "cpu",
	})
	if err != nil {
		t.Fatalf("HotThreads: %v", err)
	}
	if got.Text != "::: {es01}\n  3% cpu\n" {
		t.Errorf("Text = %q, want cluster's original body", got.Text)
	}
	if fake.got.Nodes[0] != "es01" || *fake.got.Threads != 5 || fake.got.Interval != 2*time.Second {
		t.Errorf("adapter saw %+v, want Nodes=[es01] Threads=5 Interval=2s", fake.got)
	}
}

func TestHotThreads_rejectsInvalidType(t *testing.T) {
	fake := &fakeHotThreadsInspector{}
	_, err := ops.HotThreads(context.Background(), fake, types.HotThreadsRequest{Type: "disk"})
	if err == nil {
		t.Fatalf("expected error for invalid --type=disk")
	}
	if !strings.Contains(err.Error(), "cpu, wait, block, mem") {
		t.Errorf("err = %v, want mention of the four valid types", err)
	}
}

func TestHotThreads_lowercasesType(t *testing.T) {
	// Users type "CPU" all the time — the cluster accepts only lowercase
	// so we normalise locally rather than round-tripping to a 400.
	fake := &fakeHotThreadsInspector{}
	_, err := ops.HotThreads(context.Background(), fake, types.HotThreadsRequest{Type: "CPU"})
	if err != nil {
		t.Fatalf("HotThreads: %v", err)
	}
	if fake.got.Type != "cpu" {
		t.Errorf("adapter saw Type=%q, want normalised 'cpu'", fake.got.Type)
	}
}

func TestHotThreads_rejectsNonPositiveThreads(t *testing.T) {
	fake := &fakeHotThreadsInspector{}
	for _, v := range []int{0, -1} {
		_, err := ops.HotThreads(context.Background(), fake, types.HotThreadsRequest{Threads: intPtr(v)})
		if err == nil {
			t.Errorf("Threads=%d: expected validation error", v)
		}
	}
}

func TestHotThreads_rejectsNonPositiveSnapshots(t *testing.T) {
	fake := &fakeHotThreadsInspector{}
	for _, v := range []int{0, -2} {
		_, err := ops.HotThreads(context.Background(), fake, types.HotThreadsRequest{Snapshots: intPtr(v)})
		if err == nil {
			t.Errorf("Snapshots=%d: expected validation error", v)
		}
	}
}

func TestHotThreads_propagatesAdapterError(t *testing.T) {
	sentinel := errors.New("boom")
	fake := &fakeHotThreadsInspector{err: sentinel}
	_, err := ops.HotThreads(context.Background(), fake, types.HotThreadsRequest{})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestOutputHotThreads_rawTextForTable(t *testing.T) {
	var buf bytes.Buffer
	body := "::: {es01}\n  3% cpu usage by thread \"elasticsearch[...]\"\n"
	if err := output.HotThreads(&buf, types.HotThreads{Text: body}, output.FormatTable); err != nil {
		t.Fatalf("output.HotThreads: %v", err)
	}
	if buf.String() != body {
		t.Errorf("table output = %q, want verbatim cluster text", buf.String())
	}
}

func TestOutputHotThreads_appendsTrailingNewline(t *testing.T) {
	// The cluster occasionally omits the final newline on empty or
	// idle-filtered responses. Preserving a trailing newline keeps the
	// shell prompt from landing on the last line of the stack sample.
	var buf bytes.Buffer
	if err := output.HotThreads(&buf, types.HotThreads{Text: "no-newline"}, output.FormatTable); err != nil {
		t.Fatalf("output.HotThreads: %v", err)
	}
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("output missing trailing newline: %q", buf.String())
	}
}

func TestOutputHotThreads_jsonWrapsText(t *testing.T) {
	var buf bytes.Buffer
	if err := output.HotThreads(&buf, types.HotThreads{Text: "::: {es01}\n  hot\n"}, output.FormatJSON); err != nil {
		t.Fatalf("output.HotThreads: %v", err)
	}
	var decoded struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON decode: %v (%q)", err, buf.String())
	}
	if decoded.Text != "::: {es01}\n  hot\n" {
		t.Errorf("decoded.text = %q, want the verbatim body", decoded.Text)
	}
}
