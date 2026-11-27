package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

const (
	// loopChainHeader carries the registry-proxy instance IDs that have already
	// handled the request. Each proxy appends its own ID before contacting an
	// upstream, so a request returning to an earlier proxy is detected without
	// comparing hostnames. Comparing hostnames would reject intentional
	// split-horizon deployments where the same name resolves to different
	// servers inside and outside the proxy pod.
	loopChainHeader = "X-Registry-Proxy-Chain"
	// maxProxyHops bounds header growth and catches pathological chains even
	// when every instance in the chain is distinct.
	maxProxyHops = 16
)

type loopChainContextKey struct{}

// newProxyInstanceID returns a random identifier for this process. It is not a
// security boundary; it only distinguishes proxy instances in a request chain.
func newProxyInstanceID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate proxy instance ID: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// loopChainFromContext returns the chain carried by an inbound request. The
// returned slice is read-only; callers that need to modify it must copy it.
func loopChainFromContext(ctx context.Context) []string {
	chain, _ := ctx.Value(loopChainContextKey{}).([]string)
	return chain
}

func contextWithLoopChain(ctx context.Context, chain []string) context.Context {
	if len(chain) == 0 {
		return ctx
	}
	return context.WithValue(ctx, loopChainContextKey{}, append([]string(nil), chain...))
}

// parseLoopChain accepts both repeated header values and a comma-separated
// value for compatibility with intermediaries that coalesce headers.
func parseLoopChain(header http.Header) (chain []string, tooDeep bool) {
	for _, value := range header.Values(loopChainHeader) {
		for _, part := range strings.Split(value, ",") {
			id := strings.TrimSpace(part)
			if id == "" {
				continue
			}
			if len(id) > 64 {
				// Ignore malformed values rather than letting a client make the
				// proxy retain an arbitrarily large header in memory.
				continue
			}
			chain = append(chain, id)
			if len(chain) >= maxProxyHops {
				return chain, true
			}
		}
	}
	return chain, false
}

func chainContains(chain []string, id string) bool {
	for _, hop := range chain {
		if hop == id {
			return true
		}
	}
	return false
}

func (s *server) rejectProxyLoop(w http.ResponseWriter, r *http.Request, chainLen int, message string) {
	logger.Errorf("proxy loop rejected: host=%s path=%s chain=%d: %s", r.Host, r.URL.Path, chainLen, message)
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	http.Error(w, message, http.StatusLoopDetected)
}

// loopGuardTransport appends the current instance ID to every regclient HTTP
// request. The public regclient RegClient does not expose a general-purpose
// RoundTripper option, but it does expose reg.WithHTTPClient, so this wrapper
// is installed through that option.
type loopGuardTransport struct {
	base       http.RoundTripper
	instanceID string
}

func (t *loopGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	chain := loopChainFromContext(req.Context())
	hops := make([]string, 0, len(chain)+1)
	hops = append(hops, chain...)
	if t.instanceID != "" {
		hops = append(hops, t.instanceID)
	}
	if len(hops) == 0 {
		return t.roundTripper().RoundTrip(req)
	}

	outbound := req.Clone(req.Context())
	outbound.Header.Del(loopChainHeader)
	for _, hop := range hops {
		outbound.Header.Add(loopChainHeader, hop)
	}
	return t.roundTripper().RoundTrip(outbound)
}

func (t *loopGuardTransport) roundTripper() http.RoundTripper {
	if t.base != nil {
		return t.base
	}
	return http.DefaultTransport
}
