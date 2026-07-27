package panelupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type OSCommandRunner struct{}

func (OSCommandRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Env = []string{
		"LC_ALL=C",
		"LANG=C",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	for key, value := range command.Env {
		if strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("invalid command environment")
		}
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s failed: %w: %s", command.Name, err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

type SystemdController struct {
	Runner CommandRunner
	Unit   string
}

func (s SystemdController) runner() CommandRunner {
	if s.Runner == nil {
		return OSCommandRunner{}
	}
	return s.Runner
}

func (s SystemdController) unit() string {
	if strings.TrimSpace(s.Unit) == "" {
		return "one.service"
	}
	return s.Unit
}

func (s SystemdController) IsActive(ctx context.Context) bool {
	_, err := s.runner().Run(ctx, Command{
		Name: "systemctl", Args: []string{"is-active", "--quiet", s.unit()},
	})
	return err == nil
}

func (s SystemdController) Stop(ctx context.Context) error {
	_, err := s.runner().Run(ctx, Command{
		Name: "systemctl", Args: []string{"stop", s.unit()},
	})
	return err
}

func (s SystemdController) Start(ctx context.Context) error {
	_, err := s.runner().Run(ctx, Command{
		Name: "systemctl", Args: []string{"start", s.unit()},
	})
	return err
}

type HTTPHealthChecker struct {
	Client *http.Client
}

func (h HTTPHealthChecker) WaitReady(ctx context.Context, rawURL string, timeout time.Duration) error {
	if err := validateHealthURL(rawURL); err != nil {
		return err
	}
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("HTTP status %d", response.StatusCode)
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("health check did not become ready: %w", lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func validateHealthURL(raw string) error {
	if err := validateRemoteURL(raw); err != nil {
		return fmt.Errorf("invalid health URL: %w", err)
	}
	return nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file: %s", source)
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepathDir(destination), 0750); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepathDir(destination), ".update-copy-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := io.Copy(temp, input); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, destination)
}

func filepathDir(path string) string {
	index := strings.LastIndexByte(path, os.PathSeparator)
	if index < 0 {
		return "."
	}
	if index == 0 {
		return string(os.PathSeparator)
	}
	return path[:index]
}
