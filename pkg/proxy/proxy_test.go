package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/patrickmn/go-cache"
)

// usePuller swaps the package-level puller for the duration of a test.
func usePuller(t *testing.T, syncFn func(context.Context, string, string) error) *pullPool {
	t.Helper()

	pool := newPullPool(t.Context(), 1, 0, cache.New(time.Minute, time.Minute), syncFn)
	previous := imagePuller
	imagePuller = pool
	t.Cleanup(func() {
		imagePuller = previous
		pool.Close()
	})
	return pool
}

func newUpstream(t *testing.T, calls *int32) *httptest.Server {
	t.Helper()

	return httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.WriteHeader(http.StatusOK)
	}))
}

const manifestPath = "http://registry.example/v2/team/nginx/manifests/latest"

func TestProxyPullFailureReturnsBadGateway(t *testing.T) {
	t.Setenv(imageCopyModekey, "sync")

	var upstreamCalls int32
	upstream := newUpstream(t, &upstreamCalls)

	usePuller(t, func(context.Context, string, string) error {
		return errors.New("upstream registry unavailable")
	})

	proxyServer := httptest.NewTestServer(t, newProxyHandler("127.0.0.1:5000", upstream.Client().Transport))
	resp, err := proxyServer.Client().Head(manifestPath)
	if err != nil {
		t.Fatalf("HEAD %s: %v", manifestPath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
	if got := resp.Header.Get("Docker-Distribution-API-Version"); got != "registry/2.0" {
		t.Fatalf("Docker-Distribution-API-Version = %q, want registry/2.0", got)
	}
	if got := atomic.LoadInt32(&upstreamCalls); got != 0 {
		t.Fatalf("upstream calls = %d, want 0 (request must not be forwarded)", got)
	}
}

func TestProxyPullTimeoutReturnsGatewayTimeout(t *testing.T) {
	t.Setenv(imageCopyModekey, "sync")

	previousTimeout := imagePullTimeout
	imagePullTimeout = 30 * time.Millisecond
	t.Cleanup(func() { imagePullTimeout = previousTimeout })

	var upstreamCalls int32
	upstream := newUpstream(t, &upstreamCalls)

	release := make(chan struct{})
	var releaseOnce sync.Once
	usePuller(t, func(context.Context, string, string) error {
		<-release
		return nil
	})
	// Cleanups run LIFO: unblock the pull before the pool is closed.
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	proxyServer := httptest.NewTestServer(t, newProxyHandler("127.0.0.1:5000", upstream.Client().Transport))
	resp, err := proxyServer.Client().Head(manifestPath)
	if err != nil {
		t.Fatalf("HEAD %s: %v", manifestPath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusGatewayTimeout)
	}
	if got := atomic.LoadInt32(&upstreamCalls); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
}

func TestProxyConcurrentManifestRequestsPullOnce(t *testing.T) {
	t.Setenv(imageCopyModekey, "sync")

	var syncCalls, upstreamCalls int32
	upstream := newUpstream(t, &upstreamCalls)

	release := make(chan struct{})
	var releaseOnce sync.Once
	usePuller(t, func(context.Context, string, string) error {
		atomic.AddInt32(&syncCalls, 1)
		<-release
		return nil
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	const requests = 8
	entered := make(chan struct{}, requests)
	proxyHandler := newProxyHandler("127.0.0.1:5000", upstream.Client().Transport)
	proxyServer := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		proxyHandler.ServeHTTP(w, r)
	}))
	client := proxyServer.Client()

	statuses := make(chan int, requests)
	var wg sync.WaitGroup
	wg.Add(requests)
	for i := 0; i < requests; i++ {
		go func() {
			defer wg.Done()
			resp, err := client.Head(manifestPath)
			if err != nil {
				statuses <- 0
				return
			}
			_ = resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}

	// Let every request reach the proxy while the pull is still blocked, so
	// they all join the same in-flight pull.
	for i := 0; i < requests; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("requests did not reach the proxy handler")
		}
	}

	releaseOnce.Do(func() { close(release) })
	wg.Wait()
	close(statuses)

	for status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
	}
	if got := atomic.LoadInt32(&syncCalls); got != 1 {
		t.Fatalf("sync calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&upstreamCalls); got != requests {
		t.Fatalf("upstream calls = %d, want %d", got, requests)
	}
}

// TestManifestDispatcherAsyncDoesNotBlockRequest pins the contract of async
// mode: the manifest request is proxied immediately while the copy runs in the
// background.
func TestManifestDispatcherAsyncDoesNotBlockRequest(t *testing.T) {
	t.Setenv(imageCopyModekey, "async")

	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	usePuller(t, func(context.Context, string, string) error {
		startOnce.Do(func() { close(started) })
		<-release
		return nil
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	req := httptest.NewRequest(http.MethodHead, "/v2/team/nginx/manifests/latest", nil)
	req.Host = "registry.example"

	dispatched := make(chan http.Handler, 1)
	go func() { dispatched <- manifestDispatcherCustom("127.0.0.1:5000", req) }()

	select {
	case handler := <-dispatched:
		if handler != nil {
			t.Fatalf("async dispatcher returned an error handler; want nil so the request is proxied")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("async dispatcher blocked the manifest request")
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background pull did not start")
	}
}
