package websitetask

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"oneinstack/internal/models"
)

const (
	archiveManifestName = "manifest.json"
	archiveDatabaseName = "database/dump.sql.gz"
	archiveSchema       = 1
	maxManifestBytes    = 1 << 20
	maxConfigBytes      = 1 << 20
)

type archiveLimits struct {
	MaxBytes int64
	MaxFiles int
}

type archiveFile struct {
	Path       string `json:"path"`
	Type       string `json:"type"`
	Mode       int64  `json:"mode"`
	Size       int64  `json:"size,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	LinkTarget string `json:"linkTarget,omitempty"`
}

type archiveDatabase struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Entry  string `json:"entry"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type archiveManifest struct {
	Schema      int              `json:"schema"`
	CreatedAt   time.Time        `json:"createdAt"`
	Website     models.Website   `json:"website"`
	NginxConfig string           `json:"nginxConfig"`
	Files       []archiveFile    `json:"files"`
	Database    *archiveDatabase `json:"database,omitempty"`
}

type databaseDump struct {
	ID   int64
	Name string
	Path string
}

func buildArchive(
	ctx context.Context,
	site *models.Website,
	rootPath, configPath string,
	database *databaseDump,
	destination string,
	limits archiveLimits,
) (*archiveManifest, int64, string, error) {
	if site == nil || site.ID <= 0 {
		return nil, 0, "", errors.New("website snapshot is invalid")
	}
	if limits.MaxBytes <= 0 || limits.MaxFiles <= 0 {
		return nil, 0, "", errors.New("website archive limits are invalid")
	}
	config, err := readOptionalRegularFile(configPath, maxConfigBytes)
	if err != nil {
		return nil, 0, "", fmt.Errorf("read Nginx configuration: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		return nil, 0, "", err
	}
	partial := destination + ".partial"
	_ = os.Remove(partial)
	output, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, 0, "", err
	}
	success := false
	defer func() {
		_ = output.Close()
		if !success {
			_ = os.Remove(partial)
		}
	}()
	gzipWriter, err := gzip.NewWriterLevel(output, gzip.BestSpeed)
	if err != nil {
		return nil, 0, "", err
	}
	tarWriter := tar.NewWriter(gzipWriter)
	manifest := &archiveManifest{
		Schema: archiveSchema, CreatedAt: time.Now().UTC(),
		Website: *site, NginxConfig: string(config),
	}
	var totalBytes int64
	fileCount := 0
	if rootPath != "" {
		if err := writeSiteTree(
			ctx, tarWriter, rootPath, limits, &fileCount, &totalBytes, &manifest.Files,
		); err != nil {
			return nil, 0, "", err
		}
	}
	if database != nil {
		size, checksum, err := writeRegularArchiveFile(
			ctx,
			tarWriter,
			database.Path,
			archiveDatabaseName,
			0600,
			limits,
			&fileCount,
			&totalBytes,
		)
		if err != nil {
			return nil, 0, "", fmt.Errorf("archive database dump: %w", err)
		}
		manifest.Database = &archiveDatabase{
			ID: database.ID, Name: database.Name, Entry: archiveDatabaseName,
			Size: size, SHA256: checksum,
		}
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return nil, 0, "", err
	}
	if len(manifestData) > maxManifestBytes {
		return nil, 0, "", errors.New("website backup manifest is too large")
	}
	if err := writeTarBytes(tarWriter, archiveManifestName, manifestData, 0600); err != nil {
		return nil, 0, "", err
	}
	if err := tarWriter.Close(); err != nil {
		return nil, 0, "", err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, 0, "", err
	}
	if err := output.Sync(); err != nil {
		return nil, 0, "", err
	}
	if err := output.Close(); err != nil {
		return nil, 0, "", err
	}
	if err := os.Rename(partial, destination); err != nil {
		return nil, 0, "", err
	}
	success = true
	size, checksum, err := verifyRegularFile(destination)
	if err != nil {
		return nil, 0, "", err
	}
	return manifest, size, checksum, nil
}

func writeSiteTree(
	ctx context.Context,
	writer *tar.Writer,
	root string,
	limits archiveLimits,
	fileCount *int,
	totalBytes *int64,
	records *[]archiveFile,
) error {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect website root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("website root must be a real directory")
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || !safeRelativePath(relative) {
			return errors.New("website entry escapes the managed root")
		}
		name := "site/" + filepath.ToSlash(relative)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		record := archiveFile{Path: filepath.ToSlash(relative), Mode: int64(info.Mode().Perm())}
		switch {
		case info.IsDir():
			(*fileCount)++
			if *fileCount > limits.MaxFiles {
				return errors.New("website backup exceeds the configured file limit")
			}
			record.Type = "directory"
			header := &tar.Header{
				Name: name + "/", Typeflag: tar.TypeDir,
				Mode: int64(info.Mode().Perm()), ModTime: info.ModTime(),
			}
			if err := writer.WriteHeader(header); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			record.Type = "file"
			size, checksum, err := writeRegularArchiveFile(
				ctx, writer, path, name, info.Mode().Perm(),
				limits, fileCount, totalBytes,
			)
			if err != nil {
				return err
			}
			record.Size = size
			record.SHA256 = checksum
		case info.Mode()&os.ModeSymlink != 0:
			(*fileCount)++
			if *fileCount > limits.MaxFiles {
				return errors.New("website backup exceeds the configured file limit")
			}
			record.Type = "symlink"
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if filepath.IsAbs(target) {
				return fmt.Errorf("website symlink %s has an absolute target", relative)
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(relative), target))
			if !safeRelativePath(resolved) {
				return fmt.Errorf("website symlink %s escapes the managed root", relative)
			}
			record.LinkTarget = filepath.ToSlash(target)
			header := &tar.Header{
				Name: name, Typeflag: tar.TypeSymlink, Linkname: filepath.ToSlash(target),
				Mode: int64(info.Mode().Perm()), ModTime: info.ModTime(),
			}
			if err := writer.WriteHeader(header); err != nil {
				return err
			}
		default:
			return fmt.Errorf("website entry %s has unsupported type", relative)
		}
		*records = append(*records, record)
		return nil
	})
}

