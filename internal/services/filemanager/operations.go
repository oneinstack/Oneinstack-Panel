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
	"strings"
	"syscall"
)

var ErrUnsupportedType = errors.New("unsupported file type")

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
// Symbolic links and special files are rejected so copy never follows a link
// outside the managed root.
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
// creation preserves symbolic links as tar entries, while copy operations keep
// rejecting them.
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
		return OperationResult{}, err
	}
	if !allowSymbolicLinks && rootInfo.Mode()&os.ModeSymlink != 0 {
		return OperationResult{}, fmt.Errorf("%w: symbolic links cannot be copied or archived", ErrUnsupportedType)
	}
	result := OperationResult{Path: m.VirtualPath(relative)}
	if allowSymbolicLinks && rootInfo.Mode()&os.ModeSymlink != 0 {
		result.Entries = 1
		return result, nil
	}
	err = fs.WalkDir(m.root.FS(), relative, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !allowSymbolicLinks && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symbolic links cannot be copied or archived", ErrUnsupportedType)
		}
		if !info.IsDir() && !info.Mode().IsRegular() && !(allowSymbolicLinks && info.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("%w: %s", ErrUnsupportedType, info.Mode().Type())
		}
		result.Entries++
		if info.Mode().IsRegular() {
			result.Bytes += info.Size()
		}
		return nil
	})
	return result, err
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
			return OperationResult{}, fs.ErrExist
		}
		if err := m.removeAllRelative(targetRelative); err != nil {
			return OperationResult{}, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return OperationResult{}, err
	}

	sourceInfo, err := m.root.Lstat(sourceRelative)
	if err != nil {
		return OperationResult{}, err
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 {
		return OperationResult{}, ErrUnsupportedType
	}
	keepTarget := false
	defer func() {
		if !keepTarget {
			_ = m.removeAllRelative(targetRelative)
		}
	}()
	result = OperationResult{Path: m.VirtualPath(targetRelative)}
	if err := m.copyRelative(sourceRelative, targetRelative, &result); err != nil {
		return OperationResult{}, err
	}
	keepTarget = true
	return result, nil
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
		_ = m.removeAllRelative(targetRelative)
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

	walkErr := fs.WalkDir(m.root.FS(), resolvedSourceRelative, func(current string, entry fs.DirEntry, walkErr error) error {
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

func (m *Manager) copyRelative(source, target string, result *OperationResult) error {
	info, err := m.root.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsupportedType
	}
	result.Entries++
	if info.IsDir() {
		if err := m.root.Mkdir(target, info.Mode().Perm()); err != nil {
			return err
		}
		directory, err := m.root.Open(source)
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
			if err := m.copyRelative(
				pathpkg.Join(source, entry.Name()),
				pathpkg.Join(target, entry.Name()),
				result,
			); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return ErrUnsupportedType
	}
	input, err := m.root.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := m.root.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(output, input)
	if copyErr == nil {
		copyErr = output.Sync()
	}
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	result.Bytes += written
	return nil
}
