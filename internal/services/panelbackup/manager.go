package panelbackup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/glebarez/sqlite"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"

	"oneinstack/internal/buildinfo"
)

const (
	defaultMaxBackupBytes = int64(2 << 30)
	defaultMaxFiles       = 10000
	maxManifestBytes      = 1 << 20
)

var backupIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Manager struct {
	mu     sync.Mutex
	config Config
	db     *gorm.DB
	now    func() time.Time
}

type archiveSource struct {
	Path       string
	SourcePath string
	Kind       string
	Mode       fs.FileMode
	Size       int64
	SHA256     string
}

type preparedBackup struct {
	Manifest Manifest
	Root     string
	Cleanup  func()
}

type countingWriter struct {
	writer io.Writer
	total  int64
	limit  int64
}

func (w *countingWriter) Write(content []byte) (int, error) {
	if w.total+int64(len(content)) > w.limit {
		return 0, fmt.Errorf("%w: compressed archive exceeds configured limit", ErrInvalidBackup)
	}
	count, err := w.writer.Write(content)
	w.total += int64(count)
	return count, err
}

func NewManager(config Config, database *gorm.DB) (*Manager, error) {
	config.BasePath = filepath.Clean(strings.TrimSpace(config.BasePath))
	config.ConfigPath = filepath.Clean(strings.TrimSpace(config.ConfigPath))
	config.DatabasePath = filepath.Clean(strings.TrimSpace(config.DatabasePath))
	config.CertificatePath = filepath.Clean(strings.TrimSpace(config.CertificatePath))
	config.BackupRoot = filepath.Clean(strings.TrimSpace(config.BackupRoot))
	for name, path := range map[string]string{
		"base": config.BasePath, "config": config.ConfigPath,
		"database": config.DatabasePath, "backup": config.BackupRoot,
	} {
		if !filepath.IsAbs(path) || path == string(filepath.Separator) {
			return nil, fmt.Errorf("%s path must be a non-root absolute path", name)
		}
	}
	if config.CertificatePath != "." &&
		(!filepath.IsAbs(config.CertificatePath) || config.CertificatePath == string(filepath.Separator)) {
		return nil, fmt.Errorf("certificate path must be a non-root absolute path")
	}
	if config.MaxBackupBytes == 0 {
		config.MaxBackupBytes = defaultMaxBackupBytes
	}
	if config.MaxBackupBytes < 16<<20 || config.MaxBackupBytes > 32<<30 {
		return nil, fmt.Errorf("panel backup limit must be between 16 MiB and 32 GiB")
	}
	if config.MaxFiles == 0 {
		config.MaxFiles = defaultMaxFiles
	}
	if config.MaxFiles < 2 || config.MaxFiles > 100000 {
		return nil, fmt.Errorf("panel backup file limit must be between 2 and 100000")
	}
	if err := os.MkdirAll(config.BackupRoot, 0700); err != nil {
		return nil, fmt.Errorf("create panel backup directory: %w", err)
	}
	backupRootInfo, err := os.Lstat(config.BackupRoot)
	if err != nil {
		return nil, err
	}
	if !backupRootInfo.IsDir() || backupRootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("panel backup root must be a real directory")
	}
	return &Manager{config: config, db: database, now: time.Now}, nil
}

func (m *Manager) MaxBackupBytes() int64 {
	return m.config.MaxBackupBytes
}

