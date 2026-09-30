# Caddy 组件

- 组件包版本：1.0.18
- 软件版本：2.10.2
- 安装目录：/usr/local/caddy
- 主配置：/usr/local/caddy/conf/Caddyfile
- 受管默认站点：/usr/local/caddy/conf/oneinstack-default.caddy
- Panel vhost：/usr/local/one/vhost/caddy/*.conf
- 服务：oneinstack-caddy.service（caddy:caddy）
- 数据目录：/var/lib/caddy

在线安装与完全离线安装使用同一份 Caddy 官方 Linux 静态制品，并在安装前校验固定 SHA-256。离线分支只读取 Bundle，不执行下载、仓库刷新或网络回退。

安装脚本会用用户级 ACL 为 `caddy` 运行账号补齐受管网站、日志路径的最小穿越权限，并记录由组件新增的 ACL，以便安装失败回滚或卸载时恢复；不会扩大共享目录的 `other` 权限。离线 Bundle 必须携带目标系统匹配的 ACL 工具包。

## 支持矩阵

| 系统族 | Manifest ID | 版本 | 包管理器 | 架构 |
| --- | --- | --- | --- | --- |
| Ubuntu | ubuntu | 22.04、24.04、26.04 | apt | amd64、arm64 |
| Debian | debian | 11、12、13 | apt | amd64、arm64 |
| RHEL / Rocky / Alma / Oracle Linux | rhel、rocky、almalinux、ol | 8、9、10 | dnf | amd64、arm64 |
| CentOS | centos | 7 | yum | amd64、arm64 |
| CentOS Stream | centos-stream | 8、9、10 | dnf | amd64、arm64 |
| Fedora | fedora | * | dnf | amd64、arm64 |
| Amazon Linux | amzn | 2023 | dnf | amd64、arm64 |
| SLES | sles | 15、16 | zypper | amd64、arm64 |
| openSUSE | opensuse-leap、opensuse-tumbleweed、opensuse | * | zypper | amd64、arm64 |

星号表示 Manifest 目标声明；必须在对应主机完成生命周期验收后才能标记为实机认证。

## 安装前参数

`software-version` 固定为 2.10.2；`port` 默认 80；`php-fpm-socket` 默认 /dev/shm/php-cgi.sock；`web-root` 默认 /data/wwwroot；`log-dir` 默认 /data/wwwlogs。安装来源、Bundle 路径、安装目录、vhost 根目录、运行账号和卸载策略由服务端管理。

## 安装后配置

配置接口支持 `port`、`phpFpmSocket`、`webRoot`、`logDir`，返回 revision、reload 应用方式以及监听、目录、账号、服务和实际版本等只读运行信息。配置应用采用候选文件校验、revision 并发检查、原子替换和失败恢复。

`scripts/build-offline-bundle.sh` 按 OS、版本、架构生成 Bundle。CentOS 7 如需补充低端口能力工具，只允许 Bundle 内本地 RPM，离线安装不会访问仓库。
