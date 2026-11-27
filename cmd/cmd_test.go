package cmd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"registry-proxy/pkg/proxy"

	"github.com/distribution/distribution/v3/configuration"
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

func TestDefaultRegistryMappingPath(t *testing.T) {
	tests := []struct {
		name    string
		storage configuration.Storage
		want    string
	}{
		{
			name: "filesystem root",
			storage: configuration.Storage{
				"filesystem": configuration.Parameters{"rootdirectory": "/srv/registry"},
			},
			want: "/srv/registry/registry-mapping.yaml",
		},
		{
			name: "relative filesystem root",
			storage: configuration.Storage{
				"filesystem": configuration.Parameters{"rootdirectory": "data/registry"},
			},
			want: filepath.Join("data/registry", "registry-mapping.yaml"),
		},
		{
			name:    "missing rootdirectory",
			storage: configuration.Storage{"filesystem": configuration.Parameters{}},
			want:    "",
		},
		{
			name:    "non-filesystem driver",
			storage: configuration.Storage{"inmemory": configuration.Parameters{}},
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &configuration.Configuration{Storage: tt.storage}
			if got := defaultRegistryMappingPath(config); got != tt.want {
				t.Fatalf("defaultRegistryMappingPath() = %q, want %q", got, tt.want)
			}
		})
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

func TestEnsureLazyIndexCopyConfig(t *testing.T) {
	tests := []struct {
		name       string
		mode       proxy.CopyMode
		validation configuration.Validation
		wantChange bool
		want       configuration.Platforms
	}{
		{
			name:       "sync defaults to validating all platforms",
			mode:       proxy.CopyModeSync,
			wantChange: true,
			want:       "none",
		},
		{
			name:       "async with all platforms",
			mode:       proxy.CopyModeAsync,
			validation: configuration.Validation{Manifests: configuration.ValidationManifests{Indexes: configuration.ValidationIndexes{Platforms: "all"}}},
			wantChange: true,
			want:       "none",
		},
		{
			name:       "platform list is narrowed",
			mode:       proxy.CopyModeSync,
			validation: configuration.Validation{Manifests: configuration.ValidationManifests{Indexes: configuration.ValidationIndexes{Platforms: "list"}}},
			wantChange: true,
			want:       "none",
		},
		{
			name:       "already disabled",
			mode:       proxy.CopyModeSync,
			validation: configuration.Validation{Manifests: configuration.ValidationManifests{Indexes: configuration.ValidationIndexes{Platforms: "none"}}},
			want:       "none",
		},
		{
			name: "copy mode off leaves configuration untouched",
			mode: proxy.CopyModeOff,
			want: "",
		},
		{
			name:       "validation fully disabled",
			mode:       proxy.CopyModeSync,
			validation: configuration.Validation{Disabled: true},
			want:       "",
		},
		{
			name:       "enabled takes precedence over disabled",
			mode:       proxy.CopyModeSync,
			validation: configuration.Validation{Enabled: true, Disabled: true},
			wantChange: true,
			want:       "none",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &configuration.Configuration{Validation: tt.validation}
			if got := ensureLazyIndexCopyConfig(config, tt.mode); got != tt.wantChange {
				t.Fatalf("ensureLazyIndexCopyConfig() = %v, want %v", got, tt.wantChange)
			}
			if got := config.Validation.Manifests.Indexes.Platforms; got != tt.want {
				t.Fatalf("platforms = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShippedConfigDisablesIndexValidation(t *testing.T) {
	config, err := resolveConfiguration([]string{"../config.yml"}, "")
	if err != nil {
		t.Fatalf("resolveConfiguration() error = %v", err)
	}
	if got := config.Validation.Manifests.Indexes.Platforms; got != "none" {
		t.Fatalf("validation.manifests.indexes.platforms = %q, want %q", got, "none")
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
