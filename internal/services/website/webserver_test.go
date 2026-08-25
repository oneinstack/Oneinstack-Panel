package website

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebServerConfigManagerListsAndUpdatesManagedConfigs(t *testing.T) {
	manager := newWebServerConfigTestManager(t, &fakeNginxRunner{})
	files, err := manager.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("configuration files = %d, want 2: %#v", len(files), files)
	}
	if !files[0].Main || files[0].Path != "nginx.conf" {
		t.Fatalf("main configuration was not listed first: %#v", files)
	}
	if !files[1].Site || files[1].Path != "conf.d/example.conf" {
		t.Fatalf("site configuration metadata is incorrect: %#v", files[1])
	}

	document, err := manager.Read("conf.d/example.conf")
	if err != nil {
		t.Fatal(err)
	}
	if document.Content != "server { listen 80; }\n" || document.Revision == "" {
		t.Fatalf("unexpected configuration document: %#v", document)
	}
	result, err := manager.Update(context.Background(), WebServerConfigUpdate{
		Path:     document.Path,
		Content:  "server { listen 8080; }\n",
		Revision: document.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reloaded || result.Revision == document.Revision ||
		!strings.Contains(result.Content, "8080") {
		t.Fatalf("unexpected update result: %#v", result)
	}
	if _, err := os.Stat(result.BackupPath); err != nil {
		t.Fatalf("configuration backup is missing: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(manager.Server.ConfigRoot, "conf.d", "example.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "server { listen 8080; }\n" {
		t.Fatalf("configuration was not updated: %q", content)
	}
	runner := manager.Runner.(*fakeNginxRunner)
	if len(runner.calls) != 2 ||
		runner.calls[0][1] != "-t" ||
		runner.calls[1][1] != "-s" {
		t.Fatalf("unexpected validation/reload calls: %#v", runner.calls)
	}
}

func TestWebServerConfigManagerRestoresInvalidConfig(t *testing.T) {
	runner := &fakeNginxRunner{results: []runnerResult{{
		output: "nginx: invalid directive",
		err:    errors.New("exit status 1"),
	}}}
	manager := newWebServerConfigTestManager(t, runner)
	document, err := manager.Read("conf.d/example.conf")
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Update(context.Background(), WebServerConfigUpdate{
		Path:     document.Path,
		Content:  "invalid configuration\n",
		Revision: document.Revision,
	})
	if err == nil || !strings.Contains(err.Error(), "previous content restored") {
		t.Fatalf("expected validation rollback error, got %v", err)
	}
	content, readErr := os.ReadFile(filepath.Join(manager.Server.ConfigRoot, "conf.d", "example.conf"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != document.Content {
		t.Fatalf("invalid update was not rolled back: %q", content)
	}
}

func TestWebServerConfigManagerRejectsTraversalSymlinkAndStaleRevision(t *testing.T) {
	manager := newWebServerConfigTestManager(t, &fakeNginxRunner{})
	if _, err := manager.Read("../outside.conf"); err == nil {
		t.Fatal("configuration traversal was accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside.conf")
	if err := os.WriteFile(outside, []byte("outside\n"), 0640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(manager.Server.ConfigRoot, "conf.d", "link.conf")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Read("conf.d/link.conf"); err == nil {
		t.Fatal("configuration symlink was accepted")
	}
	document, err := manager.Read("conf.d/example.conf")
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Update(context.Background(), WebServerConfigUpdate{
		Path:     document.Path,
		Content:  "server { listen 8080; }\n",
		Revision: strings.Repeat("0", 64),
	})
	if !errors.Is(err, ErrWebServerConfigConflict) {
		t.Fatalf("stale revision error = %v", err)
	}
}

func newWebServerConfigTestManager(
	t *testing.T,
	runner CommandRunner,
) *WebServerConfigManager {
	t.Helper()
	prefix := t.TempDir()
	configRoot := filepath.Join(prefix, "conf")
	siteRoot := filepath.Join(configRoot, "conf.d")
	if err := os.MkdirAll(siteRoot, 0750); err != nil {
		t.Fatal(err)
	}
	mainConfig := filepath.Join(configRoot, "nginx.conf")
	if err := os.WriteFile(
		mainConfig,
		[]byte("events {}\nhttp { include conf.d/*.conf; }\n"),
		0640,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(siteRoot, "example.conf"),
		[]byte("server { listen 80; }\n"),
		0640,
	); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(prefix, "sbin", "nginx")
	if err := os.MkdirAll(filepath.Dir(binary), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0750); err != nil {
		t.Fatal(err)
	}
	return &WebServerConfigManager{
		Server: WebServerInfo{
			Available:              true,
			Component:              "nginx",
			Name:                   "Nginx",
			Version:                "1.28.2",
			Running:                true,
			BinaryPath:             binary,
			Prefix:                 prefix,
			ConfigRoot:             configRoot,
			MainConfigPath:         mainConfig,
			SiteConfigDir:          siteRoot,
			ConfigurationAvailable: true,
		},
		Runner:     runner,
		BackupRoot: filepath.Join(t.TempDir(), "backups"),
	}
}

func TestManagedWebServerCandidatesUseOneinstackSystemdUnit(t *testing.T) {
	candidates := managedWebServerCandidatesWithLookup(func(unit string, properties ...string) (map[string]string, error) {
		if unit != "oneinstack-nginx.service" {
			return nil, errors.New("unit not found")
		}
		return map[string]string{
			"ExecStart": "{ path=/opt/oneinstack/nginx/sbin/nginx ; argv[]=/opt/oneinstack/nginx/sbin/nginx ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }",
		}, nil
	})
	if len(candidates) != 1 {
		t.Fatalf("managed candidates = %d, want 1", len(candidates))
	}
	candidate := candidates[0]
	if candidate.Component != "nginx" ||
		candidate.Service != "oneinstack-nginx" ||
		candidate.Binary != "/opt/oneinstack/nginx/sbin/nginx" {
		t.Fatalf("unexpected managed candidate: %#v", candidate)
	}
	if candidate.Prefix != "/opt/oneinstack/nginx" ||
		candidate.Config != "/opt/oneinstack/nginx/conf" {
		t.Fatalf("unexpected managed candidate layout: %#v", candidate)
	}
}

func TestParseSystemdExecStartBinary(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "serialized path",
			value: "{ path=/usr/local/nginx/sbin/nginx ; argv[]=/usr/local/nginx/sbin/nginx ; ignore_errors=no ; }",
			want:  "/usr/local/nginx/sbin/nginx",
		},
		{
			name:  "raw absolute command",
			value: "/usr/bin/caddy run --config /etc/caddy/Caddyfile",
			want:  "/usr/bin/caddy",
		},
		{
			name:  "empty",
			value: "",
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseSystemdExecStartBinary(tc.value); got != tc.want {
				t.Fatalf("parseSystemdExecStartBinary(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}
