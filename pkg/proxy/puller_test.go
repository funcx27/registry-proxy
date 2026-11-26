package proxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/patrickmn/go-cache"
)

func TestPullPoolCoalescesConcurrentPulls(t *testing.T) {
	ctx := context.Background()
	cache := cache.New(time.Minute, time.Minute)

	var calls int32
	var startOnce sync.Once
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
	})

	syncFn := func(context.Context, string, string) error {
		atomic.AddInt32(&calls, 1)
		startOnce.Do(func() { close(started) })
		<-release
		return nil
	}

	pool := newPullPool(ctx, 1, 0, cache, syncFn)
	t.Cleanup(pool.Close)

	const requests = 20
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	wg.Add(requests)
	for i := 0; i < requests; i++ {
		go func() {
			defer wg.Done()
			errs <- pool.ensure(ctx, "127.0.0.1:5000", "docker.io/library/nginx:latest")
		}()
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("pull did not start")
	}

	releaseOnce.Do(func() { close(release) })
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("ensure() error = %v", err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("sync calls = %d, want 1", got)
	}
}

func TestPullPoolRetriesAfterFailure(t *testing.T) {
	ctx := context.Background()
	cache := cache.New(time.Minute, time.Minute)

	var calls int32
	syncFn := func(context.Context, string, string) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			return errors.New("upstream unavailable")
		}
		return nil
	}

	pool := newPullPool(ctx, 1, 0, cache, syncFn)
	t.Cleanup(pool.Close)

	if err := pool.ensure(ctx, "127.0.0.1:5000", "docker.io/library/nginx:latest"); err == nil {
		t.Fatal("first ensure() error = nil, want failure")
	}
	if err := pool.ensure(ctx, "127.0.0.1:5000", "docker.io/library/nginx:latest"); err != nil {
		t.Fatalf("second ensure() error = %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("sync calls = %d, want 2", got)
	}
}

func TestPullPoolSkipsCachedImage(t *testing.T) {
	ctx := context.Background()
	cache := cache.New(time.Minute, time.Minute)
	const image = "docker.io/library/nginx:latest"
	cache.SetDefault(image, struct{}{})

	var calls int32
	syncFn := func(context.Context, string, string) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}

	pool := newPullPool(ctx, 1, 0, cache, syncFn)
	t.Cleanup(pool.Close)

	if err := pool.ensure(ctx, "127.0.0.1:5000", image); err != nil {
		t.Fatalf("ensure() error = %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("sync calls = %d, want 0", got)
	}
}

func TestPullPoolPropagatesPanicAsError(t *testing.T) {
	ctx := context.Background()
	cache := cache.New(time.Minute, time.Minute)

	syncFn := func(context.Context, string, string) error {
		panic("boom")
	}

	pool := newPullPool(ctx, 1, 0, cache, syncFn)
	t.Cleanup(pool.Close)

	err := pool.ensure(ctx, "127.0.0.1:5000", "docker.io/library/nginx:latest")
	if err == nil {
		t.Fatal("ensure() error = nil, want panic converted to error")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Fatalf("ensure() error = %v, want panic error", err)
	}
}

func TestPullPoolQueueWaitHonorsContext(t *testing.T) {
	ctx := context.Background()
	cache := cache.New(time.Minute, time.Minute)

	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
	})

	syncFn := func(context.Context, string, string) error {
		startOnce.Do(func() { close(started) })
		<-release
		return nil
	}

	pool := newPullPool(ctx, 1, 0, cache, syncFn)
	t.Cleanup(pool.Close)

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- pool.ensure(ctx, "127.0.0.1:5000", "docker.io/library/nginx:latest")
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first pull did not start")
	}

	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	err := pool.ensure(waitCtx, "127.0.0.1:5000", "docker.io/library/busybox:latest")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued ensure() error = %v, want context deadline exceeded", err)
	}

	releaseOnce.Do(func() { close(release) })
	if err := <-firstDone; err != nil {
		t.Fatalf("first ensure() error = %v", err)
	}
}

