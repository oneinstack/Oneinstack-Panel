package filemanager

import (
	"errors"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"strings"
)

func (m *Manager) Rename(virtualPath, newName string) (string, error) {
	if err := ValidateName(newName); err != nil {
		return "", err
	}

	source, err := m.Relative(virtualPath)
	if err != nil {
		return "", err
	}
	if source == "." {
		return "", ErrRootOperation
	}

	target := newName
	if parent := pathpkg.Dir(source); parent != "." {
		target = pathpkg.Join(parent, newName)
	}
	if target == source {
		return "", fs.ErrExist
	}
	if err := m.renameRelativeExclusive(source, target); err != nil {
		return "", err
	}
	return m.VirtualPath(target), nil
}

func (m *Manager) Move(sourcePath, targetPath string) (string, error) {
	source, target, err := m.resolveMoveOrCopyPaths(sourcePath, targetPath)
	if err != nil {
		return "", err
	}
	if err := m.renameRelativeExclusive(source, target); err != nil {
		return "", err
	}
	return m.VirtualPath(target), nil
}

func (m *Manager) Copy(sourcePath, targetPath string) (string, error) {
	source, target, err := m.resolveMoveOrCopyPaths(sourcePath, targetPath)
	if err != nil {
		return "", err
	}
	if err := m.copyRelative(source, target); err != nil {
		_ = m.removeAllRelative(target)
		return "", err
	}
	return m.VirtualPath(target), nil
}

func (m *Manager) resolveMoveOrCopyPaths(sourcePath, targetPath string) (string, string, error) {
	source, err := m.Relative(sourcePath)
	if err != nil {
		return "", "", err
	}
	if source == "." {
		return "", "", ErrRootOperation
	}

	target, err := m.Relative(targetPath)
	if err != nil {
		return "", "", err
	}
	if target == "." {
		return "", "", ErrRootOperation
	}
	if source == target {
		return "", "", fs.ErrExist
	}
	if strings.HasPrefix(target, source+"/") {
		return "", "", ErrInvalidPath
	}

	parent := pathpkg.Dir(target)
	parentInfo, err := m.root.Stat(parent)
	if err != nil {
		return "", "", err
	}
	if !parentInfo.IsDir() {
		return "", "", ErrInvalidPath
	}
	if _, err := m.root.Lstat(target); err == nil {
		return "", "", fs.ErrExist
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", "", err
	}
	return source, target, nil
}

func (m *Manager) copyRelative(source, target string) error {
	info, err := m.root.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidPath
	}
	if info.IsDir() {
		return m.copyDirectoryRelative(source, target, info.Mode().Perm())
	}
	if !info.Mode().IsRegular() {
		return ErrNotRegular
	}
	return m.copyFileRelative(source, target, info.Mode().Perm())
}

func (m *Manager) copyDirectoryRelative(source, target string, perm os.FileMode) error {
	if err := m.root.Mkdir(target, perm); err != nil {
		return err
	}
	entries, err := m.readPublicDirectory(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		childSource := pathpkg.Join(source, entry.Name())
		childTarget := pathpkg.Join(target, entry.Name())
		if err := m.copyRelative(childSource, childTarget); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) copyFileRelative(source, target string, perm os.FileMode) error {
	input, err := m.OpenRelative(source)
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := m.OpenFileRelative(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	defer output.Close()

	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	return output.Sync()
}

func (m *Manager) readPublicDirectory(relative string) ([]os.DirEntry, error) {
	file, err := m.OpenRelative(relative)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	entries, err := file.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	if relative == "." {
		visible := entries[:0]
		for _, entry := range entries {
			if entry.Name() != internalDirectoryName {
				visible = append(visible, entry)
			}
		}
		entries = visible
	}
	return entries, nil
}
