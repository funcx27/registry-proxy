package cmd

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"
	"time"

	"github.com/distribution/distribution/v3/configuration"
	"github.com/docker/go-metrics"
	"github.com/sirupsen/logrus"
)

// defaultDebugAddr keeps the diagnostics listener on loopback with a
// kernel-assigned port. Binary deployments may run several instances on one
// host, so a fixed port would make them compete for the same socket.
const defaultDebugAddr = "127.0.0.1:0"

// resolveDebugAddr decides where the diagnostics listener binds. The
// application-level DEBUG_ADDR wins over distribution's http.debug.addr so a
// single environment variable can pin or disable the listener in any
// deployment. An enabled listener defaults to loopback with an ephemeral port.
func resolveDebugAddr(appAddr, yamlAddr string) (addr string, enabled bool) {
	appAddr = strings.TrimSpace(appAddr)
	switch strings.ToLower(appAddr) {
	case "off", "false":
		return "", false
	case "":
	default:
		return appAddr, true
	}
	if yamlAddr = strings.TrimSpace(yamlAddr); yamlAddr != "" {
		return yamlAddr, true
	}
	return defaultDebugAddr, true
}

// debugServer owns the diagnostics listener and its HTTP server. The listener
// is created before serving so startup can log the real port when the caller
// asked the kernel to pick one.
type debugServer struct {
	ln  net.Listener
	srv *http.Server
}

// Addr returns the bound address, including the kernel-assigned port.
func (d *debugServer) Addr() string {
	return d.ln.Addr().String()
}

// Close stops the diagnostics listener. It is safe to call more than once.
func (d *debugServer) Close() error {
	return d.srv.Close()
}

// configureDebugServer starts the pprof/metrics listener when enabled. The
// diagnostics endpoint is optional: a bind failure is logged and the registry
// keeps running, because losing pprof must never take down image pulls.
func configureDebugServer(appAddr, yamlAddr string, prom configuration.Prometheus, log *logrus.Logger) {
	addr, enabled := resolveDebugAddr(appAddr, yamlAddr)
	if !enabled {
		log.Info("debug server disabled")
		return
	}
	if _, err := startDebugServer(addr, prom, log); err != nil {
		log.Errorf("debug server disabled: %v", err)
	}
}

// startDebugServer binds addr, serves pprof and optional Prometheus metrics,
// and returns once the listener is ready. The returned server can be closed by
// tests; the process itself keeps it for its whole lifetime.
func startDebugServer(addr string, prom configuration.Prometheus, log *logrus.Logger) (*debugServer, error) {
	if log == nil {
		log = logrus.New()
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen debug server on %s: %w", addr, err)
	}

	// Use a dedicated mux instead of http.DefaultServeMux: it keeps pprof out
	// of any other handler registration and lets tests start the server more
	// than once in the same process.
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	if prom.Enabled {
		path := prom.Path
		if path == "" {
			path = "/metrics"
		}
		if !strings.HasPrefix(path, "/") {
			log.Errorf("invalid prometheus path %q: must start with \"/\"; metrics disabled", path)
		} else {
			registerMetrics(mux, path, log)
		}
	}

	d := &debugServer{
		ln: ln,
		srv: &http.Server{
			Handler: mux,
			// Cap header reads so a stuck client cannot hold a connection
			// open forever. WriteTimeout is deliberately unset: CPU and trace
			// profiles stream for their whole sampling window.
			ReadHeaderTimeout: 5 * time.Second,
		},
	}

	go func() {
		if err := d.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Errorf("debug server stopped: %v", err)
		}
	}()

	if !isLoopbackListener(ln.Addr()) {
		log.Warnf("debug server listening on non-loopback address %s; profiles can expose credentials held in memory", ln.Addr())
	}
	log.Infof("debug server listening on http://%s/debug/pprof/", ln.Addr())

	return d, nil
}

// registerMetrics adds the Prometheus handler without letting a conflicting
// or invalid path panic the process; diagnostics must never stop the registry.
func registerMetrics(mux *http.ServeMux, path string, log *logrus.Logger) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("register prometheus metrics on %q: %v; metrics disabled", path, r)
		}
	}()
	mux.Handle(path, metrics.Handler())
	log.Infof("providing prometheus metrics on %s", path)
}

// isLoopbackListener reports whether the listener is bound to a loopback
// address. Wildcard binds such as ":0" are not loopback.
func isLoopbackListener(addr net.Addr) bool {
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		return false
	}
	return tcpAddr.IP.IsLoopback()
}
