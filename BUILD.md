# 构建指南 (Build Guide)

本文档说明如何构建 Oneinstack Panel 项目。

## 📋 系统要求

### 开发环境
- **Go**: 1.20+ (推荐 1.21+)
- **Git**: 用于版本控制
- **Make**: 用于构建自动化
- **Docker**: 用于容器构建 (可选)
- **Docker Compose**: 用于多容器管理 (可选)

### 系统支持
- **构建平台**: Linux, macOS, Windows
- **目标平台**: Linux (AMD64, ARM64)
- **容器平台**: CentOS 7+, Ubuntu 20.04+

## 🚀 快速开始

### 1. 克隆项目
```bash
git clone https://github.com/oneinstack/panel.git
cd panel
```

### 2. 安装依赖
```bash
go mod download
```

### 3. 构建项目
```bash
# 使用 Make
make build

# 或使用构建脚本
./scripts/build.sh
```

## 🔨 构建命令

### Make 命令

```bash
# 显示帮助
make help

# 构建当前平台
make build

# 构建所有平台
make build-all

# 运行测试
make test

# 运行代码检查
make lint

# 创建发布包
make package

# 构建 Docker 镜像
make docker-build

# 运行 Docker 容器
make docker-run

# 完整发布流程
make release

# 清理构建文件
make clean
```

### 构建脚本

```bash
# 检查依赖
./scripts/build.sh check

# 清理构建目录
./scripts/build.sh clean

# 运行测试
./scripts/build.sh test

# 运行代码检查
./scripts/build.sh lint

# 构建二进制文件
./scripts/build.sh build

# 创建发布包
./scripts/build.sh package

# 完整构建流程
./scripts/build.sh all
```

## 🐳 Docker 构建

### 构建镜像
```bash
# 构建 CentOS 版本
docker build -f docker/Dockerfile.centos -t oneinstack/panel:centos .

# 构建 Ubuntu 版本
docker build -f docker/Dockerfile.ubuntu -t oneinstack/panel:ubuntu .

# 使用 Make
make docker-build
```

### 运行容器
```bash
# 使用 Docker Compose (推荐)
docker-compose --profile centos up -d

# 直接运行
docker run -d --name oneinstack-panel \
  -p 8089:8089 \
  -v /data:/data \
  oneinstack/panel:centos
```

## 🎯 目标平台

### 支持的架构
- `linux/amd64` - Linux x86_64
- `linux/arm64` - Linux ARM64
- `darwin/amd64` - macOS x86_64 (仅二进制)
- `darwin/arm64` - macOS ARM64 (仅二进制)
- `windows/amd64` - Windows x86_64 (仅二进制)

### 发布包内容
每个 Linux 发布包包含：
- 编译好的二进制文件
- 配置文件模板
- 安装脚本 (CentOS/Ubuntu)
- 文档文件
- 许可证文件

## 📦 发布流程

### 本地发布
```bash
# 创建标签
git tag v1.0.0
git push origin v1.0.0

# 构建发布包
make release

# 发布文件位于 releases/v1.0.0/
```

### GitHub Actions 自动发布

当推送标签到 GitHub 时，会自动触发构建和发布流程：

1. **代码检查**: 运行测试和代码检查
2. **多平台构建**: 构建所有目标平台的二进制文件
3. **Docker 构建**: 构建 CentOS 和 Ubuntu 镜像
4. **创建发布**: 自动创建 GitHub Release
5. **上传制品**: 上传所有构建文件和校验和

### 发布包验证
```bash
# 验证校验和
sha256sum -c checksums.txt

# 验证二进制文件
./one --version
```

## 🔧 开发构建

### 开发模式运行
```bash
# 直接运行
go run ./cmd/main.go server start

# 调试模式
go run ./cmd/main.go debug

# 使用 Make
make dev
make dev-debug
```

### 热重载开发
```bash
# 安装 air (可选)
go install github.com/cosmtrek/air@latest

# 启动热重载
air
```

## 🧪 测试

### 运行测试
```bash
# 运行所有测试
go test ./...

# 带覆盖率
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out

# 使用 Make
make test
make test-coverage
```

### 代码检查
```bash
# 安装 golangci-lint
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin v1.54.2

# 运行检查
golangci-lint run

# 使用 Make
make lint
```

## 📊 构建优化

### 编译优化
- 使用 `-ldflags="-s -w"` 减小二进制文件大小
- 设置 `CGO_ENABLED=0` 创建静态链接二进制
- 嵌入版本信息和构建时间

### Docker 优化
- 多阶段构建减小镜像大小
- 使用 Alpine 作为构建镜像
- 非 root 用户运行提高安全性
- 健康检查确保服务可用性

## 🚨 故障排除

### 常见问题

#### 1. Go 版本过低
```bash
# 检查版本
go version

# 升级 Go (Linux)
sudo rm -rf /usr/local/go
wget https://go.dev/dl/go1.21.5.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.21.5.linux-amd64.tar.gz
```

#### 2. 依赖下载失败
```bash
# 设置代理 (中国用户)
go env -w GOPROXY=https://goproxy.cn,direct
go env -w GOSUMDB=sum.golang.google.cn

# 清理模块缓存
go clean -modcache
go mod download
```

#### 3. Docker 构建失败
```bash
# 清理 Docker 缓存
docker system prune -f

# 重新构建
docker build --no-cache -f docker/Dockerfile.centos -t oneinstack/panel:centos .
```

#### 4. 权限问题
```bash
# 设置脚本执行权限
chmod +x scripts/build.sh

# 设置 Docker 权限 (Linux)
sudo usermod -aG docker $USER
newgrp docker
```

## 📝 贡献指南

### 提交代码前
1. 运行测试: `make test`
2. 运行代码检查: `make lint`
3. 确保构建成功: `make build`
4. 更新文档 (如需要)

### 提交规范
- 使用语义化提交信息
- 包含相关的测试
- 更新 CHANGELOG.md

## 🔗 相关链接

- [项目主页](https://github.com/oneinstack/panel)
- [问题报告](https://github.com/oneinstack/panel/issues)
- [讨论区](https://github.com/oneinstack/panel/discussions)
- [Wiki 文档](https://github.com/oneinstack/panel/wiki)
