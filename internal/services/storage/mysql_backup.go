package storage

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services/databasetask"

	"github.com/shirou/gopsutil/v4/disk"
)

// MySQLDatabaseOperator executes MySQL backups and restores without exposing
// credentials through process arguments or environment variables.
type MySQLDatabaseOperator struct{}

func NewMySQLDatabaseOperator() *MySQLDatabaseOperator {
	return &MySQLDatabaseOperator{}
}

func (o *MySQLDatabaseOperator) ValidateConnection(ctx context.Context, libraryID int64) error {
	return TestLibraryConnectionContext(ctx, libraryID)
}

func TestLibraryConnectionContext(ctx context.Context, libraryID int64) error {
	_, connection, err := loadMySQLLibrary(libraryID)
	if err != nil {
		return err
	}
	return testStorageConnectionContext(ctx, connection)
}

func (o *MySQLDatabaseOperator) Backup(
	ctx context.Context,
	libraryID int64,
	destination string,
	log io.Writer,
	report databasetask.ProgressReporter,
) error {
	library, connection, err := loadMySQLLibrary(libraryID)
	if err != nil {
		return err
	}
	dumpBinary, err := mysqlBinary("mysqldump")
	if err != nil {
		return err
	}
	credentialPath, cleanup, err := writeMySQLDefaultsFile(filepath.Dir(destination), connection)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	if err := checkDatabaseBackupDiskSpace(filepath.Dir(destination)); err != nil {
		return err
	}
	partial := destination + ".partial"
	_ = os.Remove(partial)
	output, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create database backup file: %w", err)
	}
	removePartial := true
	defer func() {
		output.Close()
		if removePartial {
			_ = os.Remove(partial)
		}
	}()

	report(10, "正在连接 MySQL")
	command := exec.CommandContext(
		ctx,
		dumpBinary,
		"--defaults-extra-file="+credentialPath,
		"--single-transaction",
		"--quick",
		"--routines",
		"--events",
		"--triggers",
		"--hex-blob",
		"--add-drop-database",
		"--set-gtid-purged=OFF",
		"--databases",
		library.Name,
	)
	var stderr bytes.Buffer
	command.Stderr = io.MultiWriter(log, &stderr)
	gzipWriter, err := gzip.NewWriterLevel(output, gzip.BestSpeed)
	if err != nil {
		return fmt.Errorf("initialize database backup compression: %w", err)
	}
	command.Stdout = gzipWriter
	report(25, "正在导出并压缩数据库")
	runErr := command.Run()
	closeErr := gzipWriter.Close()
	syncErr := output.Sync()
	fileCloseErr := output.Close()
	if runErr != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return context.Canceled
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return commandError("mysqldump", runErr, stderr.String())
	}
	if closeErr != nil {
		return fmt.Errorf("finalize database backup compression: %w", closeErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sync database backup file: %w", syncErr)
	}
	if fileCloseErr != nil {
		return fmt.Errorf("close database backup file: %w", fileCloseErr)
	}
	report(90, "正在发布备份文件")
	if err := os.Rename(partial, destination); err != nil {
		return fmt.Errorf("publish database backup file: %w", err)
	}
	removePartial = false
	report(100, "数据库备份导出完成")
	return nil
}

func (o *MySQLDatabaseOperator) Restore(
	ctx context.Context,
	libraryID int64,
	source string,
	log io.Writer,
	report databasetask.ProgressReporter,
) error {
	library, connection, err := loadMySQLLibrary(libraryID)
	if err != nil {
		return err
	}
	mysql, err := mysqlBinary("mysql")
	if err != nil {
		return err
	}
	credentialPath, cleanup, err := writeMySQLDefaultsFile(filepath.Dir(source), connection)
	if err != nil {
		return err
	}
	defer cleanup()

	file, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open database backup: %w", err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open compressed database backup: %w", err)
	}
	defer gzipReader.Close()

	report(10, "正在连接 MySQL")
	command := exec.CommandContext(
		ctx,
		mysql,
		"--defaults-extra-file="+credentialPath,
		"--binary-mode=1",
		"--database="+library.Name,
	)
	command.Stdin = gzipReader
	var stderr bytes.Buffer
	command.Stderr = io.MultiWriter(log, &stderr)
	report(25, "正在恢复数据库，请勿中断服务")
	if err := command.Run(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return context.Canceled
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return commandError("mysql restore", err, stderr.String())
	}
	report(100, "数据库恢复完成")
	return nil
}

