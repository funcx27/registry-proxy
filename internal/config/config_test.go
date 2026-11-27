package config

import (
	"slices"
	"strings"
	"testing"
	"time"

	"registry-proxy/pkg/proxy"
)

func lookupFrom(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadFromEnvDefaults(t *testing.T) {
	cfg, err := LoadFromEnv(lookupFrom(nil))
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.RegistryConfigurationPath != "" {
		t.Fatalf("RegistryConfigurationPath = %q, want empty", cfg.RegistryConfigurationPath)
	}
	if cfg.AppLogLevel != "info" {
		t.Fatalf("AppLogLevel = %q, want info", cfg.AppLogLevel)
	}
	if cfg.DebugAddr != "" {
		t.Fatalf("DebugAddr = %q, want empty (automatic)", cfg.DebugAddr)
	}

	opts := cfg.Proxy
	if opts.CopyMode != proxy.CopyModeSync {
		t.Fatalf("CopyMode = %q, want %q", opts.CopyMode, proxy.CopyModeSync)
	}
	if opts.PullTimeout != 5*time.Minute {
		t.Fatalf("PullTimeout = %s, want 5m", opts.PullTimeout)
	}
	if opts.PullWorkers != 2 {
		t.Fatalf("PullWorkers = %d, want 2", opts.PullWorkers)
	}
	if opts.PullQueueSize != 0 {
		t.Fatalf("PullQueueSize = %d, want 0", opts.PullQueueSize)
	}
	if opts.RepullMinInterval != 5*time.Minute {
		t.Fatalf("RepullMinInterval = %s, want 5m", opts.RepullMinInterval)
	}
	if opts.RegistryAuthFile != "" {
		t.Fatalf("RegistryAuthFile = %q, want empty", opts.RegistryAuthFile)
	}
	if opts.RegistryMappingPath != "" {
		t.Fatalf("RegistryMappingPath = %q, want empty", opts.RegistryMappingPath)
	}

	cleanup := opts.Cleanup
	if !cleanup.Enabled {
		t.Fatal("Cleanup.Enabled = false, want true")
	}
	if cleanup.TagRetain != 2 {
		t.Fatalf("Cleanup.TagRetain = %d, want 2", cleanup.TagRetain)
	}
	if cleanup.Interval != 12*time.Hour {
		t.Fatalf("Cleanup.Interval = %s, want 12h", cleanup.Interval)
	}
	if cleanup.DeleteBefore != 24*time.Hour {
		t.Fatalf("Cleanup.DeleteBefore = %s, want 24h", cleanup.DeleteBefore)
	}
	if len(cleanup.Projects) != 0 {
		t.Fatalf("Cleanup.Projects = %v, want empty", cleanup.Projects)
	}
}

func TestLoadFromEnvOverrides(t *testing.T) {
	cfg, err := LoadFromEnv(lookupFrom(map[string]string{
		"REGISTRY_CONFIGURATION_PATH": " /etc/registry/config.yml ",
		"APP_LOG_LEVEL":               "debug",
		"DEBUG_ADDR":                  " 127.0.0.1:6060 ",
		"REGISTRY_MAPPING_PATH":       "/etc/registry/registry-mapping.yaml",
		"IMAGE_COPY_MODE":             "async",
		"IMAGE_PULL_TIMEOUT":          "90s",
		"IMAGE_PULL_WORKERS":          "4",
		"IMAGE_PULL_QUEUE_SIZE":       "16",
		"IMAGE_REPULL_MIN_INTERVAL":   "1h",
		"REGISTRY_AUTH_FILE":          "/run/auth.json",
		"IMAGE_CLEAN_TAG_RETAIN_NUMS": "5",
		"IMAGE_CLEAN_INTERVAL":        "30m",
		"IMAGE_CLEAN_PROJECT":         " team/a , library ,team/a,,",
		"IMAGE_CLEAN_BEFORE_DAYS":     "7",
	}))
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.RegistryConfigurationPath != "/etc/registry/config.yml" {
		t.Fatalf("RegistryConfigurationPath = %q", cfg.RegistryConfigurationPath)
	}
	if cfg.AppLogLevel != "debug" {
		t.Fatalf("AppLogLevel = %q, want debug", cfg.AppLogLevel)
	}
	if cfg.DebugAddr != "127.0.0.1:6060" {
		t.Fatalf("DebugAddr = %q, want 127.0.0.1:6060", cfg.DebugAddr)
	}

	opts := cfg.Proxy
	if opts.CopyMode != proxy.CopyModeAsync {
		t.Fatalf("CopyMode = %q, want %q", opts.CopyMode, proxy.CopyModeAsync)
	}
	if opts.PullTimeout != 90*time.Second {
		t.Fatalf("PullTimeout = %s, want 90s", opts.PullTimeout)
	}
	if opts.PullWorkers != 4 {
		t.Fatalf("PullWorkers = %d, want 4", opts.PullWorkers)
	}
	if opts.PullQueueSize != 16 {
		t.Fatalf("PullQueueSize = %d, want 16", opts.PullQueueSize)
	}
	if opts.RepullMinInterval != time.Hour {
		t.Fatalf("RepullMinInterval = %s, want 1h", opts.RepullMinInterval)
	}
	if opts.RegistryAuthFile != "/run/auth.json" {
		t.Fatalf("RegistryAuthFile = %q", opts.RegistryAuthFile)
	}
	if opts.RegistryMappingPath != "/etc/registry/registry-mapping.yaml" {
		t.Fatalf("RegistryMappingPath = %q", opts.RegistryMappingPath)
	}

	cleanup := opts.Cleanup
	if !cleanup.Enabled {
		t.Fatal("Cleanup.Enabled = false, want true")
	}
	if cleanup.TagRetain != 5 {
		t.Fatalf("Cleanup.TagRetain = %d, want 5", cleanup.TagRetain)
	}
	if cleanup.Interval != 30*time.Minute {
		t.Fatalf("Cleanup.Interval = %s, want 30m", cleanup.Interval)
	}
	if cleanup.DeleteBefore != 7*24*time.Hour {
		t.Fatalf("Cleanup.DeleteBefore = %s, want 168h", cleanup.DeleteBefore)
	}
	if want := []string{"team/a", "library"}; !slices.Equal(cleanup.Projects, want) {
		t.Fatalf("Cleanup.Projects = %v, want %v", cleanup.Projects, want)
	}
}

func TestLoadFromEnvAcceptsOffCopyMode(t *testing.T) {
	cfg, err := LoadFromEnv(lookupFrom(map[string]string{"IMAGE_COPY_MODE": "off"}))
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}
	if cfg.Proxy.CopyMode != proxy.CopyModeOff {
		t.Fatalf("CopyMode = %q, want %q", cfg.Proxy.CopyMode, proxy.CopyModeOff)
	}
}

