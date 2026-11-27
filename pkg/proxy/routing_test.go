package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveRoute(t *testing.T) {
	srv := &server{mappings: map[string]RegistryMapping{
		"ghcr.io":   {},
		"docker.io": {DefaultNamespace: "library"},
		"quay.io":   {Registry: "proxy.example.com/quay.io"},
	}}

	tests := []struct {
		name          string
		host          string
		path          string
		wantNil       bool
		wantErr       bool
		wantMode      routeMode
		wantRegistry  string
		wantUpstream  string
		wantLocal     string
		wantSource    string
		wantEndpoint  string
		wantReference string
		wantPath      string
		wantImage     string
	}{
		{
			name:          "configured path mode",
			host:          "proxy.example.com",
			path:          "/v2/ghcr.io/org/app/manifests/v1",
			wantMode:      routeModePath,
			wantRegistry:  "ghcr.io",
			wantUpstream:  "ghcr.io",
			wantLocal:     "ghcr.io/org/app",
			wantSource:    "org/app",
			wantEndpoint:  "manifests",
			wantReference: "v1",
			wantPath:      "/v2/ghcr.io/org/app/manifests/v1",
			wantImage:     "ghcr.io/org/app:v1",
		},
		{
			name:          "path mode applies default namespace to one component",
			host:          "proxy.example.com",
			path:          "/v2/docker.io/alpine/manifests/3.20",
			wantMode:      routeModePath,
			wantRegistry:  "docker.io",
			wantUpstream:  "docker.io",
			wantLocal:     "docker.io/alpine",
			wantSource:    "library/alpine",
			wantEndpoint:  "manifests",
			wantReference: "3.20",
			wantPath:      "/v2/docker.io/alpine/manifests/3.20",
			wantImage:     "docker.io/library/alpine:3.20",
		},
		{
			name:          "path mode keeps multi component repository",
			host:          "proxy.example.com",
			path:          "/v2/docker.io/library/alpine/manifests/3.20",
			wantMode:      routeModePath,
			wantRegistry:  "docker.io",
			wantUpstream:  "docker.io",
			wantLocal:     "docker.io/library/alpine",
			wantSource:    "library/alpine",
			wantEndpoint:  "manifests",
			wantReference: "3.20",
			wantPath:      "/v2/docker.io/library/alpine/manifests/3.20",
			wantImage:     "docker.io/library/alpine:3.20",
		},
		{
			name:          "configured host mode is rewritten",
			host:          "ghcr.io",
			path:          "/v2/org/app/manifests/v1",
			wantMode:      routeModeHost,
			wantRegistry:  "ghcr.io",
			wantUpstream:  "ghcr.io",
			wantLocal:     "ghcr.io/org/app",
			wantSource:    "org/app",
			wantEndpoint:  "manifests",
			wantReference: "v1",
			wantPath:      "/v2/ghcr.io/org/app/manifests/v1",
			wantImage:     "ghcr.io/org/app:v1",
		},
		{
			name:          "host mode matches host without port",
			host:          "ghcr.io:5000",
			path:          "/v2/org/app/manifests/v1",
			wantMode:      routeModeHost,
			wantRegistry:  "ghcr.io",
			wantUpstream:  "ghcr.io",
			wantLocal:     "ghcr.io/org/app",
			wantSource:    "org/app",
			wantEndpoint:  "manifests",
			wantReference: "v1",
			wantPath:      "/v2/ghcr.io/org/app/manifests/v1",
			wantImage:     "ghcr.io/org/app:v1",
		},
		{
			name:          "host mode applies default namespace",
			host:          "docker.io",
			path:          "/v2/alpine/manifests/latest",
			wantMode:      routeModeHost,
			wantRegistry:  "docker.io",
			wantUpstream:  "docker.io",
			wantLocal:     "docker.io/alpine",
			wantSource:    "library/alpine",
			wantEndpoint:  "manifests",
			wantReference: "latest",
			wantPath:      "/v2/docker.io/alpine/manifests/latest",
			wantImage:     "docker.io/library/alpine:latest",
		},
		{
			name:          "namespace query mode",
			host:          "proxy.example.com",
			path:          "/v2/org/app/manifests/v1?ns=ghcr.io",
			wantMode:      routeModeNamespace,
			wantRegistry:  "ghcr.io",
			wantUpstream:  "ghcr.io",
			wantLocal:     "ghcr.io/org/app",
			wantSource:    "org/app",
			wantEndpoint:  "manifests",
			wantReference: "v1",
			wantPath:      "/v2/ghcr.io/org/app/manifests/v1",
			wantImage:     "ghcr.io/org/app:v1",
		},
		{
			name:          "automatic path mode for mapping without configuration",
			host:          "proxy.example.com",
			path:          "/v2/registry.example.com/org/app/manifests/v1",
			wantMode:      routeModePath,
			wantRegistry:  "registry.example.com",
			wantUpstream:  "registry.example.com",
			wantLocal:     "registry.example.com/org/app",
			wantSource:    "org/app",
			wantEndpoint:  "manifests",
			wantReference: "v1",
			wantPath:      "/v2/registry.example.com/org/app/manifests/v1",
			wantImage:     "registry.example.com/org/app:v1",
		},
		{
			name:          "mapping registry override includes repository prefix",
			host:          "proxy.example.com",
			path:          "/v2/quay.io/org/app/manifests/v1",
			wantMode:      routeModePath,
			wantRegistry:  "quay.io",
			wantUpstream:  "proxy.example.com/quay.io",
			wantLocal:     "quay.io/org/app",
			wantSource:    "org/app",
			wantEndpoint:  "manifests",
			wantReference: "v1",
			wantPath:      "/v2/quay.io/org/app/manifests/v1",
			wantImage:     "proxy.example.com/quay.io/org/app:v1",
		},
		{
			name:          "digest reference",
			host:          "ghcr.io",
			path:          "/v2/org/app/manifests/sha256:0123456789abcdef",
			wantMode:      routeModeHost,
			wantRegistry:  "ghcr.io",
			wantUpstream:  "ghcr.io",
			wantLocal:     "ghcr.io/org/app",
			wantSource:    "org/app",
			wantEndpoint:  "manifests",
			wantReference: "sha256:0123456789abcdef",
			wantPath:      "/v2/ghcr.io/org/app/manifests/sha256:0123456789abcdef",
			wantImage:     "ghcr.io/org/app@sha256:0123456789abcdef",
		},
		{
			name:          "repository may contain endpoint-like segment",
			host:          "ghcr.io",
			path:          "/v2/org/manifests/app/manifests/v1",
			wantMode:      routeModeHost,
			wantRegistry:  "ghcr.io",
			wantUpstream:  "ghcr.io",
			wantLocal:     "ghcr.io/org/manifests/app",
			wantSource:    "org/manifests/app",
			wantEndpoint:  "manifests",
			wantReference: "v1",
			wantPath:      "/v2/ghcr.io/org/manifests/app/manifests/v1",
			wantImage:     "ghcr.io/org/manifests/app:v1",
		},
		{
			name:          "blob request is rewritten but not pulled",
			host:          "ghcr.io",
			path:          "/v2/org/app/blobs/sha256:0123456789abcdef",
			wantMode:      routeModeHost,
			wantRegistry:  "ghcr.io",
			wantUpstream:  "ghcr.io",
			wantLocal:     "ghcr.io/org/app",
			wantSource:    "org/app",
			wantEndpoint:  "blobs",
			wantReference: "sha256:0123456789abcdef",
			wantPath:      "/v2/ghcr.io/org/app/blobs/sha256:0123456789abcdef",
		},
		{
			name:          "tags list is rewritten",
			host:          "ghcr.io",
			path:          "/v2/org/app/tags/list",
			wantMode:      routeModeHost,
			wantRegistry:  "ghcr.io",
			wantUpstream:  "ghcr.io",
			wantLocal:     "ghcr.io/org/app",
			wantSource:    "org/app",
			wantEndpoint:  "tags",
			wantReference: "list",
			wantPath:      "/v2/ghcr.io/org/app/tags/list",
		},
		{
			name:          "path mode ns is validated and removed",
			host:          "proxy.example.com",
			path:          "/v2/ghcr.io/org/app/manifests/v1?ns=ghcr.io",
			wantMode:      routeModePath,
			wantRegistry:  "ghcr.io",
			wantUpstream:  "ghcr.io",
			wantLocal:     "ghcr.io/org/app",
			wantSource:    "org/app",
			wantEndpoint:  "manifests",
			wantReference: "v1",
			wantPath:      "/v2/ghcr.io/org/app/manifests/v1",
			wantImage:     "ghcr.io/org/app:v1",
		},
		{
			name:          "uppercase registry path is normalized",
			host:          "proxy.example.com",
			path:          "/v2/GHCR.IO/org/app/manifests/v1",
			wantMode:      routeModePath,
			wantRegistry:  "ghcr.io",
			wantUpstream:  "ghcr.io",
			wantLocal:     "ghcr.io/org/app",
			wantSource:    "org/app",
			wantEndpoint:  "manifests",
			wantReference: "v1",
			wantPath:      "/v2/ghcr.io/org/app/manifests/v1",
			wantImage:     "ghcr.io/org/app:v1",
		},
		{
			name:    "unconfigured single-label host is local only",
			host:    "proxy.example.com",
			path:    "/v2/org/app/manifests/v1",
			wantNil: true,
		},
		{
			name:    "ip host is local only",
			host:    "127.0.0.1:5000",
			path:    "/v2/library/alpine/manifests/latest",
			wantNil: true,
		},
		{
			name:    "v2 root is local only",
			host:    "ghcr.io",
			path:    "/v2/",
			wantNil: true,
		},
		{
			name:    "catalog is local only",
			host:    "ghcr.io",
			path:    "/v2/_catalog",
			wantNil: true,
		},
		{
			name:    "namespace mismatch is rejected",
			host:    "proxy.example.com",
			path:    "/v2/ghcr.io/org/app/manifests/v1?ns=docker.io",
			wantErr: true,
		},
		{
			name:    "invalid namespace is rejected",
			host:    "proxy.example.com",
			path:    "/v2/org/app/manifests/v1?ns=not-a-domain",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+tt.host+tt.path, nil)
			req.Host = tt.host

			route, err := srv.resolveRoute(req)
			if tt.wantErr {
				if err == nil {
					t.Fatal("resolveRoute() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveRoute() error = %v", err)
			}
			if tt.wantNil {
				if route != nil {
					t.Fatalf("resolveRoute() = %#v, want nil", route)
				}
				return
			}
			if route == nil {
				t.Fatal("resolveRoute() = nil, want route")
			}
			if route.mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", route.mode, tt.wantMode)
			}
			if route.registry != tt.wantRegistry {
				t.Errorf("registry = %q, want %q", route.registry, tt.wantRegistry)
			}
			if route.upstream != tt.wantUpstream {
				t.Errorf("upstream = %q, want %q", route.upstream, tt.wantUpstream)
			}
			if route.localRepository != tt.wantLocal {
				t.Errorf("localRepository = %q, want %q", route.localRepository, tt.wantLocal)
			}
			if route.sourceRepository != tt.wantSource {
				t.Errorf("sourceRepository = %q, want %q", route.sourceRepository, tt.wantSource)
			}
			if route.endpoint != tt.wantEndpoint {
				t.Errorf("endpoint = %q, want %q", route.endpoint, tt.wantEndpoint)
			}
			if route.reference != tt.wantReference {
				t.Errorf("reference = %q, want %q", route.reference, tt.wantReference)
			}

			route.apply(req)
			if req.URL.Path != tt.wantPath {
				t.Errorf("rewritten path = %q, want %q", req.URL.Path, tt.wantPath)
			}
			if got := req.URL.Query().Get("ns"); got != "" {
				t.Errorf("ns query = %q, want empty", got)
			}

			image, ok := route.sourceImage()
			if tt.wantImage == "" {
				if ok {
					t.Errorf("sourceImage() = %q, want no image", image)
				}
			} else if !ok || image != tt.wantImage {
				t.Errorf("sourceImage() = (%q, %v), want %q", image, ok, tt.wantImage)
			}
		})
	}
}

func TestIsRegistryDomain(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "ghcr.io", want: true},
		{name: "registry.example.com", want: true},
		{name: "registry.example.com:5000", want: true},
		{name: "localhost", want: false},
		{name: "127.0.0.1", want: false},
		{name: "127.0.0.1:5000", want: false},
		{name: "-bad.example.com", want: false},
		{name: "bad_.example.com", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRegistryDomain(tt.name); got != tt.want {
				t.Fatalf("isRegistryDomain(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
