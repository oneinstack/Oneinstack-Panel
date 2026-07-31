package ssh

import "testing"

func TestTerminalInputParserIgnoresCursorSequences(t *testing.T) {
	session := &TerminalSession{line: make([]byte, 0, 64)}
	commands := session.consumeInputLocked([]byte("echo ok\x1b[A\r"))
	if len(commands) != 1 || commands[0] != "echo ok" {
		t.Fatalf("commands = %#v, want one clean submitted line", commands)
	}
}

func TestTerminalInputParserRecognizesReadlineExecuteShortcut(t *testing.T) {
	session := &TerminalSession{line: make([]byte, 0, 64)}
	commands := session.consumeInputLocked([]byte("pwd\x0f"))
	if len(commands) != 1 || commands[0] != "pwd" {
		t.Fatalf("commands = %#v, want Ctrl-O submission to be audited", commands)
	}
}

func TestTerminalAuditTokenDoesNotKeepControlCharacters(t *testing.T) {
	if got := sanitizeAuditToken(" Client\nClosed;DROP ", 32); got != "clientcloseddrop" {
		t.Fatalf("sanitizeAuditToken() = %q", got)
	}
}
