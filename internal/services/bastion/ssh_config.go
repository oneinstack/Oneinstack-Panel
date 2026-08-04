package bastion

import (
	"errors"
	"fmt"
	"os"
	"time"

	"oneinstack/internal/models"

	"golang.org/x/crypto/ssh"
)

// buildSSHClientConfig 根据服务器的认证方式构建 SSH 客户端配置。
// password 认证使用密码；key 认证读取本地私钥文件。
// 探测与采集共用此函数，避免双轨认证逻辑。
func buildSSHClientConfig(server *models.BastionServer, password string, timeout time.Duration) (*ssh.ClientConfig, error) {
	config := &ssh.ClientConfig{
		User:            server.Username,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         timeout,
	}
	switch server.AuthMethod {
	case models.BastionAuthKey:
		if server.KeyPath == "" {
			return nil, errors.New("密钥认证需要配置私钥路径")
		}
		key, err := os.ReadFile(server.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("读取私钥文件: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("解析私钥: %w", err)
		}
		config.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	default: // BastionAuthPassword 或未指定
		if password == "" {
			return nil, errors.New("密码不能为空")
		}
		config.Auth = []ssh.AuthMethod{ssh.Password(password)}
	}
	return config, nil
}
