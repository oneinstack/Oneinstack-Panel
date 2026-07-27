//go:build linux

package filemanager

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameExclusive(sourceParent *os.File, sourceName string, targetParent *os.File, targetName string) error {
	return unix.Renameat2(
		int(sourceParent.Fd()),
		sourceName,
		int(targetParent.Fd()),
		targetName,
		unix.RENAME_NOREPLACE,
	)
}
