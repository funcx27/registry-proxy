package proxy

import (
	"context"
	"errors"
	"net/http"
	"sync"
)

// MaintenanceGate coordinates a short read-only maintenance window with
// registry writes. Read requests stay available while writes are rejected and
// in-flight write requests are allowed to drain before maintenance starts.
type MaintenanceGate struct {
	mu          sync.Mutex
	cond        *sync.Cond
	active      int
	maintenance bool
}

func NewMaintenanceGate() *MaintenanceGate {
	g := &MaintenanceGate{}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// Wrap rejects write requests while maintenance is active.
func (g *MaintenanceGate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isRegistryWrite(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		if !g.acquireWrite() {
			w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
			http.Error(w, "registry is in maintenance mode", http.StatusServiceUnavailable)
			return
		}
		defer g.releaseWrite()

		next.ServeHTTP(w, r)
	})
}

// RunExclusive waits for in-flight writes to finish, then runs fn while new
// writes are rejected. Reads remain available for the whole maintenance window.
func (g *MaintenanceGate) RunExclusive(ctx context.Context, fn func() error) error {
	if err := g.enter(ctx); err != nil {
		return err
	}
	defer g.leave()

	return fn()
}

func (g *MaintenanceGate) acquireWrite() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.maintenance {
		return false
	}
	g.active++
	return true
}

func (g *MaintenanceGate) releaseWrite() {
	g.mu.Lock()
	g.active--
	g.cond.Broadcast()
	g.mu.Unlock()
}

func (g *MaintenanceGate) enter(ctx context.Context) error {
	g.mu.Lock()
	if err := ctx.Err(); err != nil {
		g.mu.Unlock()
		return err
	}
	if g.maintenance {
		g.mu.Unlock()
		return errors.New("registry maintenance already active")
	}
	g.maintenance = true

	stopWake := context.AfterFunc(ctx, func() {
		g.mu.Lock()
		g.cond.Broadcast()
		g.mu.Unlock()
	})
	defer stopWake()

	for g.active > 0 {
		if err := ctx.Err(); err != nil {
			g.maintenance = false
			g.cond.Broadcast()
			g.mu.Unlock()
			return err
		}
		g.cond.Wait()
	}

	g.mu.Unlock()
	return nil
}

func (g *MaintenanceGate) leave() {
	g.mu.Lock()
	g.maintenance = false
	g.cond.Broadcast()
	g.mu.Unlock()
}

func isRegistryWrite(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}
