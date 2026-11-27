package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/regclient/regclient/types/errs"
	"github.com/sirupsen/logrus"
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

	syncFn := func(context.Context, string, string, string) (copyResult, error) {
		atomic.AddInt32(&calls, 1)
		startOnce.Do(func() { close(started) })
		<-release
		return copyResult{}, nil
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
			errs <- pool.ensure(ctx, "127.0.0.1:5000", "docker.io/library/nginx:latest", "docker.io/library/nginx:latest")
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

func TestPullPoolFailureLogLevel(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantLevel   logrus.Level
		wantMessage string
	}{
		{
			name:        "upstream not found warns",
			err:         fmt.Errorf("get manifest: %w", errs.ErrNotFound),
			wantLevel:   logrus.WarnLevel,
			wantMessage: "not found after",
		},
		{
			name:        "transport failure stays an error",
			err:         errors.New("dial tcp: connection refused"),
			wantLevel:   logrus.ErrorLevel,
			wantMessage: "failed after",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			previous := logger
			t.Cleanup(func() { logger = previous })

			appLogger := logrus.New()
			appLogger.SetOutput(io.Discard)
			recorder := &levelRecorder{}
			appLogger.AddHook(recorder)
			logger = appLogger

			pool := newPullPool(context.Background(), 1, 0, nil, func(context.Context, string, string, string) (copyResult, error) {
				return copyResult{}, tt.err
			})
			t.Cleanup(pool.Close)

			if err := pool.ensure(context.Background(), "127.0.0.1:5000", "docker.io/library/nginx:latest", "docker.io/library/nginx:latest"); err == nil {
				t.Fatal("ensure() error = nil, want failure")
			}
			if len(recorder.entries) != 1 {
				t.Fatalf("log entries = %d, want exactly 1", len(recorder.entries))
			}
			entry := recorder.entries[0]
			if entry.Level != tt.wantLevel {
				t.Fatalf("log level = %s, want %s", entry.Level, tt.wantLevel)
			}
			if !strings.Contains(entry.Message, tt.wantMessage) {
				t.Fatalf("log message = %q, want it to contain %q", entry.Message, tt.wantMessage)
			}
		})
	}
}

func TestPullPoolDigestLogLevel(t *testing.T) {
	tests := []struct {
		name      string
		digest    string
		delay     time.Duration
		wantLevel logrus.Level
	}{
		{
			name:      "fast child manifest copy stays debug",
			digest:    "sha256:1111",
			wantLevel: logrus.DebugLevel,
		},
		{
			name:      "slow child copy explains user latency at info",
			digest:    "sha256:2222",
			delay:     slowDigestPull + 10*time.Millisecond,
			wantLevel: logrus.InfoLevel,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			previous := logger
			t.Cleanup(func() { logger = previous })

			appLogger := logrus.New()
			appLogger.SetOutput(io.Discard)
			// Enable debug so the filtered-out case can still be asserted
			// instead of silently producing no entry.
			appLogger.SetLevel(logrus.DebugLevel)
			recorder := &levelRecorder{}
			appLogger.AddHook(recorder)
			logger = appLogger

			pool := newPullPool(context.Background(), 1, 0, nil, func(context.Context, string, string, string) (copyResult, error) {
				time.Sleep(tt.delay)
				return copyResult{}, nil
			})
			t.Cleanup(pool.Close)

			source := "registry.example.com/dockerhub/library/nginx@" + tt.digest
			if err := pool.ensure(context.Background(), "127.0.0.1:5000", source, "registry.example.com/dockerhub/library/nginx@"+tt.digest); err != nil {
				t.Fatalf("ensure() error = %v", err)
			}
			var completed *logrus.Entry
			for _, entry := range recorder.entries {
				if strings.HasPrefix(entry.Message, "copied ") {
					completed = entry
				}
			}
			if completed == nil {
				t.Fatalf("no completion log in %d entries", len(recorder.entries))
			}
			if completed.Level != tt.wantLevel {
				t.Fatalf("log level = %s, want %s", completed.Level, tt.wantLevel)
			}
		})
	}
}

type levelRecorder struct {
	entries []*logrus.Entry
}

func (r *levelRecorder) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (r *levelRecorder) Fire(entry *logrus.Entry) error {
	r.entries = append(r.entries, entry)
	return nil
}

