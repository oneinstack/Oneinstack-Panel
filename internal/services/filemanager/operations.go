package filemanager

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/uuid"
)

var ErrUnsupportedType = errors.New("unsupported file type")

// CopyPathError keeps the virtual path and phase of a copy failure while
// retaining the underlying error for classification. The path is virtual and
// never exposes the configured physical file-manager root.
type CopyPathError struct {
	Phase string
	Path  string
	Cause error
}

type copyCommitError struct {
	Cause          error
	PreserveStaged bool
}

func (e *copyCommitError) Error() string {
	if e == nil {
		return "copy commit error"
	}
	return e.Cause.Error()
}

func (e *copyCommitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *CopyPathError) Error() string {
	if e == nil {
		return "copy path error"
	}
	return fmt.Sprintf("copy %s %s: %v", e.Phase, e.Path, e.Cause)
}

func (e *CopyPathError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type OperationResult struct {
	Path    string `json:"path"`
	Bytes   int64  `json:"bytes"`
	Entries int    `json:"entries"`
}

// ArchiveProgress reports the amount of source data copied into an archive.
// Bytes are uncompressed source bytes, which makes the percentage stable even
// though the final gzip file size is not known until the archive is closed.
type ArchiveProgress struct {
	ProcessedBytes int64  `json:"processedBytes"`
	TotalBytes     int64  `json:"totalBytes"`
	Entries        int    `json:"entries"`
	CurrentPath    string `json:"currentPath"`
}

type ArchiveProgressFunc func(ArchiveProgress)

// Measure returns the regular-file bytes and entry count for a file tree.
// Directory symbolic links supplied as the source are counted as one link
// entry; links encountered inside a real directory are validated without being
// followed so copy never leaves the copied directory.
func (m *Manager) Measure(virtualPath string) (OperationResult, error) {
	return m.measure(virtualPath, false)
}

// MeasureForArchive returns the capacity estimate for an archive source. A
// source symbolic link is resolved only when its target remains in the managed
// root; links within the resolved tree are preserved without being followed.
func (m *Manager) MeasureForArchive(virtualPath string) (OperationResult, error) {
	relative, err := m.Relative(virtualPath)
	if err != nil {
		return OperationResult{}, err
	}
	if relative == "." {
		return OperationResult{}, ErrRootOperation
	}
	resolved, _, err := m.resolveArchiveSource(relative)
	if err != nil {
		return OperationResult{}, err
	}
	return m.measure(m.VirtualPath(resolved), true)
}

func (m *Manager) resolveArchiveSource(relative string) (string, os.FileInfo, error) {
	seen := make(map[string]struct{})
	for {
		if _, ok := seen[relative]; ok {
			return "", nil, fmt.Errorf("%w: symbolic link cycle", ErrInvalidPath)
		}
		seen[relative] = struct{}{}
		info, err := m.root.Lstat(relative)
		if err != nil {
			return "", nil, err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return relative, info, nil
		}
		parent, err := m.root.Open(pathpkg.Dir(relative))
		if err != nil {
			return "", nil, err
		}
		linkTarget, linkErr := readlinkAt(parent, pathpkg.Base(relative))
		closeErr := parent.Close()
		if linkErr != nil {
			return "", nil, linkErr
		}
		if closeErr != nil {
			return "", nil, closeErr
		}
		if pathpkg.IsAbs(linkTarget) {
			if m.rootPath != "/" {
				return "", nil, fmt.Errorf("%w: absolute symbolic link target is outside managed root", ErrInvalidPath)
			}
			relative = strings.TrimPrefix(pathpkg.Clean(linkTarget), "/")
		} else {
			relative = pathpkg.Clean(pathpkg.Join(pathpkg.Dir(relative), linkTarget))
		}
		if relative == "." || !fs.ValidPath(relative) {
			return "", nil, ErrInvalidPath
		}
		if _, err := m.Relative(m.VirtualPath(relative)); err != nil {
			return "", nil, err
		}
	}
}

// measure calculates the source size without following symbolic links. Archive
// creation preserves symbolic links as tar entries, while copy preserves links
// only when their targets remain within the copied directory.
func (m *Manager) measure(virtualPath string, allowSymbolicLinks bool) (OperationResult, error) {
	relative, err := m.Relative(virtualPath)
	if err != nil {
		return OperationResult{}, err
	}
	if relative == "." {
		return OperationResult{}, ErrRootOperation
	}
	rootInfo, err := m.root.Lstat(relative)
	if err != nil {
		return OperationResult{}, m.copyPathError("source", relative, err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		if allowSymbolicLinks {
			result := OperationResult{Path: m.VirtualPath(relative), Entries: 1}
			return result, nil
		}
		targetInfo, err := m.statSymlinkTarget(relative)
		if err != nil {
			return OperationResult{}, m.copyPathError("source", relative, err)
		}
		if !targetInfo.IsDir() && !targetInfo.Mode().IsRegular() {
			return OperationResult{}, m.copyPathError("source", relative, fmt.Errorf("%w: symbolic link target is not a regular file or directory", ErrUnsupportedType))
		}
		return OperationResult{Path: m.VirtualPath(relative), Entries: 1}, nil
	}
	result := OperationResult{Path: m.VirtualPath(relative)}
	err = m.Walk(m.VirtualPath(relative), func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return m.copyPathError("source", current, walkErr)
		}
		info, err := entry.Info()
		if err != nil {
			return m.copyPathError("source", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if !allowSymbolicLinks {
				if err := m.validateInternalCopySymlink(relative, current); err != nil {
					return m.copyPathError("source", current, err)
				}
			}
		} else if !info.IsDir() && !info.Mode().IsRegular() {
			return m.copyPathError("source", current, fmt.Errorf("%w: %s", ErrUnsupportedType, info.Mode().Type()))
		}
		result.Entries++
		if info.Mode().IsRegular() {
			result.Bytes += info.Size()
		}
		return nil
	})
	return result, m.copyPathError("source", relative, err)
}

// Copy copies source into targetDir using targetName. Existing targets are
// overwritten only when overwrite is true.
func (m *Manager) Copy(source, targetDir, targetName string, overwrite bool) (result OperationResult, err error) {
	sourceRelative, targetRelative, err := m.resolveOperationTarget(source, targetDir, targetName)
	if err != nil {
		return OperationResult{}, err
	}
	if targetRelative == sourceRelative ||
		strings.HasPrefix(targetRelative, sourceRelative+"/") {
		return OperationResult{}, fmt.Errorf("%w: target cannot be inside source", ErrInvalidPath)
	}
	if _, err := m.root.Lstat(targetRelative); err == nil {
		if !overwrite {
			return OperationResult{}, m.copyPathError("target", targetRelative, fs.ErrExist)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return OperationResult{}, m.copyPathError("target", targetRelative, err)
	}

	stagedTarget, err := m.newTemporaryCopyPath(pathpkg.Dir(targetRelative), ".file-copy-")
	if err != nil {
		return OperationResult{}, m.copyPathError("target", targetRelative, err)
	}
	committed := false
	stagedKept := false
	defer func() {
		if !committed && !stagedKept {
			_ = m.removeAllRelative(stagedTarget)
		}
	}()

	sourceInfo, err := m.root.Lstat(sourceRelative)
	if err != nil {
		return OperationResult{}, m.copyPathError("source", sourceRelative, err)
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 {
		targetInfo, err := m.statSymlinkTarget(sourceRelative)
		if err != nil {
			return OperationResult{}, m.copyPathError("source", sourceRelative, err)
		}
		if !targetInfo.IsDir() && !targetInfo.Mode().IsRegular() {
			return OperationResult{}, m.copyPathError("source", sourceRelative, fmt.Errorf("%w: symbolic link target is not a regular file or directory", ErrUnsupportedType))
		}
		linkTarget, err := m.copySymlinkTarget(sourceRelative, stagedTarget)
		if err != nil {
			return OperationResult{}, m.copyPathError("source", sourceRelative, err)
		}
		if err := m.root.Symlink(linkTarget, stagedTarget); err != nil {
			return OperationResult{}, m.copyPathError("target", targetRelative, err)
		}
		result = OperationResult{Path: m.VirtualPath(targetRelative), Entries: 1}
	} else {
		result = OperationResult{Path: m.VirtualPath(targetRelative)}
		if err := m.copyRelative(sourceRelative, stagedTarget, targetRelative, &result); err != nil {
			return OperationResult{}, m.copyPathError("target", targetRelative, err)
		}
	}
	if err := m.commitCopiedTarget(stagedTarget, targetRelative, overwrite); err != nil {
		var commitErr *copyCommitError
		if errors.As(err, &commitErr) && commitErr.PreserveStaged {
			stagedKept = true
		}
		return OperationResult{}, m.copyPathError("target", targetRelative, err)
	}
	committed = true
	return result, nil
}

func (m *Manager) copyPathError(phase, relative string, err error) error {
	if err == nil {
		return nil
	}
	var pathErr *CopyPathError
	if errors.As(err, &pathErr) {
		return err
	}
	return &CopyPathError{
		Phase: phase,
		Path:  m.VirtualPath(relative),
		Cause: err,
	}
}

func (m *Manager) newTemporaryCopyPath(parent, prefix string) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		candidate := pathpkg.Join(parent, prefix+uuid.NewString()+".partial")
		if _, err := m.root.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("create temporary copy path: %w", fs.ErrExist)
}

// commitCopiedTarget publishes a fully copied sibling. When overwriting, the
// old target is first moved to a sibling backup, so a failed copy or commit
// never removes the only complete copy of the target.
func (m *Manager) commitCopiedTarget(staged, target string, overwrite bool) error {
	if !overwrite {
		return m.renameRelativeExclusive(staged, target)
	}

	if _, err := m.root.Lstat(target); errors.Is(err, fs.ErrNotExist) {
		return m.renameRelativeExclusive(staged, target)
	} else if err != nil {
		return err
	}

	backup, err := m.newTemporaryCopyPath(pathpkg.Dir(target), ".file-copy-backup-")
	if err != nil {
		return err
	}
	if err := m.renameRelativeExclusive(target, backup); err != nil {
		return fmt.Errorf("preserve existing target: %w", err)
	}
	if err := m.renameRelativeExclusive(staged, target); err != nil {
		if restoreErr := m.renameRelativeExclusive(backup, target); restoreErr != nil {
			return &copyCommitError{
				Cause:          fmt.Errorf("commit copied target failed: %w; restore original target failed: %v; staged copy retained at %s", err, restoreErr, m.VirtualPath(staged)),
				PreserveStaged: true,
			}
		}
		return fmt.Errorf("commit copied target: %w", err)
	}
	// A cleanup failure leaves the old complete target in the uniquely named
	// backup, but must not turn an already committed copy into a false failure.
	_ = m.removeAllRelative(backup)
	return nil
}

// Move moves source into targetDir using targetName without replacing an
// existing target. Cross-filesystem moves fall back to a safe copy/delete.
func (m *Manager) Move(source, targetDir, targetName string) (OperationResult, error) {
	sourceRelative, targetRelative, err := m.resolveOperationTarget(source, targetDir, targetName)
	if err != nil {
		return OperationResult{}, err
	}
	if targetRelative == sourceRelative {
		return OperationResult{Path: m.VirtualPath(sourceRelative)}, nil
	}
	if strings.HasPrefix(targetRelative, sourceRelative+"/") {
		return OperationResult{}, fmt.Errorf("%w: target cannot be inside source", ErrInvalidPath)
	}
	measured, err := m.Measure(source)
	if err != nil {
		return OperationResult{}, err
	}
	if err := m.renameRelativeExclusive(sourceRelative, targetRelative); err == nil {
		measured.Path = m.VirtualPath(targetRelative)
		return measured, nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return OperationResult{}, err
	}

	copied, err := m.Copy(source, targetDir, targetName, false)
	if err != nil {
		return OperationResult{}, err
	}
	if err := m.removeAllRelative(sourceRelative); err != nil {
		// Keep the complete copy when source cleanup fails. Removing it here
		// could turn a recoverable cleanup error into data loss, especially when
		// a cross-filesystem source directory was only partially removed.
		return OperationResult{}, fmt.Errorf("remove source after cross-filesystem copy: %w", err)
	}
	return copied, nil
}

func (m *Manager) Rename(source, newName string) (OperationResult, error) {
	relative, err := m.Relative(source)
	if err != nil {
		return OperationResult{}, err
	}
	if relative == "." {
		return OperationResult{}, ErrRootOperation
	}
	return m.Move(source, m.VirtualPath(pathpkg.Dir(relative)), newName)
}

// Archive creates a gzip-compressed tar archive without following symbolic
// links or including the archive itself.
func (m *Manager) Archive(source, targetDir, archiveName string) (result OperationResult, err error) {
	return m.ArchiveWithProgress(source, targetDir, archiveName, nil)
}

// ArchiveWithProgress creates a gzip-compressed tar archive and reports
// source-byte progress. The callback is invoked synchronously and must return
// quickly; callers that persist progress should throttle their writes.
func (m *Manager) ArchiveWithProgress(source, targetDir, archiveName string, report ArchiveProgressFunc) (result OperationResult, err error) {
	if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(archiveName)), ".tar.gz") {
		return OperationResult{}, fmt.Errorf("%w: archive name must end in .tar.gz", ErrInvalidName)
	}
	sourceRelative, targetRelative, err := m.resolveOperationTarget(source, targetDir, archiveName)
	if err != nil {
		return OperationResult{}, err
	}
	resolvedSourceRelative, _, err := m.resolveArchiveSource(sourceRelative)
	if err != nil {
		return OperationResult{}, err
	}
	if targetRelative == sourceRelative || strings.HasPrefix(targetRelative, sourceRelative+"/") ||
		targetRelative == resolvedSourceRelative || strings.HasPrefix(targetRelative, resolvedSourceRelative+"/") {
		return OperationResult{}, fmt.Errorf("%w: archive cannot be created inside source", ErrInvalidPath)
	}
	measured, err := m.MeasureForArchive(source)
	if err != nil {
		return OperationResult{}, err
	}
	if report != nil {
		report(ArchiveProgress{TotalBytes: measured.Bytes})
	}

	output, err := m.root.OpenFile(targetRelative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return OperationResult{}, err
	}
	keepArchive := false
	defer func() {
		if closeErr := output.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
		if !keepArchive {
			_ = m.root.Remove(targetRelative)
		}
	}()
	gzipWriter := gzip.NewWriter(output)
	tarWriter := tar.NewWriter(gzipWriter)
	baseName := pathpkg.Base(sourceRelative)
	result.Path = m.VirtualPath(targetRelative)

	walkErr := m.Walk(m.VirtualPath(resolvedSourceRelative), func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		isSymbolicLink := info.Mode()&os.ModeSymlink != 0
		if !isSymbolicLink && !info.IsDir() && !info.Mode().IsRegular() {
			return ErrUnsupportedType
		}
		relativeName := "."
		if current != resolvedSourceRelative {
			relativeName = strings.TrimPrefix(current, resolvedSourceRelative+"/")
		}
		archivePath := baseName
		if relativeName != "." {
			archivePath = pathpkg.Join(baseName, relativeName)
		}
		linkTarget := ""
		if isSymbolicLink {
			parent, openErr := m.root.Open(pathpkg.Dir(current))
			if openErr != nil {
				return openErr
			}
			linkTarget, err = readlinkAt(parent, pathpkg.Base(current))
			closeErr := parent.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		}
		header, err := tar.FileInfoHeader(info, linkTarget)
		if err != nil {
			return err
		}
		header.Name = archivePath
		header.Uid = 0
		header.Gid = 0
		header.Uname = ""
		header.Gname = ""
		if info.IsDir() && !strings.HasSuffix(header.Name, "/") {
			header.Name += "/"
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		result.Entries++
		if report != nil {
			report(ArchiveProgress{ProcessedBytes: result.Bytes, TotalBytes: measured.Bytes, Entries: result.Entries, CurrentPath: m.VirtualPath(current)})
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		input, err := m.root.Open(current)
		if err != nil {
			return err
		}
		written, copyErr := io.Copy(&archiveProgressWriter{writer: tarWriter, onWrite: func(written int) {
			if report != nil {
				report(ArchiveProgress{ProcessedBytes: result.Bytes + int64(written), TotalBytes: measured.Bytes, Entries: result.Entries, CurrentPath: m.VirtualPath(current)})
			}
		}}, input)
		closeErr := input.Close()
		result.Bytes += written
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if walkErr != nil {
		_ = tarWriter.Close()
		_ = gzipWriter.Close()
		return OperationResult{}, walkErr
	}
	if err := tarWriter.Close(); err != nil {
		return OperationResult{}, err
	}
	if err := gzipWriter.Close(); err != nil {
		return OperationResult{}, err
	}
	if err := output.Sync(); err != nil {
		return OperationResult{}, err
	}
	keepArchive = true
	if report != nil {
		report(ArchiveProgress{ProcessedBytes: result.Bytes, TotalBytes: measured.Bytes, Entries: result.Entries})
	}
	return result, nil
}

type archiveProgressWriter struct {
	writer  io.Writer
	onWrite func(int)
}

func (w *archiveProgressWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	if n > 0 && w.onWrite != nil {
		w.onWrite(n)
	}
	return n, err
}

func (m *Manager) resolveOperationTarget(source, targetDir, targetName string) (string, string, error) {
	sourceRelative, err := m.Relative(source)
	if err != nil {
		return "", "", err
	}
	if sourceRelative == "." {
		return "", "", ErrRootOperation
	}
	if strings.TrimSpace(targetName) == "" {
		targetName = pathpkg.Base(sourceRelative)
	}
	if err := ValidateName(targetName); err != nil {
		return "", "", err
	}
	targetRelative, err := m.Join(targetDir, targetName)
	if err != nil {
		return "", "", err
	}
	targetInfo, _, err := m.Stat(targetDir)
	if err != nil {
		return "", "", err
	}
	if !targetInfo.IsDir() {
		return "", "", ErrInvalidPath
	}
	return sourceRelative, targetRelative, nil
}

func (m *Manager) statSymlinkTarget(relative string) (os.FileInfo, error) {
	target, err := m.root.Readlink(relative)
	if err != nil {
		return nil, err
	}
	resolved, err := m.resolveSymlinkTarget(relative, target)
	if err != nil {
		return nil, err
	}
	info, err := m.root.Stat(resolved)
	if err != nil {
		return nil, err
	}
	return info, nil
}

func (m *Manager) copySymlinkTarget(source, target string) (string, error) {
	linkTarget, err := m.root.Readlink(source)
	if err != nil {
		return "", err
	}
	resolved, err := m.resolveSymlinkTarget(source, linkTarget)
	if err != nil {
		return "", err
	}
	relativeTarget, err := filepath.Rel(filepath.FromSlash(pathpkg.Dir(target)), filepath.FromSlash(resolved))
	if err != nil {
		return "", fmt.Errorf("%w: resolve copied symbolic link", ErrInvalidPath)
	}
	if relativeTarget == "." || filepath.IsAbs(relativeTarget) {
		return "", fmt.Errorf("%w: copied symbolic link target is invalid", ErrInvalidPath)
	}
	return filepath.ToSlash(relativeTarget), nil
}

func (m *Manager) resolveSymlinkTarget(source, target string) (string, error) {
	var resolved string
	if pathpkg.IsAbs(target) {
		if m.rootPath != string(filepath.Separator) {
			return "", fmt.Errorf("%w: absolute symbolic link target is outside managed root", ErrInvalidPath)
		}
		resolved = strings.TrimPrefix(pathpkg.Clean(target), "/")
	} else {
		resolved = pathpkg.Clean(pathpkg.Join(pathpkg.Dir(source), target))
	}
	if resolved == "." || !fs.ValidPath(resolved) {
		return "", ErrInvalidPath
	}
	if _, err := m.Relative(m.VirtualPath(resolved)); err != nil {
		return "", err
	}
	return resolved, nil
}

func (m *Manager) copyRelative(source, target, displayTarget string, result *OperationResult) error {
	return m.copyRelativeTree(source, target, source, target, displayTarget, result)
}

func (m *Manager) copyRelativeTree(source, target, sourceRoot, targetRoot, displayTargetRoot string, result *OperationResult) error {
	displayTarget := pathpkg.Join(displayTargetRoot, strings.TrimPrefix(target, targetRoot))
	info, err := m.root.Lstat(source)
	if err != nil {
		return m.copyPathError("source", source, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		linkTarget, err := m.copyInternalSymlinkTarget(sourceRoot, source, targetRoot, target)
		if err != nil {
			return m.copyPathError("source", source, err)
		}
		if err := m.root.Symlink(linkTarget, target); err != nil {
			return m.copyPathError("target", displayTarget, err)
		}
		result.Entries++
		return nil
	}
	result.Entries++
	if info.IsDir() {
		if err := m.root.Mkdir(target, info.Mode().Perm()); err != nil {
			return m.copyPathError("target", displayTarget, err)
		}
		entries, err := m.readCopyDirectory(source)
		if err != nil {
			return m.copyPathError("source", source, err)
		}
		for _, entry := range entries {
			if err := m.copyRelativeTree(
				pathpkg.Join(source, entry.Name()),
				pathpkg.Join(target, entry.Name()),
				sourceRoot,
				targetRoot,
				displayTargetRoot,
				result,
			); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return m.copyPathError("source", source, ErrUnsupportedType)
	}
	input, err := m.root.Open(source)
	if err != nil {
		return m.copyPathError("source", source, err)
	}
	defer input.Close()
	output, err := m.root.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return m.copyPathError("target", displayTarget, err)
	}
	// Do not use io.Copy here. When both handles are regular files, Go may
	// select Linux copy_file_range(2), which returns ETXTBSY for an active
	// swap file even though ordinary read/write can read it successfully.
	written, copyErr := copyRegularFileContents(output, input)
	if copyErr == nil {
		copyErr = output.Sync()
	}
	closeErr := output.Close()
	if copyErr != nil {
		return m.copyPathError("target", displayTarget, copyErr)
	}
	if closeErr != nil {
		return m.copyPathError("target", displayTarget, closeErr)
	}
	result.Bytes += written
	return nil
}

const regularFileCopyBufferSize = 1 << 20

// copyRegularFileContents deliberately uses explicit buffered reads and
// writes. This keeps file-manager copies compatible with active swap files
// and other regular files that reject zero-copy range operations.
func copyRegularFileContents(output, input *os.File) (int64, error) {
	buffer := make([]byte, regularFileCopyBufferSize)
	var written int64
	for {
		read, readErr := input.Read(buffer)
		if read > 0 {
			for offset := 0; offset < read; {
				writtenCount, writeErr := output.Write(buffer[offset:read])
				if writtenCount > 0 {
					written += int64(writtenCount)
					offset += writtenCount
				}
				if writeErr != nil {
					return written, writeErr
				}
				if writtenCount == 0 {
					return written, io.ErrShortWrite
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return written, nil
			}
			return written, readErr
		}
	}
}

func (m *Manager) readCopyDirectory(relative string) ([]os.DirEntry, error) {
	directory, err := m.root.Open(relative)
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}

	visible := entries[:0]
	for _, entry := range entries {
		child := pathpkg.Join(relative, entry.Name())
		if isInternalPath(child) || m.isProtectedRelative(child) {
			continue
		}
		visible = append(visible, entry)
	}
	return visible, nil
}

func (m *Manager) validateInternalCopySymlink(sourceRoot, source string) error {
	linkTarget, err := m.root.Readlink(source)
	if err != nil {
		return err
	}
	resolved, err := m.resolveSymlinkTarget(source, linkTarget)
	if err != nil {
		return err
	}
	if !isPathWithin(resolved, sourceRoot) {
		return fmt.Errorf("%w: symbolic link target is outside copied directory", ErrUnsupportedType)
	}
	if isInternalPath(resolved) || m.isProtectedRelative(resolved) {
		return fmt.Errorf("%w: symbolic link target is protected", ErrReservedPath)
	}
	return nil
}

func (m *Manager) copyInternalSymlinkTarget(sourceRoot, source, targetRoot, target string) (string, error) {
	linkTarget, err := m.root.Readlink(source)
	if err != nil {
		return "", err
	}
	resolved, err := m.resolveSymlinkTarget(source, linkTarget)
	if err != nil {
		return "", err
	}
	if !isPathWithin(resolved, sourceRoot) {
		return "", fmt.Errorf("%w: symbolic link target is outside copied directory", ErrUnsupportedType)
	}
	if isInternalPath(resolved) || m.isProtectedRelative(resolved) {
		return "", fmt.Errorf("%w: symbolic link target is protected", ErrReservedPath)
	}
	mappedTarget := targetRoot + strings.TrimPrefix(resolved, sourceRoot)
	relativeTarget, err := filepath.Rel(filepath.FromSlash(pathpkg.Dir(target)), filepath.FromSlash(mappedTarget))
	if err != nil {
		return "", fmt.Errorf("%w: resolve copied symbolic link", ErrInvalidPath)
	}
	if relativeTarget == "." || filepath.IsAbs(relativeTarget) {
		return "", fmt.Errorf("%w: copied symbolic link target is invalid", ErrInvalidPath)
	}
	return filepath.ToSlash(relativeTarget), nil
}

func isPathWithin(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}
