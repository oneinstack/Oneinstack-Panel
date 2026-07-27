package software

import (
	"strings"
	"testing"
)

func TestParseComponentServiceProbeAcceptsStrictStatus(t *testing.T) {
	definition, err := NormalizeServiceComponent("webserver")
	if err != nil {
		t.Fatal(err)
	}
	probe, err := parseComponentServiceProbe([]byte(strings.Join([]string{
		"component=nginx",
		"service=nginx",
		"load_state=loaded",
		"active_state=active",
		"sub_state=running",
		"unit_file_state=enabled",
		"runtime_version=1.28.2",
		"can_reload=true",
	}, "\n")+"\n"), definition)
	if err != nil {
		t.Fatal(err)
	}
	if probe.ActiveState != "active" || probe.RuntimeVersion != "1.28.2" || !probe.CanReload {
		t.Fatalf("unexpected probe: %#v", probe)
	}
}

func TestParseComponentServiceProbeRejectsUntrustedFields(t *testing.T) {
	definition, err := NormalizeServiceComponent("nginx")
	if err != nil {
		t.Fatal(err)
	}
	base := strings.Join([]string{
		"component=nginx",
		"service=nginx",
		"load_state=loaded",
		"active_state=active",
		"sub_state=running",
		"unit_file_state=enabled",
		"runtime_version=1.28.2",
		"can_reload=true",
	}, "\n")
	for _, output := range []string{
		base + "\nmessage=<script>alert(1)</script>\n",
		strings.Replace(base, "component=nginx", "component=mysql", 1) + "\n",
		strings.Replace(base, "active_state=active", "active_state=active now", 1) + "\n",
		base + "\nactive_state=failed\n",
	} {
		if _, err := parseComponentServiceProbe([]byte(output), definition); err == nil {
			t.Fatalf("expected output to be rejected: %q", output)
		}
	}
}

func TestSupportedComponentServicesAreStable(t *testing.T) {
	definitions := SupportedComponentServices()
	if len(definitions) != 4 {
		t.Fatalf("service definitions = %d", len(definitions))
	}
	for _, value := range []string{"nginx", "mysql", "php-fpm", "redis"} {
		if _, err := NormalizeServiceComponent(value); err != nil {
			t.Fatalf("normalize %s: %v", value, err)
		}
	}
}
