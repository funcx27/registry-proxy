// Package config loads registry-proxy's application configuration from the
// environment. Distribution's own YAML configuration is intentionally handled
// separately by the distribution configuration package.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"registry-proxy/pkg/proxy"
)

const (
	envAppLogLevel                      = "APP_LOG_LEVEL"
	envRegistryConfigurationPath        = "REGISTRY_CONFIGURATION_PATH"
	envRegistryAuthFile                 = "REGISTRY_AUTH_FILE"
	envDockerHubMirror                  = "DOCKERHUB_MIRROR"
	envImageCopyMode                    = "IMAGE_COPY_MODE"
	envImagePullTimeout                 = "IMAGE_PULL_TIMEOUT"
	envImagePullWorkers                 = "IMAGE_PULL_WORKERS"
	envImagePullQueueSize               = "IMAGE_PULL_QUEUE_SIZE"
	envImageRepullMinInterval           = "IMAGE_REPULL_MIN_INTERVAL"
	envImageCleanTagRetainNums          = "IMAGE_CLEAN_TAG_RETAIN_NUMS"
	envImageCleanInterval               = "IMAGE_CLEAN_INTERVAL"
	envImageCleanProject                = "IMAGE_CLEAN_PROJECT"
	envImageCleanBeforeDays             = "IMAGE_CLEAN_BEFORE_DAYS"
	defaultAppLogLevel                  = "info"
	defaultPullTimeout                  = 5 * time.Minute
	defaultPullWorkers                  = 2
	defaultPullQueueSize                = 0
	defaultRepullMinInterval            = 5 * time.Minute
	defaultCleanInterval                = time.Minute
	defaultCleanBeforeDays              = 1
	maxCleanupDays               uint64 = uint64(1<<63-1) / uint64(24*time.Hour)
)

// Config contains process-level configuration and the proxy runtime options.
type Config struct {
	RegistryConfigurationPath string
	AppLogLevel               string
	Proxy                     proxy.Options
}

// Load reads application configuration from the process environment.
func Load() (Config, error) {
	return LoadFromEnv(os.LookupEnv)
}

// LoadFromEnv reads configuration using lookup. Tests can supply a deterministic
// lookup function instead of mutating the process environment.
func LoadFromEnv(lookup func(string) (string, bool)) (Config, error) {
	cfg := Config{
		RegistryConfigurationPath: envValue(lookup, envRegistryConfigurationPath),
		AppLogLevel:               envValueOrDefault(lookup, envAppLogLevel, defaultAppLogLevel),
		Proxy: proxy.Options{
			DockerHubMirror:   envValue(lookup, envDockerHubMirror),
			PullWorkers:       defaultPullWorkers,
			PullQueueSize:     defaultPullQueueSize,
			RepullMinInterval: defaultRepullMinInterval,
			RegistryAuthFile:  envValue(lookup, envRegistryAuthFile),
			Cleanup: proxy.CleanupOptions{
				Interval:     defaultCleanInterval,
				DeleteBefore: defaultCleanBeforeDays * 24 * time.Hour,
			},
		},
	}

	copyMode, err := parseCopyMode(envValueOrDefault(lookup, envImageCopyMode, string(proxy.CopyModeSync)))
	if err != nil {
		return Config{}, err
	}
	cfg.Proxy.CopyMode = copyMode

	cfg.Proxy.PullTimeout, err = parseDuration(lookup, envImagePullTimeout, defaultPullTimeout)
	if err != nil {
		return Config{}, err
	}
	cfg.Proxy.PullWorkers, err = parseInt(lookup, envImagePullWorkers, defaultPullWorkers, 1)
	if err != nil {
		return Config{}, err
	}
	cfg.Proxy.PullQueueSize, err = parseInt(lookup, envImagePullQueueSize, defaultPullQueueSize, 0)
	if err != nil {
		return Config{}, err
	}
	cfg.Proxy.RepullMinInterval, err = parseDuration(lookup, envImageRepullMinInterval, defaultRepullMinInterval)
	if err != nil {
		return Config{}, err
	}

	cleanup, err := parseCleanup(lookup)
	if err != nil {
		return Config{}, err
	}
	cfg.Proxy.Cleanup = cleanup
	return cfg, nil
}

