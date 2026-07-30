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

func TestCompatibleVersionNormalizesDevelopmentBuild(t *testing.T) {
	previousVersion := Version
	t.Cleanup(func() {
		Version = previousVersion
	})

	Version = "dev"
	if actual := CompatibleVersion(); actual != "0.1.0-dev" {
		t.Fatalf("CompatibleVersion() = %q, want 0.1.0-dev", actual)
	}

	Version = "v0.1.0-test.19"
	if actual := CompatibleVersion(); actual != Version {
		t.Fatalf("CompatibleVersion() = %q, want %q", actual, Version)
	}
}
