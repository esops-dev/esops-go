package cluster

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestBoundedTransport_capsConcurrency stands up a slow HTTP server,
// fires more requests than the cap allows, and verifies the in-flight
// counter never exceeds the configured limit.
func TestBoundedTransport_capsConcurrency(t *testing.T) {
	const cap = 2

	var inFlight int32
	var maxSeen int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := atomic.AddInt32(&inFlight, 1)
		for {
			seen := atomic.LoadInt32(&maxSeen)
			if now <= seen || atomic.CompareAndSwapInt32(&maxSeen, seen, now) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	rt := newBoundedTransport(http.DefaultTransport, cap)
	c := &http.Client{Transport: rt}

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			resp, err := c.Do(req)
			if err != nil {
				t.Errorf("request: %v", err)
				return
			}
			_ = resp.Body.Close()
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&maxSeen); got > cap {
		t.Errorf("maxSeen = %d, exceeded cap %d", got, cap)
	}
}

// TestBoundedTransport_releasesOnError verifies a transport-level
// error doesn't leak a slot — the next request must still get through.
func TestBoundedTransport_releasesOnError(t *testing.T) {
	rt := newBoundedTransport(&errorTransport{err: errors.New("boom")}, 1)
	c := &http.Client{Transport: rt}

	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodGet, "http://invalid.example", nil)
		_, err := c.Do(req)
		if err == nil {
			t.Fatalf("iter %d: expected error, got nil", i)
		}
	}
	// All three must complete; if a slot leaked, the second iteration
	// would block forever (the test would time out).
}

// TestBoundedTransport_contextCancelDoesNotLeakSlot ensures the
// acquire path honours the request context — a cancelled request
// shouldn't grab a slot then never release it.
func TestBoundedTransport_contextCancelDoesNotLeakSlot(t *testing.T) {
	// Cap of 1, with one slot held by a long-running request.
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-gate
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	rt := newBoundedTransport(http.DefaultTransport, 1)
	c := &http.Client{Transport: rt}

	// Start request A; it'll hold the slot.
	done := make(chan struct{})
	go func() {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		resp, err := c.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		close(done)
	}()

	// Give A time to acquire the slot.
	time.Sleep(20 * time.Millisecond)

	// Request B with a context that cancels almost immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	_, err := c.Do(req)
	if err == nil {
		t.Fatal("expected cancelled context error, got nil")
	}

	// Release A so the test can finish.
	close(gate)
	<-done
}

type errorTransport struct{ err error }

func (e *errorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, e.err
}
