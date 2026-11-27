package proxy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRegistryMappings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry-mapping.yaml")
	content := `docker.io:
  defaultNamespace: library
ghcr.io: {}
quay.io:
  registry: proxy.example.com/quay.io
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write mapping: %v", err)
	}

	mappings, err := loadRegistryMappings(path)
	if err != nil {
		t.Fatalf("loadRegistryMappings() error = %v", err)
	}
	if got := mappings["docker.io"].DefaultNamespace; got != "library" {
		t.Fatalf("docker.io defaultNamespace = %q, want library", got)
	}
	if got := mappings["ghcr.io"]; got != (RegistryMapping{}) {
		t.Fatalf("ghcr.io mapping = %#v, want empty", got)
	}
	if got := mappings["quay.io"].Registry; got != "proxy.example.com/quay.io" {
		t.Fatalf("quay.io registry = %q", got)
	}
}

func TestLoadRegistryMappingsMissingFileIsEmpty(t *testing.T) {
	mappings, err := loadRegistryMappings(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("loadRegistryMappings() error = %v", err)
	}
	if len(mappings) != 0 {
		t.Fatalf("mappings = %v, want empty", mappings)
	}
}

func TestLoadRegistryMappingsRejectsInvalidFiles(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "invalid yaml", content: "docker.io: [", want: "parse"},
		{name: "unknown field", content: "docker.io:\n  namespace: library\n", want: "field namespace"},
		{name: "url scheme in key", content: "\"https://docker.io\": {}\n", want: "scheme"},
		{name: "path in key", content: "\"docker.io/library\": {}\n", want: "without a path"},
		{name: "invalid default namespace", content: "docker.io:\n  defaultNamespace: library/extra\n", want: "single path component"},
		{name: "invalid upstream", content: "docker.io:\n  registry: https://mirror.example.com\n", want: "scheme"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "registry-mapping.yaml")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatalf("write mapping: %v", err)
			}
			_, err := loadRegistryMappings(path)
			if err == nil {
				t.Fatal("loadRegistryMappings() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestLogRegistryMappingsIsSorted(t *testing.T) {
	var lines []string
	logRegistryMappings(map[string]RegistryMapping{
		"quay.io":   {},
		"docker.io": {DefaultNamespace: "library"},
		"ghcr.io":   {Registry: "proxy.example.com/ghcr.io"},
	}, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})

	if len(lines) != 3 {
		t.Fatalf("log lines = %d, want 3", len(lines))
	}
	if !strings.Contains(lines[0], "docker.io") || !strings.Contains(lines[1], "ghcr.io") || !strings.Contains(lines[2], "quay.io") {
		t.Fatalf("log lines are not sorted: %v", lines)
	}
}
