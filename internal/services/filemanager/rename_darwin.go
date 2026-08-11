//go:build darwin

package filemanager

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameExclusive(sourceParent *os.File, sourceName string, targetParent *os.File, targetName string) error {
	return unix.RenameatxNp(
		int(sourceParent.Fd()),
		sourceName,
		int(targetParent.Fd()),
		targetName,
		unix.RENAME_EXCL,
	)
}

func readlinkAt(parent *os.File, name string) (string, error) {
	for size := 128; size <= 1<<20; size *= 2 {
		buffer := make([]byte, size)
		length, err := unix.Readlinkat(int(parent.Fd()), name, buffer)
		if err != nil {
			return "", err
		}
		if length < len(buffer) {
			return string(buffer[:length]), nil
		}
	}
	return "", ErrInvalidPath
}

func symlinkAt(target string, parent *os.File, name string) error {
	return unix.Symlinkat(target, int(parent.Fd()), name)
}