func (m *Manager) Create(ctx context.Context, options CreateOptions) (BackupInfo, error) {
	if err := validatePassphrase(options.Passphrase); err != nil {
		return BackupInfo{}, err
	}
	if m.db == nil {
		return BackupInfo{}, errors.New("panel database is unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return BackupInfo{}, err
	}
	workspace, err := os.MkdirTemp(m.config.BackupRoot, ".create-*")
	if err != nil {
		return BackupInfo{}, err
	}
	defer os.RemoveAll(workspace)
	if err := os.Chmod(workspace, 0700); err != nil {
		return BackupInfo{}, err
	}
	databaseSnapshot := filepath.Join(workspace, "myadmin.db")
	escapedSnapshot := strings.ReplaceAll(databaseSnapshot, "'", "''")
	if result := m.db.Exec("VACUUM INTO '" + escapedSnapshot + "'"); result.Error != nil {
		return BackupInfo{}, fmt.Errorf("create consistent SQLite snapshot: %w", result.Error)
	}
	sources, err := m.collectSources(databaseSnapshot, options.IncludeCertificates)
	if err != nil {
		return BackupInfo{}, err
	}
	createdAt := m.now().UTC()
	manifest := Manifest{
		SchemaVersion: BackupSchemaVersion,
		CreatedAt:     createdAt, PanelVersion: buildinfo.Current().Version,
		IncludesCertificates: options.IncludeCertificates,
		Files:                make([]ManifestFile, 0, len(sources)),
	}
	for _, source := range sources {
		manifest.Files = append(manifest.Files, ManifestFile{
			Path: source.Path, Kind: source.Kind, Size: source.Size,
			Mode: uint32(source.Mode.Perm()), SHA256: source.SHA256,
		})
	}
	plainArchive := filepath.Join(workspace, "payload.tar.gz")
	if err := m.writeArchive(ctx, plainArchive, manifest, sources); err != nil {
		return BackupInfo{}, err
	}
	id, err := randomBackupID()
	if err != nil {
		return BackupInfo{}, err
	}
	fileName := "panel-" + createdAt.Format("20060102T150405Z") + "-" + id[:8] + ".onebak"
	finalPath := filepath.Join(m.config.BackupRoot, fileName)
	if err := encryptArchive(plainArchive, finalPath, options.Passphrase, createdAt); err != nil {
		return BackupInfo{}, err
	}
	info, err := backupFileInfo(finalPath)
	if err != nil {
		_ = os.Remove(finalPath)
		return BackupInfo{}, err
	}
	result := BackupInfo{
		ID: id, FileName: fileName, CreatedAt: createdAt,
		PanelVersion: manifest.PanelVersion, Size: info.Size,
		SHA256: info.SHA256, FileCount: len(manifest.Files),
		IncludesCertificates: manifest.IncludesCertificates,
	}
	// Validate the exact archive that was just written before publishing its
	// metadata. This keeps a successful create operation equivalent to a
	// backup that can pass the restore preflight.
	prepared, err := m.prepare(ctx, result, options.Passphrase)
	if err != nil {
		_ = os.Remove(finalPath)
		return BackupInfo{}, err
	}
	prepared.Cleanup()
	if err := m.writeMetadata(result); err != nil {
		_ = os.Remove(finalPath)
		return BackupInfo{}, err
	}
	return result, nil
}

func (m *Manager) Import(ctx context.Context, source io.Reader, passphrase string) (BackupInfo, error) {
	if source == nil {
		return BackupInfo{}, ErrInvalidBackup
	}
	if err := validatePassphrase(passphrase); err != nil {
		return BackupInfo{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	temporary, err := os.CreateTemp(m.config.BackupRoot, ".import-*.onebak")
	if err != nil {
		return BackupInfo{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return BackupInfo{}, err
	}
	maxEncryptedBytes := m.config.MaxBackupBytes + (1 << 20)
	written, err := io.Copy(temporary, io.LimitReader(source, maxEncryptedBytes+1))
	if err != nil {
		temporary.Close()
		return BackupInfo{}, err
	}
	if written < 1 || written > maxEncryptedBytes {
		temporary.Close()
		return BackupInfo{}, fmt.Errorf("%w: encrypted archive exceeds configured limit", ErrInvalidBackup)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return BackupInfo{}, err
	}
	if err := temporary.Close(); err != nil {
		return BackupInfo{}, err
	}
	header, err := readEncryptionHeader(temporaryPath)
	if err != nil {
		return BackupInfo{}, err
	}
	id, err := randomBackupID()
	if err != nil {
		return BackupInfo{}, err
	}
	fileName := "panel-import-" + header.CreatedAt.UTC().Format("20060102T150405Z") + "-" + id[:8] + ".onebak"
	finalPath := filepath.Join(m.config.BackupRoot, fileName)
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return BackupInfo{}, err
	}
	if err := syncDirectory(m.config.BackupRoot); err != nil {
		return BackupInfo{}, err
	}
	removeFinal := true
	defer func() {
		if removeFinal {
			_ = os.Remove(finalPath)
		}
	}()
	fileInfo, err := backupFileInfo(finalPath)
	if err != nil {
		return BackupInfo{}, err
	}
	candidate := BackupInfo{
		ID: id, FileName: fileName, CreatedAt: header.CreatedAt.UTC(),
		Size: fileInfo.Size, SHA256: fileInfo.SHA256, Imported: true,
	}
	prepared, err := m.prepare(ctx, candidate, passphrase)
	if err != nil {
		return BackupInfo{}, err
	}
	candidate.PanelVersion = prepared.Manifest.PanelVersion
	candidate.FileCount = len(prepared.Manifest.Files)
	candidate.IncludesCertificates = prepared.Manifest.IncludesCertificates
	prepared.Cleanup()
	if err := m.writeMetadata(candidate); err != nil {
		return BackupInfo{}, err
	}
	removeFinal = false
	return candidate, nil
}

func (m *Manager) List() ([]BackupInfo, error) {
	entries, err := os.ReadDir(m.config.BackupRoot)
	if err != nil {
		return nil, err
	}
	result := make([]BackupInfo, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := m.readMetadata(filepath.Join(m.config.BackupRoot, entry.Name()))
		if err != nil {
			continue
		}
		if entry.Name() != info.ID+".json" {
			continue
		}
		archivePath, err := m.archivePath(info)
		if err != nil {
			continue
		}
		stat, err := os.Lstat(archivePath)
		if err != nil || !stat.Mode().IsRegular() || stat.Size() != info.Size {
			continue
		}
		result = append(result, info)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result, nil
}

func (m *Manager) Get(id string) (BackupInfo, error) {
	if !backupIDPattern.MatchString(strings.TrimSpace(id)) {
		return BackupInfo{}, ErrNotFound
	}
	return m.readMetadata(filepath.Join(m.config.BackupRoot, id+".json"))
}

func (m *Manager) Open(id string) (*os.File, BackupInfo, error) {
	info, err := m.Get(id)
	if err != nil {
		return nil, BackupInfo{}, err
	}
	path, err := m.archivePath(info)
	if err != nil {
		return nil, BackupInfo{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, BackupInfo{}, ErrNotFound
		}
		return nil, BackupInfo{}, err
	}
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() != info.Size {
		file.Close()
		return nil, BackupInfo{}, ErrInvalidBackup
	}
	digest, err := fileSHA256(path)
	if err != nil || digest != info.SHA256 {
		file.Close()
		return nil, BackupInfo{}, ErrInvalidBackup
	}
	return file, info, nil
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, err := m.Get(id)
	if err != nil {
		return err
	}
	path, err := m.archivePath(info)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(filepath.Join(m.config.BackupRoot, info.ID+".json")); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(m.config.BackupRoot)
}

func (m *Manager) Preflight(ctx context.Context, id, passphrase string) (PreflightResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, err := m.Get(id)
	if err != nil {
		return PreflightResult{}, err
	}
	prepared, err := m.prepare(ctx, info, passphrase)
	if err != nil {
		return PreflightResult{}, err
	}
	defer prepared.Cleanup()
	return PreflightResult{
		Backup: info, SchemaVersion: prepared.Manifest.SchemaVersion,
		PanelVersion:         prepared.Manifest.PanelVersion,
		FileCount:            len(prepared.Manifest.Files),
		IncludesCertificates: prepared.Manifest.IncludesCertificates,
		ConfigValid:          true, DatabaseValid: true,
	}, nil
}

func (m *Manager) collectSources(databaseSnapshot string, includeCertificates bool) ([]archiveSource, error) {
	required := []archiveSource{
		{Path: "config/config.yaml", SourcePath: m.config.ConfigPath, Kind: "config", Mode: 0600},
		{Path: "database/myadmin.db", SourcePath: databaseSnapshot, Kind: "database", Mode: 0600},
	}
	optional := []archiveSource{
		{Path: "identity/panel-instance-id", SourcePath: filepath.Join(m.config.BasePath, "panel-instance-id"), Kind: "identity", Mode: 0600},
		{Path: "update/trusted-keys.json", SourcePath: filepath.Join(m.config.BasePath, "update", "trusted-keys.json"), Kind: "trust", Mode: 0600},
	}
	result := make([]archiveSource, 0, len(required)+len(optional))
	for _, source := range required {
		enriched, exists, err := inspectArchiveSource(source)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("required panel backup source %s is missing", source.SourcePath)
		}
		result = append(result, enriched)
	}
	for _, source := range optional {
		enriched, exists, err := inspectArchiveSource(source)
		if err != nil {
			return nil, err
		}
		if exists {
			result = append(result, enriched)
		}
	}
	if includeCertificates {
		certificates, err := m.collectCertificateSources()
		if err != nil {
			return nil, err
		}
		result = append(result, certificates...)
	}
	if len(result) > m.config.MaxFiles {
		return nil, fmt.Errorf("panel backup contains more than %d files", m.config.MaxFiles)
	}
	var total int64
	for _, source := range result {
		total += source.Size
		if total > m.config.MaxBackupBytes {
			return nil, fmt.Errorf("panel backup source data exceeds configured limit")
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func (m *Manager) collectCertificateSources() ([]archiveSource, error) {
	if m.config.CertificatePath == "." {
		return nil, nil
	}
	info, err := os.Lstat(m.config.CertificatePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("certificate root must be a real directory")
	}
	result := make([]archiveSource, 0)
	err = filepath.WalkDir(m.config.CertificatePath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == m.config.CertificatePath {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("certificate backup refuses symbolic link %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("certificate backup refuses non-regular file %s", path)
		}
		relative, err := filepath.Rel(m.config.CertificatePath, path)
		if err != nil || !safeArchivePath(relative) {
			return ErrInvalidBackup
		}
		source := archiveSource{
			Path:       "certificates/" + filepath.ToSlash(relative),
			SourcePath: path, Kind: "certificate",
		}
		enriched, exists, err := inspectArchiveSource(source)
		if err != nil {
			return err
		}
		if exists {
			result = append(result, enriched)
		}
		if len(result)+2 > m.config.MaxFiles {
			return fmt.Errorf("certificate backup contains more than %d files", m.config.MaxFiles-2)
		}
		return nil
	})
	return result, err
}

func inspectArchiveSource(source archiveSource) (archiveSource, bool, error) {
	info, err := os.Lstat(source.SourcePath)
	if errors.Is(err, os.ErrNotExist) {
		return archiveSource{}, false, nil
	}
	if err != nil {
		return archiveSource{}, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return archiveSource{}, false, fmt.Errorf("panel backup source must be a regular file: %s", source.SourcePath)
	}
	if source.Mode == 0 {
		source.Mode = info.Mode().Perm()
	}
	source.Size = info.Size()
	digest, err := fileSHA256(source.SourcePath)
	if err != nil {
		return archiveSource{}, false, err
	}
	source.SHA256 = digest
	return source, true, nil
}

func (m *Manager) writeArchive(ctx context.Context, path string, manifest Manifest, sources []archiveSource) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	limited := &countingWriter{writer: file, limit: m.config.MaxBackupBytes}
	compressor := gzip.NewWriter(limited)
	archive := tar.NewWriter(compressor)
	closeArchive := func() error {
		if err := archive.Close(); err != nil {
			return err
		}
		if err := compressor.Close(); err != nil {
			return err
		}
		return nil
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := archive.WriteHeader(&tar.Header{
		Name: "manifest.json", Mode: 0600, Size: int64(len(manifestBytes)),
		Typeflag: tar.TypeReg, ModTime: manifest.CreatedAt,
	}); err != nil {
		return err
	}
	if _, err := archive.Write(manifestBytes); err != nil {
		return err
	}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := archive.WriteHeader(&tar.Header{
			Name: source.Path, Mode: int64(source.Mode.Perm()), Size: source.Size,
			Typeflag: tar.TypeReg, ModTime: manifest.CreatedAt,
		}); err != nil {
			return err
		}
		input, err := os.Open(source.SourcePath)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.CopyN(io.MultiWriter(archive, hash), input, source.Size)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if hex.EncodeToString(hash.Sum(nil)) != source.SHA256 {
			return fmt.Errorf("panel backup source changed while reading: %s", source.Path)
		}
	}
	if err := closeArchive(); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	remove = false
	return nil
}

func randomBackupID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func backupFileInfo(path string) (struct {
	Size   int64
	SHA256 string
}, error) {
	var result struct {
		Size   int64
		SHA256 string
	}
	info, err := os.Lstat(path)
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() {
		return result, ErrInvalidBackup
	}
	result.Size = info.Size()
	result.SHA256, err = fileSHA256(path)
	return result, err
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (m *Manager) writeMetadata(info BackupInfo) error {
	content, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(m.config.BackupRoot, info.ID+".json"), append(content, '\n'), 0600)
}

func (m *Manager) readMetadata(path string) (BackupInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return BackupInfo{}, ErrNotFound
	}
	if err != nil {
		return BackupInfo{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 64<<10 {
		return BackupInfo{}, ErrInvalidBackup
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return BackupInfo{}, err
	}
	var result BackupInfo
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || ensureJSONEOF(decoder) != nil ||
		!backupIDPattern.MatchString(result.ID) || result.FileName == "" ||
		filepath.Base(result.FileName) != result.FileName || !strings.HasSuffix(result.FileName, ".onebak") ||
		result.CreatedAt.IsZero() || result.Size < 1 || len(result.SHA256) != sha256.Size*2 {
		return BackupInfo{}, ErrInvalidBackup
	}
	if _, err := hex.DecodeString(result.SHA256); err != nil {
		return BackupInfo{}, ErrInvalidBackup
	}
	return result, nil
}

func (m *Manager) archivePath(info BackupInfo) (string, error) {
	if !backupIDPattern.MatchString(info.ID) || filepath.Base(info.FileName) != info.FileName {
		return "", ErrInvalidBackup
	}
	path := filepath.Join(m.config.BackupRoot, info.FileName)
	if filepath.Dir(path) != m.config.BackupRoot {
		return "", ErrInvalidBackup
	}
	return path, nil
}

func (m *Manager) prepare(ctx context.Context, info BackupInfo, passphrase string) (preparedBackup, error) {
	archivePath, err := m.archivePath(info)
	if err != nil {
		return preparedBackup{}, err
	}
	actual, err := backupFileInfo(archivePath)
	if err != nil {
		return preparedBackup{}, withValidationStage(ValidationStageIntegrity, err)
	}
	if actual.Size != info.Size || actual.SHA256 != info.SHA256 {
		return preparedBackup{}, withValidationStage(
			ValidationStageIntegrity,
			fmt.Errorf("%w: encrypted archive digest does not match metadata", ErrInvalidBackup),
		)
	}
	workspace, err := os.MkdirTemp(m.config.BackupRoot, ".preflight-*")
	if err != nil {
		return preparedBackup{}, err
	}
	cleanup := func() { _ = os.RemoveAll(workspace) }
	if err := os.Chmod(workspace, 0700); err != nil {
		cleanup()
		return preparedBackup{}, err
	}
	plainArchive := filepath.Join(workspace, "payload.tar.gz")
	if _, err := decryptArchive(archivePath, plainArchive, passphrase, m.config.MaxBackupBytes); err != nil {
		cleanup()
		return preparedBackup{}, withValidationStage(ValidationStageDecrypt, err)
	}
	extractedRoot := filepath.Join(workspace, "payload")
	if err := os.Mkdir(extractedRoot, 0700); err != nil {
		cleanup()
		return preparedBackup{}, err
	}
	manifest, err := m.extractAndValidate(ctx, plainArchive, extractedRoot)
	if err != nil {
		cleanup()
		return preparedBackup{}, withValidationStage(ValidationStageManifest, err)
	}
	_ = os.Remove(plainArchive)
	if err := validateBackupConfig(filepath.Join(extractedRoot, "config", "config.yaml"), m.config.CertificatePath); err != nil {
		cleanup()
		return preparedBackup{}, withValidationStage(ValidationStageConfig, err)
	}
	if err := validateSQLiteSnapshot(filepath.Join(extractedRoot, "database", "myadmin.db")); err != nil {
		cleanup()
		return preparedBackup{}, withValidationStage(ValidationStageDatabase, err)
	}
	return preparedBackup{Manifest: manifest, Root: extractedRoot, Cleanup: cleanup}, nil
}

func (m *Manager) extractAndValidate(ctx context.Context, archivePath, destination string) (Manifest, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return Manifest{}, err
	}
	defer file.Close()
	compressor, err := gzip.NewReader(file)
	if err != nil {
		return Manifest{}, ErrInvalidBackup
	}
	defer compressor.Close()
	archive := tar.NewReader(compressor)
	header, err := archive.Next()
	if err != nil || header.Name != "manifest.json" || header.Typeflag != tar.TypeReg ||
		header.Size < 1 || header.Size > maxManifestBytes {
		return Manifest{}, ErrInvalidBackup
	}
	content, err := io.ReadAll(io.LimitReader(archive, maxManifestBytes+1))
	if err != nil || int64(len(content)) != header.Size {
		return Manifest{}, ErrInvalidBackup
	}
	var manifest Manifest
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || ensureJSONEOF(decoder) != nil {
		return Manifest{}, ErrInvalidBackup
	}
	if err := validateManifest(manifest, m.config.MaxFiles, m.config.MaxBackupBytes); err != nil {
		return Manifest{}, err
	}
	expected := make(map[string]ManifestFile, len(manifest.Files))
	for _, entry := range manifest.Files {
		expected[entry.Path] = entry
	}
	seen := make(map[string]struct{}, len(expected))
	for {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		header, err = archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil || header.Typeflag != tar.TypeReg || !safeArchivePath(header.Name) {
			return Manifest{}, ErrInvalidBackup
		}
		entry, exists := expected[header.Name]
		if !exists || header.Size != entry.Size || uint32(header.Mode)&0777 != entry.Mode {
			return Manifest{}, ErrInvalidBackup
		}
		if _, duplicate := seen[header.Name]; duplicate {
			return Manifest{}, ErrInvalidBackup
		}
		target := filepath.Join(destination, filepath.FromSlash(header.Name))
		if !pathWithin(destination, target) {
			return Manifest{}, ErrInvalidBackup
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return Manifest{}, err
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fs.FileMode(entry.Mode))
		if err != nil {
			return Manifest{}, err
		}
		hash := sha256.New()
		written, copyErr := io.CopyN(io.MultiWriter(output, hash), archive, entry.Size)
		closeErr := output.Close()
		if copyErr != nil || closeErr != nil || written != entry.Size ||
			hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
			return Manifest{}, ErrInvalidBackup
		}
		seen[header.Name] = struct{}{}
	}
	if len(seen) != len(expected) {
		return Manifest{}, ErrInvalidBackup
	}
	return manifest, nil
}

func validateManifest(manifest Manifest, maxFiles int, maxBytes int64) error {
	if manifest.SchemaVersion != BackupSchemaVersion || manifest.CreatedAt.IsZero() ||
		strings.TrimSpace(manifest.PanelVersion) == "" ||
		len(manifest.Files) < 2 || len(manifest.Files) > maxFiles {
		return ErrInvalidBackup
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	var total int64
	hasConfig := false
	hasDatabase := false
	for _, entry := range manifest.Files {
		if !safeArchivePath(entry.Path) || entry.Size < 0 || len(entry.SHA256) != sha256.Size*2 ||
			entry.Mode == 0 || entry.Mode&^0777 != 0 {
			return ErrInvalidBackup
		}
		if _, err := hex.DecodeString(entry.SHA256); err != nil {
			return ErrInvalidBackup
		}
		if _, duplicate := seen[entry.Path]; duplicate {
			return ErrInvalidBackup
		}
		switch {
		case entry.Path == "config/config.yaml" && entry.Kind == "config":
			hasConfig = true
		case entry.Path == "database/myadmin.db" && entry.Kind == "database":
			hasDatabase = true
		case entry.Path == "identity/panel-instance-id" && entry.Kind == "identity":
		case entry.Path == "update/trusted-keys.json" && entry.Kind == "trust":
		case strings.HasPrefix(entry.Path, "certificates/") && entry.Kind == "certificate":
			if !manifest.IncludesCertificates {
				return ErrInvalidBackup
			}
		default:
			return ErrInvalidBackup
		}
		seen[entry.Path] = struct{}{}
		total += entry.Size
		if total > maxBytes {
			return ErrInvalidBackup
		}
	}
	if !hasConfig || !hasDatabase {
		return ErrInvalidBackup
	}
	return nil
}

func validateBackupConfig(path, expectedCertificatePath string) error {
	_, err := readBackupConfig(path, expectedCertificatePath)
	return err
}

type backupRuntimeConfig struct {
	Port string
}

func readBackupConfig(path, expectedCertificatePath string) (backupRuntimeConfig, error) {
	var result backupRuntimeConfig
	content, err := os.ReadFile(path)
	if err != nil || len(content) == 0 || len(content) > 4<<20 {
		return result, fmt.Errorf("%w: config.yaml cannot be read", ErrInvalidBackup)
	}
	var candidate struct {
		System struct {
			Port            string `yaml:"port"`
			JWTSecret       string `yaml:"jwtSecret"`
			CredentialKey   string `yaml:"credentialKey"`
			CertificatePath string `yaml:"certificatePath"`
		} `yaml:"system"`
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(content)))
	decoder.KnownFields(false)
	if err := decoder.Decode(&candidate); err != nil {
		return result, fmt.Errorf("%w: config.yaml is invalid", ErrInvalidBackup)
	}
	port, err := strconv.Atoi(strings.TrimSpace(candidate.System.Port))
	if err != nil || port < 1 || port > 65535 {
		return result, fmt.Errorf("%w: panel HTTP port is invalid", ErrInvalidBackup)
	}
	if len(strings.TrimSpace(candidate.System.JWTSecret)) < 32 {
		return result, fmt.Errorf("%w: JWT key is missing", ErrInvalidBackup)
	}
	key, err := hex.DecodeString(strings.TrimSpace(candidate.System.CredentialKey))
	if err != nil || len(key) != 32 {
		return result, fmt.Errorf("%w: credential encryption key is invalid", ErrInvalidBackup)
	}
	certificatePath := filepath.Clean(strings.TrimSpace(candidate.System.CertificatePath))
	if expectedCertificatePath != "." && certificatePath != expectedCertificatePath {
		return result, fmt.Errorf(
			"%w: certificate path %q does not match this Panel path %q",
			ErrInvalidBackup,
			certificatePath,
			expectedCertificatePath,
		)
	}
	result.Port = strconv.Itoa(port)
	return result, nil
}

func (m *Manager) HealthURL() (string, error) {
	config, err := readBackupConfig(m.config.ConfigPath, m.config.CertificatePath)
	if err != nil {
		return "", err
	}
	return "http://127.0.0.1:" + config.Port + "/health/ready", nil
}

func validateSQLiteSnapshot(path string) error {
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro"
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("%w: open SQLite snapshot: %v", ErrInvalidBackup, err)
	}
	var result string
	if err := database.Raw("PRAGMA quick_check").Scan(&result).Error; err != nil || result != "ok" {
		return fmt.Errorf("%w: SQLite integrity check failed", ErrInvalidBackup)
	}
	sqlDB, err := database.DB()
	if err == nil {
		_ = sqlDB.Close()
	}
	return nil
}

func safeArchivePath(path string) bool {
	if path == "" || strings.Contains(path, "\\") || strings.HasPrefix(path, "/") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean != path || clean == "." {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func atomicWriteFile(path string, content []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.new")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
