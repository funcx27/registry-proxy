package proxy

import (
	"fmt"
	"net"
	"net/http"
)

// NewProxy starts the plain-HTTP proxy in front of the loopback registry.
func NewProxy(proxyAddr, registryAddr string, opts Options) error {
	srv, err := newServer(registryAddr, opts)
	if err != nil {
		return err
	}
	defer srv.Close()

	ln, err := net.Listen("tcp", proxyAddr)
	if err != nil {
		return fmt.Errorf("listen proxy on %s: %w", proxyAddr, err)
	}
	return http.Serve(ln, srv.handler(nil))
}
