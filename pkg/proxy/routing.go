package proxy

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

type routeMode string

const (
	routeModePath      routeMode = "path"
	routeModeHost      routeMode = "host"
	routeModeNamespace routeMode = "namespace"
)

// registryRoute is the normalized view of one registry API request.
//
// Every entry point (path containing a registry domain, Host header, or ?ns=)
// resolves to the same local repository. Host/ns requests are rewritten before
// they reach the inner registry; path requests already contain the namespaced
// repository and are forwarded unchanged.
type registryRoute struct {
	mode             routeMode
	registry         string
	upstream         string
	localRepository  string
	sourceRepository string
	endpoint         string
	reference        string
	suffix           string
	rewritePath      bool
}

func (rt registryRoute) apply(r *http.Request) {
	if rt.rewritePath {
		r.URL.Path = "/v2/" + rt.localRepository + rt.suffix
		r.URL.RawPath = ""
	}
	if r.URL.Query().Has("ns") {
		query := r.URL.Query()
		query.Del("ns")
		r.URL.RawQuery = query.Encode()
	}
}

// sourceImage returns the upstream image reference for a manifest request.
func (rt registryRoute) sourceImage() (string, bool) {
	if rt.endpoint != "manifests" || rt.reference == "" || rt.sourceRepository == "" {
		return "", false
	}
	separator := ":"
	if strings.Contains(rt.reference, ":") {
		separator = "@"
	}
	return rt.upstream + "/" + rt.sourceRepository + separator + rt.reference, true
}

// resolveRoute maps a request to a registry route. A nil route means the
// request is a plain local-registry request and must be forwarded untouched.
// A non-nil error means the client supplied an invalid namespace selector.
//
// The routing priority is: a registry-shaped first path component, an explicit
// Host mapping, an explicit first-component mapping, ?ns=, an automatic host
// fallback for DNS Host headers, and finally a plain local request. The
// automatic fallback keeps Host/ns-style clients working without a mapping
// entry while IP and single-label hosts remain local.
func (s *server) resolveRoute(r *http.Request) (*registryRoute, error) {
	repository, endpoint, reference, suffix, ok := splitRegistryPath(r.URL.Path)
	if !ok {
		return nil, nil
	}

	originalFirst, _, _ := strings.Cut(repository, "/")
	first := normalizeRegistryName(originalFirst)
	host := normalizeRegistryName(r.Host)
	ns := normalizeRegistryName(r.URL.Query().Get("ns"))

	var (
		registry string
		mapping  RegistryMapping
		mode     routeMode
	)
	switch {
	case isRegistryDomain(first):
		// A registry-shaped first path component is an explicit path-mode
		// request. It must win over Host mappings so clients can address a
		// different registry without changing the Host header.
		registry, mapping = s.mappingEntry(first)
		mode = routeModePath
	case s.hasMappingHost(host):
		// Host mappings describe the logical registry selected by the Host
		// header. They take precedence over namespace mappings with the same
		// first path component (for example "dockerhub" in host mode).
		registry, mapping = s.mappingHost(host)
		mode = routeModeHost
	case s.hasMappingExact(first):
		registry, mapping = s.mappingEntry(first)
		mode = routeModePath
	case ns != "":
		if s.hasMappingExact(ns) {
			registry, mapping = s.mappingEntry(ns)
		} else if isRegistryDomain(ns) {
			registry = ns
		} else {
			return nil, fmt.Errorf("unknown namespace %q", ns)
		}
		mode = routeModeNamespace
	case isRegistryDomain(host):
		// Hosts.toml style without a mapping: the client resolves an upstream
		// registry name to the proxy and keeps that name in the Host header.
		// Use the host itself as the upstream registry. Explicit mappings above
		// still override the upstream and namespace defaults. IP and
		// single-label hosts stay local so the proxy can be addressed directly.
		registry = hostWithoutPort(host)
		mode = routeModeHost
	default:
		return nil, nil
	}

	if ns != "" && ns != registry {
		return nil, fmt.Errorf("namespace %q does not match registry %q", ns, registry)
	}

	var localRepository, sourceBase string
	switch mode {
	case routeModePath:
		localRepository = first + strings.TrimPrefix(repository, originalFirst)
		if mapping.preserveRepositoryPath(registry) {
			// Namespace mappings address repositories such as
			// /v2/dockerhub/alpine/socat on the upstream, so the namespace must
			// remain part of the source path.
			sourceBase = repository
		} else {
			sourceBase = strings.TrimPrefix(repository, originalFirst)
			sourceBase = strings.TrimPrefix(sourceBase, "/")
		}
	case routeModeHost, routeModeNamespace:
		localRepository = registry + "/" + repository
		sourceBase = repository
	}
	if localRepository == "" || sourceBase == "" {
		return nil, nil
	}

	upstream := mapping.Registry
	if upstream == "" {
		upstream = registry
	}
	sourceRepository := sourceBase
	if mapping.DefaultNamespace != "" && !strings.Contains(sourceBase, "/") {
		sourceRepository = mapping.DefaultNamespace + "/" + sourceBase
	}

	return &registryRoute{
		mode:             mode,
		registry:         registry,
		upstream:         upstream,
		localRepository:  localRepository,
		sourceRepository: sourceRepository,
		endpoint:         endpoint,
		reference:        reference,
		suffix:           suffix,
		rewritePath:      mode != routeModePath || localRepository != repository,
	}, nil
}

