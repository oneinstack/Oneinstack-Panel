package ssh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"oneinstack/app"
	"oneinstack/internal/services/audit"
)

var ErrTerminalIsolationUnavailable = errors.New("terminal isolation is unavailable")

type TerminalPolicy struct {
	Enabled          bool          `json:"enabled"`
	User             string        `json:"user"`
	WorkingDirectory string        `json:"workingDirectory"`
	MaxDuration      time.Duration `json:"-"`
	IdleTimeout      time.Duration `json:"-"`
	MaxConcurrent    int           `json:"maxConcurrent"`
	MaxPerUser       int           `json:"maxPerUser"`
}

type TerminalSecurityStatus struct {
	Enabled            bool   `json:"enabled"`
	IsolationAvailable bool   `json:"isolationAvailable"`
	Reason             string `json:"reason,omitempty"`
	RuntimeUser        string `json:"runtimeUser"`
	WorkingDirectory   string `json:"workingDirectory"`
	MaxSessionMinutes  int    `json:"maxSessionMinutes"`
	IdleMinutes        int    `json:"idleMinutes"`
	MaxOutputMB        int    `json:"maxOutputMB"`
	MaxConcurrent      int    `json:"maxConcurrent"`
	MaxPerUser         int    `json:"maxPerUser"`
	ActiveSessions     int    `json:"activeSessions"`
	CommandAudit       bool   `json:"commandAudit"`
	NoNewPrivileges    bool   `json:"noNewPrivileges"`
	Capabilities       string `json:"capabilities"`
}

type isolatedIdentity struct {
	username string
	uid      int
	gid      int
	home     string
}

func CurrentTerminalPolicy() TerminalPolicy {
	system := app.ONE_CONFIG.System
	return TerminalPolicy{
		Enabled:          system.TerminalEnabled,
		User:             strings.TrimSpace(system.TerminalUser),
		WorkingDirectory: filepath.Clean(strings.TrimSpace(system.TerminalWorkingDirectory)),
		MaxDuration:      time.Duration(system.TerminalSessionMins) * time.Minute,
		IdleTimeout:      time.Duration(system.TerminalIdleMins) * time.Minute,
		MaxConcurrent:    system.TerminalMaxConcurrent,
		MaxPerUser:       system.TerminalMaxPerUser,
	}
}

func GetTerminalSecurityStatus() TerminalSecurityStatus {
	policy := CurrentTerminalPolicy()
	status := TerminalSecurityStatus{
		Enabled:           policy.Enabled,
		RuntimeUser:       policy.User,
		WorkingDirectory:  policy.WorkingDirectory,
		MaxSessionMinutes: int(policy.MaxDuration / time.Minute),
		IdleMinutes:       int(policy.IdleTimeout / time.Minute),
		MaxOutputMB:       terminalMaxOutputBytes >> 20,
		MaxConcurrent:     policy.MaxConcurrent,
		MaxPerUser:        policy.MaxPerUser,
		ActiveSessions:    DefaultSessions.ActiveCount(),
		CommandAudit:      audit.Default() != nil,
		NoNewPrivileges:   true,
		Capabilities:      "none",
	}
	_, _, err := isolatedTerminalCommand(context.Background(), policy)
	if err != nil {
		status.Reason = err.Error()
		return status
	}
	if !status.CommandAudit {
		status.Reason = "终端审计链不可用"
		return status
	}
	status.IsolationAvailable = true
	return status
}

