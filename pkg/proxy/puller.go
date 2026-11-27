package proxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/regclient/regclient/types/errs"
	"golang.org/x/sync/singleflight"
)

var errPullPoolClosed = errors.New("image pull pool closed")

// slowDigestPull is the point where a digest-level copy is no longer an
// implementation detail: it explains the latency of the user-facing pull, so
// it is reported at info instead of debug.
const slowDigestPull = time.Second

type imagePullJob struct {
	registryAddr string
	sourceImage  string
	localTarget  string
	loopChain    []string
	done         chan error
}

// pullPool coalesces concurrent pulls for the same local target and executes
// the actual copies on a bounded set of workers.
type pullPool struct {
	group  singleflight.Group
	jobs   chan imagePullJob
	cache  *cache.Cache
	copyFn func(context.Context, string, string, string) (copyResult, error)

	// closeMu guards the closed flag and is held across job handoff so Close
	// cannot stop the workers while a producer is mid-send.
	closeMu sync.RWMutex
	closed  bool

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newPullPool(
	ctx context.Context,
	workers, queueSize int,
	cache *cache.Cache,
	copyFn func(context.Context, string, string, string) (copyResult, error),
) *pullPool {
	if workers < 1 {
		workers = 1
	}
	if queueSize < 0 {
		queueSize = 0
	}

	workerCtx, cancel := context.WithCancel(ctx)
	p := &pullPool{
		jobs:   make(chan imagePullJob, queueSize),
		cache:  cache,
		copyFn: copyFn,
		cancel: cancel,
	}
	p.wg.Add(workers)
	for i := 0; i < workers; i++ {
		go p.worker(workerCtx)
	}
	return p
}

// Close stops accepting new work, waits for active workers, and fails jobs
// that were still queued.
func (p *pullPool) Close() {
	p.closeMu.Lock()
	if p.closed {
		p.closeMu.Unlock()
		return
	}
	p.closed = true
	p.cancel()
	p.closeMu.Unlock()

	p.wg.Wait()

	for {
		select {
		case job := <-p.jobs:
			job.done <- errPullPoolClosed
		default:
			return
		}
	}
}

// ensure makes sourceImage available under localTarget. Concurrent callers for
// the same local target share a single worker job and the same result.
func (p *pullPool) ensure(ctx context.Context, registryAddr, sourceImage, localTarget string) error {
	key := pullCacheKey(registryAddr, localTarget)
	if p.isPulled(key) {
		return nil
	}

	result := p.group.DoChan(key, func() (any, error) {
		if p.isPulled(key) {
			return nil, nil
		}

		job := imagePullJob{
			registryAddr: registryAddr,
			sourceImage:  sourceImage,
			localTarget:  localTarget,
			loopChain:    append([]string(nil), loopChainFromContext(ctx)...),
			done:         make(chan error, 1),
		}

		if err := p.enqueue(ctx, job); err != nil {
			return nil, err
		}
		logger.Debugf("pulling image: %s", sourceImage)

		// Once the job is queued, keep the flight alive until the worker
		// finishes. A timed-out caller exits independently, but later callers
		// still join this flight instead of starting a duplicate pull.
		return nil, <-job.done
	})

	select {
	case r := <-result:
		return r.Err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func pullCacheKey(registryAddr, localTarget string) string {
	return registryAddr + "/" + localTarget
}

// enqueue hands a job to a worker. The read lock is held for the whole send so
// Close (which takes the write lock) can only proceed after every in-flight
// handoff has completed or been abandoned by its caller.
func (p *pullPool) enqueue(ctx context.Context, job imagePullJob) error {
	p.closeMu.RLock()
	defer p.closeMu.RUnlock()

	if p.closed {
		return errPullPoolClosed
	}

	select {
	case p.jobs <- job:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *pullPool) worker(ctx context.Context) {
	defer p.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case job := <-p.jobs:
			job.done <- p.pull(ctx, job)
		}
	}
}

func (p *pullPool) pull(ctx context.Context, job imagePullJob) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("pull image %s panic: %v", job.sourceImage, r)
		}
	}()

	key := pullCacheKey(job.registryAddr, job.localTarget)
	// A pull queued behind another job for the same target can become a no-op.
	if p.isPulled(key) {
		return nil
	}

	begin := time.Now()
	copyCtx := contextWithLoopChain(ctx, job.loopChain)
	result, err := p.copyFn(copyCtx, job.registryAddr, job.sourceImage, job.localTarget)
	if err != nil {
		logPullFailure(job.sourceImage, time.Since(begin), err)
		return fmt.Errorf("pull image %s: %w", job.sourceImage, err)
	}

	if p.cache != nil {
		p.cache.SetDefault(key, struct{}{})
	}
	// A single user-facing pull fans out into additional copies of the
	// digest references referenced by the tag (index and child manifests).
	// Those are an implementation detail, so only the tag-level completion
	// stays at info level. A digest copy that actually moved a large image is
	// the exception: it dominates the latency the user observes, so report it
	// even though the tag-level line finished long before.
	elapsed := time.Since(begin)
	message := "copied " + describeCopyResult(job.sourceImage, result, elapsed)
	switch {
	case !isDigestReference(job.sourceImage), elapsed >= slowDigestPull:
		logger.Info(message)
	default:
		logger.Debug(message)
	}
	return nil
}

// describeCopyResult renders what a copy actually moved. A multi-platform tag
// only copies the index, so say so explicitly instead of implying that the
// layers were transferred as well.
func describeCopyResult(image string, result copyResult, elapsed time.Duration) string {
	if result.kind == "index" {
		return fmt.Sprintf("index %s in %s (media type: %s, children: %d, manifest size: %s); child manifests and layers are copied on demand",
			image, elapsed, emptyDash(result.mediaType), result.children, humanBytes(result.size))
	}
	if result.kind == "image" {
		return fmt.Sprintf("image %s in %s (media type: %s, layers: %d, size: %s)",
			image, elapsed, emptyDash(result.mediaType), result.layers, humanBytes(result.size))
	}
	return fmt.Sprintf("image %s in %s", image, elapsed)
}

func emptyDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

// humanBytes renders a byte count with binary units for log readability.
func humanBytes(size int64) string {
	if size <= 0 {
		return "-"
	}
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%dB", size)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(size)
	for _, suffix := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f%s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1fPiB", value/unit)
}

// logPullFailure records one line per failed pull. Expected upstream misses
// are warnings with a "not found" message; transport failures, timeouts, and
// registry 5xx responses remain errors.
func logPullFailure(image string, elapsed time.Duration, err error) {
	if errors.Is(err, errs.ErrNotFound) {
		logger.Warnf("pull image %s not found after %s: %v", image, elapsed, err)
		return
	}
	logger.Errorf("pull image %s failed after %s: %v", image, elapsed, err)
}

// isDigestReference reports whether image is pinned by digest (name@sha256:...)
// rather than resolved by tag.
func isDigestReference(image string) bool {
	return strings.Contains(image, "@")
}

func (p *pullPool) isPulled(key string) bool {
	if p.cache == nil {
		return false
	}
	_, ok := p.cache.Get(key)
	return ok
}