func parseCleanup(lookup func(string) (string, bool)) (proxy.CleanupOptions, error) {
	opts := proxy.CleanupOptions{
		Interval:     defaultCleanInterval,
		DeleteBefore: defaultCleanBeforeDays * 24 * time.Hour,
	}

	retainRaw, ok := lookup(envImageCleanTagRetainNums)
	retainRaw = strings.TrimSpace(retainRaw)
	if !ok || retainRaw == "" {
		return opts, nil
	}

	retain, err := strconv.Atoi(retainRaw)
	if err != nil {
		return proxy.CleanupOptions{}, fmt.Errorf("%s=%q: %w", envImageCleanTagRetainNums, retainRaw, err)
	}
	if retain < 0 {
		return proxy.CleanupOptions{}, fmt.Errorf("%s=%q: must not be negative", envImageCleanTagRetainNums, retainRaw)
	}

	opts.Enabled = true
	opts.TagRetain = retain
	opts.Interval, err = parseDuration(lookup, envImageCleanInterval, defaultCleanInterval)
	if err != nil {
		return proxy.CleanupOptions{}, err
	}

	days, err := parseInt(lookup, envImageCleanBeforeDays, defaultCleanBeforeDays, 0)
	if err != nil {
		return proxy.CleanupOptions{}, err
	}
	if uint64(days) > maxCleanupDays {
		return proxy.CleanupOptions{}, fmt.Errorf("%s=%d: value is too large", envImageCleanBeforeDays, days)
	}
	opts.DeleteBefore = time.Duration(days) * 24 * time.Hour

	opts.Projects = parseProjects(envValue(lookup, envImageCleanProject))
	if len(opts.Projects) == 0 {
		return proxy.CleanupOptions{}, fmt.Errorf("%s must contain at least one project when %s is set", envImageCleanProject, envImageCleanTagRetainNums)
	}
	return opts, nil
}

func parseProjects(raw string) []string {
	seen := make(map[string]struct{})
	projects := make([]string, 0)
	for _, item := range strings.Split(raw, ",") {
		project := strings.TrimSpace(item)
		if project == "" {
			continue
		}
		if _, ok := seen[project]; ok {
			continue
		}
		seen[project] = struct{}{}
		projects = append(projects, project)
	}
	return projects
}

func parseCopyMode(raw string) (proxy.CopyMode, error) {
	switch proxy.CopyMode(strings.TrimSpace(raw)) {
	case proxy.CopyModeSync:
		return proxy.CopyModeSync, nil
	case proxy.CopyModeAsync:
		return proxy.CopyModeAsync, nil
	default:
		return "", fmt.Errorf("%s=%q: must be %q or %q", envImageCopyMode, raw, proxy.CopyModeSync, proxy.CopyModeAsync)
	}
}

func parseDuration(lookup func(string) (string, bool), key string, fallback time.Duration) (time.Duration, error) {
	raw := envValue(lookup, key)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", key, raw, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s=%q: must be positive", key, raw)
	}
	return value, nil
}

func parseInt(lookup func(string) (string, bool), key string, fallback, minimum int) (int, error) {
	raw := envValue(lookup, key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", key, raw, err)
	}
	if value < minimum {
		return 0, fmt.Errorf("%s=%q: must be at least %d", key, raw, minimum)
	}
	return value, nil
}

func envValue(lookup func(string) (string, bool), key string) string {
	value, _ := lookup(key)
	return strings.TrimSpace(value)
}

func envValueOrDefault(lookup func(string) (string, bool), key, fallback string) string {
	if value := envValue(lookup, key); value != "" {
		return value
	}
	return fallback
}