func TestPullPoolCloseFailsQueuedAndNewWork(t *testing.T) {
	ctx := context.Background()
	cache := cache.New(time.Minute, time.Minute)

	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
	})

	syncFn := func(context.Context, string, string) error {
		startOnce.Do(func() { close(started) })
		<-release
		return nil
	}

	pool := newPullPool(ctx, 1, 1, cache, syncFn)

	firstDone := make(chan error, 1)
	go func() { firstDone <- pool.ensure(ctx, "127.0.0.1:5000", "docker.io/library/nginx:latest") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first pull did not start")
	}

	secondDone := make(chan error, 1)
	go func() { secondDone <- pool.ensure(ctx, "127.0.0.1:5000", "docker.io/library/busybox:latest") }()

	deadline := time.Now().Add(time.Second)
	for len(pool.jobs) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(pool.jobs) == 0 {
		t.Fatal("second job was not queued")
	}

	closed := make(chan struct{})
	go func() {
		pool.Close()
		close(closed)
	}()

	releaseOnce.Do(func() { close(release) })

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close() did not return")
	}

	if err := <-firstDone; err != nil {
		t.Fatalf("first ensure() error = %v", err)
	}
	// A worker may still pick up the job while shutting down; either outcome
	// is fine as long as the caller is released.
	select {
	case err := <-secondDone:
		if err != nil && !errors.Is(err, errPullPoolClosed) {
			t.Fatalf("queued ensure() error = %v, want nil or errPullPoolClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued ensure() blocked after Close()")
	}

	if err := pool.ensure(ctx, "127.0.0.1:5000", "docker.io/library/alpine:latest"); !errors.Is(err, errPullPoolClosed) {
		t.Fatalf("ensure() after Close() error = %v, want errPullPoolClosed", err)
	}

	pool.Close() // idempotent
}

// TestPullPoolLeaderTimeoutKeepsFollowerPull reproduces the narrow window where
// the first (leader) caller times out after its job is already queued. The
// flight must stay alive so a later caller still receives the real result
// instead of starting a duplicate pull. A fake clock keeps this deterministic.
func TestPullPoolLeaderTimeoutKeepsFollowerPull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls int32
		started := make(chan struct{})
		release := make(chan struct{})
		var startOnce sync.Once
		syncFn := func(context.Context, string, string) error {
			atomic.AddInt32(&calls, 1)
			startOnce.Do(func() { close(started) })
			<-release
			return nil
		}

		// A nil cache keeps go-cache's janitor goroutine outside the bubble.
		pool := newPullPool(t.Context(), 1, 0, nil, syncFn)
		t.Cleanup(pool.Close)

		leaderCtx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		leaderDone := make(chan error, 1)
		go func() { leaderDone <- pool.ensure(leaderCtx, "127.0.0.1:5000", "repo/app:1") }()

		<-started
		time.Sleep(2 * time.Second) // advances the fake clock past the leader deadline

		select {
		case err := <-leaderDone:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("leader ensure() error = %v, want deadline exceeded", err)
			}
		default:
			t.Fatal("leader ensure() did not time out")
		}

		followerDone := make(chan error, 1)
		go func() { followerDone <- pool.ensure(t.Context(), "127.0.0.1:5000", "repo/app:1") }()
		synctest.Wait() // follower has joined the still-running flight

		close(release)
		if err := <-followerDone; err != nil {
			t.Fatalf("follower ensure() error = %v", err)
		}
		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Fatalf("sync calls = %d, want 1", got)
		}
	})
}

// TestPullPoolCloseDoesNotStrandCallers hammers the Close/enqueue handoff window
// so the race detector and the bounded timeout both get a chance to catch a
// stranded producer.
func TestPullPoolCloseDoesNotStrandCallers(t *testing.T) {
	for round := 0; round < 20; round++ {
		pool := newPullPool(context.Background(), 2, 2, cache.New(time.Minute, time.Minute),
			func(context.Context, string, string) error { return nil })

		const callers = 16
		var wg sync.WaitGroup
		wg.Add(callers)
		for i := 0; i < callers; i++ {
			go func(i int) {
				defer wg.Done()
				err := pool.ensure(context.Background(), "127.0.0.1:5000", fmt.Sprintf("repo/app:%d", i))
				if err != nil && !errors.Is(err, errPullPoolClosed) {
					t.Errorf("ensure() error = %v, want nil or errPullPoolClosed", err)
				}
			}(i)
		}

		pool.Close()

		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("ensure() callers stranded after Close()")
		}
		pool.Close() // idempotent
	}
}
