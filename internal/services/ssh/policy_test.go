package ssh

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestBuildRootTerminalCommandUsesFullRootShell(t *testing.T) {
	command := buildRootTerminalCommand(
		context.Background(),
		TerminalPolicy{
			MaxDuration: 15 * time.Minute,
			IdleTimeout: 5 * time.Minute,
		},
		"/usr/bin/prlimit",
		"/bin/bash",
		"/tmp/one-terminal-test.bashrc",
	)

	if command.Dir != "/root" {
		t.Fatalf("expected /root working directory, got %q", command.Dir)
	}
	arguments := strings.Join(command.Args, " ")
	for _, expected := range []string{
		"/usr/bin/prlimit",
		"/bin/bash --noprofile --rcfile /tmp/one-terminal-test.bashrc -i",
	} {
		if !strings.Contains(arguments, expected) {
			t.Fatalf("expected command arguments to contain %q: %s", expected, arguments)
		}
	}
	for _, forbidden := range []string{"setpriv", "--no-new-privs", "--reuid", "--bounding-set"} {
		if strings.Contains(arguments, forbidden) {
			t.Fatalf("root terminal must not contain %q: %s", forbidden, arguments)
		}
	}
	environment := strings.Join(command.Env, "\n")
	for _, expected := range []string{
		"TERM=xterm-256color",
		"HOME=/root",
		"USER=root",
		"LOGNAME=root",
		"TMOUT=300",
	} {
		if !strings.Contains(environment, expected) {
			t.Fatalf("expected root terminal environment to contain %q: %s", expected, environment)
		}
	}
	if strings.Contains(environment, "COLORTERM=truecolor") {
		t.Fatalf("root terminal must stay on the CSP-safe 256-color palette: %s", environment)
	}
}

func TestRootTerminalBashRCHasValidSyntaxAndVisualDefaults(t *testing.T) {
	for _, expected := range []string{
		"dircolors -b",
		"ls --color=always --classify --format=vertical --tabsize=8",
		"38;5;75m",
		"]0;%s@%s:%q",
		"PROMPT_COMMAND=_one_terminal_prompt_sync",
	} {
		if !strings.Contains(rootTerminalBashRC, expected) {
			t.Fatalf("expected terminal bashrc to contain %q", expected)
		}
	}
	command := exec.Command("/bin/bash", "--noprofile", "--norc", "-n", "-c", rootTerminalBashRC)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("invalid root terminal bashrc: %v: %s", err, output)
	}
}

func TestCreateRootTerminalBashRCCreatesPrivateTemporaryFile(t *testing.T) {
	path, cleanup, err := createRootTerminalBashRC()
	if err != nil {
		t.Fatalf("create terminal bashrc: %v", err)
	}
	defer cleanup()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat terminal bashrc: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected terminal bashrc mode 0600, got %o", info.Mode().Perm())
	}
}