func writeRegularArchiveFile(
	ctx context.Context,
	writer *tar.Writer,
	source, name string,
	mode os.FileMode,
	limits archiveLimits,
	fileCount *int,
	totalBytes *int64,
) (int64, string, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return 0, "", err
	}
	if !info.Mode().IsRegular() {
		return 0, "", errors.New("archive source is not a regular file")
	}
	if info.Size() < 0 || info.Size() > limits.MaxBytes-*totalBytes {
		return 0, "", errors.New("website backup exceeds the configured size limit")
	}
	(*fileCount)++
	if *fileCount > limits.MaxFiles {
		return 0, "", errors.New("website backup exceeds the configured file limit")
	}
	file, err := os.Open(source)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return 0, "", err
	}
	if !os.SameFile(info, openedInfo) {
		return 0, "", errors.New("archive source changed while opening")
	}
	header := &tar.Header{
		Name: name, Typeflag: tar.TypeReg, Mode: int64(mode.Perm()),
		Size: openedInfo.Size(), ModTime: openedInfo.ModTime(),
	}
	if err := writer.WriteHeader(header); err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	reader := io.TeeReader(file, hash)
	if _, err := copyWithContext(ctx, writer, reader, openedInfo.Size()); err != nil {
		return 0, "", err
	}
	*totalBytes += openedInfo.Size()
	return openedInfo.Size(), hex.EncodeToString(hash.Sum(nil)), nil
}

func writeTarBytes(writer *tar.Writer, name string, value []byte, mode int64) error {
	if err := writer.WriteHeader(&tar.Header{
		Name: name, Typeflag: tar.TypeReg, Mode: mode,
		Size: int64(len(value)), ModTime: time.Now().UTC(),
	}); err != nil {
		return err
	}
	_, err := writer.Write(value)
	return err
}

