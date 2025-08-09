# 🚀 脚本贡献指南

欢迎为 Oneinstack Panel 项目贡献安装脚本！我们建立了一个简单易用的脚本贡献系统，让社区开发者能够轻松提交和维护各种软件的安装脚本。

## 📋 目录
- [快速开始](#快速开始)
- [脚本规范](#脚本规范)
- [目录结构](#目录结构)
- [脚本模板](#脚本模板)
- [测试指南](#测试指南)
- [提交流程](#提交流程)
- [维护指南](#维护指南)

## 🚀 快速开始

### 1. Fork 项目
```bash
# 1. Fork 本项目到你的GitHub账户
# 2. Clone 你的Fork
git clone https://github.com/YOUR_USERNAME/Oneinstack-Panel.git
cd Oneinstack-Panel
```

### 2. 创建新脚本
```bash
# 使用脚本生成器创建新脚本
go run tools/script-generator.go --name=postgresql --type=install --version=15

# 或者手动创建
cp scripts/templates/install.template scripts/install/postgresql.sh
```

### 3. 编辑脚本
```bash
# 使用你喜欢的编辑器
vim scripts/install/postgresql.sh
```

### 4. 更新配置
```bash
# 编辑脚本配置文件
vim scripts/config.yaml
```

### 5. 测试脚本
```bash
# 运行测试
go run tools/script-tester.go --script=postgresql --type=install
```

### 6. 提交PR
```bash
git add .
git commit -m "feat: add PostgreSQL 15 installation script"
git push origin feature/postgresql-install
# 然后在GitHub上创建Pull Request
```

## 📁 目录结构

```
scripts/
├── config.yaml                 # 脚本配置文件
├── templates/                  # 脚本模板
│   ├── install.template        # 安装脚本模板
│   ├── uninstall.template      # 卸载脚本模板
│   └── config.template         # 配置脚本模板
├── install/                    # 安装脚本
│   ├── nginx.sh               # Nginx安装脚本
│   ├── postgresql.sh          # PostgreSQL安装脚本
│   └── ...
├── uninstall/                 # 卸载脚本
│   ├── nginx.sh
│   └── ...
├── config/                    # 配置脚本
│   ├── nginx-vhost.sh
│   └── ...
└── contrib/                   # 社区贡献脚本
    ├── experimental/          # 实验性脚本
    ├── legacy/               # 遗留版本脚本
    └── testing/              # 测试中的脚本
```

## 📝 脚本规范

### 基本要求

1. **脚本头部信息**
```bash
#!/bin/bash

#=============================================================================
# PostgreSQL 安装脚本
# 版本: 15.0
# 作者: Your Name <your.email@example.com>
# 描述: 自动安装 PostgreSQL 数据库服务器
# 支持系统: Ubuntu 18.04+, CentOS 7+, Debian 10+
# 最后更新: 2024-01-15
#=============================================================================
```

2. **严格模式**
```bash
set -euo pipefail  # 遇到错误立即退出
```

3. **颜色和日志**
```bash
# 使用统一的颜色定义
readonly RED='\033[0;31m'
readonly GREEN='\033[0;32m'
readonly YELLOW='\033[1;33m'
readonly NC='\033[0m'

# 使用统一的日志函数
log() {
    local level=$1
    shift
    local message="$*"
    local timestamp=$(date '+%Y-%m-%d %H:%M:%S')
    
    case $level in
        "INFO")  echo -e "${GREEN}[INFO]${NC} ${timestamp} - $message" ;;
        "WARN")  echo -e "${YELLOW}[WARN]${NC} ${timestamp} - $message" ;;
        "ERROR") echo -e "${RED}[ERROR]${NC} ${timestamp} - $message" ;;
    esac
}
```

### 参数支持

脚本应支持标准参数格式：
```bash
# 支持的参数格式
./postgresql.sh --version=15 --password=mypassword --port=5432 --data-dir=/var/lib/postgresql

# 参数解析示例
while [[ $# -gt 0 ]]; do
    case $1 in
        --version=*)
            VERSION="${1#*=}"
            shift
            ;;
        --password=*)
            PASSWORD="${1#*=}"
            shift
            ;;
        --port=*)
            PORT="${1#*=}"
            shift
            ;;
        --help)
            show_help
            exit 0
            ;;
        *)
            echo "未知参数: $1"
            show_help
            exit 1
            ;;
    esac
done
```

### 系统兼容性

脚本应支持主流Linux发行版：
```bash
# 系统检测
detect_os() {
    if [[ -f /etc/os-release ]]; then
        . /etc/os-release
        OS=$ID
        OS_VERSION=$VERSION_ID
        log "INFO" "检测到操作系统: $PRETTY_NAME"
    else
        log "ERROR" "无法检测操作系统类型"
        exit 1
    fi
}

# 包管理器适配
install_dependencies() {
    case $OS in
        ubuntu|debian)
            apt-get update
            apt-get install -y postgresql-$VERSION postgresql-contrib
            ;;
        centos|rhel|rocky|almalinux)
            yum install -y postgresql$VERSION-server postgresql$VERSION-contrib
            ;;
        fedora)
            dnf install -y postgresql-server postgresql-contrib
            ;;
        *)
            log "ERROR" "不支持的操作系统: $OS"
            exit 1
            ;;
    esac
}
```

## 🎯 脚本模板

### 安装脚本模板

```bash
#!/bin/bash

#=============================================================================
# {{SOFTWARE_NAME}} 安装脚本
# 版本: {{SOFTWARE_VERSION}}
# 作者: {{AUTHOR_NAME}} <{{AUTHOR_EMAIL}}>
# 描述: 自动安装 {{SOFTWARE_NAME}}
# 支持系统: {{SUPPORTED_OS}}
# 最后更新: {{UPDATE_DATE}}
#=============================================================================

set -euo pipefail

# 颜色定义
readonly RED='\033[0;31m'
readonly GREEN='\033[0;32m'
readonly YELLOW='\033[1;33m'
readonly NC='\033[0m'

# 默认配置
readonly DEFAULT_VERSION="{{DEFAULT_VERSION}}"
readonly DEFAULT_PORT="{{DEFAULT_PORT}}"
readonly INSTALL_DIR="/usr/local/{{SOFTWARE_NAME}}"

# 全局变量
VERSION=""
PORT=""
PASSWORD=""
DATA_DIR=""

# 日志函数
log() {
    local level=$1
    shift
    local message="$*"
    local timestamp=$(date '+%Y-%m-%d %H:%M:%S')
    
    case $level in
        "INFO")  echo -e "${GREEN}[INFO]${NC} ${timestamp} - $message" ;;
        "WARN")  echo -e "${YELLOW}[WARN]${NC} ${timestamp} - $message" ;;
        "ERROR") echo -e "${RED}[ERROR]${NC} ${timestamp} - $message" ;;
    esac
}

# 显示帮助信息
show_help() {
    cat << EOF
{{SOFTWARE_NAME}} 安装脚本

用法: $0 [选项]

选项:
    --version=VERSION    指定安装版本 (默认: ${DEFAULT_VERSION})
    --port=PORT         指定端口 (默认: ${DEFAULT_PORT})
    --password=PASS     设置密码
    --data-dir=DIR      指定数据目录
    --help              显示此帮助信息

示例:
    $0 --version=15 --password=mypass --port=5432

EOF
}

# 参数解析
parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            --version=*)
                VERSION="${1#*=}"
                shift
                ;;
            --port=*)
                PORT="${1#*=}"
                shift
                ;;
            --password=*)
                PASSWORD="${1#*=}"
                shift
                ;;
            --data-dir=*)
                DATA_DIR="${1#*=}"
                shift
                ;;
            --help)
                show_help
                exit 0
                ;;
            *)
                log "ERROR" "未知参数: $1"
                show_help
                exit 1
                ;;
        esac
    done
    
    # 设置默认值
    VERSION=${VERSION:-$DEFAULT_VERSION}
    PORT=${PORT:-$DEFAULT_PORT}
}

# 检查root权限
check_root() {
    if [[ $EUID -ne 0 ]]; then
        log "ERROR" "此脚本需要root权限运行"
        exit 1
    fi
}

# 检测操作系统
detect_os() {
    if [[ -f /etc/os-release ]]; then
        . /etc/os-release
        OS=$ID
        OS_VERSION=$VERSION_ID
        log "INFO" "检测到操作系统: $PRETTY_NAME"
    else
        log "ERROR" "无法检测操作系统类型"
        exit 1
    fi
}

# 安装依赖
install_dependencies() {
    log "INFO" "安装依赖包..."
    
    case $OS in
        ubuntu|debian)
            apt-get update
            apt-get install -y wget curl gnupg2
            ;;
        centos|rhel|rocky|almalinux)
            yum install -y wget curl
            ;;
        fedora)
            dnf install -y wget curl
            ;;
        *)
            log "ERROR" "不支持的操作系统: $OS"
            exit 1
            ;;
    esac
}

# 安装软件
install_software() {
    log "INFO" "开始安装 {{SOFTWARE_NAME}} ${VERSION}..."
    
    # 在这里实现具体的安装逻辑
    # ...
    
    log "INFO" "{{SOFTWARE_NAME}} 安装完成"
}

# 配置软件
configure_software() {
    log "INFO" "配置 {{SOFTWARE_NAME}}..."
    
    # 在这里实现配置逻辑
    # ...
    
    log "INFO" "{{SOFTWARE_NAME}} 配置完成"
}

# 启动服务
start_service() {
    log "INFO" "启动 {{SOFTWARE_NAME}} 服务..."
    
    systemctl enable {{SOFTWARE_NAME}}
    systemctl start {{SOFTWARE_NAME}}
    
    if systemctl is-active --quiet {{SOFTWARE_NAME}}; then
        log "INFO" "{{SOFTWARE_NAME}} 服务启动成功"
    else
        log "ERROR" "{{SOFTWARE_NAME}} 服务启动失败"
        exit 1
    fi
}

# 显示安装信息
show_info() {
    cat << EOF

${GREEN}=============================================================================
🎉 {{SOFTWARE_NAME}} ${VERSION} 安装完成！
=============================================================================${NC}

${GREEN}📁 安装信息：${NC}
   • 安装目录: ${INSTALL_DIR}
   • 端口: ${PORT}
   • 数据目录: ${DATA_DIR}

${GREEN}🔧 服务管理：${NC}
   • 启动: systemctl start {{SOFTWARE_NAME}}
   • 停止: systemctl stop {{SOFTWARE_NAME}}
   • 重启: systemctl restart {{SOFTWARE_NAME}}
   • 状态: systemctl status {{SOFTWARE_NAME}}

${GREEN}🌐 访问信息：${NC}
   • 本地访问: localhost:${PORT}

${YELLOW}⚠️  重要提示：${NC}
   • 请妥善保管密码信息
   • 建议配置防火墙规则
   • 定期备份数据

EOF
}

# 主函数
main() {
    log "INFO" "开始安装 {{SOFTWARE_NAME}}..."
    
    parse_args "$@"
    check_root
    detect_os
    install_dependencies
    install_software
    configure_software
    start_service
    show_info
    
    log "INFO" "安装完成！"
}

# 执行主函数
main "$@"
```

## ✅ 配置文件规范

在 `scripts/config.yaml` 中添加你的脚本配置：

```yaml
scripts:
  install:
    database:
      - name: postgresql
        display_name: "PostgreSQL"
        versions: ["13", "14", "15", "16"]
        default_version: "15"
        description: "PostgreSQL 关系型数据库"
        author: "Your Name"
        email: "your.email@example.com"
        supported_os: ["ubuntu", "debian", "centos", "rhel", "rocky", "almalinux"]
        parameters:
          - name: "password"
            type: "string"
            required: true
            description: "数据库管理员密码"
          - name: "port"
            type: "integer"
            default: 5432
            description: "数据库端口"
          - name: "data_dir"
            type: "string"
            default: "/var/lib/postgresql"
            description: "数据目录"
        tags: ["database", "postgresql", "sql"]
        category: "database"
        difficulty: "medium"
        estimated_time: "5-10分钟"
```

## 🧪 测试指南

### 1. 本地测试

```bash
# 语法检查
shellcheck scripts/install/postgresql.sh

# 功能测试
bash -n scripts/install/postgresql.sh

# 集成测试
go run tools/script-tester.go --script=postgresql --type=install --dry-run
```

### 2. Docker测试

```bash
# 在不同系统中测试
docker run --rm -v $(pwd):/workspace ubuntu:20.04 \
    bash /workspace/scripts/install/postgresql.sh --version=15 --password=test123

docker run --rm -v $(pwd):/workspace centos:8 \
    bash /workspace/scripts/install/postgresql.sh --version=15 --password=test123
```

### 3. 自动化测试

我们提供了自动化测试工具：

```bash
# 运行所有测试
make test-scripts

# 测试特定脚本
make test-script SCRIPT=postgresql

# 测试特定系统
make test-script-os SCRIPT=postgresql OS=ubuntu:20.04
```

## 📤 提交流程

### 1. 分支命名规范

```bash
# 新功能
git checkout -b feature/add-postgresql-script

# 修复问题
git checkout -b fix/nginx-install-issue

# 文档更新
git checkout -b docs/update-script-guide
```

### 2. 提交信息规范

```bash
# 格式: type(scope): description

# 示例
git commit -m "feat(scripts): add PostgreSQL 15 installation script"
git commit -m "fix(nginx): fix SSL certificate configuration"
git commit -m "docs(contrib): update script contribution guide"
```

### 3. PR模板

创建PR时，请填写以下信息：

```markdown
## 📋 变更说明
- [ ] 新增安装脚本
- [ ] 修复现有脚本
- [ ] 更新文档
- [ ] 其他改进

## 🔧 脚本信息
- **软件名称**: PostgreSQL
- **版本**: 15.0
- **支持系统**: Ubuntu 18.04+, CentOS 7+
- **测试状态**: ✅ 已测试

## ✅ 检查清单
- [ ] 脚本通过shellcheck检查
- [ ] 在至少2个不同系统上测试通过
- [ ] 更新了config.yaml配置
- [ ] 添加了必要的文档
- [ ] 遵循了代码规范

## 🧪 测试结果
| 系统 | 版本 | 状态 |
|------|------|------|
| Ubuntu | 20.04 | ✅ |
| CentOS | 8 | ✅ |
| Debian | 11 | ✅ |
```

## 🏆 贡献者激励

### 贡献等级

- 🥉 **Bronze**: 贡献1个脚本
- 🥈 **Silver**: 贡献3个脚本或修复5个问题
- 🥇 **Gold**: 贡献5个脚本或成为维护者
- 💎 **Diamond**: 长期活跃贡献者

### 认可方式

1. **README致谢**: 在项目README中列出贡献者
2. **脚本署名**: 在脚本头部标注作者信息
3. **社区徽章**: 在GitHub Profile显示项目徽章
4. **技术博客**: 邀请写技术分享文章

## 🤝 维护指南

### 脚本维护职责

1. **响应Issue**: 及时回复脚本相关问题
2. **版本更新**: 跟进软件版本更新
3. **兼容性维护**: 确保新系统版本兼容
4. **文档更新**: 保持文档最新

### 维护者权限

- 直接提交小修复
- Review相关PR
- 参与技术决策
- 指导新贡献者

## 📞 获取帮助

### 讨论渠道

- **GitHub Issues**: 报告问题和建议
- **GitHub Discussions**: 技术讨论
- **QQ群**: 123456789
- **微信群**: 联系管理员邀请

### 常见问题

**Q: 如何选择脚本分类？**
A: 参考现有分类，如database、webserver、runtime等。

**Q: 脚本测试失败怎么办？**
A: 检查系统兼容性，查看测试日志，或在讨论区求助。

**Q: 如何处理依赖冲突？**
A: 在脚本中添加依赖检查和冲突处理逻辑。

---

感谢您为 Oneinstack Panel 项目做出贡献！每一个脚本都让这个项目变得更强大。🚀
