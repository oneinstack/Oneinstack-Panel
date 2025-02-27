package software

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const logDir = "/usr/local/onesoft/log/install"

type InstallLogger struct {
	LogPath string
	file    *os.File
}

func NewInstallLogger(softwareName, version string) (*InstallLogger, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("创建日志目录失败: %v", err)
	}

	logName := fmt.Sprintf("%s-%s-%d.log",
		softwareName,
		version,
		time.Now().Unix(),
	)
	path := filepath.Join(logDir, logName)

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("创建日志文件失败: %v", err)
	}

	return &InstallLogger{
		LogPath: path,
		file:    file,
	}, nil
}

func (l *InstallLogger) Write(format string, args ...interface{}) {
	entry := fmt.Sprintf("[%s] %s\n",
		time.Now().Format("2006-01-02 15:04:05"),
		fmt.Sprintf(format, args...),
	)
	l.file.WriteString(entry)
}

func (l *InstallLogger) Close() {
	l.file.Close()
}
