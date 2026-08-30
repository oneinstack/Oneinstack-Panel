package filemanager

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"strings"
	"syscall"

	"github.com/bodgit/sevenzip"
	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
	"github.com/nwaples/rardecode/v2"
	"github.com/ulikunitz/xz"
)

type ExtractOptions struct {
	Overwrite      bool
	MaxBytes       int64
	MaxEntries     int
	CapacityPolicy CapacityPolicy
}

type ExtractProgress struct {
	ProcessedBytes int64  `json:"processedBytes"`
	TotalBytes     int64  `json:"totalBytes"`
	Entries        int    `json:"entries"`
	CurrentPath    string `json:"currentPath"`
}

type ExtractProgressFunc func(ExtractProgress)

type archiveEntry struct {
	name string
	dir  bool
	size int64
	mode fs.FileMode
	open func() (io.ReadCloser, error)
}

// ProbeArchive validates a source file and identifies its real archive format.
func (m *Manager) ProbeArchive(virtualPath string) (ArchiveNameInfo, error) {
	relative, err := m.Relative(virtualPath)
	if err != nil {
		return ArchiveNameInfo{}, err
	}
	if relative == "." {
		return ArchiveNameInfo{}, ErrRootOperation
	}
	if err := m.validateNoSymlinkParents(relative); err != nil {
		return ArchiveNameInfo{}, err
	}
	info, err := m.root.Lstat(relative)
	if err != nil {
		return ArchiveNameInfo{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ArchiveNameInfo{}, ErrNotRegular
	}
	nameInfo := InspectArchiveName(info.Name())
	if nameInfo.MultiVolume {
		return ArchiveNameInfo{}, ErrArchiveMultiVolume
	}
	file, err := m.root.Open(relative)
	if err != nil {
		return ArchiveNameInfo{}, err
	}
	defer file.Close()
	expected := ""
	if nameInfo.Supported {
		expected = nameInfo.Format
	}
	format, err := detectArchiveFormat(file, expected)
	if err != nil {
		return ArchiveNameInfo{}, err
	}
	nameInfo.Format = format
	nameInfo.Supported = true
	if strings.TrimSpace(nameInfo.BaseName) == "" {
		nameInfo.BaseName = info.Name() + "-extracted"
	}
	return nameInfo, nil
}

// ExtractWithProgress extracts into targetDir. All archive data is written to
// a staging directory on the target filesystem first, then committed to the
// public target without crossing mount boundaries.
func (m *Manager) ExtractWithProgress(source, targetDir string, options ExtractOptions, report ExtractProgressFunc) (result OperationResult, err error) {
	if options.MaxBytes <= 0 || options.MaxEntries <= 0 {
		return OperationResult{}, ErrArchiveLimitExceeded
	}
	sourceRelative, err := m.Relative(source)
	if err != nil {
		return OperationResult{}, err
	}
	targetRelative, err := m.Relative(targetDir)
	if err != nil {
		return OperationResult{}, err
	}
	if sourceRelative == "." || sourceRelative == targetRelative {
		return OperationResult{}, ErrInvalidPath
	}
	if err := m.validateNoSymlinkParents(targetRelative); err != nil {
		return OperationResult{}, err
	}
	targetParentRelative := pathpkg.Dir(targetRelative)
	if targetParentRelative != "." {
		parentInfo, parentErr := m.root.Lstat(targetParentRelative)
		if parentErr != nil {
			return OperationResult{}, parentErr
		}
		if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
			return OperationResult{}, ErrArchiveTargetConflict
		}
	}

	probe, err := m.ProbeArchive(source)
	if err != nil {
		return OperationResult{}, err
	}
	totalBytes, totalEntries, err := m.scanArchive(sourceRelative, probe.Format, options)
	if err != nil {
		return OperationResult{}, err
	}
	if report != nil {
		report(ExtractProgress{TotalBytes: totalBytes})
	}
	reservation, _, err := m.ReserveCapacity(totalBytes, options.CapacityPolicy)
	if err != nil {
		return OperationResult{}, err
	}
	defer reservation.Release()

	// The staging directory must share the target's filesystem. A fixed
	// staging directory under the configured root breaks when targetDir is a
	// separate mount such as /tmp, causing renameat2 to return EXDEV.
	workRelative := pathpkg.Join(targetParentRelative, ".oneinstack-extract-"+uuid.NewString())
	payloadRelative := pathpkg.Join(workRelative, "payload")
	backupRelative := pathpkg.Join(workRelative, "backup")
	if err := m.mkdirAllStagingRelative(payloadRelative, 0700); err != nil {
		return OperationResult{}, err
	}
	defer func() {
		_ = m.removeAllRelative(workRelative)
	}()

	processedBytes, extractedEntries, err := m.extractArchive(sourceRelative, probe.Format, payloadRelative, targetRelative, totalBytes, options, report)
	if err != nil {
		return OperationResult{}, err
	}
	if processedBytes != totalBytes || extractedEntries != totalEntries {
		return OperationResult{}, ErrArchiveInvalid
	}
	if err := m.commitExtracted(payloadRelative, targetRelative, backupRelative, options.Overwrite); err != nil {
		return OperationResult{}, err
	}
	result = OperationResult{Path: m.VirtualPath(targetRelative), Bytes: processedBytes, Entries: extractedEntries}
	if report != nil {
		report(ExtractProgress{ProcessedBytes: processedBytes, TotalBytes: totalBytes, Entries: extractedEntries})
	}
	return result, nil
}

