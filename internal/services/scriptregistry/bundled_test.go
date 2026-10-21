package scriptregistry

import (
	"path/filepath"
	"testing"
)

func TestProductionBundledPackagesValidate(t *testing.T) {
	root := filepath.Join("..", "..", "..", "script-registry", "bundled")
	lock, err := VerifyBundledProduction(root)
	if err != nil {
		t.Fatal(err)
	}
	packageVersions := make(map[string]string, len(lock.Packages))
	for _, record := range lock.Packages {
		packageVersions[record.Component] = record.Version
	}
	expected := map[string][]string{
		"firewalld": {"2.1.1"},
		"nginx":     {"1.26.2", "1.28.2", "1.31.0"},
		"mysql":     {"8.0.45"},
		"php":       {"8.1.34", "8.2.30", "8.3.30"},
		"redis":     {"7.4.8"},
	}
	for component, softwareVersions := range expected {
		t.Run(component, func(t *testing.T) {
			manifest, err := validateDirectory(filepath.Join(root, component, packageVersions[component]))
			if err != nil {
				t.Fatal(err)
			}
			if manifest.Component.ID != component {
				t.Fatalf("component id = %q", manifest.Component.ID)
			}
			for _, version := range softwareVersions {
				if !manifest.supportsSoftwareVersion(version) {
					t.Fatalf("package does not support %s", version)
				}
			}
			if manifest.Actions.Status == "" ||
				manifest.Actions.Start == "" ||
				manifest.Actions.Stop == "" ||
				manifest.Actions.Restart == "" {
				t.Fatalf("component service actions are incomplete: %#v", manifest.Actions)
			}
			if (component == "nginx" || component == "php") && manifest.Actions.Reload == "" {
				t.Fatalf("%s should support safe reload", component)
			}
			if (component == "mysql" || component == "redis") && manifest.Actions.Reload != "" {
				t.Fatalf("%s must not advertise unsupported reload", component)
			}
			if manifest.Actions.ConfigGet == "" || manifest.Actions.ConfigApply == "" {
				t.Fatalf("%s managed configuration actions are incomplete", component)
			}
			if manifest.Timeouts.ConfigGet < 1 || manifest.Timeouts.ConfigApply < 1 {
				t.Fatalf("%s managed configuration timeouts are incomplete", component)
			}
		})
	}
}

func TestParseManifestRejectsUnknownFields(t *testing.T) {
	contents := []byte(`schemaVersion: 1
component:
  id: nginx
  name: Nginx
  version: 1.0.0
  softwareVersions: ["1.28.2"]
  channel: stable
compatibility:
  systems:
    - id: ubuntu
      versions: ["24.04"]
  architectures: [amd64]
actions:
  precheck: scripts/precheck.sh
  install: scripts/install.sh
  verify: scripts/verify.sh
  uninstall: scripts/uninstall.sh
parameters:
  - name: SOFTWARE_VERSION
    type: string
    required: true
    misspelledField: true
`)
	if _, err := parseManifest(contents); err == nil {
		t.Fatal("expected unknown manifest field to be rejected")
	}
}
