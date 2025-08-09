# 🚀 Oneinstack Panel v{VERSION}

## 📋 新增功能 (New Features)

- [ ] 新功能描述

## 🔧 改进优化 (Improvements)

- [ ] 改进描述

## 🐛 问题修复 (Bug Fixes)

- [ ] 修复描述

## 🔒 安全更新 (Security Updates)

- [ ] 安全更新描述

## ⚠️ 破坏性变更 (Breaking Changes)

- [ ] 破坏性变更描述

## 📦 安装方式 (Installation)

### 快速安装 (Quick Install)
```bash
curl -fsSL https://github.com/oneinstack/panel/releases/latest/download/install.sh | bash
```

### 手动安装 (Manual Install)

#### Linux AMD64
```bash
wget https://github.com/oneinstack/panel/releases/download/v{VERSION}/one-linux-amd64.tar.gz
tar -xzf one-linux-amd64.tar.gz
cd one-linux-amd64
sudo ./install-ubuntu.sh  # 或 ./install-centos.sh
```

#### Linux ARM64
```bash
wget https://github.com/oneinstack/panel/releases/download/v{VERSION}/one-linux-arm64.tar.gz
tar -xzf one-linux-arm64.tar.gz
cd one-linux-arm64
sudo ./install-ubuntu.sh  # 或 ./install-centos.sh
```

### Docker 安装 (Docker Install)

#### CentOS 版本
```bash
docker run -d --name oneinstack-panel \
  -p 8089:8089 \
  -v /data:/data \
  oneinstack/panel:v{VERSION}-centos
```

#### Ubuntu 版本
```bash
docker run -d --name oneinstack-panel \
  -p 8089:8089 \
  -v /data:/data \
  oneinstack/panel:v{VERSION}-ubuntu
```

### Docker Compose
```bash
curl -fsSL https://raw.githubusercontent.com/oneinstack/panel/main/docker-compose.yml -o docker-compose.yml
docker-compose --profile centos up -d  # 或 --profile ubuntu
```

## 🔄 升级方式 (Upgrade)

### 从 v{PREVIOUS_VERSION} 升级
```bash
# 停止服务
sudo systemctl stop one

# 备份配置
sudo cp -r /usr/local/one /usr/local/one.backup

# 下载新版本
wget https://github.com/oneinstack/panel/releases/download/v{VERSION}/one-linux-amd64.tar.gz
tar -xzf one-linux-amd64.tar.gz

# 替换二进制文件
sudo cp one-linux-amd64/one /usr/local/one/

# 启动服务
sudo systemctl start one
```

## 📊 系统要求 (System Requirements)

- **操作系统**: CentOS 7+, Ubuntu 18.04+, Debian 9+
- **内存**: 512MB 最低, 1GB 推荐
- **磁盘空间**: 1GB 可用空间
- **网络**: 互联网连接 (用于安装依赖)

## 🔐 安全验证 (Security Verification)

所有发布文件都包含 SHA256 校验和:
```bash
# 验证文件完整性
sha256sum -c checksums.txt
```

## 📖 文档链接 (Documentation)

- [安装指南](https://github.com/oneinstack/panel/wiki/Installation)
- [配置说明](https://github.com/oneinstack/panel/wiki/Configuration)
- [API 文档](https://github.com/oneinstack/panel/wiki/API)
- [故障排除](https://github.com/oneinstack/panel/wiki/Troubleshooting)

## 🆘 获取帮助 (Get Help)

- 🐛 [报告问题](https://github.com/oneinstack/panel/issues/new/choose)
- 💬 [讨论区](https://github.com/oneinstack/panel/discussions)
- 📧 [邮件支持](mailto:support@oneinstack.com)

## 🙏 贡献者 (Contributors)

感谢所有为此版本做出贡献的开发者！

## 📝 完整变更日志 (Full Changelog)

**完整变更**: https://github.com/oneinstack/panel/compare/v{PREVIOUS_VERSION}...v{VERSION}
