# OneinStack Panel

[![最新版本](https://img.shields.io/github/v/release/oneinstack/Oneinstack-Panel?sort=semver&display_name=tag)](https://github.com/oneinstack/Oneinstack-Panel/releases)
[![CI 构建](https://github.com/oneinstack/Oneinstack-Panel/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/oneinstack/Oneinstack-Panel/actions/workflows/ci.yml)
[![Go 版本](https://img.shields.io/github/go-mod/go-version/oneinstack/Oneinstack-Panel)](https://github.com/oneinstack/Oneinstack-Panel/blob/main/go.mod)
[![许可证](https://img.shields.io/github/license/oneinstack/Oneinstack-Panel)](LICENSE)
[![GitHub Stars](https://img.shields.io/github/stars/oneinstack/Oneinstack-Panel?style=flat)](https://github.com/oneinstack/Oneinstack-Panel/stargazers)

OneinStack Panel 是面向 Linux 服务器的开源运维面板，提供网站、软件、数据库、容器、文件、证书、安全、监控和审计等管理能力，并支持多个独立 Panel 组成用户自己的控制端/节点集群。

> Center 只负责版本、组件脚本包和软件商城目录发布，不参与节点运行时管理。

## 主要功能

- 服务器资源监控：CPU、内存、磁盘、网络、服务健康度和历史指标
- 软件商城：签名目录、组件脚本包、安装、升级、卸载和服务配置
- 网站管理：静态站点、反向代理、Nginx 配置、证书、ACME、备份和恢复
- 数据库管理：连接、备份、恢复和任务日志
- Docker：容器、镜像、网络、卷、Compose 和受控终端
- 系统运维：文件管理、回收站、SSH、防火墙、Fail2ban、定时任务
- 安全与合规：操作预览、审批、配置快照、差异、审计和告警
- 多节点管理：节点注册、令牌轮换、心跳、资源指标、任务队列和网站下发

## 多节点模式

用户可以部署多个独立的 OneinStack Panel：

- **控制端**：在“多节点管理”页面添加其他 Panel 节点，查看指标并下发任务。
- **节点端**：在“多节点管理”页面选择“节点端”，打开节点模式开关，填写控制端地址和节点令牌。
- 配置保存到节点端后端；代理会自动注册、心跳和领取任务，修改或关闭开关会自动生效。

支持任务：`software.install`、`software.uninstall`、`service.start`、`service.stop`、`service.restart`、`service.reload`、`system.command`、`file.upload`、`database.sync`、`website.sync`、`website.content_sync`。

网站下发支持指定节点、标签或最低负载策略，可选同步网站目录（最大 64 MiB）。文件上传最大 16 MiB，数据库 Dump 最大 64 MiB。系统命令使用参数数组并限制为受控命令，禁止 Shell、下载器和脚本解释器。

节点接口仅接受节点令牌，控制端接口要求超级管理员权限。令牌只保存哈希，轮换后旧令牌立即失效。

## 系统要求

- Linux amd64 或 arm64；已验证 Ubuntu、Debian、CentOS、RHEL、Rocky、AlmaLinux、OpenCloudOS、Anolis
- 推荐内存 1 GB 以上、可用磁盘 20 GB 以上
- root 权限、systemd 和 `prlimit`

## 安装与更新

从镜像站下载对应架构的发布包和 `.sha256` 文件，校验后在临时目录运行安装器：

```bash
VERSION="v0.3.0-build.11"
PACKAGE="one-linux-amd64-${VERSION}.tar.gz"
BASE_URL="https://mirrors.oneinstack.com/oneinstack"
work_dir="$(mktemp -d)"; trap 'rm -rf -- "$work_dir"' EXIT; cd "$work_dir"
wget -c "$BASE_URL/$PACKAGE" "$BASE_URL/$PACKAGE.sha256"
sha256sum -c "$PACKAGE.sha256" && tar -xzf "$PACKAGE"
cd "${PACKAGE%.tar.gz}" && sudo ./install.sh --force
```

安装后访问 `http://服务器IP:8089`。默认 HTTP 入口保持可用，可在设置中另行配置 HTTPS。使用新的校验发布包再次运行 `install.sh --force` 更新，Panel 会保留现有配置并执行就绪检查。

普通卸载：`sudo ./install.sh uninstall`；永久删除：`sudo ./install.sh uninstall --purge --yes`。

## Center 与软件商城

Center 管理发布渠道、版本灰度、软件目录和组件脚本包。Panel 只在验证签名、修订摘要、制品大小和 SHA-256 后应用更新；Center 不可用时继续使用最后一次可信快照。

```yaml
scriptCenter:
  enabled: true
  url: "https://center.example.com"
  channel: "stable"
  trustedKeys:
    center-key-id: "BASE64_ED25519_PUBLIC_KEY"
updateCenter:
  enabled: true
  centerUrl: "https://center.example.com"
```

详细构建、发布和集群接口说明见 [BUILD.md](BUILD.md)、[CLUSTER_MANAGEMENT.md](CLUSTER_MANAGEMENT.md) 和 [docs/cluster-management.md](docs/cluster-management.md)。

## 开发与协议

```bash
go test ./...
go run ./cmd server
```

技术栈：Go、Gin、GORM、SQLite、Systemd、Vue.js。项目采用 [Apache License 2.0](LICENSE) 开源。

官网：[oneinstack.com](https://oneinstack.com) · 反馈：[GitHub Issues](https://github.com/oneinstack/Oneinstack-Panel/issues)

## Star 趋势

[![Star History Chart](https://api.star-history.com/svg?repos=oneinstack/Oneinstack-Panel&type=Date)](https://star-history.com/#oneinstack/Oneinstack-Panel&Date)