func (m *Manager) scanArchive(sourceRelative, format string, options ExtractOptions) (int64, int, error) {
	seen := make(map[string]bool)
	var totalBytes int64
	entries := 0
	err := m.walkArchive(sourceRelative, format, func(entry archiveEntry) error {
		name, err := normalizeArchiveEntry(entry.name)
		if err != nil {
			return err
		}
		if _, exists := seen[name]; exists {
			return ErrArchiveUnsafePath
		}
		for parent := pathpkg.Dir(name); parent != "."; parent = pathpkg.Dir(parent) {
			if isDir, exists := seen[parent]; exists && !isDir {
				return ErrArchiveUnsafePath
			}
		}
		if !entry.dir {
			for existing := range seen {
				if strings.HasPrefix(existing, name+"/") {
					return ErrArchiveUnsafePath
				}
			}
		}
		seen[name] = entry.dir
		entries++
		if entries > options.MaxEntries {
			return ErrArchiveLimitExceeded
		}
		if entry.dir {
			return nil
		}
		if entry.size >= 0 {
			if entry.size > options.MaxBytes-totalBytes {
				return ErrArchiveLimitExceeded
			}
			totalBytes += entry.size
			return nil
		}
		reader, err := entry.open()
		if err != nil {
			return normalizeArchiveError(err)
		}
		counted, copyErr := copyWithLimit(io.Discard, reader, options.MaxBytes-totalBytes)
		closeErr := reader.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return normalizeArchiveError(closeErr)
		}
		totalBytes += counted
		return nil
	})
	if err != nil {
		return 0, 0, normalizeArchiveError(err)
	}
	return totalBytes, entries, nil
}

