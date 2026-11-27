package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestDescribeCopyResult(t *testing.T) {
	tests := []struct {
		name   string
		result copyResult
		want   []string
	}{
		{
			name: "index only copies the manifest",
			result: copyResult{
				kind:      "index",
				mediaType: "application/vnd.oci.image.index.v1+json",
				children:  8,
				size:      1746,
			},
			want: []string{
				"index ghcr.io/org/app:latest in 123ms",
				"media type: application/vnd.oci.image.index.v1+json",
				"children: 8",
				"manifest size: 1.7KiB",
				"child manifests and layers are copied on demand",
			},
		},
		{
			name: "single platform image copies config and layers",
			result: copyResult{
				kind:      "image",
				mediaType: "application/vnd.oci.image.manifest.v1+json",
				layers:    7,
				size:      60 * 1024 * 1024,
			},
			want: []string{
				"image ghcr.io/org/app:latest in 123ms",
				"media type: application/vnd.oci.image.manifest.v1+json",
				"layers: 7",
				"size: 60.0MiB",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeCopyResult("ghcr.io/org/app:latest", tt.result, 123*time.Millisecond)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Fatalf("describeCopyResult() = %q, missing %q", got, want)
				}
			}
		})
	}
}

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		size int64
		want string
	}{
		{0, "-"},
		{-1, "-"},
		{512, "512B"},
		{1024, "1.0KiB"},
		{1746, "1.7KiB"},
		{60 * 1024 * 1024, "60.0MiB"},
	}
	for _, tt := range tests {
		if got := humanBytes(tt.size); got != tt.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tt.size, got, tt.want)
		}
	}
}

func TestLogClientPullRequest(t *testing.T) {
	previous := logger
	t.Cleanup(func() { logger = previous })

	recorder := captureLogs(t, logrus.DebugLevel)

	req := httptest.NewRequest(http.MethodGet,
		"http://registry.example.com/v2/example.com/org/app/manifests/latest", nil)
	req.RemoteAddr = "192.0.2.9:5000"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 192.0.2.2")
	req.Header.Set("User-Agent", "containerd/2.0.0")
	route := &registryRoute{mode: routeModeHost, upstream: "example.com", sourceRepository: "org/app", endpoint: "manifests", reference: "latest"}

	logClientPullRequest(req, route, req.URL.Path, http.StatusOK, 123*time.Millisecond)

	if len(recorder.entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(recorder.entries))
	}
	entry := recorder.entries[0]
	if entry.Level != logrus.InfoLevel {
		t.Fatalf("log level = %s, want info", entry.Level)
	}
	for _, want := range []string{
		"method=GET",
		"client=203.0.113.7",
		"host=registry.example.com",
		"path=/v2/example.com/org/app/manifests/latest",
		"mode=host",
		"upstream=example.com",
		"source=example.com/org/app:latest",
		"status=200",
		"duration=123ms",
		`user_agent="containerd/2.0.0"`,
	} {
		if !strings.Contains(entry.Message, want) {
			t.Fatalf("log message = %q, missing %q", entry.Message, want)
		}
	}
}

func TestLogClientPullRequestDigestIsDebug(t *testing.T) {
	previous := logger
	t.Cleanup(func() { logger = previous })

	recorder := captureLogs(t, logrus.DebugLevel)
	req := httptest.NewRequest(http.MethodGet,
		"http://registry.example.com/v2/example.com/org/app/manifests/sha256:0123456789abcdef", nil)
	req.RemoteAddr = "192.0.2.9:5000"
	route := &registryRoute{mode: routeModeHost, upstream: "example.com", reference: "sha256:0123456789abcdef"}

	logClientPullRequest(req, route, req.URL.Path, http.StatusOK, time.Millisecond)

	if len(recorder.entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(recorder.entries))
	}
	if got := recorder.entries[0].Level; got != logrus.DebugLevel {
		t.Fatalf("log level = %s, want debug", got)
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		want       string
	}{
		{
			name:       "forwarded for wins and first hop is used",
			remoteAddr: "192.0.2.9:5000",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.7, 192.0.2.2"},
			want:       "203.0.113.7",
		},
		{
			name:       "real ip is the fallback",
			remoteAddr: "192.0.2.9:5000",
			headers:    map[string]string{"X-Real-IP": "198.51.100.5"},
			want:       "198.51.100.5",
		},
		{
			name:       "remote addr is the last resort",
			remoteAddr: "192.0.2.10:4321",
			want:       "192.0.2.10",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://registry.example.com/v2/", nil)
			req.RemoteAddr = tt.remoteAddr
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}
			if got := clientIP(req); got != tt.want {
				t.Fatalf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestManifestRequestLoggingSkipsBlobs pins the signal-to-noise contract:
// manifest requests identify the puller, while blob traffic stays silent.
func TestManifestRequestLoggingSkipsBlobs(t *testing.T) {
	previous := logger
	t.Cleanup(func() { logger = previous })
	recorder := captureLogs(t, logrus.DebugLevel)

	var upstreamCalls int32
	upstream := newUpstream(t, &upstreamCalls)
	srv := newTestProxyServer(t, Options{CopyMode: CopyModeOff}, nil, nil)
	proxyServer := httptest.NewTestServer(t, srv.handler(upstream.Client().Transport))
	client := proxyServer.Client()

	newRequest := func(method, path string) *http.Request {
		req, err := http.NewRequest(method, proxyServer.URL+path, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Host = "ghcr.io"
		req.Header.Set("X-Real-IP", "198.51.100.5")
		return req
	}

	resp, err := client.Do(newRequest(http.MethodGet, "/v2/org/app/manifests/latest"))
	if err != nil {
		t.Fatalf("manifest request: %v", err)
	}
	_ = resp.Body.Close()

	resp, err = client.Do(newRequest(http.MethodGet, "/v2/org/app/blobs/sha256:0123456789abcdef"))
	if err != nil {
		t.Fatalf("blob request: %v", err)
	}
	_ = resp.Body.Close()

	pullLogs := 0
	for _, entry := range recorder.entries {
		if strings.Contains(entry.Message, "pull request:") {
			pullLogs++
			if !strings.Contains(entry.Message, "client=198.51.100.5") {
				t.Fatalf("pull log = %q, missing client IP", entry.Message)
			}
		}
	}
	if pullLogs != 1 {
		t.Fatalf("pull request logs = %d, want 1", pullLogs)
	}
}

func captureLogs(t *testing.T, level logrus.Level) *levelRecorder {
	t.Helper()
	appLogger := logrus.New()
	appLogger.SetOutput(io.Discard)
	appLogger.SetLevel(level)
	recorder := &levelRecorder{}
	appLogger.AddHook(recorder)
	logger = appLogger
	return recorder
}
