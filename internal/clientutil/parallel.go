package clientutil

import (
	"context"
	"sync"
)

// RunParallel fans the given closures out, waits for all of them, and
// returns the first non-nil error any of them produced. Closures run
// without an artificial concurrency cap — used where the input is a
// small fixed list (e.g. the 3 audit fetchers that can race after the
// users probe finishes), not unbounded user-provided work.
//
// We roll this rather than depend on golang.org/x/sync/errgroup so the
// project stays inside the 12-direct-dep budget. Trade-off: no shared
// context cancellation on first error — the other goroutines run to
// completion. For the few callers using this helper, that's fine: an
// audit fetcher running 100ms past a peer's failure costs nothing.
func RunParallel(fns ...func() error) error {
	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	wg.Add(len(fns))
	for _, fn := range fns {
		go func(fn func() error) {
			defer wg.Done()
			if err := fn(); err != nil {
				once.Do(func() { firstErr = err })
			}
		}(fn)
	}
	wg.Wait()
	return firstErr
}

// RunBounded runs each task with at most `concurrency` in flight at any
// moment. On the first error, the shared context is cancelled so the
// remaining tasks can observe it and exit early — important for diag
// where the auth-failure path means "stop all remaining collectors and
// abort the bundle".
//
// concurrency <= 0 falls through to len(tasks) — unbounded. Tasks
// receive the cancellable child context; they should pass it to any
// HTTP call so cancellation actually propagates.
func RunBounded(ctx context.Context, concurrency int, tasks []func(context.Context) error) error {
	if len(tasks) == 0 {
		return nil
	}
	if concurrency <= 0 || concurrency > len(tasks) {
		concurrency = len(tasks)
	}

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, concurrency)
	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	wg.Add(len(tasks))
	for _, task := range tasks {
		go func(task func(context.Context) error) {
			defer wg.Done()
			// Block until a slot is free or the context is cancelled —
			// when an earlier task failed, queued tasks bail out here
			// without running their HTTP call.
			select {
			case sem <- struct{}{}:
			case <-cctx.Done():
				return
			}
			defer func() { <-sem }()

			if err := task(cctx); err != nil {
				once.Do(func() {
					firstErr = err
					cancel()
				})
			}
		}(task)
	}
	wg.Wait()
	return firstErr
}