type extractedArchive struct {
	Manifest     archiveManifest
	SiteRoot     string
	DatabasePath string
}

func extractArchive(
	ctx context.Context,
	source, stagingRoot string,
	limits archiveLimits,
) (*extractedArchive, error) {
	if err := os.MkdirAll(stagingRoot, 0700); err != nil {
		return nil, err
	}
	siteRoot := filepath.Join(stagingRoot, "site")
	databasePath := filepath.Join(stagingRoot, "database.sql.gz")
	if err := os.MkdirAll(siteRoot, 0750); err != nil {
		return nil, err
	}
	file, err := os.Open(source)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return nil, errors.New("website backup is not a valid gzip archive")
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	observed := make(map[string]archiveFile)
	var manifest archiveManifest
	manifestSeen := false
	databaseSeen := false
	var totalBytes int64
	fileCount := 0
	seenEntries := make(map[string]struct{})
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		name := filepath.ToSlash(filepath.Clean(header.Name))
		if name == "." || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") ||
			name == ".." || strings.HasPrefix(name, "../") {
			return nil, errors.New("website backup contains an unsafe path")
		}
		if _, exists := seenEntries[name]; exists {
			return nil, errors.New("website backup contains duplicate entries")
		}
		seenEntries[name] = struct{}{}
		fileCount++
		if fileCount > limits.MaxFiles+2 {
			return nil, errors.New("website backup exceeds the configured file limit")
		}
		switch {
		case name == archiveManifestName:
			if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > maxManifestBytes {
				return nil, errors.New("website backup manifest is invalid")
			}
			data, err := io.ReadAll(io.LimitReader(reader, maxManifestBytes+1))
			if err != nil || int64(len(data)) != header.Size {
				return nil, errors.New("website backup manifest is truncated")
			}
			if err := json.Unmarshal(data, &manifest); err != nil {
				return nil, errors.New("website backup manifest cannot be decoded")
			}
			manifestSeen = true
		case name == archiveDatabaseName:
			if header.Typeflag != tar.TypeReg {
				return nil, errors.New("database backup entry is not a regular file")
			}
			if _, err := extractRegular(ctx, reader, header, databasePath, limits, &totalBytes); err != nil {
				return nil, err
			}
			databaseSeen = true
		case strings.HasPrefix(name, "site/"):
			relative := strings.TrimPrefix(name, "site/")
			relative = strings.TrimSuffix(relative, "/")
			if !safeRelativePath(filepath.FromSlash(relative)) {
				return nil, errors.New("website file entry escapes the staging root")
			}
			destination := filepath.Join(siteRoot, filepath.FromSlash(relative))
			if err := ensureSafeParent(siteRoot, destination); err != nil {
				return nil, err
			}
			record := archiveFile{
				Path: relative, Mode: header.Mode & 0777,
				Size: header.Size,
			}
			switch header.Typeflag {
			case tar.TypeDir:
				record.Type = "directory"
				record.Size = 0
				if err := os.MkdirAll(destination, os.FileMode(header.Mode&0777)); err != nil {
					return nil, err
				}
			case tar.TypeReg:
				record.Type = "file"
				checksum, err := extractRegular(
					ctx, reader, header, destination, limits, &totalBytes,
				)
				if err != nil {
					return nil, err
				}
				record.SHA256 = checksum
			case tar.TypeSymlink:
				record.Type = "symlink"
				target := filepath.FromSlash(header.Linkname)
				if filepath.IsAbs(target) {
					return nil, errors.New("website backup contains an absolute symlink")
				}
				resolved := filepath.Clean(filepath.Join(filepath.Dir(relative), target))
				if !safeRelativePath(filepath.FromSlash(resolved)) {
					return nil, errors.New("website backup symlink escapes the staging root")
				}
				record.LinkTarget = filepath.ToSlash(target)
				if err := os.Symlink(target, destination); err != nil {
					return nil, err
				}
			default:
				return nil, errors.New("website backup contains an unsupported entry type")
			}
			observed[relative] = record
		default:
			return nil, errors.New("website backup contains an unknown entry")
		}
	}
	if !manifestSeen || manifest.Schema != archiveSchema || manifest.Website.ID <= 0 {
		return nil, errors.New("website backup manifest is missing or unsupported")
	}
	if len(manifest.Files) != len(observed) {
		return nil, errors.New("website backup file manifest does not match archive entries")
	}
	for _, expected := range manifest.Files {
		actual, ok := observed[expected.Path]
		if !ok ||
			actual.Type != expected.Type ||
			actual.Mode != expected.Mode ||
			actual.Size != expected.Size ||
			!strings.EqualFold(actual.SHA256, expected.SHA256) ||
			actual.LinkTarget != expected.LinkTarget {
			return nil, fmt.Errorf("website backup entry %s failed integrity verification", expected.Path)
		}
	}
	if manifest.Database != nil {
		if manifest.Database.Entry != archiveDatabaseName {
			return nil, errors.New("website backup database entry is invalid")
		}
		size, checksum, err := verifyRegularFile(databasePath)
		if err != nil {
			return nil, errors.New("website backup database entry is missing")
		}
		if size != manifest.Database.Size ||
			!strings.EqualFold(checksum, manifest.Database.SHA256) {
			return nil, errors.New("website database dump failed integrity verification")
		}
	} else {
		if databaseSeen {
			return nil, errors.New("website backup contains an undeclared database entry")
		}
		databasePath = ""
	}
	return &extractedArchive{
		Manifest: manifest, SiteRoot: siteRoot, DatabasePath: databasePath,
	}, nil
}

