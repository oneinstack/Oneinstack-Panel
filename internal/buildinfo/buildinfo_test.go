package buildinfo

import "testing"

func TestCurrentContainsRuntimeAndConfiguredBuildMetadata(t *testing.T) {
	previousVersion := Version
	Version = "v1.2.3"
	t.Cleanup(func() {
		Version = previousVersion
	})

	info := Current()
	if info.Version != "v1.2.3" {
		t.Fatalf("version = %q, want v1.2.3", info.Version)
	}
	if info.GoVersion == "" || info.OS == "" || info.Arch == "" {
		t.Fatalf("runtime metadata is incomplete: %#v", info)
	}
}
