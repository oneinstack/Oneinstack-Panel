package ssh

import (
	"strings"
	"testing"
)

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

func TestTerminalAuditCommandMessageKeepsUsefulCommandText(t *testing.T) {
	if got := terminalAuditCommandMessage("  systemctl   restart nginx  "); got != "command=systemctl restart nginx" {
		t.Fatalf("terminalAuditCommandMessage() = %q", got)
	}
}

func TestTerminalAuditCommandMessageRedactsCommonSecrets(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{
			name:    "environment assignment",
			command: "export API_TOKEN=secret-value && ./deploy.sh",
			want:    "command=export API_TOKEN=[REDACTED] && ./deploy.sh",
		},
		{
			name:    "mysql password",
			command: "mysql -uroot -pSecret123 database",
			want:    "command=mysql -uroot -p[REDACTED] database",
		},
		{
			name:    "authorization header",
			command: `curl -H "Authorization: Bearer top-secret" https://example.com`,
			want:    `command=curl -H "Authorization: Bearer [REDACTED]" https://example.com`,
		},
		{
			name:    "url credentials",
			command: "curl https://operator:secret@example.com/status",
			want:    "command=curl https://operator:[REDACTED]@example.com/status",
		},
		{
			name:    "password pipeline",
			command: "echo root:secret | chpasswd",
			want:    "command=[REDACTED: sensitive password command]",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := terminalAuditCommandMessage(test.command); got != test.want {
				t.Fatalf("terminalAuditCommandMessage() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTerminalAuditCommandMessageIsBoundedAndUTF8Safe(t *testing.T) {
	got := terminalAuditCommandMessage(strings.Repeat("命", 200))
	if len(got) > len("command=")+terminalAuditCommandMaxBytes {
		t.Fatalf("terminal audit message is too long: %d bytes", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("terminal audit message was not marked as truncated: %q", got)
	}
}

func TestTerminalHiddenInputIsNeverCollected(t *testing.T) {
	manager := NewTerminalSessionManager()
	session := &TerminalSession{
		manager: manager,
		line:    []byte("partial-command"),
	}
	if err := session.RecordInput([]byte("interactive-password\r"), false); err != nil {
		t.Fatal(err)
	}
	if len(session.line) != 0 || session.info.Commands != 0 {
		t.Fatalf("hidden terminal input was retained: line=%q commands=%d", session.line, session.info.Commands)
	}
}
