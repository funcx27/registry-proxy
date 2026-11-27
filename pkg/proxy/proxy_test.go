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

// newTestProxyServer builds a server with a deterministic pull pool so tests
// never touch package-level state or the real registry.
func newTestProxyServer(
	t *testing.T,
	opts Options,
	mappings map[string]RegistryMapping,
	syncFn func(context.Context, string, string, string) error,
) *server {
	t.Helper()

	if opts.CopyMode == "" {
		opts.CopyMode = CopyModeSync
	}
	if opts.PullTimeout == 0 {
		opts.PullTimeout = time.Minute
	}

	srv := &server{
		registryAddr: "127.0.0.1:5000",
		instanceID:   "test-instance",
		options:      opts,
		mappings:     mappings,
	}
	if opts.CopyMode == CopyModeOff {
		return srv
	}
	if syncFn == nil {
		syncFn = func(context.Context, string, string, string) error { return nil }
	}

	pool := newPullPool(t.Context(), 1, 0, cache.New(time.Minute, time.Minute), syncFn)
	t.Cleanup(pool.Close)
	srv.puller = pool
	return srv
}

func newUpstream(t *testing.T, calls *int32) *httptest.Server {
	t.Helper()

	return httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.WriteHeader(http.StatusOK)
	}))
}

const (
	manifestPath      = "http://ghcr.io/v2/org/app/manifests/latest"
	manifestProxyPath = "/v2/org/app/manifests/latest"
)

func TestProxyPullFailureReturnsBadGateway(t *testing.T) {
	var upstreamCalls int32
	upstream := newUpstream(t, &upstreamCalls)

	srv := newTestProxyServer(t, Options{CopyMode: CopyModeSync}, map[string]RegistryMapping{
		"ghcr.io": {},
	}, func(context.Context, string, string, string) error {
		return errors.New("upstream registry unavailable")
	})

	proxyServer := httptest.NewTestServer(t, srv.handler(upstream.Client().Transport))
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
	var upstreamCalls int32
	upstream := newUpstream(t, &upstreamCalls)

	release := make(chan struct{})
	var releaseOnce sync.Once
	srv := newTestProxyServer(t, Options{
		CopyMode:    CopyModeSync,
		PullTimeout: 30 * time.Millisecond,
	}, map[string]RegistryMapping{"ghcr.io": {}}, func(context.Context, string, string, string) error {
		<-release
		return nil
	})
	// Cleanups run LIFO: unblock the pull before the pool is closed.
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	proxyServer := httptest.NewTestServer(t, srv.handler(upstream.Client().Transport))
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

// TestHostModeWithoutMappingPullsFromHost pins that a DNS Host header selects
// its own upstream registry even when no mapping entry exists.
func TestHostModeWithoutMappingPullsFromHost(t *testing.T) {
	var upstreamCalls int32
	var gotPath string
	upstream := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamCalls, 1)
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))

	pulled := make(chan string, 1)
	srv := newTestProxyServer(t, Options{CopyMode: CopyModeSync}, nil,
		func(_ context.Context, _ string, source, local string) error {
			pulled <- source + " -> " + local
			return nil
		})
	proxyServer := httptest.NewTestServer(t, srv.handler(upstream.Client().Transport))
	client := proxyServer.Client()

	req, err := http.NewRequest(http.MethodHead, proxyServer.URL+manifestProxyPath, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = "ghcr.io"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	gotPull := <-pulled
	if want := "ghcr.io/org/app:latest -> ghcr.io/org/app:latest"; gotPull != want {
		t.Fatalf("pull = %q, want %q", gotPull, want)
	}
	if want := "/v2/ghcr.io/org/app/manifests/latest"; gotPath != want {
		t.Fatalf("upstream path = %q, want %q", gotPath, want)
	}
	if got := atomic.LoadInt32(&upstreamCalls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
}

func TestProxyConcurrentManifestRequestsPullOnce(t *testing.T) {
	var syncCalls, upstreamCalls int32
	upstream := newUpstream(t, &upstreamCalls)

	release := make(chan struct{})
	var releaseOnce sync.Once
	srv := newTestProxyServer(t, Options{CopyMode: CopyModeSync}, map[string]RegistryMapping{
		"ghcr.io": {},
	}, func(context.Context, string, string, string) error {
		atomic.AddInt32(&syncCalls, 1)
		<-release
		return nil
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	const requests = 8
	entered := make(chan struct{}, requests)
	proxyHandler := srv.handler(upstream.Client().Transport)
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
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	srv := newTestProxyServer(t, Options{CopyMode: CopyModeAsync}, map[string]RegistryMapping{
		"ghcr.io": {},
	}, func(context.Context, string, string, string) error {
		startOnce.Do(func() { close(started) })
		<-release
		return nil
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	var upstreamCalls int32
	upstream := newUpstream(t, &upstreamCalls)
	proxyServer := httptest.NewTestServer(t, srv.handler(upstream.Client().Transport))
	client := proxyServer.Client()

	req, err := http.NewRequest(http.MethodHead, proxyServer.URL+manifestProxyPath, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = "ghcr.io"

	done := make(chan int, 1)
	go func() {
		resp, err := client.Do(req)
		if err != nil {
			done <- 0
			return
		}
		_ = resp.Body.Close()
		done <- resp.StatusCode
	}()

	select {
	case status := <-done:
		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
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

// TestOffModeRewritesHostRouteWithoutPull pins that Host/ns path normalization
// is a routing feature and remains active when on-demand copying is disabled.
func TestOffModeRewritesHostRouteWithoutPull(t *testing.T) {
	var upstreamCalls int32
	var gotPath string
	upstream := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamCalls, 1)
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))

	srv := newTestProxyServer(t, Options{CopyMode: CopyModeOff}, map[string]RegistryMapping{
		"ghcr.io": {},
	}, nil)
	proxyServer := httptest.NewTestServer(t, srv.handler(upstream.Client().Transport))
	client := proxyServer.Client()

	req, err := http.NewRequest(http.MethodHead, proxyServer.URL+manifestProxyPath, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = "ghcr.io"

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if want := "/v2/ghcr.io/org/app/manifests/latest"; gotPath != want {
		t.Fatalf("upstream path = %q, want %q", gotPath, want)
	}
	if got := atomic.LoadInt32(&upstreamCalls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
}
