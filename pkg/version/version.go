package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"

	distributionversion "github.com/distribution/distribution/v3/version"
)

// These values are replaced at build time through -ldflags. Keeping them as
// variables rather than constants also makes the fallback useful for local
// development builds.
var (
	Version             = "dev"
	Revision            = "unknown"
	DistributionVersion = cleanVersion(distributionversion.Version())
)

func init() {
	if isUnset(Revision) {
		Revision = vcsRevision()
	}
}

// String returns the registry-proxy version together with the upstream
// Distribution version and the source revision used to build the binary.
func String() string {
	return fmt.Sprintf(
		"registry-proxy %s (distribution %s, revision %s, %s)",
		cleanVersion(Version),
		cleanVersion(DistributionVersion),
		cleanVersion(Revision),
		runtime.Version(),
	)
}

// Print writes the build version information to stdout.
func Print() {
	fmt.Println(String())
}

func cleanVersion(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "v")
	value = strings.TrimSuffix(value, "+unknown")
	if value == "" {
		return "unknown"
	}
	return value
}

func isUnset(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || value == "unknown"
}

func vcsRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}

	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			return setting.Value
		}
	}

	return "unknown"
}
