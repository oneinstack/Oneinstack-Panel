package safe

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

const (
	BackendNone      = "none"
	BackendUFW       = "ufw"
	BackendFirewalld = "firewalld"
	BackendIPTables  = "iptables"

	DisableConfirmation = "DISABLE FIREWALL"
	panelRuleRemark     = "Panel 管理端口（系统保护）"
)

var (
	ErrValidation  = errors.New("firewall validation failed")
	ErrProtected   = errors.New("protected firewall rule")
	ErrUnsupported = errors.New("unsupported firewall operation")
)

type CommandRunner interface {
	LookPath(name string) (string, error)
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type OSCommandRunner struct{}

func (OSCommandRunner) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (OSCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{
		"LC_ALL=C",
		"LANG=C",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s failed: %w: %s", name, err, string(output))
	}
	return output, nil
}

type backendState struct {
	Name           string
	Installed      bool
	Enabled        bool
	Persistent     bool
	CanToggle      bool
	RepairRequired bool
	Warning        string
}

func backendPersistentDefault(name string) bool {
	switch name {
	case BackendUFW, BackendFirewalld:
		return true
	default:
		return false
	}
}

type commandOperation struct {
	name     string
	args     []string
	undoName string
	undoArgs []string
}
