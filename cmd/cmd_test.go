package cmd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPickLoopbackAddr(t *testing.T) {
	addr, err := pickLoopbackAddr()
	if err != nil {
		t.Fatalf("pickLoopbackAddr() error = %v", err)
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q) error = %v", addr, err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("host = %q, want %q", host, "127.0.0.1")
	}
	if port == "" || port == "0" {
		t.Fatalf("port = %q, want a kernel-assigned port", port)
	}
}

func TestResolveConfigurationPrefersArgumentOverEnvironment(t *testing.T) {
	dir := t.TempDir()
	argumentPath := writeTestConfig(t, filepath.Join(dir, "argument.yml"), "127.0.0.1:5001")
	environmentPath := writeTestConfig(t, filepath.Join(dir, "environment.yml"), "127.0.0.1:5002")

	config, err := resolveConfiguration([]string{argumentPath}, environmentPath)
	if err != nil {
		t.Fatalf("resolveConfiguration() error = %v", err)
	}
	if got, want := config.HTTP.Addr, "127.0.0.1:5001"; got != want {
		t.Fatalf("HTTP.Addr = %q, want %q", got, want)
	}
}

func TestResolveConfigurationFallsBackToEnvironmentPath(t *testing.T) {
	dir := t.TempDir()
	environmentPath := writeTestConfig(t, filepath.Join(dir, "environment.yml"), "127.0.0.1:5002")

	config, err := resolveConfiguration(nil, environmentPath)
	if err != nil {
		t.Fatalf("resolveConfiguration() error = %v", err)
	}
	if got, want := config.HTTP.Addr, "127.0.0.1:5002"; got != want {
		t.Fatalf("HTTP.Addr = %q, want %q", got, want)
	}
}

func TestResolveConfigurationRequiresPath(t *testing.T) {
	if _, err := resolveConfiguration(nil, ""); err == nil {
		t.Fatal("resolveConfiguration() error = nil, want error")
	}
}

func TestResolveConfigurationMissingFile(t *testing.T) {
	if _, err := resolveConfiguration([]string{filepath.Join(t.TempDir(), "missing.yml")}, ""); err == nil {
		t.Fatal("resolveConfiguration() error = nil, want error")
	}
}

func writeTestConfig(t *testing.T, path, addr string) string {
	t.Helper()

	content := fmt.Sprintf(`version: 0.1
storage:
  filesystem:
    rootdirectory: %q
http:
  addr: %q
`, filepath.Join(filepath.Dir(path), "registry"), addr)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config %s: %v", path, err)
	}
	return path
}
