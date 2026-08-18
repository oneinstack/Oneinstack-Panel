//go:build linux

package website

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// ensureManagedDirectory creates/open each component relative to a directory
// descriptor. O_NOFOLLOW prevents a concurrent or pre-existing symlink from
// redirecting the operation outside the configured web root.
func ensureManagedDirectory(base, target string) (bool, error) {
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
		if errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(fd, part, 0755); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				return false, fmt.Errorf("create website root: %w", mkdirErr)
			}
			created = true
			child, openErr = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			return false, fmt.Errorf("open website root component: %w", openErr)
		}
		unix.Close(fd)
		fd = child
	}
	return created, nil
}
