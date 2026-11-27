package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestManifestDispatcherPullsTagAndDigestSeparately pins the lazy-index
// contract: the tag key and the child digest key are different cache and
// singleflight entries, so both must trigger their own copy.
func TestManifestDispatcherPullsTagAndDigestSeparately(t *testing.T) {
	var mu sync.Mutex
	type copied struct {
		source string
		local  string
	}
	var pulled []copied
	srv := newTestProxyServer(t, Options{CopyMode: CopyModeSync}, map[string]RegistryMapping{
		"ghcr.io": {},
	}, func(_ context.Context, _, source, local string) error {
		mu.Lock()
		pulled = append(pulled, copied{source: source, local: local})
		mu.Unlock()
		return nil
	})

	var upstreamCalls int32
	upstream := newUpstream(t, &upstreamCalls)
	proxyServer := httptest.NewTestServer(t, srv.handler(upstream.Client().Transport))
	client := proxyServer.Client()
	baseURL := proxyServer.URL

	paths := []string{
		"/v2/org/app/manifests/latest",
		"/v2/org/app/manifests/sha256:0123456789abcdef",
	}
	for _, path := range paths {
		req, err := http.NewRequest(http.MethodHead, baseURL+path, nil)
		if err != nil {
			t.Fatalf("new request %s: %v", path, err)
		}
		req.Host = "ghcr.io"
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("HEAD %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("HEAD %s status = %d, want %d", path, resp.StatusCode, http.StatusOK)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	want := []copied{
		{source: "ghcr.io/org/app:latest", local: "ghcr.io/org/app:latest"},
		{source: "ghcr.io/org/app@sha256:0123456789abcdef", local: "ghcr.io/org/app@sha256:0123456789abcdef"},
	}
	if len(pulled) != len(want) {
		t.Fatalf("pulled images = %v, want %v", pulled, want)
	}
	for i := range want {
		if pulled[i] != want[i] {
			t.Fatalf("pulled[%d] = %#v, want %#v", i, pulled[i], want[i])
		}
	}
}
