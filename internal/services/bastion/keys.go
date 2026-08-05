package bastion

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"oneinstack/app"
	"oneinstack/internal/models"

	"golang.org/x/crypto/ssh"
)

const (
	privateKeyDirectoryMode os.FileMode = 0700
	privateKeyFileMode      os.FileMode = 0600
	maxPrivateKeySize                   = 64 * 1024
)

func privateKeyRoot() string {
	return filepath.Join(app.GetBasePath(), ".ssh", "secrets", "bastion")
}

func privateKeyPath(serverID uint) string {
	return filepath.Join(privateKeyRoot(), fmt.Sprintf("id_rsa_%d", serverID))
}

func privateKeyDir() string {
	return privateKeyRoot()
}

func validatePrivateKey(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("私钥不能为空")
	}
	if len(value) > maxPrivateKeySize {
		return fmt.Errorf("私钥不能超过 %d 字节", maxPrivateKeySize)
	}
	if _, err := ssh.ParsePrivateKey([]byte(value)); err != nil {
		return errors.New("私钥格式无效或不支持带口令私钥")
	}
	return nil
}

func writePrivateKey(serverID uint, value string) error {
	if err := validatePrivateKey(value); err != nil {
		return err
	}

	directory := privateKeyDir()
	if err := os.MkdirAll(directory, privateKeyDirectoryMode); err != nil {
		return fmt.Errorf("创建私钥目录失败: %w", err)
	}
	sshRoot := filepath.Join(app.GetBasePath(), ".ssh")
	secretRoot := filepath.Join(sshRoot, "secrets")
	for _, path := range []string{sshRoot, secretRoot, privateKeyRoot(), directory} {
		if err := os.Chmod(path, privateKeyDirectoryMode); err != nil {
			return fmt.Errorf("设置私钥目录权限失败: %w", err)
		}
	}

	temporary, err := os.CreateTemp(directory, ".id_rsa-*")
	if err != nil {
		return fmt.Errorf("创建私钥临时文件失败: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(privateKeyFileMode); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("设置私钥文件权限失败: %w", err)
	}
	if _, err := io.WriteString(temporary, value); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("写入私钥文件失败: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("同步私钥文件失败: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("关闭私钥文件失败: %w", err)
	}
	if err := os.Rename(temporaryPath, privateKeyPath(serverID)); err != nil {
		return fmt.Errorf("替换私钥文件失败: %w", err)
	}
	return nil
}

func removePrivateKey(serverID uint) error {
	if err := os.Remove(privateKeyPath(serverID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("清理私钥文件失败: %w", err)
	}
	return nil
}

func hasPrivateKey(server *models.BastionServer) bool {
	if server == nil || server.ID == 0 {
		return false
	}
	info, err := os.Lstat(privateKeyPath(server.ID))
	return err == nil && info.Mode().IsRegular()
}

func markKeyConfigured(server *models.BastionServer) {
	if server != nil {
		server.KeyConfigured = hasPrivateKey(server)
	}
}

func resolvePrivateKeyPath(server *models.BastionServer) (string, error) {
	if server == nil || server.ID == 0 {
		return "", errors.New("服务器 ID 无效")
	}
	expected := filepath.Clean(privateKeyPath(server.ID))
	if strings.TrimSpace(server.KeyPath) != "" && filepath.Clean(server.KeyPath) != expected {
		return "", errors.New("私钥路径未使用受控存储，请重新配置私钥")
	}
	return expected, nil
}

func migrateLegacyPrivateKey(server *models.BastionServer) error {
	if server == nil || strings.TrimSpace(server.KeyPath) == "" {
		return nil
	}
	expected := filepath.Clean(privateKeyPath(server.ID))
	legacy := filepath.Clean(server.KeyPath)
	if legacy == expected {
		if err := os.Chmod(expected, privateKeyFileMode); err != nil {
			return err
		}
		return nil
	}

	info, err := os.Lstat(legacy)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("旧私钥不是普通文件")
	}
	content, err := os.ReadFile(legacy)
	if err != nil {
		return err
	}
	if err := validatePrivateKey(string(content)); err != nil {
		return err
	}
	if err := writePrivateKey(server.ID, string(content)); err != nil {
		return err
	}
	return nil
}
