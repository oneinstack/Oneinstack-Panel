package software

import (
	"strings"
	"testing"
)

func TestParseComponentConfigurationAcceptsManagedValues(t *testing.T) {
	definition, err := componentConfigurationDefinition("nginx")
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := parseComponentConfiguration([]byte(strings.Join([]string{
		"component=nginx",
		"revision=" + strings.Repeat("a", 64),
		"apply_mode=reload",
		"workerProcesses=auto",
		"workerConnections=4096",
		"keepaliveTimeout=65",
		"clientMaxBodySize=128",
	}, "\n")+"\n"), definition)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Values["workerProcesses"] != "auto" ||
		configuration.Values["clientMaxBodySize"] != "128" ||
		len(configuration.Fields) != 4 {
		t.Fatalf("unexpected configuration: %#v", configuration)
	}
}

func TestParseComponentConfigurationRejectsUntrustedOutput(t *testing.T) {
	definition, err := componentConfigurationDefinition("redis")
	if err != nil {
		t.Fatal(err)
	}
	base := strings.Join([]string{
		"component=redis",
		"revision=" + strings.Repeat("b", 64),
		"apply_mode=restart",
		"maxmemory=512",
		"maxmemoryPolicy=allkeys-lru",
		"appendonly=true",
		"timeout=0",
		"tcpKeepalive=300",
	}, "\n")
	for _, output := range []string{
		base + "\nrequirepass=secret\n",
		base + "\nmaxmemory=1024\n",
		strings.Replace(base, "component=redis", "component=mysql", 1) + "\n",
		strings.Replace(base, "maxmemoryPolicy=allkeys-lru", "maxmemoryPolicy=unsafe", 1) + "\n",
	} {
		if _, err := parseComponentConfiguration([]byte(output), definition); err == nil {
			t.Fatalf("expected output to be rejected: %q", output)
		}
	}
}

func TestNormalizeConfigurationValuesEnforcesCrossFieldRules(t *testing.T) {
	values := map[string]string{
		"memoryLimit":       "512",
		"uploadMaxFilesize": "128",
		"postMaxSize":       "64",
		"maxExecutionTime":  "30",
		"pmMaxChildren":     "32",
		"pmStartServers":    "4",
		"pmMinSpareServers": "2",
		"pmMaxSpareServers": "8",
	}
	if _, err := NormalizeConfigurationValues("php", values); err == nil ||
		!strings.Contains(err.Error(), "postMaxSize") {
		t.Fatalf("expected PHP upload relation error, got %v", err)
	}
	values["postMaxSize"] = "256"
	values["pmMaxSpareServers"] = "64"
	if _, err := NormalizeConfigurationValues("php", values); err == nil ||
		!strings.Contains(err.Error(), "process counts") {
		t.Fatalf("expected PHP-FPM process relation error, got %v", err)
	}
}

func TestPreviewConfigurationRequiresCurrentRevision(t *testing.T) {
	current := ComponentConfiguration{
		Component: "nginx",
		Revision:  strings.Repeat("c", 64),
		ApplyMode: "reload",
		Fields: []ConfigurationField{
			{Key: "workerProcesses", Label: "工作进程数", Type: "worker_processes"},
			integerField("workerConnections", "单进程连接数", "", "", 512, 65535),
			integerField("keepaliveTimeout", "长连接超时", "秒", "", 5, 300),
			integerField("clientMaxBodySize", "请求体上限", "MB", "", 1, 10240),
		},
		Values: map[string]string{
			"workerProcesses":   "auto",
			"workerConnections": "4096",
			"keepaliveTimeout":  "65",
			"clientMaxBodySize": "128",
		},
	}
	changed := map[string]string{
		"workerProcesses":   "auto",
		"workerConnections": "8192",
		"keepaliveTimeout":  "65",
		"clientMaxBodySize": "128",
	}
	if _, err := PreviewConfiguration(current, strings.Repeat("d", 64), changed); err != ErrConfigurationConflict {
		t.Fatalf("expected revision conflict, got %v", err)
	}
	preview, err := PreviewConfiguration(current, current.Revision, changed)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.HasChanges || len(preview.Changes) != 1 ||
		preview.Changes[0].Key != "workerConnections" {
		t.Fatalf("unexpected preview: %#v", preview)
	}
}
