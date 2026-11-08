//go:build linux

package website

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// ensureManagedDirectory creates/open each component relative to a directory
// descriptor. O_NOFOLLOW prevents a concurrent or pre-existing symlink from
// redirecting the operation outside the configured web root.
func ensureManagedDirectory(base, target string, owner *websiteRootOwner) (bool, error) {
	relative, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false, errors.New("managed path must be strictly below its configured root")
	}
	fd, err := unix.Open(filepath.Clean(base), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false, fmt.Errorf("open managed website root: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	created := false
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return false, errors.New("managed path contains an invalid directory component")
		}
		child, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		createdChild := false
		if errors.Is(openErr, unix.ENOENT) {
			mkdirErr := unix.Mkdirat(fd, part, 0755)
			if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				return false, fmt.Errorf("create website root: %w", mkdirErr)
			}
			createdChild = mkdirErr == nil
			created = created || createdChild
			child, openErr = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			return false, fmt.Errorf("open website root component: %w", openErr)
		}
		if createdChild && owner != nil {
			// Operate on the no-follow descriptor, never a path that could have
			// been replaced by a symlink. Existing directories keep their owner.
			if err := unix.Fchown(child, owner.uid, owner.gid); err != nil {
				unix.Close(child)
				_ = unix.Unlinkat(fd, part, unix.AT_REMOVEDIR)
				return false, fmt.Errorf("设置网站目录属主失败: %w", err)
			}
			if err := unix.Fchmod(child, 0750); err != nil {
				unix.Close(child)
				_ = unix.Unlinkat(fd, part, unix.AT_REMOVEDIR)
				return false, fmt.Errorf("设置网站目录权限失败: %w", err)
			}
		}
		unix.Close(fd)
		fd = child
	}
	return created, nil
}

func ensureWebsiteBase(base string, owner *websiteRootOwner) error {
	base, err := normalizedBasePath(base, "website root")
	if err != nil {
		return err
	}
	_, err = ensureManagedDirectory(string(filepath.Separator), base, owner)
	return err
}

func (owner *websiteRootOwner) checkAccess(ctx context.Context, path, permission string) error {
	if owner == nil || path == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "test", permission, path)
	if os.Geteuid() == 0 {
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
			Uid: uint32(owner.uid), Gid: uint32(owner.gid), Groups: owner.groups,
		}}
	} else if os.Geteuid() != owner.uid || os.Getegid() != owner.gid {
		return fmt.Errorf("Panel 无法切换到 Web 运行账号 %s 验证网站权限", owner.user)
	}
	if err := command.Run(); err != nil {
		return fmt.Errorf("Web 运行账号 %s 无法访问网站路径 %s（%s），请检查目录链及 ACL 权限: %w", owner.user, path, permission, err)
	}
	return nil
}
