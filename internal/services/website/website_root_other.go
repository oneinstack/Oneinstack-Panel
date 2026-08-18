//go:build !linux

package website

import (
	"errors"
	"fmt"
	"os"
)

// Non-Linux builds retain the existing Lstat-before-MkdirAll behavior. Panel
// production deployments are Linux and use the descriptor-relative helper.
func ensureManagedDirectory(base, target string) (bool, error) {
	if _, err := validateManagedPath(base, target); err != nil {
		return false, err
	}
	info, err := os.Stat(target)
	if err == nil {
		if !info.IsDir() {
			return false, errors.New("website root exists but is not a directory")
		}
		return false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("stat website root: %w", err)
	}
	if err := os.MkdirAll(target, 0755); err != nil {
		return false, fmt.Errorf("create website root: %w", err)
	}
	return true, nil
}
