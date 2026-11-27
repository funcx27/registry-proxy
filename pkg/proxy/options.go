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
)

// Options are the runtime knobs that affect image pulls and cleanup.
type Options struct {
	CopyMode          CopyMode
	DockerHubMirror   string
	PullTimeout       time.Duration
	PullWorkers       int
	PullQueueSize     int
	RepullMinInterval time.Duration
	RegistryAuthFile  string
	Cleanup           CleanupOptions
}

// CleanupOptions controls the periodic tag cleanup job.
type CleanupOptions struct {
	Enabled      bool
	TagRetain    int
	Interval     time.Duration
	Projects     []string
	DeleteBefore time.Duration
}
