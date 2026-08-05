package filemanager

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"unicode"
)

var (
	ErrInvalidPath      = errors.New("invalid file path")
	ErrInvalidName      = errors.New("invalid file name")
	ErrRootOperation    = errors.New("operation on file root is not allowed")
	ErrNotRegular       = errors.New("path is not a regular file")
	ErrReservedPath     = errors.New("path is reserved for internal use")
	ErrRevisionConflict = errors.New("file revision conflict")
)

const internalDirectoryName = ".oneinstack-trash"

// Manager confines filesystem access to a configured directory tree. Paths
// accepted by its methods are virtual paths: "/" is the configured root and
// "/sites/example" is a child beneath that root. When scopePrefixes is set,
// virtual paths outside the listed prefixes are rejected.
type Manager struct {
	root           *os.Root
	rootPath       string
	scopePrefixes  []string
	protectedPaths []string
}

func New(rootPath string) (*Manager, error) {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return nil, fmt.Errorf("%w: file root is empty", ErrInvalidPath)
	}

	absoluteRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve file root: %w", err)
	}
	if err := os.MkdirAll(absoluteRoot, 0755); err != nil {
		return nil, fmt.Errorf("create file root: %w", err)
	}

	info, err := os.Stat(absoluteRoot)
	if err != nil {
		return nil, fmt.Errorf("stat file root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: configured root is not a directory", ErrInvalidPath)
	}

	root, err := os.OpenRoot(absoluteRoot)
	if err != nil {
		return nil, fmt.Errorf("open file root: %w", err)
	}
	return &Manager{root: root, rootPath: absoluteRoot}, nil
}

func (m *Manager) Close() error {
	return m.root.Close()
}

// WithScope restricts operations to virtual paths starting with one of the
// given prefixes. A nil or empty slice means full access (no restriction).
func (m *Manager) WithScope(prefixes []string) *Manager {
	m.scopePrefixes = prefixes
	return m
}

// WithProtectedPaths prevents all file-manager operations below the given
// absolute paths, including list, read, write, share, and delete operations.
func (m *Manager) WithProtectedPaths(paths []string) *Manager {
	m.protectedPaths = make([]string, 0, len(paths))
	for _, path := range paths {
		if absolute, err := filepath.Abs(filepath.Clean(strings.TrimSpace(path))); err == nil && absolute != "" {
			m.protectedPaths = append(m.protectedPaths, absolute)
		}
	}
	return m
}

func (m *Manager) RootPath() string {
	return m.rootPath
}

