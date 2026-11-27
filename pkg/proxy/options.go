package proxy

import "time"

// CopyMode controls whether manifest requests wait for the upstream copy to
// complete before being proxied.
type CopyMode string

const (
	// CopyModeSync copies the image before proxying the manifest request.
	CopyModeSync CopyMode = "sync"
	// CopyModeAsync starts the copy in the background and proxies immediately.
	CopyModeAsync CopyMode = "async"
	// CopyModeOff disables on-demand upstream copying. The proxy still
	// normalizes Host/ns routes so every entry point addresses the same local
	// repository, but it behaves like a plain local registry.
	CopyModeOff CopyMode = "off"
)

// RegistryMapping describes how one upstream registry or repository namespace
// is addressed.
//
// The map key in the mapping file is the logical registry name used in local
// repository paths, for example "ghcr.io" or "dockerhub". Registry may point at
// a different host or another proxy, optionally with a repository prefix.
// DefaultNamespace is prepended to single-component repositories, mirroring
// Docker Hub's "library" convention.
//
// Domain keys use registry semantics: the first path component identifies the
// upstream registry and is removed from the upstream repository path. Namespace
// keys keep the path unchanged, which allows chaining through another proxy
// that owns namespaces such as "dockerhub". PreservePath overrides that default.
type RegistryMapping struct {
	Registry         string `yaml:"registry,omitempty"`
	DefaultNamespace string `yaml:"defaultNamespace,omitempty"`
	PreservePath     *bool  `yaml:"preservePath,omitempty"`
}

// Options are the runtime knobs that affect image pulls and cleanup.
type Options struct {
	CopyMode            CopyMode
	RegistryMappingPath string
	PullTimeout         time.Duration
	PullWorkers         int
	PullQueueSize       int
	RepullMinInterval   time.Duration
	RegistryAuthFile    string
	Cleanup             CleanupOptions
}

// CleanupOptions controls the periodic tag cleanup job.
type CleanupOptions struct {
	Enabled      bool
	TagRetain    int
	Interval     time.Duration
	Projects     []string
	DeleteBefore time.Duration
}