func isolatedTerminalCommand(
	ctx context.Context,
	policy TerminalPolicy,
) (*exec.Cmd, *isolatedIdentity, error) {
	if !policy.Enabled {
		return nil, nil, fmt.Errorf("%w: Web 终端未启用", ErrTerminalIsolationUnavailable)
	}
	if os.Geteuid() != 0 {
		return nil, nil, fmt.Errorf(
			"%w: Panel 必须由 root 管理进程启动，终端子进程才可安全降权",
			ErrTerminalIsolationUnavailable,
		)
	}
	identity, err := resolveIsolatedIdentity(policy.User)
	if err != nil {
		return nil, nil, err
	}
	if err := validateTerminalWorkingDirectory(policy.WorkingDirectory, identity); err != nil {
		return nil, nil, err
	}
	setpriv, err := resolveTerminalExecutable(
		"setpriv",
		"/usr/bin/setpriv",
		"/bin/setpriv",
	)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: 缺少 setpriv", ErrTerminalIsolationUnavailable)
	}
	prlimit, err := resolveTerminalExecutable(
		"prlimit",
		"/usr/bin/prlimit",
		"/bin/prlimit",
	)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: 缺少 prlimit", ErrTerminalIsolationUnavailable)
	}
	bash, err := resolveTerminalExecutable(
		"bash",
		"/bin/bash",
		"/usr/bin/bash",
	)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: /bin/bash 不可用", ErrTerminalIsolationUnavailable)
	}
	cpuSeconds := int(policy.MaxDuration.Seconds())
	if cpuSeconds < 60 {
		cpuSeconds = 60
	}
	command := exec.CommandContext(
		ctx,
		prlimit,
		"--core=0",
		"--nproc=128",
		"--nofile=256",
		"--fsize=536870912",
		"--as=2147483648",
		"--cpu="+strconv.Itoa(cpuSeconds),
		"--",
		setpriv,
		"--reuid="+strconv.Itoa(identity.uid),
		"--regid="+strconv.Itoa(identity.gid),
		"--init-groups",
		"--no-new-privs",
		"--inh-caps=-all",
		"--ambient-caps=-all",
		"--bounding-set=-all",
		"--",
		bash,
		"--noprofile",
		"--norc",
		"-c",
		"umask 077; exec "+bash+" --noprofile --norc -i",
	)
	command.Dir = policy.WorkingDirectory
	command.Env = []string{
		"TERM=xterm-256color",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + identity.home,
		"USER=" + identity.username,
		"LOGNAME=" + identity.username,
		"SHELL=" + bash,
		"HISTFILE=/dev/null",
		"HISTSIZE=0",
		"PROMPT_COMMAND=",
		"PS1=[one-terminal@\\h \\W]\\$ ",
		"TMOUT=" + strconv.Itoa(int(policy.IdleTimeout.Seconds())),
	}
	return command, identity, nil
}

func resolveIsolatedIdentity(username string) (*isolatedIdentity, error) {
	username = strings.TrimSpace(username)
	if username == "" || username == "root" {
		return nil, fmt.Errorf("%w: 终端运行用户不能是 root", ErrTerminalIsolationUnavailable)
	}
	account, err := user.Lookup(username)
	if err != nil {
		return nil, fmt.Errorf("%w: 找不到终端用户 %s", ErrTerminalIsolationUnavailable, username)
	}
	uid, uidErr := strconv.Atoi(account.Uid)
	gid, gidErr := strconv.Atoi(account.Gid)
	if uidErr != nil || gidErr != nil || uid <= 0 || gid <= 0 {
		return nil, fmt.Errorf("%w: 终端用户 UID/GID 无效", ErrTerminalIsolationUnavailable)
	}
	if err := rejectPrivilegedGroups(account); err != nil {
		return nil, err
	}
	home := filepath.Clean(strings.TrimSpace(account.HomeDir))
	if !filepath.IsAbs(home) || home == "/" {
		return nil, fmt.Errorf("%w: 终端用户主目录无效", ErrTerminalIsolationUnavailable)
	}
	return &isolatedIdentity{
		username: account.Username,
		uid:      uid,
		gid:      gid,
		home:     home,
	}, nil
}

func resolveTerminalExecutable(name string, candidates ...string) (string, error) {
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s executable not found in trusted locations", name)
}

func rejectPrivilegedGroups(account *user.User) error {
	groupIDs, err := account.GroupIds()
	if err != nil {
		return fmt.Errorf("%w: 无法读取终端用户组", ErrTerminalIsolationUnavailable)
	}
	privileged := map[string]struct{}{
		"root": {}, "sudo": {}, "wheel": {}, "admin": {},
		"docker": {}, "lxd": {}, "podman": {}, "systemd-journal": {},
	}
	for _, groupID := range groupIDs {
		if groupID == "0" {
			return fmt.Errorf("%w: 终端用户属于 root 组", ErrTerminalIsolationUnavailable)
		}
		group, lookupErr := user.LookupGroupId(groupID)
		if lookupErr != nil {
			continue
		}
		if _, denied := privileged[group.Name]; denied {
			return fmt.Errorf(
				"%w: 终端用户不能属于特权组 %s",
				ErrTerminalIsolationUnavailable,
				group.Name,
			)
		}
	}
	return nil
}

func validateTerminalWorkingDirectory(
	directory string,
	identity *isolatedIdentity,
) error {
	directory = filepath.Clean(strings.TrimSpace(directory))
	if !filepath.IsAbs(directory) || directory == "/" {
		return fmt.Errorf("%w: 终端工作目录无效", ErrTerminalIsolationUnavailable)
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: 终端工作目录不存在", ErrTerminalIsolationUnavailable)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: 无法校验终端工作目录权限", ErrTerminalIsolationUnavailable)
	}
	mode := info.Mode().Perm()
	allowed := mode&0001 != 0
	if stat.Uid == uint32(identity.uid) {
		allowed = mode&0100 != 0
	} else if stat.Gid == uint32(identity.gid) {
		allowed = mode&0010 != 0
	}
	if !allowed {
		return fmt.Errorf("%w: 终端用户不能进入工作目录", ErrTerminalIsolationUnavailable)
	}
	return nil
}
