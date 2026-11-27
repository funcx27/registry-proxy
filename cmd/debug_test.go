package cmd

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/distribution/distribution/v3/configuration"
	"github.com/sirupsen/logrus"
)

func testDebugLogger() *logrus.Logger {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return log
}

func TestResolveDebugAddr(t *testing.T) {
	tests := []struct {
		name     string
		appAddr  string
		yamlAddr string
		want     string
		enabled  bool
	}{
		{
			name:    "defaults to loopback ephemeral port",
			want:    "127.0.0.1:0",
			enabled: true,
		},
		{
			name:     "yaml address overrides the default",
			yamlAddr: "127.0.0.1:6060",
			want:     "127.0.0.1:6060",
			enabled:  true,
		},
		{
			name:     "environment address overrides yaml",
			appAddr:  "127.0.0.1:7070",
			yamlAddr: "127.0.0.1:6060",
			want:     "127.0.0.1:7070",
			enabled:  true,
		},
		{
			name:     "off disables even when yaml sets an address",
			appAddr:  "off",
			yamlAddr: "127.0.0.1:6060",
			enabled:  false,
		},
		{
			name:    "false is treated as off",
			appAddr: " FALSE ",
			enabled: false,
		},
		{
			name:    "addresses are trimmed",
			appAddr: " 127.0.0.1:7070 ",
			want:    "127.0.0.1:7070",
			enabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, enabled := resolveDebugAddr(tt.appAddr, tt.yamlAddr)
			if addr != tt.want || enabled != tt.enabled {
				t.Fatalf("resolveDebugAddr(%q, %q) = (%q, %v), want (%q, %v)", tt.appAddr, tt.yamlAddr, addr, enabled, tt.want, tt.enabled)
			}
		})
	}
}

func TestDebugServerServesPprof(t *testing.T) {
	srv, err := startDebugServer("127.0.0.1:0", configuration.Prometheus{}, testDebugLogger())
	if err != nil {
		t.Fatalf("startDebugServer() error = %v", err)
	}
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	host, port, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("SplitHostPort(%q) error = %v", srv.Addr(), err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("host = %q, want 127.0.0.1", host)
	}
	if port == "" || port == "0" {
		t.Fatalf("port = %q, want a kernel-assigned port", port)
	}

	baseURL := "http://" + srv.Addr()
	status, body := httpGetBody(t, baseURL+"/debug/pprof/")
	if status != http.StatusOK {
		t.Fatalf("GET /debug/pprof/ status = %d, want 200", status)
	}
	if !strings.Contains(body, "goroutineleak") {
		t.Fatalf("GET /debug/pprof/ body does not list goroutineleak:\n%s", body)
	}

	status, body = httpGetBody(t, baseURL+"/debug/pprof/goroutineleak?debug=1")
	if status != http.StatusOK {
		t.Fatalf("GET /debug/pprof/goroutineleak status = %d, want 200", status)
	}
	if !strings.Contains(body, "goroutineleak profile:") {
		t.Fatalf("goroutineleak body = %q, want a goroutineleak profile", body)
	}
}

func TestDebugServerServesMetrics(t *testing.T) {
	srv, err := startDebugServer("127.0.0.1:0", configuration.Prometheus{
		Enabled: true,
		Path:    "/custom-metrics",
	}, testDebugLogger())
	if err != nil {
		t.Fatalf("startDebugServer() error = %v", err)
	}
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	status, _ := httpGetBody(t, "http://"+srv.Addr()+"/custom-metrics")
	if status != http.StatusOK {
		t.Fatalf("GET /custom-metrics status = %d, want 200", status)
	}
}

func TestDebugServerListenFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer func() {
		if err := ln.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()

	if _, err := startDebugServer(ln.Addr().String(), configuration.Prometheus{}, testDebugLogger()); err == nil {
		t.Fatal("startDebugServer() error = nil, want an address-in-use error")
	}
}

// TestDebugServerStartsRepeatedly pins that the server does not register
// handlers on a process-global mux, which would panic on a second start.
func TestDebugServerStartsRepeatedly(t *testing.T) {
	for i := 0; i < 2; i++ {
		srv, err := startDebugServer("127.0.0.1:0", configuration.Prometheus{Enabled: true}, testDebugLogger())
		if err != nil {
			t.Fatalf("startDebugServer() attempt %d error = %v", i, err)
		}
		if status, _ := httpGetBody(t, "http://"+srv.Addr()+"/debug/pprof/"); status != http.StatusOK {
			t.Fatalf("attempt %d: GET /debug/pprof/ status = %d, want 200", i, status)
		}
		if err := srv.Close(); err != nil {
			t.Fatalf("attempt %d: Close() error = %v", i, err)
		}
	}
}

func httpGetBody(t *testing.T, url string) (int, string) {
	t.Helper()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s error = %v", url, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("Close %s body error = %v", url, err)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s body error = %v", url, err)
	}
	return resp.StatusCode, string(body)
}
