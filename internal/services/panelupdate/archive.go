package panelupdate

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxArchiveFiles = 20000

func extractRelease(archivePath, destination string, maxExpandedBytes int64) (string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return "", fmt.Errorf("open release gzip: %w", err)
	}
	defer gzipReader.Close()

	reader := tar.NewReader(gzipReader)
	var root string
	var expanded int64
	var count int
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read release archive: %w", err)
		}
		count++
		if count > maxArchiveFiles {
			return "", fmt.Errorf("release archive exceeds %d entries", maxArchiveFiles)
		}
		name := filepath.ToSlash(strings.TrimSpace(header.Name))
		if name == "" || strings.Contains(name, `\`) || strings.HasPrefix(name, "/") {
			return "", fmt.Errorf("unsafe release archive path %q", header.Name)
		}
		cleaned := filepath.ToSlash(filepath.Clean(name))
		if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
			return "", fmt.Errorf("unsafe release archive path %q", header.Name)
		}
		parts := strings.Split(cleaned, "/")
		if root == "" {
			root = parts[0]
		}
		if parts[0] != root {
			return "", fmt.Errorf("release archive must contain exactly one root directory")
		}
		relative := strings.TrimPrefix(cleaned, root)
		relative = strings.TrimPrefix(relative, "/")
		switch header.Typeflag {
		case tar.TypeDir:
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 {
				return "", fmt.Errorf("release archive contains a negative file size")
			}
			expanded += header.Size
			if expanded > maxExpandedBytes {
				return "", fmt.Errorf("release archive exceeds expanded size limit")
			}
		default:
			return "", fmt.Errorf("release archive contains unsupported entry %q", header.Name)
		}
		if relative == "" {
			if header.Typeflag != tar.TypeDir {
				return "", fmt.Errorf("release archive root must be a directory")
			}
			continue
		}
		allowed := relative == "one" ||
			relative == "script-registry" ||
			relative == "script-registry/bundled" ||
			strings.HasPrefix(relative, "script-registry/bundled/")
		if !allowed {
			continue
		}
		target := filepath.Join(destination, filepath.FromSlash(relative))
		if !pathWithin(destination, target) {
			return "", fmt.Errorf("release archive escapes extraction root")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0750); err != nil {
				return "", err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := writeArchiveFile(reader, target, header.Size, header.FileInfo().Mode()); err != nil {
				return "", err
			}
		}
	}
	binary := filepath.Join(destination, "one")
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("release archive does not contain a regular one binary")
	}
	if info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("release binary is not executable")
	}
	bundled := filepath.Join(destination, "script-registry", "bundled")
	if info, err := os.Stat(bundled); err != nil || !info.IsDir() {
		return "", fmt.Errorf("release archive does not contain bundled component scripts")
	}
	return destination, nil
}

func writeArchiveFile(reader io.Reader, target string, size int64, sourceMode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
		return err
	}
	mode := os.FileMode(0640)
	if sourceMode.Perm()&0111 != 0 {
		mode = 0750
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	written, copyErr := io.CopyN(file, reader, size)
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil || written != size {
		return fmt.Errorf("extract %s: truncated content", target)
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func copyTree(source, destination string) error {
	rootInfo, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("source tree is not a directory")
	}
	return filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if !pathWithin(destination, target) {
			return fmt.Errorf("tree copy escapes destination")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("tree contains symbolic link: %s", relative)
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("tree contains unsupported file: %s", relative)
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func pathWithin(root, candidate string) bool {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
