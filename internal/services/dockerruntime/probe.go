package dockerruntime

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// ProbeResult distinguishes CLI presence from daemon and Compose availability.
type ProbeResult struct {
	Installed      bool
	ClientVersion  string
	ServerVersion  string
	ComposeVersion string
	DaemonError    error
}

// Probe uses the same read-only commands for inventory and container management.
func Probe(ctx context.Context, binary string, run func(context.Context, ...string) (string, error)) ProbeResult {
	var result ProbeResult
	path, err := exec.LookPath(binary)
	if err != nil {
		return result
	}
	result.Installed = true
	if run == nil {
		run = func(ctx context.Context, args ...string) (string, error) {
			command := exec.CommandContext(ctx, path, args...)
			command.WaitDelay = time.Second
			out, err := command.Output()
			return string(out), err
		}
	}
	version, err := run(ctx, "version", "--format", "{{.Client.Version}}|{{.Server.Version}}")
	result.DaemonError = err
	if err == nil {
		client, server, _ := strings.Cut(strings.TrimSpace(version), "|")
		result.ClientVersion, result.ServerVersion = client, server
	}
	if version, err := run(ctx, "compose", "version", "--short"); err == nil {
		result.ComposeVersion = strings.TrimSpace(version)
	}
	return result
}
