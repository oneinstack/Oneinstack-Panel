# OneinStack Panel 构建与发布

本文档描述当前仓库实际支持的构建、质量检查和发布流程。Panel 的生产目标是 Linux，安装方式仍采用 OneinStack 风格的 Shell 安装脚本，不依赖 Docker。

## 环境要求

- Go：以 `go.mod` 的 `go` 和 `toolchain` 声明为准。
- Git、GNU Make、tar、jq。
- `sha256sum`（Linux）或 `shasum`（macOS）。
- 安装器契约测试需要 Bats；CI 固定在 Ubuntu 22.04/24.04 执行。
- 仅在重新构建前端时需要 Node.js 22 和 npm。

后端和前端仓库默认放在同一目录：

```text
workspace/
├── Oneinstack-Panel/
└── Oneinstack-Panel-Web/
```

## 本地质量门禁

```bash
go mod download
make quality
```

`make quality` 会强制执行 Go 格式检查、`go vet`、全部单元测试、Shell 语法检查和高置信凭据扫描。代码与依赖安全检查由 GitHub CodeQL、Dependabot 和仓库安全告警统一完成，避免本地命令在未授权时把私有依赖元数据发送给外部漏洞服务。

需要竞态检测时执行：

```bash
make test-race
```

安装器契约测试：

```bash
make install-test
```

用例在隔离测试根目录中验证首次安装、重复安装、升级保留配置、显式替换配置、普通卸载、彻底清理和危险系统变更禁用。

## 构建后端

默认构建 Linux AMD64：

```bash
make build
./dist/one-linux-amd64 version
```

构建正式支持的两个 Linux 架构：

```bash
make build-all
```

也可以显式传入版本元数据：

```bash
make build \
  VERSION=v1.0.0 \
  COMMIT_HASH="$(git rev-parse --short HEAD)" \
  BUILD_TIME="$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
  WEB_VERSION=1.0.0
```

前端生产文件已压缩嵌入后端二进制，因此部署时不需要单独复制静态目录。

## 更新嵌入前端

前端代码修改后，在前端仓库安装锁定依赖，再由后端统一刷新嵌入包：

```bash
cd ../Oneinstack-Panel-Web
npm ci
cd ../Oneinstack-Panel
make build-ui
```

`make build-ui` 会执行前端类型检查和生产构建，把生成的 `version/app-1.0.0.zip` 同步到 `webui/app.zip`，并验证入口页引用的静态资源均存在。

## 创建并验证发布包

```bash
make release VERSION=v1.0.0
```

发布输出位于 `packages/`：

```text
one-linux-amd64-v1.0.0.tar.gz
one-linux-amd64-v1.0.0.tar.gz.sha256
one-linux-arm64-v1.0.0.tar.gz
one-linux-arm64-v1.0.0.tar.gz.sha256
```

每个包包含二进制、配置模板、统一 Shell 安装入口及两个兼容入口、中英文 README、构建说明和许可证。单独验证下载包：

```bash
./scripts/verify-release.sh packages/one-linux-amd64-v1.0.0.tar.gz
```

发布包中的 `install-ubuntu.sh` 和 `install-cent.sh` 只是兼容入口，全部转交统一的 `install.sh`，不会维护三套不同安装行为。生产初始化使用权限为 `0600` 或更严格的密码文件：

```bash
sudo ./install.sh \
  --admin-user admin \
  --admin-password-file /run/one-admin-password
```

更新和卸载：

```bash
sudo ./install.sh --force
sudo ./install.sh uninstall
sudo ./install.sh uninstall --purge --yes
```

## 审计日志与保留策略

面板会持久化登录、失败请求、所有变更操作和敏感读取。HTTP 审计不保存请求正文或查询字符串，避免密码、Token、私钥和一次性终端票据落库；Web 终端会保存经过敏感参数脱敏的提交指令，PTY 关闭回显时的交互输入和终端输出不会落库。只有数据库中的管理员可以通过“审计日志”页面查询、校验和导出。

默认策略：

```yaml
system:
  auditRetentionDays: 365
  auditCleanupSchedule: "45 4 * * *"
  auditExportMaxRows: 10000
```

对应环境变量为：

- `ONEINSTACK_SYSTEM_AUDIT_RETENTION_DAYS`：30～3650 天。
- `ONEINSTACK_SYSTEM_AUDIT_CLEANUP_SCHEDULE`：五段 Cron 表达式。
- `ONEINSTACK_SYSTEM_AUDIT_EXPORT_MAX_ROWS`：100～100000 行。

每条记录通过实例凭据密钥派生的 HMAC 密钥形成只追加链；自动清理前会校验整条链并写入签名检查点。链异常时追加和清理都会停止。该机制可检测数据库内容篡改、链断裂和常规尾部删除；需要防御整套数据库快照回滚时，应把周期性链头签名额外发送到独立不可变存储。