func TestPullPoolRetriesAfterFailure(t *testing.T) {
	ctx := context.Background()
	cache := cache.New(time.Minute, time.Minute)

	var calls int32
	syncFn := func(context.Context, string, string, string) (copyResult, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return copyResult{}, errors.New("upstream unavailable")
		}
		return copyResult{}, nil
	}

	pool := newPullPool(ctx, 1, 0, cache, syncFn)
	t.Cleanup(pool.Close)

	if err := pool.ensure(ctx, "127.0.0.1:5000", "docker.io/library/nginx:latest", "docker.io/library/nginx:latest"); err == nil {
		t.Fatal("first ensure() error = nil, want failure")
	}
	if err := pool.ensure(ctx, "127.0.0.1:5000", "docker.io/library/nginx:latest", "docker.io/library/nginx:latest"); err != nil {
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
	cache.SetDefault(pullCacheKey("127.0.0.1:5000", image), struct{}{})

	var calls int32
	syncFn := func(context.Context, string, string, string) (copyResult, error) {
		atomic.AddInt32(&calls, 1)
		return copyResult{}, nil
	}

	pool := newPullPool(ctx, 1, 0, cache, syncFn)
	t.Cleanup(pool.Close)

	if err := pool.ensure(ctx, "127.0.0.1:5000", image, image); err != nil {
		t.Fatalf("ensure() error = %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("sync calls = %d, want 0", got)
	}
}

func TestPullPoolPropagatesPanicAsError(t *testing.T) {
	ctx := context.Background()
	cache := cache.New(time.Minute, time.Minute)

	syncFn := func(context.Context, string, string, string) (copyResult, error) {
		panic("boom")
	}

	pool := newPullPool(ctx, 1, 0, cache, syncFn)
	t.Cleanup(pool.Close)

	err := pool.ensure(ctx, "127.0.0.1:5000", "docker.io/library/nginx:latest", "docker.io/library/nginx:latest")
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

	syncFn := func(context.Context, string, string, string) (copyResult, error) {
		startOnce.Do(func() { close(started) })
		<-release
		return copyResult{}, nil
	}

	pool := newPullPool(ctx, 1, 0, cache, syncFn)
	t.Cleanup(pool.Close)

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- pool.ensure(ctx, "127.0.0.1:5000", "docker.io/library/nginx:latest", "docker.io/library/nginx:latest")
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first pull did not start")
	}

	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	err := pool.ensure(waitCtx, "127.0.0.1:5000", "docker.io/library/busybox:latest", "docker.io/library/busybox:latest")
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

	syncFn := func(context.Context, string, string, string) (copyResult, error) {
		startOnce.Do(func() { close(started) })
		<-release
		return copyResult{}, nil
	}

	pool := newPullPool(ctx, 1, 1, cache, syncFn)

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- pool.ensure(ctx, "127.0.0.1:5000", "docker.io/library/nginx:latest", "docker.io/library/nginx:latest")
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first pull did not start")
	}

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- pool.ensure(ctx, "127.0.0.1:5000", "docker.io/library/busybox:latest", "docker.io/library/busybox:latest")
	}()

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

	if err := pool.ensure(ctx, "127.0.0.1:5000", "docker.io/library/alpine:latest", "docker.io/library/alpine:latest"); !errors.Is(err, errPullPoolClosed) {
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
		syncFn := func(context.Context, string, string, string) (copyResult, error) {
			atomic.AddInt32(&calls, 1)
			startOnce.Do(func() { close(started) })
			<-release
			return copyResult{}, nil
		}

		// A nil cache keeps go-cache's janitor goroutine outside the bubble.
		pool := newPullPool(t.Context(), 1, 0, nil, syncFn)
		t.Cleanup(pool.Close)

		leaderCtx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		leaderDone := make(chan error, 1)
		go func() { leaderDone <- pool.ensure(leaderCtx, "127.0.0.1:5000", "repo/app:1", "repo/app:1") }()

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
		go func() { followerDone <- pool.ensure(t.Context(), "127.0.0.1:5000", "repo/app:1", "repo/app:1") }()
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
			func(context.Context, string, string, string) (copyResult, error) { return copyResult{}, nil })

		const callers = 16
		var wg sync.WaitGroup
		wg.Add(callers)
		for i := 0; i < callers; i++ {
			go func(i int) {
				defer wg.Done()
				image := fmt.Sprintf("repo/app:%d", i)
				err := pool.ensure(context.Background(), "127.0.0.1:5000", image, image)
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

func TestIsDigestReference(t *testing.T) {
	tests := []struct {
		image string
		want  bool
	}{
		{"registry.example.com/dockerhub/alpine/socat:latest", false},
		{"127.0.0.1:5000/library/nginx", false},
		{"registry.example.com/dockerhub/alpine/socat@sha256:" + strings.Repeat("a", 64), true},
	}
	for _, tt := range tests {
		if got := isDigestReference(tt.image); got != tt.want {
			t.Errorf("isDigestReference(%q) = %v, want %v", tt.image, got, tt.want)
		}
	}
}
