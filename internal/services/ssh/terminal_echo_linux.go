//go:build linux

package ssh

import (
	"os"

	"golang.org/x/sys/unix"
)

func terminalInputVisible(terminal *os.File) bool {
	if terminal == nil {
		return false
	}
	termios, err := unix.IoctlGetTermios(int(terminal.Fd()), unix.TCGETS)
	return err == nil && termios.Lflag&unix.ECHO != 0
}
