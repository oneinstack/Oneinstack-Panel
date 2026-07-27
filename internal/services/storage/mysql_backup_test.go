package storage

import (
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/utils"
)

func TestWriteMySQLDefaultsFileKeepsCredentialOutOfArgumentsAndSecuresFile(t *testing.T) {
	directory := t.TempDir()
	connection := &models.Storage{
		Addr: "db.example.com", Port: "3306", Root: "root",
		Password: "quote\" slash\\ line\nnext",
	}
	path, cleanup, err := writeMySQLDefaultsFile(directory, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if filepath.Dir(path) != directory {
		t.Fatalf("credential file escaped target directory: %s", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("credential permissions = %04o, want 0600", info.Mode().Perm())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, `password="quote\" slash\\ line\nnext"`) {
		t.Fatalf("credential was not safely escaped: %q", text)
	}
}

func TestEscapeMySQLOptionEscapesControlCharacters(t *testing.T) {
	got := escapeMySQLOption("a\\b\"c\nd\re\tf")
	want := `a\\b\"c\nd\re\tf`
	if got != want {
		t.Fatalf("escape = %q, want %q", got, want)
	}
}

func TestMySQLDatabaseOperatorBackupAndRestoreCommandContract(t *testing.T) {
	prepareStorageTest(t)
	binDirectory := t.TempDir()
	argumentLog := filepath.Join(t.TempDir(), "dump-args.log")
	restoreLog := filepath.Join(t.TempDir(), "restore.sql")
	t.Setenv("MYSQL_ARGS_LOG", argumentLog)
	t.Setenv("MYSQL_RESTORE_LOG", restoreLog)
	t.Setenv("ONEINSTACK_MYSQL_BIN_DIR", binDirectory)

	writeExecutable := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(binDirectory, name), []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable("mysqldump", `#!/bin/sh
printf '%s\n' "$@" > "$MYSQL_ARGS_LOG"
printf '%s' 'CREATE TABLE example(id INT);'
`)
	writeExecutable("mysql", `#!/bin/sh
cat > "$MYSQL_RESTORE_LOG"
`)

	encrypted, err := utils.EncryptCredential(
		"root-command-secret",
		utils.CredentialPurposeStoragePassword,
	)
	if err != nil {
		t.Fatal(err)
	}
	connection := &models.Storage{
		Addr: "127.0.0.1", Port: "3306", Root: "root",
		Password: encrypted, Type: "mysql",
	}
	if err := app.DB().Create(connection).Error; err != nil {
		t.Fatal(err)
	}
	library := &models.Library{
		PID: connection.ID, Name: "site_db", Type: "mysql",
	}
	if err := app.DB().Create(library).Error; err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "site_db.sql.gz")
	operator := NewMySQLDatabaseOperator()
	if err := operator.Backup(
		context.Background(),
		library.ID,
		destination,
		io.Discard,
		func(int, string) {},
	); err != nil {
		t.Fatalf("backup: %v", err)
	}
	compressed, err := os.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(compressed)
	if err != nil {
		compressed.Close()
		t.Fatal(err)
	}
	sql, err := io.ReadAll(reader)
	reader.Close()
	compressed.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(sql) != "CREATE TABLE example(id INT);" {
		t.Fatalf("unexpected backup SQL: %q", sql)
	}
	arguments, err := os.ReadFile(argumentLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(arguments), "root-command-secret") {
		t.Fatalf("database secret leaked into command arguments: %s", arguments)
	}
	if !strings.Contains(string(arguments), "--single-transaction") ||
		!strings.Contains(string(arguments), "--add-drop-database") {
		t.Fatalf("required safe dump arguments missing: %s", arguments)
	}

	if err := operator.Restore(
		context.Background(),
		library.ID,
		destination,
		io.Discard,
		func(int, string) {},
	); err != nil {
		t.Fatalf("restore: %v", err)
	}
	restored, err := os.ReadFile(restoreLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(sql) {
		t.Fatalf("restored SQL = %q, want %q", restored, sql)
	}
	credentialFiles, err := filepath.Glob(filepath.Join(filepath.Dir(destination), ".mysql-client-*.cnf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(credentialFiles) != 0 {
		t.Fatalf("temporary credential files were not removed: %v", credentialFiles)
	}
}
