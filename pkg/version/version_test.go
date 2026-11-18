package version

import (
	"fmt"
	"runtime"
	"testing"
)

func TestString(t *testing.T) {
	originalVersion := Version
	originalRevision := Revision
	originalDistributionVersion := DistributionVersion
	t.Cleanup(func() {
		Version = originalVersion
		Revision = originalRevision
		DistributionVersion = originalDistributionVersion
	})

	Version = "v1.0.1"
	Revision = "0123456789ab"
	DistributionVersion = "v3.1.2+unknown"

	got := String()
	want := fmt.Sprintf(
		"registry-proxy 1.0.1 (distribution 3.1.2, revision 0123456789ab, %s)",
		runtime.Version(),
	)
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestCleanVersionFallback(t *testing.T) {
	if got := cleanVersion(" +unknown "); got != "unknown" {
		t.Fatalf("cleanVersion() = %q, want %q", got, "unknown")
	}
}