func (m *Manager) extractArchive(sourceRelative, format, payloadRelative, targetRelative string, totalBytes int64, options ExtractOptions, report ExtractProgressFunc) (int64, int, error) {
	seen := make(map[string]struct{})
	var processed int64
	entries := 0
	err := m.walkArchive(sourceRelative, format, func(entry archiveEntry) error {
		name, err := normalizeArchiveEntry(entry.name)
		if err != nil {
			return err
		}
		if _, exists := seen[name]; exists {
			return ErrArchiveUnsafePath
		}
		seen[name] = struct{}{}
		entries++
		if entries > options.MaxEntries {
			return ErrArchiveLimitExceeded
		}
		staged := pathpkg.Join(payloadRelative, name)
		currentPath := m.VirtualPath(pathpkg.Join(targetRelative, name))
		if entry.dir {
			if err := m.mkdirAllStagingRelative(staged, safeDirectoryMode(entry.mode)); err != nil {
				return err
			}
			if report != nil {
				report(ExtractProgress{ProcessedBytes: processed, TotalBytes: totalBytes, Entries: entries, CurrentPath: currentPath})
			}
			return nil
		}
		if err := m.mkdirAllStagingRelative(pathpkg.Dir(staged), 0700); err != nil {
			return err
		}
		reader, err := entry.open()
		if err != nil {
			return normalizeArchiveError(err)
		}
		output, err := m.root.OpenFile(staged, os.O_CREATE|os.O_EXCL|os.O_WRONLY, safeFileMode(entry.mode))
		if err != nil {
			_ = reader.Close()
			return err
		}
		// Never write more than the amount measured and reserved during the
		// preflight pass, even if the source changes while the task is running.
		remaining := totalBytes - processed
		written, copyErr := copyWithLimit(&extractProgressWriter{writer: output, onWrite: func(n int64) {
			if report != nil {
				report(ExtractProgress{ProcessedBytes: processed + n, TotalBytes: totalBytes, Entries: entries, CurrentPath: currentPath})
			}
		}}, reader, remaining)
		readerCloseErr := reader.Close()
		if copyErr == nil {
			copyErr = output.Sync()
		}
		outputCloseErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if readerCloseErr != nil {
			return normalizeArchiveError(readerCloseErr)
		}
		if outputCloseErr != nil {
			return outputCloseErr
		}
		if entry.size >= 0 && written != entry.size {
			return ErrArchiveInvalid
		}
		processed += written
		return nil
	})
	if err != nil {
		return 0, 0, normalizeArchiveError(err)
	}
	return processed, entries, nil
}

type extractProgressWriter struct {
	writer  io.Writer
	written int64
	onWrite func(int64)
}

func (w *extractProgressWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.written += int64(n)
	if n > 0 && w.onWrite != nil {
		w.onWrite(w.written)
	}
	return n, err
}

func copyWithLimit(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	if limit < 0 {
		return 0, ErrArchiveLimitExceeded
	}
	written, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return written, normalizeArchiveError(err)
	}
	if written > limit {
		return written, ErrArchiveLimitExceeded
	}
	return written, nil
}