// Relative validates a virtual path and converts it to an os.Root-relative
// path. Parent traversal is rejected instead of silently cleaned.
func (m *Manager) Relative(virtualPath string) (string, error) {
	if strings.IndexByte(virtualPath, 0) >= 0 || strings.Contains(virtualPath, `\`) {
		return "", ErrInvalidPath
	}

	for _, segment := range strings.Split(virtualPath, "/") {
		if segment == ".." {
			return "", ErrInvalidPath
		}
		if segment != "" && segment != "." {
			if err := ValidateName(segment); err != nil {
				return "", ErrInvalidPath
			}
		}
	}

	cleaned := pathpkg.Clean("/" + strings.TrimSpace(virtualPath))
	relative := strings.TrimPrefix(cleaned, "/")
	if relative == "" {
		relative = "."
	}
	if !fs.ValidPath(relative) {
		return "", ErrInvalidPath
	}
	if isInternalPath(relative) {
		return "", ErrReservedPath
	}
	if len(m.scopePrefixes) > 0 {
		virtual := m.VirtualPath(relative)
		if !m.isInScope(virtual) {
			return "", ErrInvalidPath
		}
	}
	if m.isProtectedRelative(relative) {
		return "", ErrReservedPath
	}
	return relative, nil
}

func (m *Manager) isInScope(virtualPath string) bool {
	if len(m.scopePrefixes) == 0 {
		return true
	}
	if virtualPath == "/" {
		return true
	}
	for _, prefix := range m.scopePrefixes {
		if virtualPath == prefix || strings.HasPrefix(virtualPath, prefix+"/") {
			return true
		}
	}
	return false
}

func (m *Manager) VirtualPath(relative string) string {
	if relative == "" || relative == "." {
		return "/"
	}
	return "/" + filepath.ToSlash(relative)
}

func (m *Manager) Join(virtualDir, name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	dir, err := m.Relative(virtualDir)
	if err != nil {
		return "", err
	}
	if dir == "." {
		return name, nil
	}
	return pathpkg.Join(filepath.ToSlash(dir), name), nil
}

func ValidateName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return ErrInvalidName
	}
	if strings.ContainsAny(name, `/\`) || strings.IndexByte(name, 0) >= 0 {
		return ErrInvalidName
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return ErrInvalidName
		}
	}
	return nil
}

func (m *Manager) Open(virtualPath string) (*os.File, string, error) {
	relative, err := m.Relative(virtualPath)
	if err != nil {
		return nil, "", err
	}
	file, err := m.root.Open(relative)
	if err != nil {
		return nil, "", err
	}
	return file, relative, nil
}

func (m *Manager) OpenRelative(relative string) (*os.File, error) {
	if err := validatePublicRelative(relative); err != nil {
		return nil, err
	}
	if m.isProtectedRelative(relative) {
		return nil, ErrReservedPath
	}
	return m.root.Open(relative)
}

func (m *Manager) OpenFile(virtualPath string, flag int, perm os.FileMode) (*os.File, string, error) {
	relative, err := m.Relative(virtualPath)
	if err != nil {
		return nil, "", err
	}
	file, err := m.root.OpenFile(relative, flag, perm)
	if err != nil {
		return nil, "", err
	}
	return file, relative, nil
}

func (m *Manager) OpenFileRelative(relative string, flag int, perm os.FileMode) (*os.File, error) {
	if err := validatePublicRelative(relative); err != nil {
		return nil, err
	}
	if m.isProtectedRelative(relative) {
		return nil, ErrReservedPath
	}
	return m.root.OpenFile(relative, flag, perm)
}

func (m *Manager) Stat(virtualPath string) (os.FileInfo, string, error) {
	relative, err := m.Relative(virtualPath)
	if err != nil {
		return nil, "", err
	}
	info, err := m.root.Stat(relative)
	return info, relative, err
}

func (m *Manager) LstatRelative(relative string) (os.FileInfo, error) {
	if err := validatePublicRelative(relative); err != nil {
		return nil, err
	}
	if m.isProtectedRelative(relative) {
		return nil, ErrReservedPath
	}
	return m.root.Lstat(relative)
}

func (m *Manager) isProtectedRelative(relative string) bool {
	absolute := filepath.Clean(filepath.Join(m.rootPath, filepath.FromSlash(relative)))
	for _, protected := range m.protectedPaths {
		if absolute == protected || strings.HasPrefix(absolute, protected+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func (m *Manager) ReadDir(virtualPath string) ([]os.DirEntry, string, error) {
	file, relative, err := m.Open(virtualPath)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	if !info.IsDir() {
		return nil, "", fmt.Errorf("%w: path is not a directory", ErrInvalidPath)
	}

	entries, err := file.ReadDir(-1)
	if err != nil {
		return nil, "", err
	}
	visible := entries[:0]
	for _, entry := range entries {
		if relative == "." && entry.Name() == internalDirectoryName {
			continue
		}
		childRelative := pathpkg.Join(relative, entry.Name())
		if m.isProtectedRelative(childRelative) {
			continue
		}
		visible = append(visible, entry)
	}
	entries = visible
	return entries, relative, nil
}

func (m *Manager) MkdirAll(virtualPath string, perm os.FileMode) error {
	relative, err := m.Relative(virtualPath)
	if err != nil {
		return err
	}
	if relative == "." {
		return nil
	}
	return m.mkdirAllRelative(relative, perm)
}

func (m *Manager) CreateFile(virtualPath string, perm os.FileMode) error {
	file, _, err := m.OpenFile(virtualPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	return file.Close()
}

func (m *Manager) WriteExistingFile(virtualPath string, content []byte) error {
	file, _, err := m.OpenFile(virtualPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrNotRegular
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		return err
	}
	return file.Sync()
}

func (m *Manager) RemoveAll(virtualPath string) error {
	relative, err := m.Relative(virtualPath)
	if err != nil {
		return err
	}
	if relative == "." {
		return ErrRootOperation
	}
	return m.removeAllRelative(relative)
}

func (m *Manager) removeAllRelative(relative string) error {
	info, err := m.root.Lstat(relative)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		dir, err := m.root.Open(relative)
		if err != nil {
			return err
		}
		entries, readErr := dir.ReadDir(-1)
		closeErr := dir.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		for _, entry := range entries {
			if err := m.removeAllRelative(pathpkg.Join(relative, entry.Name())); err != nil {
				return err
			}
		}
	}
	return m.root.Remove(relative)
}

func (m *Manager) Walk(virtualPath string, walkFn fs.WalkDirFunc) error {
	relative, err := m.Relative(virtualPath)
	if err != nil {
		return err
	}
	return fs.WalkDir(m.root.FS(), relative, func(path string, entry fs.DirEntry, walkErr error) error {
		if path != "." && m.isProtectedRelative(path) {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == internalDirectoryName || strings.HasPrefix(path, internalDirectoryName+"/") {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		return walkFn(path, entry, walkErr)
	})
}

func validatePublicRelative(relative string) error {
	if !fs.ValidPath(relative) {
		return ErrInvalidPath
	}
	if isInternalPath(relative) {
		return ErrReservedPath
	}
	return nil
}

func isInternalPath(relative string) bool {
	return relative == internalDirectoryName || strings.HasPrefix(relative, internalDirectoryName+"/")
}