// preserveRepositoryPath reports whether the mapping key is part of the
// upstream repository path. Domain keys are registry hosts and are stripped.
// Namespace keys are preserved by default so a request for dockerhub/alpine is
// forwarded as dockerhub/alpine rather than being reinterpreted as Docker Hub.
func (m RegistryMapping) preserveRepositoryPath(registry string) bool {
	if m.PreservePath != nil {
		return *m.PreservePath
	}
	return !strings.ContainsAny(registry, ".:")
}

func (s *server) mappingEntry(name string) (string, RegistryMapping) {
	mapping, ok := s.mappings[name]
	if !ok {
		return name, RegistryMapping{}
	}
	return name, mapping
}

func (s *server) hasMappingExact(name string) bool {
	_, ok := s.mappings[name]
	return ok
}

func (s *server) hasMappingHost(host string) bool {
	if s.hasMappingExact(host) {
		return true
	}
	withoutPort := hostWithoutPort(host)
	return withoutPort != host && s.hasMappingExact(withoutPort)
}

func (s *server) mappingHost(host string) (string, RegistryMapping) {
	if s.hasMappingExact(host) {
		return s.mappingEntry(host)
	}
	return s.mappingEntry(hostWithoutPort(host))
}

// splitRegistryPath splits /v2/<repository>/<endpoint>/<reference>. Repository
// names may themselves contain endpoint-like segments, so the last valid
// marker wins.
func splitRegistryPath(path string) (repository, endpoint, reference, suffix string, ok bool) {
	if !strings.HasPrefix(path, "/v2/") {
		return "", "", "", "", false
	}
	rest := strings.TrimPrefix(path, "/v2/")
	if rest == "" {
		return "", "", "", "", false
	}
	segments := strings.Split(rest, "/")
	for i := len(segments) - 1; i >= 1; i-- {
		switch segments[i] {
		case "manifests", "blobs", "referrers":
			if i+1 >= len(segments) || segments[i+1] == "" {
				continue
			}
			return strings.Join(segments[:i], "/"), segments[i], strings.Join(segments[i+1:], "/"),
				"/" + strings.Join(segments[i:], "/"), true
		case "tags":
			if i+2 != len(segments) || segments[i+1] != "list" {
				continue
			}
			return strings.Join(segments[:i], "/"), "tags", "list", "/tags/list", true
		}
	}
	return "", "", "", "", false
}

func hostWithoutPort(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return hostport
}

// isRegistryDomain reports whether name is a DNS registry name (optionally
// with a port). IP addresses and single-label names are deliberately excluded
// so a proxy Service hostname is never mistaken for an upstream registry.
func isRegistryDomain(name string) bool {
	if name == "" {
		return false
	}
	host := name
	if h, _, err := net.SplitHostPort(name); err == nil {
		host = h
	} else if strings.Contains(name, ":") {
		return false
	}
	host = strings.Trim(host, "[]")
	if host == "" || net.ParseIP(host) != nil {
		return false
	}
	return validDomainName(host)
}

func validDomainName(name string) bool {
	if len(name) > 253 || !strings.Contains(name, ".") {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return false
		}
	}
	return true
}

// localTarget formats a repository and reference for the local registry.
func localTarget(repository, reference string) string {
	if strings.Contains(reference, ":") {
		return repository + "@" + reference
	}
	return repository + ":" + reference
}