func (m *Manager) walkArchive(sourceRelative, format string, visit func(archiveEntry) error) error {
	file, err := m.root.Open(sourceRelative)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	baseName := InspectArchiveName(info.Name()).BaseName
	if baseName == "" {
		baseName = info.Name() + "-extracted"
	}

	switch format {
	case ArchiveFormatZIP:
		reader, err := zip.NewReader(file, info.Size())
		if err != nil {
			return normalizeArchiveError(err)
		}
		for _, item := range reader.File {
			if item.Flags&0x1 != 0 {
				return ErrArchiveEncrypted
			}
			mode := item.Mode()
			if mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
				return ErrArchiveUnsupportedEntry
			}
			entry := archiveEntry{name: item.Name, dir: mode.IsDir(), size: int64(item.UncompressedSize64), mode: mode, open: item.Open}
			if entry.dir {
				entry.size = 0
			}
			if err := visit(entry); err != nil {
				return err
			}
		}
		return nil
	case ArchiveFormatRAR:
		reader, err := rardecode.NewReader(file)
		if err != nil {
			return normalizeArchiveError(err)
		}
		for {
			header, nextErr := reader.Next()
			if errors.Is(nextErr, io.EOF) {
				return nil
			}
			if nextErr != nil {
				return normalizeArchiveError(nextErr)
			}
			if header.Encrypted || header.HeaderEncrypted {
				return ErrArchiveEncrypted
			}
			if header.LinkType != 0 {
				return ErrArchiveUnsupportedEntry
			}
			size := header.UnPackedSize
			if header.UnKnownSize {
				size = -1
			}
			if err := visit(archiveEntry{name: header.Name, dir: header.IsDir, size: size, mode: header.Mode(), open: func() (io.ReadCloser, error) {
				return io.NopCloser(reader), nil
			}}); err != nil {
				return err
			}
		}
	case ArchiveFormat7Z:
		reader, err := sevenzip.NewReader(file, info.Size())
		if err != nil {
			return normalizeArchiveError(err)
		}
		for _, item := range reader.File {
			itemInfo := item.FileInfo()
			mode := itemInfo.Mode()
			if mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
				return ErrArchiveUnsupportedEntry
			}
			entry := archiveEntry{name: item.Name, dir: itemInfo.IsDir(), size: itemInfo.Size(), mode: mode, open: item.Open}
			if entry.dir {
				entry.size = 0
			}
			if err := visit(entry); err != nil {
				return err
			}
		}
		return nil
	case ArchiveFormatTAR, ArchiveFormatTARGZ, ArchiveFormatTARBZ2, ArchiveFormatTARXZ, ArchiveFormatTARZSTD:
		stream, closeStream, err := archiveTarStream(file, format)
		if err != nil {
			return normalizeArchiveError(err)
		}
		defer closeStream()
		reader := tar.NewReader(stream)
		for {
			header, nextErr := reader.Next()
			if errors.Is(nextErr, io.EOF) {
				return nil
			}
			if nextErr != nil {
				return normalizeArchiveError(nextErr)
			}
			dir := header.Typeflag == tar.TypeDir
			regular := header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA
			if !dir && !regular {
				return ErrArchiveUnsupportedEntry
			}
			if err := visit(archiveEntry{name: header.Name, dir: dir, size: header.Size, mode: header.FileInfo().Mode(), open: func() (io.ReadCloser, error) {
				return io.NopCloser(reader), nil
			}}); err != nil {
				return err
			}
		}
	case ArchiveFormatGZIP, ArchiveFormatBZIP2, ArchiveFormatXZ, ArchiveFormatZSTD:
		stream, closeStream, err := archivePlainStream(file, format)
		if err != nil {
			return normalizeArchiveError(err)
		}
		defer closeStream()
		return visit(archiveEntry{name: baseName, size: -1, mode: 0644, open: func() (io.ReadCloser, error) {
			return io.NopCloser(stream), nil
		}})
	default:
		return ErrArchiveUnsupportedFormat
	}
}

func archiveTarStream(file *os.File, format string) (io.Reader, func(), error) {
	switch format {
	case ArchiveFormatTAR:
		return file, func() {}, nil
	case ArchiveFormatTARGZ:
		reader, err := gzip.NewReader(file)
		if err != nil {
			return nil, func() {}, err
		}
		return reader, func() { _ = reader.Close() }, nil
	case ArchiveFormatTARBZ2:
		return bzip2.NewReader(file), func() {}, nil
	case ArchiveFormatTARXZ:
		reader, err := xz.NewReader(file)
		return reader, func() {}, err
	case ArchiveFormatTARZSTD:
		reader, err := zstd.NewReader(file)
		if err != nil {
			return nil, func() {}, err
		}
		return reader, reader.Close, nil
	default:
		return nil, func() {}, ErrArchiveUnsupportedFormat
	}
}

func archivePlainStream(file *os.File, format string) (io.Reader, func(), error) {
	switch format {
	case ArchiveFormatGZIP:
		reader, err := gzip.NewReader(file)
		if err != nil {
			return nil, func() {}, err
		}
		return reader, func() { _ = reader.Close() }, nil
	case ArchiveFormatBZIP2:
		return bzip2.NewReader(file), func() {}, nil
	case ArchiveFormatXZ:
		reader, err := xz.NewReader(file)
		return reader, func() {}, err
	case ArchiveFormatZSTD:
		reader, err := zstd.NewReader(file)
		if err != nil {
			return nil, func() {}, err
		}
		return reader, reader.Close, nil
	default:
		return nil, func() {}, ErrArchiveUnsupportedFormat
	}
}

