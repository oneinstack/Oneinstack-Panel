//go:build !darwin && !linux

package filemanager

import (
	"errors"
	"os"
)

func renameExclusive(_ *os.File, _ string, _ *os.File, _ string) error {
	return errors.New("atomic no-replace rename is not supported on this platform")
}
