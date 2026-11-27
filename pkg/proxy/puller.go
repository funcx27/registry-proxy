package proxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
	"golang.org/x/sync/singleflight"
)

var errPullPoolClosed = errors.New("image pull pool closed")

type imagePullJob struct {
	registryAddr string
	image        string
	done         chan error
}

// pullPool coalesces concurrent pulls for the same image and executes the
// actual copies on a bounded set of workers.
type pullPool struct {
	group  singleflight.Group
	jobs   chan imagePullJob
	cache  *cache.Cache
	copyFn func(context.Context, string, string) error

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
	copyFn func(context.Context, string, string) error,
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

// ensure makes the image available locally. Concurrent callers for the same
// image share a single worker job and the same result.
func (p *pullPool) ensure(ctx context.Context, registryAddr, image string) error {
	if p.isPulled(image) {
		return nil
	}

	// The flight key must match the cache key: the cache is keyed by image
	// only, so keying the flight by registry address would let two callers
	// pull the same image concurrently when the pool is shared.
	result := p.group.DoChan(image, func() (any, error) {
		if p.isPulled(image) {
			return nil, nil
		}

		job := imagePullJob{
			registryAddr: registryAddr,
			image:        image,
			done:         make(chan error, 1),
		}

		if err := p.enqueue(ctx, job); err != nil {
			return nil, err
		}
		logger.Debugf("pulling image: %s", image)

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
			err = fmt.Errorf("pull image %s panic: %v", job.image, r)
		}
	}()

	// A pull queued behind another job for the same image can become a no-op.
	if p.isPulled(job.image) {
		return nil
	}

	begin := time.Now()
	if err := p.copyFn(ctx, job.registryAddr, job.image); err != nil {
		logger.Errorf("pull image %s failed after %s: %v", job.image, time.Since(begin), err)
		return fmt.Errorf("pull image %s: %w", job.image, err)
	}

	if p.cache != nil {
		p.cache.SetDefault(job.image, struct{}{})
	}
	// A single user-facing pull fans out into additional copies of the
	// digest references referenced by the tag (index and child manifests).
	// Those are an implementation detail, so only the tag-level completion
	// stays at info level.
	if isDigestReference(job.image) {
		logger.Debugf("pulled image: %s in %s", job.image, time.Since(begin))
	} else {
		logger.Infof("pulled image: %s in %s", job.image, time.Since(begin))
	}
	return nil
}

// isDigestReference reports whether image is pinned by digest (name@sha256:...)
// rather than resolved by tag.
func isDigestReference(image string) bool {
	return strings.Contains(image, "@")
}

func (p *pullPool) isPulled(image string) bool {
	if p.cache == nil {
		return false
	}
	_, ok := p.cache.Get(image)
	return ok
}