func TestLoadFromEnvDebugAddrDisabled(t *testing.T) {
	for _, raw := range []string{"off", "false", " OFF "} {
		t.Run(raw, func(t *testing.T) {
			cfg, err := LoadFromEnv(lookupFrom(map[string]string{"DEBUG_ADDR": raw}))
			if err != nil {
				t.Fatalf("LoadFromEnv() error = %v", err)
			}
			if cfg.DebugAddr != "off" {
				t.Fatalf("DebugAddr = %q, want off", cfg.DebugAddr)
			}
		})
	}
}

func TestLoadFromEnvRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
		want   string
	}{
		{
			name:   "invalid debug addr",
			values: map[string]string{"DEBUG_ADDR": "127.0.0.1"},
			want:   "DEBUG_ADDR",
		},
		{
			name:   "unknown copy mode",
			values: map[string]string{"IMAGE_COPY_MODE": "eventual"},
			want:   "IMAGE_COPY_MODE",
		},
		{
			name:   "invalid pull timeout",
			values: map[string]string{"IMAGE_PULL_TIMEOUT": "soon"},
			want:   "IMAGE_PULL_TIMEOUT",
		},
		{
			name:   "non positive pull timeout",
			values: map[string]string{"IMAGE_PULL_TIMEOUT": "0s"},
			want:   "must be positive",
		},
		{
			name:   "invalid repull interval",
			values: map[string]string{"IMAGE_REPULL_MIN_INTERVAL": "0s"},
			want:   "IMAGE_REPULL_MIN_INTERVAL",
		},
		{
			name:   "invalid pull workers",
			values: map[string]string{"IMAGE_PULL_WORKERS": "0"},
			want:   "IMAGE_PULL_WORKERS",
		},
		{
			name:   "invalid queue size",
			values: map[string]string{"IMAGE_PULL_QUEUE_SIZE": "-1"},
			want:   "IMAGE_PULL_QUEUE_SIZE",
		},
		{
			name:   "negative cleanup retain",
			values: map[string]string{"IMAGE_CLEAN_TAG_RETAIN_NUMS": "-1"},
			want:   "at least 1",
		},
		{
			name:   "zero cleanup retain",
			values: map[string]string{"IMAGE_CLEAN_TAG_RETAIN_NUMS": "0"},
			want:   "at least 1",
		},
		{
			name:   "non numeric cleanup retain",
			values: map[string]string{"IMAGE_CLEAN_TAG_RETAIN_NUMS": "many"},
			want:   "IMAGE_CLEAN_TAG_RETAIN_NUMS",
		},
		{
			name: "invalid cleanup interval",
			values: map[string]string{
				"IMAGE_CLEAN_TAG_RETAIN_NUMS": "1",
				"IMAGE_CLEAN_PROJECT":         "team/a",
				"IMAGE_CLEAN_INTERVAL":        "sometimes",
			},
			want: "IMAGE_CLEAN_INTERVAL",
		},
		{
			name: "invalid cleanup days",
			values: map[string]string{
				"IMAGE_CLEAN_TAG_RETAIN_NUMS": "1",
				"IMAGE_CLEAN_PROJECT":         "team/a",
				"IMAGE_CLEAN_BEFORE_DAYS":     "-1",
			},
			want: "IMAGE_CLEAN_BEFORE_DAYS",
		},
		{
			name: "cleanup days overflow",
			values: map[string]string{
				"IMAGE_CLEAN_TAG_RETAIN_NUMS": "1",
				"IMAGE_CLEAN_PROJECT":         "team/a",
				"IMAGE_CLEAN_BEFORE_DAYS":     "999999",
			},
			want: "too large",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadFromEnv(lookupFrom(tt.values))
			if err == nil {
				t.Fatal("LoadFromEnv() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadFromEnv() error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

// TestLoadFromEnvCleanupDisabledIgnoresSettings pins that an explicitly empty
// IMAGE_CLEAN_TAG_RETAIN_NUMS disables cleanup before any other cleanup
// setting is parsed. The Helm chart renders that value when cleanup is
// disabled, while an unset variable keeps the default policy enabled.
func TestLoadFromEnvCleanupDisabledIgnoresSettings(t *testing.T) {
	cfg, err := LoadFromEnv(lookupFrom(map[string]string{
		"IMAGE_CLEAN_TAG_RETAIN_NUMS": "  ",
		"IMAGE_CLEAN_INTERVAL":        "not-a-duration",
		"IMAGE_CLEAN_BEFORE_DAYS":     "not-a-number",
	}))
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}
	if cfg.Proxy.Cleanup.Enabled {
		t.Fatal("Cleanup.Enabled = true, want false")
	}
}

func TestLoadFromEnvCleanupAllProjects(t *testing.T) {
	cfg, err := LoadFromEnv(lookupFrom(map[string]string{
		"IMAGE_CLEAN_TAG_RETAIN_NUMS": "2",
		"IMAGE_CLEAN_PROJECT":         "",
	}))
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}
	if !cfg.Proxy.Cleanup.Enabled {
		t.Fatal("Cleanup.Enabled = false, want true")
	}
	if len(cfg.Proxy.Cleanup.Projects) != 0 {
		t.Fatalf("Cleanup.Projects = %v, want empty (all repositories)", cfg.Proxy.Cleanup.Projects)
	}
}

func TestParseProjects(t *testing.T) {
	got := parseProjects(" team/a , library ,,team/a, team/b ")
	want := []string{"team/a", "library", "team/b"}
	if !slices.Equal(got, want) {
		t.Fatalf("parseProjects() = %v, want %v", got, want)
	}
}