func extractRegular(
	ctx context.Context,
	reader io.Reader,
	header *tar.Header,
	destination string,
	limits archiveLimits,
	totalBytes *int64,
) (string, error) {
	if header.Size < 0 || header.Size > limits.MaxBytes-*totalBytes {
		return "", errors.New("website backup exceeds the configured expanded size limit")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		return "", err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode&0777))
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	writer := io.MultiWriter(output, hash)
	_, copyErr := copyWithContext(ctx, writer, reader, header.Size)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if syncErr != nil {
		return "", syncErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	*totalBytes += header.Size
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader, size int64) (int64, error) {
	if size < 0 {
		return 0, errors.New("negative archive entry size")
	}
	buffer := make([]byte, 128*1024)
	var written int64
	for written < size {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		remaining := size - written
		readSize := len(buffer)
		if remaining < int64(readSize) {
			readSize = int(remaining)
		}
		count, err := io.ReadFull(source, buffer[:readSize])
		if count > 0 {
			outputCount, writeErr := destination.Write(buffer[:count])
			written += int64(outputCount)
			if writeErr != nil {
				return written, writeErr
			}
			if outputCount != count {
				return written, io.ErrShortWrite
			}
		}
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

func ensureSafeParent(root, destination string) error {
	relative, err := filepath.Rel(root, filepath.Dir(destination))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("archive destination escapes staging root")
	}
	current := root
	if relative == "." {
		return nil
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0750); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("archive destination contains an unsafe parent")
		}
	}
	return nil
}

func safeRelativePath(value string) bool {
	cleaned := filepath.Clean(value)
	return cleaned != "." && cleaned != ".." && !filepath.IsAbs(cleaned) &&
		!strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}

func readOptionalRegularFile(path string, maxBytes int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, errors.New("file is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return nil, errors.New("file changed while opening")
	}
	return io.ReadAll(io.LimitReader(file, maxBytes+1))
}

func verifyRegularFile(path string) (int64, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, "", err
	}
	if !info.Mode().IsRegular() {
		return 0, "", errors.New("backup artifact is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return 0, "", errors.New("backup artifact changed while opening")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return 0, "", err
	}
	return openedInfo.Size(), hex.EncodeToString(hash.Sum(nil)), nil
}
