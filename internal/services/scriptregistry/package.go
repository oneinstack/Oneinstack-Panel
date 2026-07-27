package scriptregistry

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	manifestFileName = "manifest.yaml"
	checksumFileName = "files.sha256"
	maxPackageFiles  = 512
)

func extractPackage(archiveName, destination string, maxExpandedBytes int64) (Manifest, error) {
	parent := filepath.Dir(destination)
	temporaryRoot, err := os.MkdirTemp(parent, ".extract-*")
	if err != nil {
		return Manifest{}, fmt.Errorf("create extraction directory: %w", err)
	}
	defer os.RemoveAll(temporaryRoot)

	archive, err := os.Open(archiveName)
	if err != nil {
		return Manifest{}, fmt.Errorf("open downloaded package: %w", err)
	}
	defer archive.Close()
	gzipReader, err := gzip.NewReader(archive)
	if err != nil {
		return Manifest{}, fmt.Errorf("open package gzip stream: %w", err)
	}
	defer gzipReader.Close()

	seen := make(map[string]struct{})
	var expanded int64
	tarReader := tar.NewReader(gzipReader)
	for {
		header, nextErr := tarReader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return Manifest{}, fmt.Errorf("read package archive: %w", nextErr)
		}
		archivePath := header.Name
		if header.Typeflag == tar.TypeDir {
			archivePath = strings.TrimSuffix(archivePath, "/")
		}
		name, pathErr := safePackagePath(archivePath)
		if pathErr != nil {
			return Manifest{}, pathErr
		}
		if _, exists := seen[name]; exists {
			return Manifest{}, fmt.Errorf("duplicate package entry %s", name)
		}
		if len(seen) >= maxPackageFiles {
			return Manifest{}, fmt.Errorf("package contains too many files")
		}
		seen[name] = struct{}{}
		target := filepath.Join(temporaryRoot, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0750); err != nil {
				return Manifest{}, fmt.Errorf("create package directory: %w", err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > maxExpandedBytes-expanded {
				return Manifest{}, fmt.Errorf("expanded package exceeds configured limit")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
				return Manifest{}, fmt.Errorf("create package parent directory: %w", err)
			}
			mode := os.FileMode(0640)
			if header.Mode&0111 != 0 {
				mode = 0750
			}
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return Manifest{}, fmt.Errorf("create package file: %w", err)
			}
			written, copyErr := io.Copy(file, io.LimitReader(tarReader, header.Size+1))
			closeErr := file.Close()
			if copyErr != nil || written != header.Size {
				return Manifest{}, fmt.Errorf("extract package file %s", name)
			}
			if closeErr != nil {
				return Manifest{}, fmt.Errorf("close package file %s: %w", name, closeErr)
			}
			expanded += written
		default:
			return Manifest{}, fmt.Errorf("package contains forbidden entry type for %s", name)
		}
	}
	manifest, err := validateDirectory(temporaryRoot)
	if err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(temporaryRoot, destination); err != nil {
		if existing, existingErr := validateDirectory(destination); existingErr == nil &&
			existing.Component.ID == manifest.Component.ID &&
			existing.Component.Version == manifest.Component.Version {
			return existing, nil
		}
		return Manifest{}, fmt.Errorf("install package in cache: %w", err)
	}
	return manifest, nil
}

func validateDirectory(root string) (Manifest, error) {
	manifestContents, err := os.ReadFile(filepath.Join(root, manifestFileName))
	if err != nil {
		return Manifest{}, fmt.Errorf("read package manifest: %w", err)
	}
	manifest, err := parseManifest(manifestContents)
	if err != nil {
		return Manifest{}, err
	}
	checksumContents, err := os.ReadFile(filepath.Join(root, checksumFileName))
	if err != nil {
		return Manifest{}, fmt.Errorf("read package checksums: %w", err)
	}
	checksums, err := parseChecksums(checksumContents)
	if err != nil {
		return Manifest{}, err
	}
	verified := make(map[string]bool)
	err = filepath.WalkDir(root, func(fileName string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if fileName == root {
			return nil
		}
		relative, err := filepath.Rel(root, fileName)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!entry.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("package contains forbidden file type at %s", relative)
		}
		if entry.IsDir() || relative == manifestFileName || relative == checksumFileName {
			return nil
		}
		expected, exists := checksums[relative]
		if !exists {
			return fmt.Errorf("package checksum list does not cover %s", relative)
		}
		file, err := os.Open(fileName)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if actual := hex.EncodeToString(hash.Sum(nil)); actual != expected {
			return fmt.Errorf("package checksum mismatch for %s", relative)
		}
		verified[relative] = true
		return nil
	})
	if err != nil {
		return Manifest{}, err
	}
	for fileName := range checksums {
		if !verified[fileName] {
			return Manifest{}, fmt.Errorf("package checksum references missing file %s", fileName)
		}
	}
	for actionName, relative := range manifest.actionMap() {
		if relative == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			return Manifest{}, fmt.Errorf("%s action is missing or not executable", actionName)
		}
	}
	return manifest, nil
}

func parseChecksums(contents []byte) (map[string]string, error) {
	result := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(contents)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 || len(parts[0]) != sha256.Size*2 {
			return nil, fmt.Errorf("invalid package checksum line")
		}
		if _, err := hex.DecodeString(parts[0]); err != nil {
			return nil, fmt.Errorf("invalid package checksum")
		}
		fileName, err := safePackagePath(parts[1])
		if err != nil {
			return nil, err
		}
		if _, exists := result[fileName]; exists {
			return nil, fmt.Errorf("duplicate package checksum for %s", fileName)
		}
		result[fileName] = strings.ToLower(parts[0])
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func safePackagePath(value string) (string, error) {
	if value == "" || strings.Contains(value, "\\") || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("invalid package path")
	}
	cleaned := path.Clean(value)
	if path.IsAbs(value) || cleaned != value || cleaned == "." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("unsafe package path %q", value)
	}
	return cleaned, nil
}