func commandError(command string, runErr error, stderr string) error {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		return fmt.Errorf("%s failed: %w", command, runErr)
	}
	return fmt.Errorf("%s failed: %s: %w", command, detail, runErr)
}

func loadMySQLLibrary(libraryID int64) (*models.Library, *models.Storage, error) {
	if libraryID <= 0 {
		return nil, nil, errors.New("database is required")
	}
	var library models.Library
	if err := app.DB().First(&library, libraryID).Error; err != nil {
		return nil, nil, err
	}
	if library.Type != "mysql" {
		return nil, nil, errors.New("database backup and restore currently support MySQL only")
	}
	if err := validateMySQLIdentifier(library.Name, 64); err != nil {
		return nil, nil, err
	}
	connection, err := loadStorage(library.PID)
	if err != nil {
		return nil, nil, err
	}
	if connection.Type != "mysql" {
		return nil, nil, errors.New("database connection is not MySQL")
	}
	port, err := strconv.Atoi(connection.Port)
	if err != nil || port < 1 || port > 65535 {
		return nil, nil, errors.New("database connection port is invalid")
	}
	return &library, connection, nil
}

func mysqlBinary(name string) (string, error) {
	if configured := strings.TrimSpace(os.Getenv("ONEINSTACK_MYSQL_BIN_DIR")); configured != "" {
		candidate := filepath.Join(filepath.Clean(configured), name)
		info, err := os.Stat(candidate)
		if err != nil {
			return "", fmt.Errorf("find %s: %w", name, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return "", fmt.Errorf("%s is not an executable regular file", candidate)
		}
		return candidate, nil
	}
	candidates := []string{
		filepath.Join("/usr/local/mysql/bin", name),
		filepath.Join("/usr/bin", name),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil &&
			info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return candidate, nil
		}
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s command is not installed: %w", name, err)
	}
	return path, nil
}

func writeMySQLDefaultsFile(directory string, connection *models.Storage) (string, func(), error) {
	if err := os.MkdirAll(directory, 0750); err != nil {
		return "", func() {}, err
	}
	file, err := os.CreateTemp(directory, ".mysql-client-*.cnf")
	if err != nil {
		return "", func() {}, fmt.Errorf("create MySQL credential file: %w", err)
	}
	path := file.Name()
	cleanup := func() {
		_ = os.Remove(path)
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		cleanup()
		return "", func() {}, err
	}
	content := "[client]\n" +
		"protocol=tcp\n" +
		"host=\"" + escapeMySQLOption(connection.Addr) + "\"\n" +
		"port=\"" + escapeMySQLOption(connection.Port) + "\"\n" +
		"user=\"" + escapeMySQLOption(connection.Root) + "\"\n" +
		"password=\"" + escapeMySQLOption(connection.Password) + "\"\n"
	if _, err := io.WriteString(file, content); err != nil {
		file.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("write MySQL credential file: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

func escapeMySQLOption(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
		"\t", `\t`,
	)
	return replacer.Replace(value)
}

func checkDatabaseBackupDiskSpace(directory string) error {
	usage, err := disk.Usage(directory)
	if err != nil {
		return fmt.Errorf("read database backup disk capacity: %w", err)
	}
	minimum := app.ONE_CONFIG.System.FileMinFreeBytes
	if minimum < 0 {
		minimum = 0
	}
	const operationHeadroom = uint64(1 << 20)
	required := uint64(minimum)
	if required > ^uint64(0)-operationHeadroom ||
		usage.Free <= required+operationHeadroom {
		return fmt.Errorf(
			"insufficient disk space for database backup: available %d bytes, reserved %d bytes",
			usage.Free,
			minimum,
		)
	}
	return nil
}
