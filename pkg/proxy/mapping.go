package proxy

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// loadRegistryMappings reads the optional registry mapping file. A missing
// file is not an error: mappings are optional and path-mode requests carrying
// a registry domain work without them.
func loadRegistryMappings(path string) (map[string]RegistryMapping, error) {
	if path == "" {
		return map[string]RegistryMapping{}, nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Debugf("registry mapping file %s does not exist; continuing without mappings", path)
			return map[string]RegistryMapping{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	logger.Debugf("loading registry mappings from %s", path)

	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var raw map[string]RegistryMapping
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	mappings := make(map[string]RegistryMapping, len(raw))
	for key, mapping := range raw {
		name := normalizeRegistryName(key)
		if err := validateRegistryName(name); err != nil {
			return nil, fmt.Errorf("%s: registry key %q: %w", path, key, err)
		}
		if _, exists := mappings[name]; exists {
			return nil, fmt.Errorf("%s: duplicate registry key %q", path, name)
		}
		mapping.Registry = strings.TrimSpace(mapping.Registry)
		mapping.DefaultNamespace = strings.TrimSpace(mapping.DefaultNamespace)
		if mapping.Registry != "" {
			if err := validateUpstream(mapping.Registry); err != nil {
				return nil, fmt.Errorf("%s: registry %q: %w", path, name, err)
			}
		}
		if mapping.DefaultNamespace != "" {
			if err := validateNamespace(mapping.DefaultNamespace); err != nil {
				return nil, fmt.Errorf("%s: registry %q defaultNamespace: %w", path, name, err)
			}
		}
		mappings[name] = mapping
	}
	return mappings, nil
}

// logRegistryMappings prints the effective routing table once at startup in a
// stable order so operators can confirm what the proxy will do.
func logRegistryMappings(mappings map[string]RegistryMapping, infof func(string, ...any)) {
	if len(mappings) == 0 {
		return
	}
	keys := make([]string, 0, len(mappings))
	for key := range mappings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		mapping := mappings[key]
		upstream := mapping.Registry
		if upstream == "" {
			upstream = key
		}
		namespace := mapping.DefaultNamespace
		if namespace == "" {
			namespace = "-"
		}
		infof("registry mapping: %s -> %s (default namespace: %s)", key, upstream, namespace)
	}
}

func normalizeRegistryName(name string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
}

func validateRegistryName(name string) error {
	if name == "" {
		return fmt.Errorf("must not be empty")
	}
	if strings.Contains(name, "://") {
		return fmt.Errorf("must not include a URL scheme")
	}
	if strings.Contains(name, "/") {
		return fmt.Errorf("must be a host[:port] without a path")
	}
	if strings.ContainsAny(name, " \t\r\n") {
		return fmt.Errorf("must not contain whitespace")
	}
	if _, _, err := net.SplitHostPort(name); err != nil && strings.Contains(name, ":") {
		// Accept bare IPv6 hosts only when they are bracketed, matching URL
		// host syntax. A colon without a valid port is otherwise a typo.
		if !strings.HasPrefix(name, "[") {
			return fmt.Errorf("invalid host or host:port")
		}
	}
	return nil
}

func validateUpstream(upstream string) error {
	if strings.Contains(upstream, "://") {
		return fmt.Errorf("must not include a URL scheme")
	}
	if strings.ContainsAny(upstream, " \t\r\n?#") {
		return fmt.Errorf("must not contain whitespace, query, or fragment")
	}
	host, prefix, _ := strings.Cut(upstream, "/")
	if host == "" {
		return fmt.Errorf("host must not be empty")
	}
	if err := validateRegistryName(host); err != nil {
		return err
	}
	if prefix == "" {
		return nil
	}
	for _, segment := range strings.Split(prefix, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("repository prefix %q is invalid", prefix)
		}
	}
	return nil
}

func validateNamespace(namespace string) error {
	if namespace == "" {
		return nil
	}
	if strings.Contains(namespace, "/") {
		return fmt.Errorf("must be a single path component")
	}
	if namespace == "." || namespace == ".." {
		return fmt.Errorf("must not be %q", namespace)
	}
	if strings.ContainsAny(namespace, " \t\r\n") {
		return fmt.Errorf("must not contain whitespace")
	}
	return nil
}
