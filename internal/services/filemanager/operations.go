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

// Measure returns the regular-file bytes and entry count for a file tree.
// Symbolic links and special files are rejected so copy/archive never follows
// a link outside the managed root.
func (m *Manager) Measure(virtualPath string) (OperationResult, error) {
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
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return OperationResult{}, fmt.Errorf("%w: symbolic links cannot be copied or archived", ErrUnsupportedType)
	}
	result := OperationResult{Path: m.VirtualPath(relative)}
	err = fs.WalkDir(m.root.FS(), relative, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symbolic links cannot be copied or archived", ErrUnsupportedType)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
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
	if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(archiveName)), ".tar.gz") {
		return OperationResult{}, fmt.Errorf("%w: archive name must end in .tar.gz", ErrInvalidName)
	}
	sourceRelative, targetRelative, err := m.resolveOperationTarget(source, targetDir, archiveName)
	if err != nil {
		return OperationResult{}, err
	}
	if targetRelative == sourceRelative ||
		strings.HasPrefix(targetRelative, sourceRelative+"/") {
		return OperationResult{}, fmt.Errorf("%w: archive cannot be created inside source", ErrInvalidPath)
	}
	if _, err := m.Measure(source); err != nil {
		return OperationResult{}, err
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

	walkErr := fs.WalkDir(m.root.FS(), sourceRelative, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrUnsupportedType
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return ErrUnsupportedType
		}
		relativeName := "."
		if current != sourceRelative {
			relativeName = strings.TrimPrefix(current, sourceRelative+"/")
		}
		archivePath := baseName
		if relativeName != "." {
			archivePath = pathpkg.Join(baseName, relativeName)
		}
		header, err := tar.FileInfoHeader(info, "")
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
		if !info.Mode().IsRegular() {
			return nil
		}
		input, err := m.root.Open(current)
		if err != nil {
			return err
		}
		written, copyErr := io.Copy(tarWriter, input)
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
	return result, nil
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
