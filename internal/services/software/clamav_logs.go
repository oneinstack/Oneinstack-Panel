package software

import (
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
)

const (
	clamavLogDir      = "/var/log/oneinstack/clamav"
	maxClamavLogBytes = 256 * 1024
	maxClamavLogLines = 200
)

var ErrInvalidClamavLogSource = errors.New("invalid ClamAV log source")

type ClamavLogLine struct {
	OccurredAt *time.Time `json:"occurredAt,omitempty"`
	Text       string     `json:"text"`
}

type ClamavServiceLog struct {
	Source     string          `json:"source"`
	Path       string          `json:"path"`
	Exists     bool            `json:"exists"`
	ReadAt     time.Time       `json:"readAt"`
	ModifiedAt *time.Time      `json:"modifiedAt,omitempty"`
	FirstAt    *time.Time      `json:"firstAt,omitempty"`
	LastAt     *time.Time      `json:"lastAt,omitempty"`
	Truncated  bool            `json:"truncated"`
	Lines      []ClamavLogLine `json:"lines"`
}

// ReadClamavServiceLog reads only the two paths owned by the ClamAV component.
// The response is fetched from disk each time; task logs and Panel runtime logs
// are separate historical sources.
func ReadClamavServiceLog(source string) (ClamavServiceLog, error) {
	filename := ""
	switch source {
	case "clamd":
		filename = "clamd.log"
	case "freshclam":
		filename = "freshclam.log"
	default:
		return ClamavServiceLog{}, ErrInvalidClamavLogSource
	}
	result := ClamavServiceLog{
		Source: source,
		Path:   clamavLogDir + "/" + filename,
		ReadAt: time.Now(),
		Lines:  []ClamavLogLine{},
	}
	fd, err := syscall.Open(result.Path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, syscall.ENOENT) {
		return result, nil
	}
	if err != nil {
		return ClamavServiceLog{}, err
	}
	file := os.NewFile(uintptr(fd), result.Path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ClamavServiceLog{}, err
	}
	if !info.Mode().IsRegular() {
		return ClamavServiceLog{}, errors.New("ClamAV log is not a regular file")
	}
	result.Exists = true
	modified := info.ModTime()
	result.ModifiedAt = &modified
	start := info.Size() - maxClamavLogBytes
	if start < 0 {
		start = 0
	} else {
		result.Truncated = true
	}
	data := make([]byte, info.Size()-start)
	n, err := file.ReadAt(data, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return ClamavServiceLog{}, err
	}
	content := string(data[:n])
	if start > 0 {
		if index := strings.IndexByte(content, '\n'); index >= 0 {
			content = content[index+1:]
		} else {
			content = ""
		}
	}
	rows := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(rows) > maxClamavLogLines {
		rows = rows[len(rows)-maxClamavLogLines:]
		result.Truncated = true
	}
	for _, row := range rows {
		if row == "" {
			continue
		}
		line := ClamavLogLine{Text: row}
		if prefix, _, ok := strings.Cut(row, " -> "); ok {
			if parsed, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", prefix, time.Local); err == nil {
				line.OccurredAt = &parsed
				if result.FirstAt == nil {
					result.FirstAt = &parsed
				}
				result.LastAt = &parsed
			}
		}
		result.Lines = append(result.Lines, line)
	}
	return result, nil
}