func normalizeArchiveEntry(name string) (string, error) {
	if strings.TrimSpace(name) == "" || strings.IndexByte(name, 0) >= 0 || strings.Contains(name, `\`) || pathpkg.IsAbs(name) {
		return "", ErrArchiveUnsafePath
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == ".." {
			return "", ErrArchiveUnsafePath
		}
	}
	cleaned := pathpkg.Clean(name)
	if cleaned == "." || len(cleaned) > 4096 || !fs.ValidPath(cleaned) || strings.HasPrefix(cleaned, "../") {
		return "", ErrArchiveUnsafePath
	}
	for _, segment := range strings.Split(cleaned, "/") {
		if ValidateName(segment) != nil {
			return "", ErrArchiveUnsafePath
		}
	}
	return cleaned, nil
}

func safeFileMode(mode fs.FileMode) fs.FileMode {
	permissions := mode.Perm() & 0777
	if permissions == 0 {
		permissions = 0644
	}
	return permissions
}

func safeDirectoryMode(mode fs.FileMode) fs.FileMode {
	permissions := mode.Perm() & 0777
	return permissions | 0700
}

func normalizeArchiveError(err error) error {
	if err == nil {
		return nil
	}
	known := []error{
		ErrArchiveUnsupportedFormat, ErrArchiveInvalid, ErrArchiveFormatMismatch, ErrArchiveEncrypted,
		ErrArchiveMultiVolume, ErrArchiveUnsafePath, ErrArchiveUnsupportedEntry, ErrArchiveLimitExceeded,
		ErrArchiveTargetConflict, ErrArchiveRollbackFailed, ErrInvalidPath, ErrNotRegular,
		ErrReservedPath, ErrRootOperation, ErrQuotaExceeded, ErrInsufficientSpace,
		fs.ErrExist, fs.ErrNotExist, fs.ErrPermission, syscall.ENOSPC, syscall.EDQUOT,
		syscall.EROFS, syscall.ENOTDIR,
	}
	for _, candidate := range known {
		if errors.Is(err, candidate) {
			return err
		}
	}
	if errors.Is(err, rardecode.ErrArchiveEncrypted) || errors.Is(err, rardecode.ErrArchivedFileEncrypted) ||
		strings.Contains(strings.ToLower(err.Error()), "password") || strings.Contains(strings.ToLower(err.Error()), "encrypted") {
		return ErrArchiveEncrypted
	}
	if errors.Is(err, rardecode.ErrMultiVolume) {
		return ErrArchiveMultiVolume
	}
	return fmt.Errorf("%w: archive decoder rejected input", ErrArchiveInvalid)
}

func (m *Manager) validateNoSymlinkParents(relative string) error {
	if relative == "." {
		return nil
	}
	current := ""
	for _, segment := range strings.Split(relative, "/") {
		current = pathpkg.Join(current, segment)
		info, err := m.root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrArchiveUnsafePath
		}
		if current != relative && !info.IsDir() {
			return ErrArchiveTargetConflict
		}
	}
	return nil
}

func (m *Manager) mkdirAllInternalRelative(relative string, perm os.FileMode) error {
	if !fs.ValidPath(relative) || relative == "." || !isInternalPath(relative) {
		return ErrInvalidPath
	}
	current := ""
	for _, segment := range strings.Split(relative, "/") {
		current = pathpkg.Join(current, segment)
		if err := m.root.Mkdir(current, perm); err != nil {
			if !errors.Is(err, fs.ErrExist) {
				return err
			}
			info, statErr := m.root.Lstat(current)
			if statErr != nil {
				return statErr
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return ErrInvalidPath
			}
		}
	}
	return nil
}

// mkdirAllStagingRelative creates paths generated by the extraction workflow.
// The caller supplies a validated target parent and archive entry names have
// already passed normalizeArchiveEntry, so these paths remain inside the
// target filesystem and never become public file-manager paths.
func (m *Manager) mkdirAllStagingRelative(relative string, perm os.FileMode) error {
	if !fs.ValidPath(relative) || relative == "." {
		return ErrInvalidPath
	}
	current := ""
	for _, segment := range strings.Split(relative, "/") {
		current = pathpkg.Join(current, segment)
		if err := m.root.Mkdir(current, perm); err != nil {
			if !errors.Is(err, fs.ErrExist) {
				return err
			}
			info, statErr := m.root.Lstat(current)
			if statErr != nil {
				return statErr
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return ErrInvalidPath
			}
		}
	}
	return nil
}

type extractCommitAction struct {
	target   string
	backup   string
	replaced bool
}

func (m *Manager) commitExtracted(payload, target, backup string, overwrite bool) error {
	if parent := pathpkg.Dir(target); parent != "." {
		parentInfo, err := m.root.Lstat(parent)
		if err != nil {
			return err
		}
		if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
			return ErrArchiveTargetConflict
		}
	}
	if err := m.validateNoSymlinkParents(target); err != nil {
		return err
	}
	targetInfo, err := m.root.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return m.renameRelativeExclusive(payload, target)
	}
	if err != nil {
		return err
	}
	if !targetInfo.IsDir() || targetInfo.Mode()&os.ModeSymlink != 0 {
		return ErrArchiveTargetConflict
	}
	if !overwrite {
		if err := m.preflightExtractConflicts(payload, target); err != nil {
			return err
		}
	}
	actions := make([]extractCommitAction, 0)
	if err := m.mergeExtractedDirectory(payload, target, backup, overwrite, &actions); err != nil {
		if rollbackErr := m.rollbackExtractCommit(actions); rollbackErr != nil {
			return fmt.Errorf("%w: commit error followed by rollback error", ErrArchiveRollbackFailed)
		}
		return err
	}
	return nil
}

func (m *Manager) preflightExtractConflicts(sourceDir, targetDir string) error {
	directory, err := m.root.Open(sourceDir)
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, entry := range entries {
		source := pathpkg.Join(sourceDir, entry.Name())
		target := pathpkg.Join(targetDir, entry.Name())
		targetInfo, err := m.root.Lstat(target)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if entry.IsDir() && targetInfo.IsDir() && targetInfo.Mode()&os.ModeSymlink == 0 {
			if err := m.preflightExtractConflicts(source, target); err != nil {
				return err
			}
			continue
		}
		return ErrArchiveTargetConflict
	}
	return nil
}

func (m *Manager) mergeExtractedDirectory(sourceDir, targetDir, backupDir string, overwrite bool, actions *[]extractCommitAction) error {
	directory, err := m.root.Open(sourceDir)
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, entry := range entries {
		source := pathpkg.Join(sourceDir, entry.Name())
		target := pathpkg.Join(targetDir, entry.Name())
		backup := pathpkg.Join(backupDir, entry.Name())
		targetInfo, statErr := m.root.Lstat(target)
		if errors.Is(statErr, fs.ErrNotExist) {
			if err := m.renameRelativeExclusive(source, target); err != nil {
				return err
			}
			*actions = append(*actions, extractCommitAction{target: target})
			continue
		}
		if statErr != nil {
			return statErr
		}
		if targetInfo.Mode()&os.ModeSymlink != 0 {
			return ErrArchiveUnsafePath
		}
		if entry.IsDir() && targetInfo.IsDir() {
			if err := m.mergeExtractedDirectory(source, target, backup, overwrite, actions); err != nil {
				return err
			}
			continue
		}
		if !overwrite {
			return ErrArchiveTargetConflict
		}
		if err := m.mkdirAllStagingRelative(pathpkg.Dir(backup), 0700); err != nil {
			return err
		}
		if err := m.renameRelativeExclusive(target, backup); err != nil {
			return err
		}
		if err := m.renameRelativeExclusive(source, target); err != nil {
			if restoreErr := m.renameRelativeExclusive(backup, target); restoreErr != nil {
				return ErrArchiveRollbackFailed
			}
			return err
		}
		*actions = append(*actions, extractCommitAction{target: target, backup: backup, replaced: true})
	}
	return nil
}

func (m *Manager) rollbackExtractCommit(actions []extractCommitAction) error {
	var rollbackErr error
	for index := len(actions) - 1; index >= 0; index-- {
		action := actions[index]
		if err := m.removeAllRelative(action.target); err != nil && rollbackErr == nil {
			rollbackErr = err
		}
		if action.replaced {
			if err := m.renameRelativeExclusive(action.backup, action.target); err != nil && rollbackErr == nil {
				rollbackErr = err
			}
		}
	}
	return rollbackErr
}
