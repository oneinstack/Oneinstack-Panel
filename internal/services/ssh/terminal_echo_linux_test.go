//go:build linux

package ssh

import (
	"testing"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func TestTerminalInputVisibleTracksPTYEcho(t *testing.T) {
	terminal, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	defer slave.Close()

	if !terminalInputVisible(terminal) {
		t.Fatal("new PTY unexpectedly has input echo disabled")
	}
	termios, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	termios.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(int(slave.Fd()), unix.TCSETS, termios); err != nil {
		t.Fatal(err)
	}
	if terminalInputVisible(terminal) {
		t.Fatal("PTY input remained auditable after echo was disabled")
	}
}
