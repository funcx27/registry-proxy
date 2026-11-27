package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestManifestImage(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		host    string
		path    string
		mirror  string
		mapping map[string]string
		want    string
		wantOK  bool
	}{
		{
			name:   "tag on registry host",
			method: http.MethodGet,
			host:   "registry.example",
			path:   "/v2/team/nginx/manifests/latest",
			want:   "registry.example/team/nginx:latest",
			wantOK: true,
		},
		{
			name:   "digest on registry host",
			method: http.MethodHead,
			host:   "registry.example",
			path:   "/v2/team/nginx/manifests/sha256:0123456789abcdef",
			want:   "registry.example/team/nginx@sha256:0123456789abcdef",
			wantOK: true,
		},
		{
			name:   "ip host without mirror is ignored",
			method: http.MethodGet,
			host:   "127.0.0.1:5000",
			path:   "/v2/library/nginx/manifests/latest",
			wantOK: false,
		},
		{
			name:   "ip host with mirror pulls library image",
			method: http.MethodGet,
			host:   "127.0.0.1:5000",
			path:   "/v2/library/nginx/manifests/latest",
			mirror: "registry.dockermirror.com",
			want:   "registry.dockermirror.com/library/nginx:latest",
			wantOK: true,
		},
		{
			name:   "ip host with mirror ignores non-library image",
			method: http.MethodGet,
			host:   "127.0.0.1:5000",
			path:   "/v2/team/nginx/manifests/latest",
			mirror: "registry.dockermirror.com",
			wantOK: false,
		},
		{
			name:   "docker hub host with mirror rewrites library image",
			method: http.MethodGet,
			host:   "docker.io",
			path:   "/v2/library/alpine/manifests/3.20",
			mirror: "registry.dockermirror.com",
			want:   "registry.dockermirror.com/library/alpine:3.20",
			wantOK: true,
		},
		{
			name:    "proxy mapping rewrites upstream host",
			method:  http.MethodGet,
			host:    "proxy.example",
			path:    "/v2/team/nginx/manifests/latest",
			mapping: map[string]string{"proxy.example": "docker.io"},
			want:    "docker.io/team/nginx:latest",
			wantOK:  true,
		},
		{
			name:   "non manifest request is ignored",
			method: http.MethodPost,
			host:   "registry.example",
			path:   "/v2/team/nginx/manifests/latest",
			wantOK: false,
		},
		{
			name:   "blob request is ignored",
			method: http.MethodGet,
			host:   "registry.example",
			path:   "/v2/team/nginx/blobs/sha256:0123456789abcdef",
			wantOK: false,
		},
		{
			name:   "empty reference is ignored",
			method: http.MethodGet,
			host:   "registry.example",
			path:   "/v2/team/nginx/manifests/",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := &server{
				options: Options{DockerHubMirror: tt.mirror},
				mapping: tt.mapping,
			}

			r := httptest.NewRequest(tt.method, "http://"+tt.host+tt.path, nil)
			r.Host = tt.host

			got, ok := srv.manifestImage(r)
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("manifestImage() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// TestManifestDispatcherPullsTagAndDigestSeparately pins the lazy-index
// contract: the tag key and the child digest key are different cache and
// singleflight entries, so both must trigger their own copy.
func TestManifestDispatcherPullsTagAndDigestSeparately(t *testing.T) {
	var mu sync.Mutex
	var pulled []string
	srv := newTestProxyServer(t, Options{CopyMode: CopyModeSync}, nil, func(_ context.Context, _, image string) error {
		mu.Lock()
		pulled = append(pulled, image)
		mu.Unlock()
		return nil
	})

	var upstreamCalls int32
	upstream := newUpstream(t, &upstreamCalls)
	proxyServer := httptest.NewTestServer(t, srv.handler(upstream.Client().Transport))
	client := proxyServer.Client()
	baseURL := proxyServer.URL

	paths := []string{
		"/v2/team/nginx/manifests/latest",
		"/v2/team/nginx/manifests/sha256:0123456789abcdef",
	}
	for _, path := range paths {
		req, err := http.NewRequest(http.MethodHead, baseURL+path, nil)
		if err != nil {
			t.Fatalf("new request %s: %v", path, err)
		}
		req.Host = "registry.example"
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
	want := []string{
		"registry.example/team/nginx:latest",
		"registry.example/team/nginx@sha256:0123456789abcdef",
	}
	if len(pulled) != len(want) {
		t.Fatalf("pulled images = %v, want %v", pulled, want)
	}
	for i := range want {
		if pulled[i] != want[i] {
			t.Fatalf("pulled[%d] = %q, want %q", i, pulled[i], want[i])
		}
	}
}

// TestManifestImageIgnoresEmptyMirror guards the regression where an empty
// DOCKERHUB_MIRROR replaced the request host with an empty string.
func TestManifestImageIgnoresEmptyMirror(t *testing.T) {
	srv := &server{}

	r := httptest.NewRequest(http.MethodGet, "http://docker.io/v2/library/alpine/manifests/3.20", nil)
	r.Host = "docker.io"

	got, ok := srv.manifestImage(r)
	if !ok {
		t.Fatal("manifestImage() ok = false, want true")
	}
	if want := "docker.io/library/alpine:3.20"; got != want {
		t.Fatalf("manifestImage() = %q, want %q", got, want)
	}
}