## Center 托管的签名更新与回滚

安装器同时写入 `one.service` 和独立的 `one-update.service`。后者运行旧版本内存中的更新进程，因此主面板停服、替换磁盘上的 `one` 链接时不会终止更新事务。

生产环境由 Oneinstack-Center 统一管理 Panel 版本、制品、发布渠道、灰度范围和撤回。Panel 使用固定实例标识向 Center 查询版本，Center 返回当前实例被分配到的 Ed25519 签名清单；没有合适版本时返回 `204`，Panel 保持当前版本。

把 Center `GET /v1/keys` 返回的 Ed25519 公钥预置到每台 Panel 的 `config.yaml`：

```yaml
scriptCenter:
  enabled: true
  url: "https://center.example.com"
  trustedKeys:
    center-2026: "BASE64_ED25519_PUBLIC_KEY"

updateCenter:
  enabled: true
  centerUrl: "https://center.example.com"
  channel: "stable"
  requestTimeoutSeconds: 20
  maxPackageBytes: 268435456
  maxExpandedBytes: 536870912
  healthTimeoutSeconds: 60
  backupRetention: 5
  trustedKeys: {}
```

`updateCenter.trustedKeys` 为空时会复用 `scriptCenter.trustedKeys`。也可以单独配置更新公钥。生产地址强制 HTTPS；只有回环地址允许 HTTP 联调。

静态 `manifestUrl` 和 `cmd/update-keygen`、`cmd/update-manifest` 继续保留为离线/兼容模式。当 `centerUrl` 已配置时，日常版本分配由 Center 控制。

更新器会校验清单签名、目标平台、制品大小和 SHA-256，使用数据库副本执行迁移预检，再切换双版本指针。新服务未通过 `/health/ready` 时会恢复旧二进制、数据库、配置和内置脚本。常用命令：

```bash
sudo one update check
sudo one update apply --yes
sudo one update status
sudo one update rollback --yes
```

`rollback` 用于恢复被异常终止的活动事务。存在活动事务时，新更新会被拒绝，避免覆盖唯一恢复点。

标签 Release 工作流要求配置：

- `CENTER_RELEASE_URL`：可从 GitHub Runner 访问的 Center HTTPS 地址。
- `CENTER_RELEASE_TOKEN`：Center 中角色为 `publisher` 的独立 CI Token。
- 仓库变量 `CENTER_RELEASE_ROLLOUT_PERCENTAGE`：首次发布的灰度百分比，未配置时为 `0`。

工作流把 Linux amd64/arm64 两个制品上传到 Center，发布后由 Center 管理百分比和实例白名单。建议保持初始 `0%`，先加入测试实例，依次提升到 `5%`、`20%`、`50%`、`100%`。Center 操作手册见同级 `Oneinstack-Center/docs/PANEL_RELEASES.md`。

兼容模式仍可配置可选的 `UPDATE_SIGNING_PRIVATE_KEY` 和 `UPDATE_SIGNING_KEY_ID`，生成 GitHub Release 静态清单。Center 模式下签名私钥只保存在 Center，不复制到 Panel 或普通构建机。密钥轮换必须先把新公钥加入受管 Panel，再切换 Center 在线密钥。

## GitHub 质量与发布流程

- `CI`：对 `main`、`develop` 和 Pull Request 执行格式、vet、竞态测试、ShellCheck、Secret Scan、双架构编译，以及 Ubuntu 22.04/24.04 安装生命周期矩阵。
- `CodeQL`：对 Go 代码执行代码安全分析，并每周定时复查。
- `Dependabot`：每周检查 Go 模块和 GitHub Actions 依赖并生成升级 PR。
- `Release`：仅在 `v*` 标签或手动触发时运行；生成双架构包、SHA-256、可选兼容签名清单、CycloneDX 1.6 SBOM、Go 模块清单和许可证证据表，标签发布会创建或更新 GitHub Release，并把双架构制品发布到 Center。
- 前端 CI 独立生成 npm CycloneDX SBOM、依赖树、许可证表和校验和制品。
- 推送普通分支不会自动创建 Beta Release，也不会自动修改或推送仓库文件。

发布标签前应先确保前后端改动已经提交，并执行：

```bash
make release VERSION=v1.0.0
git tag v1.0.0
git push origin v1.0.0
```

## 运行时探针

服务启动后可用于 systemd、负载均衡器或监控平台：

```bash
curl -fsS http://127.0.0.1:8089/health/live
curl -fsS http://127.0.0.1:8089/health/ready
```

- `/health/live`：HTTP 进程可响应。
- `/health/ready`：数据库和嵌入前端均可用。
- `/v1/sys/version`：认证后返回后端、前端、提交和 Go 运行时版本。
