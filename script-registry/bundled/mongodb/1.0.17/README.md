# MongoDB 在线与完全离线生命周期

组件包版本 `1.0.17`，支持 MongoDB `8.0.17`、`8.0.32`，默认推荐 `8.0.32`；只允许受管实例从 `8.0.17` 原地升级到 `8.0.32`。

## 正式服务器矩阵

| 系统 | 版本 | amd64 | arm64 | 约束 |
| --- | --- | --- | --- | --- |
| Ubuntu | 20.04、22.04、24.04 | 支持 | 支持 | 每个版本使用对应官方 TGZ |
| Debian | 12 | 支持 | 不开放 | 不复用 Ubuntu 制品 |
| RHEL / Rocky / AlmaLinux | 8、9、10 | 支持 | 支持 | 8.8+、9.3+；`8.0.17` 无 RHEL 10 制品 |
| Oracle Linux | 8、9、10 | 支持 | 不开放 | 仅 RHCK，拒绝 UEK；`8.0.17` 无 RHEL 10 制品 |
| CentOS Stream | 8、9、10 | 支持 | 待真机验证后开放 | 严格拒绝普通 CentOS；`8.0.17` 无 RHEL 10 制品 |
| Amazon Linux | 2023 | 支持 | 支持 | 最低 2023.3 |
| SLES | 15 | 支持 | 不开放 | 最低 SP5 |

Ubuntu 26.04、Debian 11/13、CentOS 7、非 Stream CentOS 8、Fedora、SLES 16、openSUSE、Oracle arm64 与尚未验收的 CentOS Stream arm64 仅为实验目标，不进入 Manifest Source。

## 安装前参数

| 参数 | 默认值 | 约束 |
| --- | --- | --- |
| `software-version` | `8.0.32` | 只允许 `8.0.17`、`8.0.32` |
| `mongodb-port` | `27017` | 1–65535，首次安装检查端口占用 |
| `mongodb-bind-ip` | `127.0.0.1` | IP/主机名列表；非回环绑定会输出安全提示，认证始终启用 |
| `install-dir` | `/usr/local/mongodb` | 安全绝对路径，安装后不可在线迁移 |
| `data-dir` | `/data/mongodb` | 非空且非受管目录拒绝接管 |
| `log-dir` | `/data/mongodb` | 安全绝对路径 |
| `run-user` / `run-group` | `mongod` | 合法系统账号和组 |
| `mongodb-admin-username` | `root` | 1–64 位受限标识符 |
| `mongodb-admin-password` | 无 | 首次安装必填，12–128 字符，不记录、不持久化 |

安装模式、离线包路径、状态目录、Bundle 身份与摘要由 Panel 后端控制。完全离线模式只安装 Bundle 中当前系统的本地 `.deb`/`.rpm`，不会刷新仓库或调用网络下载工具。

安装时会以实际运行账号检查安装、数据和日志目录的完整父目录链。仅在缺少遍历权限且现有 ACL 策略允许时，为该账号增加最小 `--x` 命名用户 ACL；不会扩大 `other`、覆盖已有同名 ACL 或放宽不含 `x` 的 ACL mask。本次新增条目会写入受管状态，安装失败时回滚，卸载时仅在条目仍为组件写入值时删除。在线安装显式依赖 `acl`，离线 Bundle 必须包含目标系统、版本和架构匹配的 ACL 包。

卸载选择保留数据时，组件会在受管状态目录记录原安装归属。同版本、同路径重新安装只允许接管该受管保留数据，并必须使用原管理员用户名和密码验证；任意非空外部数据目录仍会被拒绝。

## 安装后配置和只读信息

| 配置项 | 默认值 | 应用规则 |
| --- | --- | --- |
| `mongodbPort` | `27017` | 重启并验证新端口 |
| `bindIp` | `127.0.0.1` | 地址列表，禁止换行和 YAML 控制字符 |
| `maxIncomingConnections` | `0` | `0` 自动，否则 100–1000000 |
| `wiredTigerCacheSizeGB` | `0` | `0` 自动，否则 1–1024 GB |
| `operationProfilingMode` | `off` | `off`、`slowOp`、`all` |
| `slowOpThresholdMs` | `100` | 1–600000 毫秒 |

配置查询还返回实际版本、端口、绑定地址、安装/数据/日志/配置路径、运行账号和服务名，以及管理员用户名和 `passwordConfigured`。不会返回密码，也不提供关闭认证的配置项。候选配置经 `mongod --config <candidate> --outputConfig` 解析后原子发布，重启或探针失败会恢复旧配置。

## Bundle 构建

`scripts/build-offline-bundle.sh OUTPUT SOFTWARE_VERSION OS_ID OS_VERSION ARCH SOURCE_DIR PACKAGE_DIR` 为一个精确版本和目标平台构建 Bundle。`SOURCE_DIR` 必须含精确 MongoDB/mongosh TGZ、对应 `.sig`、`server-8.0.asc` 和 `mongosh.asc`；`PACKAGE_DIR` 必须含目标系统完整依赖闭包。生成结果包含 Manifest、动作脚本、制品、密钥、`bundle-info` 与 `files.sha256`。

## 证明边界

Manifest 校验、脚本静态检查和本地打包只证明源代码与制品结构。Center 发布/resolve、Panel 固定任务以及真实在线和断网主机验收必须分别保留证据；没有真实主机证据的矩阵行不能标记为已验收。
