package filemanager

import (
	"bytes"
	"errors"
	"io"
	"os"
	pathpkg "path"
	"strings"
)

const (
	ArchiveFormatZIP     = "zip"
	ArchiveFormatTAR     = "tar"
	ArchiveFormatTARGZ   = "tar.gz"
	ArchiveFormatGZIP    = "gzip"
	ArchiveFormatTARBZ2  = "tar.bz2"
	ArchiveFormatBZIP2   = "bzip2"
	ArchiveFormatTARXZ   = "tar.xz"
	ArchiveFormatXZ      = "xz"
	ArchiveFormatTARZSTD = "tar.zst"
	ArchiveFormatZSTD    = "zstd"
	ArchiveFormatRAR     = "rar"
	ArchiveFormat7Z      = "7z"
)

var (
	ErrArchiveUnsupportedFormat = errors.New("unsupported archive format")
	ErrArchiveInvalid           = errors.New("invalid archive")
	ErrArchiveFormatMismatch    = errors.New("archive format does not match file name")
	ErrArchiveEncrypted         = errors.New("encrypted archive is not supported")
	ErrArchiveMultiVolume       = errors.New("multi-volume archive is not supported")
	ErrArchiveUnsafePath        = errors.New("unsafe archive path")
	ErrArchiveUnsupportedEntry  = errors.New("unsupported archive entry")
	ErrArchiveLimitExceeded     = errors.New("archive extraction limit exceeded")
	ErrArchiveTargetConflict    = errors.New("archive extraction target conflict")
	ErrArchiveRollbackFailed    = errors.New("archive extraction rollback failed")
)

type ArchiveNameInfo struct {
	Format      string
	BaseName    string
	Supported   bool
	MultiVolume bool
}

// InspectArchiveName identifies formats supported by the extraction service.
// Split archives are identified for clear UI feedback but are not extractable.
func InspectArchiveName(name string) ArchiveNameInfo {
	name = pathpkg.Base(strings.TrimSpace(name))
	lower := strings.ToLower(name)
	if lower == "" || lower == "." {
		return ArchiveNameInfo{}
	}
	if strings.HasSuffix(lower, ".7z.001") {
		return ArchiveNameInfo{Format: ArchiveFormat7Z, BaseName: name[:len(name)-len(".7z.001")], MultiVolume: true}
	}
	if strings.Contains(lower, ".part") && strings.HasSuffix(lower, ".rar") {
		return ArchiveNameInfo{Format: ArchiveFormatRAR, BaseName: strings.TrimSuffix(name, pathpkg.Ext(name)), MultiVolume: true}
	}
	if len(lower) > 4 && lower[len(lower)-4] == '.' && lower[len(lower)-3] == 'r' && lower[len(lower)-2] >= '0' && lower[len(lower)-2] <= '9' && lower[len(lower)-1] >= '0' && lower[len(lower)-1] <= '9' {
		return ArchiveNameInfo{Format: ArchiveFormatRAR, BaseName: name[:len(name)-4], MultiVolume: true}
	}

	formats := []struct {
		suffix string
		format string
	}{
		{".tar.gz", ArchiveFormatTARGZ}, {".tgz", ArchiveFormatTARGZ},
		{".tar.bz2", ArchiveFormatTARBZ2}, {".tbz2", ArchiveFormatTARBZ2}, {".tbz", ArchiveFormatTARBZ2},
		{".tar.xz", ArchiveFormatTARXZ}, {".txz", ArchiveFormatTARXZ},
		{".tar.zstd", ArchiveFormatTARZSTD}, {".tar.zst", ArchiveFormatTARZSTD}, {".tzst", ArchiveFormatTARZSTD},
		{".zip", ArchiveFormatZIP}, {".tar", ArchiveFormatTAR}, {".gz", ArchiveFormatGZIP},
		{".bz2", ArchiveFormatBZIP2}, {".xz", ArchiveFormatXZ}, {".zstd", ArchiveFormatZSTD}, {".zst", ArchiveFormatZSTD},
		{".rar", ArchiveFormatRAR}, {".7z", ArchiveFormat7Z},
	}
	for _, candidate := range formats {
		if strings.HasSuffix(lower, candidate.suffix) && len(name) > len(candidate.suffix) {
			return ArchiveNameInfo{
				Format: candidate.format, BaseName: name[:len(name)-len(candidate.suffix)], Supported: true,
			}
		}
	}
	return ArchiveNameInfo{}
}

func archiveContainerFormat(format string) string {
	switch format {
	case ArchiveFormatTARGZ:
		return ArchiveFormatGZIP
	case ArchiveFormatTARBZ2:
		return ArchiveFormatBZIP2
	case ArchiveFormatTARXZ:
		return ArchiveFormatXZ
	case ArchiveFormatTARZSTD:
		return ArchiveFormatZSTD
	default:
		return format
	}
}

func detectArchiveFormat(file *os.File, expected string) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	header := make([]byte, 512)
	n, err := io.ReadFull(file, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	header = header[:n]
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}

	detected := ""
	switch {
	case len(header) >= 6 && bytes.Equal(header[:6], []byte{'7', 'z', 0xbc, 0xaf, 0x27, 0x1c}):
		detected = ArchiveFormat7Z
	case len(header) >= 7 && bytes.Equal(header[:7], []byte{'R', 'a', 'r', '!', 0x1a, 0x07, 0x00}):
		detected = ArchiveFormatRAR
	case len(header) >= 8 && bytes.Equal(header[:8], []byte{'R', 'a', 'r', '!', 0x1a, 0x07, 0x01, 0x00}):
		detected = ArchiveFormatRAR
	case len(header) >= 4 && (bytes.Equal(header[:4], []byte{'P', 'K', 0x03, 0x04}) || bytes.Equal(header[:4], []byte{'P', 'K', 0x05, 0x06}) || bytes.Equal(header[:4], []byte{'P', 'K', 0x07, 0x08})):
		detected = ArchiveFormatZIP
	case len(header) >= 2 && header[0] == 0x1f && header[1] == 0x8b:
		detected = ArchiveFormatGZIP
	case len(header) >= 3 && bytes.Equal(header[:3], []byte("BZh")):
		detected = ArchiveFormatBZIP2
	case len(header) >= 6 && bytes.Equal(header[:6], []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}):
		detected = ArchiveFormatXZ
	case len(header) >= 4 && bytes.Equal(header[:4], []byte{0x28, 0xb5, 0x2f, 0xfd}):
		detected = ArchiveFormatZSTD
	case len(header) >= 262 && bytes.Equal(header[257:262], []byte("ustar")):
		detected = ArchiveFormatTAR
	}

	if expected == "" {
		if detected == "" {
			return "", ErrArchiveUnsupportedFormat
		}
		return detected, nil
	}
	if detected == "" {
		if expected == ArchiveFormatTAR {
			return expected, nil
		}
		return "", ErrArchiveFormatMismatch
	}
	if archiveContainerFormat(expected) != detected {
		return "", ErrArchiveFormatMismatch
	}
	return expected, nil
}
