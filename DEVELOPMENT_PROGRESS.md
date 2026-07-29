# OneinStack Panel 开发进度

> 最后更新：2026-07-28
> 当前阶段：0.4 上线工程与安全闭环
> 当前状态：开发中，尚不可用于生产环境

## 1. 文档用途

本文档用于持续跟踪项目的实际开发状态，重点回答：

- 现在完成了什么。
- 当前代码是否通过验证。
- 正在做什么。
- 下一步做什么。
- 还有哪些上线阻断问题。

更完整的产品范围与架构见：

- [产品蓝图](./PRODUCT_BLUEPRINT.md)
- [项目审计与路线图](./PROJECT_STATUS_AND_ROADMAP.md)

## 2. 产品目标

OneinStack Panel 的目标是：

> 宝塔级服务器管理能力 + OneinStack Shell 环境安装内核

产品最终覆盖网站、运行环境、数据库、文件、FTP、SSL、Docker、监控、安全、计划任务、备份、日志、终端、插件、API 和多节点等能力。

环境安装、升级、卸载和配置变更使用版本化 Shell 脚本完成。Go 后端负责任务编排、权限、安全校验、日志、状态和 API，不在 HTTP 接口中直接接受任意 Shell 命令。

## 3. 当前基线

| 项目 | 仓库 | 基线提交 | 当前状态 |
| --- | --- | --- | --- |
| 后端 | `Oneinstack-Panel` | `21775e1` | 0.1 安全内核持续开发中 |
| 前端 | `Oneinstack-Panel-Web` | `b871fe06` | 已完成全界面现代化、回收站、同源运行时地址和安全会话 |

当前后端工作区存在尚未提交的开发改动和规划文档。开发完成一个可独立验收的批次后再统一提交。

## 4. 进度摘要

### 4.1 当前阶段

0.3 版本目标是补齐建站、数据库、计划任务等核心业务闭环，并继续关闭生产上线阻断项。

当前 P0 工程任务完成情况：

| 状态 | 数量 |
| --- | ---: |
| 已完成 | 6 |
| 待远程验收 | 1 |
| 待完成 | 0 |
| 合计 | 7 |

已完成：

- `DEV-P0-01` 路由鉴权与安全回归测试。
- `DEV-P0-02` 服务入口、配置加载和优雅关闭。
- `DEV-P0-03` 用户上下文和敏感用户响应。
- `DEV-P0-04` 文件服务安全、容量配额、磁盘预留、回收站和自动清理闭环。
- `DEV-P0-05` 终端、前端运行时地址和浏览器会话安全。
- `DEV-P0-06` 前端嵌入和单二进制静态交付。

待远程验收：

- `DEV-P0-07` CI、健康检查、可复现构建和发布质量门禁。

### 4.2 2026-07-28 软件安装完成后商城状态自动刷新

状态：代码完成并部署测试环境

完成内容：

- 修复安装任务通过轮询进入成功状态时，任务中心已完成但商城卡片仍显示旧状态的问题。
- 将任务终态通知统一收口到任务状态写入入口，SSE、快照轮询和历史同步任一路径发现任务完成都会触发商城数据与服务状态刷新。
- 对同一个终态进行去重，重复的 SSE、轮询或历史响应不会造成重复刷新。
- OpenResty 等组件安装成功后，卡片会自动显示已安装版本和卸载入口，无需手动刷新页面。

验证结果：

- 新增 3 个 Vitest 用例，覆盖轮询成功、历史同步终态和重复终态去重，全部通过。
- 前端类型检查和生产构建通过；后端嵌入资源与 HTTP 服务测试通过。
- 新 `webui/app.zip` SHA-256 为
  `17b1e10cd146bce16c0f20ed46c770db0e4192dddcda25feb5b71af600631d81`。
- 测试服务器已升级到 `v0.1.0-test.15`，`one.service` 为 active，
  `/health/ready` 返回数据库和 WebUI 正常。
- 升级前版本已备份至 `/var/backups/oneinstack-panel/20260728-195459`。

### 4.3 2026-07-28 OpenResty 卸载与孤儿进程修复

状态：代码完成、Center 包已发布并部署测试环境

完成内容：

- 定位 OpenResty 卸载失败根因：development 包使用 `panel: >=0.1.0`，
  按语义版本规则会拒绝 `v0.1.0-test.15` 这样的预发布 Panel。
- 将 development 组件生成器的兼容下限调整为 `>=0.1.0-0`，既支持当前
  测试版，也继续兼容正式版。
- OpenResty 脚本包升级至 `1.0.1`，保留安装、验证、卸载、状态和启停动作，
  并发布到测试 Center 的 development 通道。
- 实机确认修复后的新卸载任务已经成功；用户再次安装失败的实际原因是旧
  OpenResty master/worker 进程未退出，继续占用 TCP 80 端口。
- 已校验并优雅停止这两个属于已卸载 OpenResty 的孤儿进程，80/443 端口
  当前均已释放。
- OpenResty 包继续升级至 `1.0.2`：卸载前读取 PID 文件并核对 `/proc`
  可执行文件归属，依次执行 QUIT 和 TERM，进程无法停止时中止删除，避免
  出现“文件已删除但旧进程仍占用端口”的状态。
- Panel 将“无兼容脚本包”归类为 `PACKAGE_UNAVAILABLE`；只有 Center 连接、
  就绪或下载失败才归类为 `CENTER_UNAVAILABLE`。

验证结果：

- Center 全量 Go 测试、OpenResty Shell 语法检查和包结构校验通过。
- OpenResty `1.0.1` 归档 SHA-256 为
  `52593512fa084f7413867b0c7cf11aa98f36ce47b15a74af66b1e3947e6bf8f0`。
- OpenResty `1.0.2` 归档 SHA-256 为
  `b5ddb87da04c4284904319c82d3cebaba1214038346510ca393a42fe9137e2da`。
- Center 已实际解析
  `OpenResty 1.27.1.2 + v0.1.0-test.16 + Debian 12 + amd64`，
  返回已签名的 `openresty 1.0.2` 和 `scripts/uninstall.sh`。
- 数据库核验显示 `20:06:15` 的卸载任务状态为 `succeeded`；用户粘贴的
  `CENTER_UNAVAILABLE` 来自 `19:56` 的旧失败任务。后续 `20:06:20`
  安装任务因端口冲突失败并已回滚，当前 OpenResty 状态为未安装。
- Panel 软件任务、脚本注册中心和安装器测试通过。
- 测试服务器 Panel 已升级到 `v0.1.0-test.16`，`one.service` 为 active，
  `/health/ready` 返回数据库和 WebUI 正常。
- 升级前版本已备份至 `/var/backups/oneinstack-panel/20260728-200400`。

### 4.4 2026-07-28 安装任务抽屉间距与操作区优化

状态：代码完成并部署测试环境

完成内容：

- 安装任务抽屉与页面顶部、右侧保留外部间距，增加完整边框、圆角和柔和阴影，避免抽屉紧贴浏览器边缘。
- 抽屉标题、进度、步骤和日志区域增加统一的左右内容间距，改善左侧拥挤问题。
- 底部操作区改为带边框和阴影的独立按钮栏，按钮组居中排列，并与抽屉底部保留间隔。
- 移动端使用更紧凑的边距和圆角，保持小屏幕下的可用空间。

验证结果：

- 前端 5 个 Vitest 用例、类型检查和生产构建全部通过。
- 后端嵌入资源与 HTTP 服务测试通过。
- 新 `webui/app.zip` SHA-256 为
  `0fe3d1438aa638da6f8cb7eff03dac654bb2a969eb4bbc5a4020909c7bdde991`。
- 测试服务器已升级到 `v0.1.0-test.17`，`one.service` 为 active，
  `/health/ready` 返回数据库和 WebUI 正常。
- 升级前版本已备份至 `/var/backups/oneinstack-panel/20260728-202350`。

### 4.5 2026-07-28 Nginx 请求版本与实际版本漂移修复

状态：代码完成、测试环境数据修复并部署

完成内容：

- 定位 `PACKAGE_UNAVAILABLE` 根因：Center 管理目录发布了 Nginx
  `1.31.0`，但没有可解析的对应组件包；Panel 随后错误回退到旧版
  OneinStack 脚本，实际安装 `1.26.2`，却按请求值记录为 `1.31.0`。
- Center 管理的软件安装现在必须取得匹配的已校验组件包；Center 包缺失时
  任务直接失败，不再静默调用可能安装不同版本的旧脚本。
- 软件任务抽屉对已结束记录明确显示“历史任务”和创建时间，避免把修复前的
  失败详情误认为刚刚执行的新任务。
- Nginx 内置离线包增加已存在 `1.26.2` 安装的生命周期兼容，支持状态、
  启动、停止、重启、重载、配置和卸载；安装入口仍只允许经过验证的
  `1.28.2`，避免把生命周期兼容误当作新版安装能力。
- 实机读取 `/usr/local/nginx/sbin/nginx -v` 并确认 `nginx.service`
  active 后，备份 Panel SQLite，再将已安装版本记录由错误的 `1.31.0`
  修正为真实的 `1.26.2`。
- Center 的固定 OneinStack 上游映射同步修正为真实版本 `1.26.2`，
  发布 Nginx 组件包 `2.0.1`，并从开发通道目录移除错误的 `1.31.0`
  展示项；历史不可变包保留审计记录，不再向 Panel 商城分发。

验证结果：

- Nginx 包全部 Shell 脚本通过语法检查，`files.sha256` 全量校验通过。
- 新增回归测试，确认 Center 管理的软件在包不可用时不会进入旧脚本；
  Panel `go test ./... -count=1` 全量通过。
- 测试机执行与 Panel 相同的 Nginx `start.sh` 成功；`status.sh` 返回
  `active/running/enabled`、运行版本 `1.26.2` 和可平滑重载。
- 通过 Panel 正式任务管理器再次提交 Nginx 启动任务，任务
  `a00f3587-ac8f-4099-8113-3bde3c7db5ea` 请求版本为 `1.26.2`，
  最终状态为 `succeeded`；用户提供的 `1.31.0` 报错对应
  `20:31:09` 创建的修复前历史任务。
- Center 解析接口以
  `Nginx 1.26.2 + Panel v0.1.0-test.18 + Ubuntu 24.04 + amd64`
  实测返回 Nginx 组件包 `2.0.1`；软件目录同步后 Panel 同时显示
  `stable 1.28.2` 与 `development 1.26.2`。
- Nginx `2.0.1` 发布包 SHA-256 为
  `01abb91af6d977cb49709114129df759f57900287510c47a503d0becd5173a8b`。
- 测试服务器已升级到 `v0.1.0-test.19`，`one.service` 为 active，
  `/health/ready` 返回数据库和 WebUI 正常。
- `v0.1.0-test.19` 二进制 SHA-256 为
  `43486f8539cbed44986b864ef37aee25d1302e11743c25aa80945ad374c1a186`，
  内嵌 WebUI SHA-256 为
  `645bdbcd93c558e04b4d5fd3acc683fdeffbf1286aa02a911b17538e39f6665e`。
- 部署前 Panel 与 Nginx 包备份位于
  `/var/backups/oneinstack-panel/20260728-204151`。
- 数据修复前 SQLite 备份位于
  `/var/backups/oneinstack-panel/nginx-version-repair-20260728-204204/myadmin.db`。
- Center 目录更新前后快照位于
  `/var/backups/oneinstack-center/catalog-20260728-205903`。
- `v0.1.0-test.19` 部署前 Panel 二进制和 SQLite 备份位于
  `/var/backups/oneinstack-panel/20260728-211253`。

### 4.6 2026-07-28 组件配置抽屉与软件卸载入口

状态：代码完成并部署测试环境

完成内容：

- 配置抽屉正文限制最大宽度，标题、正文和底部按钮统一对齐，不再横向铺满。
- 顶部改为紧凑工具栏，返回与关闭使用统一边框按钮，增加“服务配置”和生效方式状态。
- 三行大面积橙色提示压缩为“安全配置模式”说明卡，并保留白名单、配置保护和发布前语法校验信息。
- 配置版本、脚本来源和生效方式组成带分隔线的摘要条；运行参数进入独立卡片，各参数增加边框、内边距和统一帮助文字间距。
- 已安装软件的“升级”和“卸载”拆分为独立按钮；存在新版本时也始终保留红色描边卸载入口。
- 卸载继续使用二次确认、持久化后台任务、实时日志、任务中心和终态自动刷新，默认保留网站、数据库等业务数据。
- 修复切换商城通道后已安装组件无法读取配置的问题：新安装仍严格使用当前通道，已安装组件的状态、配置、服务动作和卸载则复用安装时已验证且支持所需动作的脚本包。

验证结果：

- 前端类型检查、2 个 Vitest 用例和生产构建全部通过；后端嵌入资源与 HTTP 服务测试通过。
- 新 `webui/app.zip` SHA-256 为
  `62a7f9ea6b5f31489fe12fedd8aa072c5d662f8941ebc8893a8c651e462db900`。
- 使用测试服务器真实 Nginx 配置完成视觉验收；新版顶部、摘要条、参数边界、固定底栏和关闭交互正常。
- 软件商城实机确认 Nginx 同时显示“升级”和“卸载”，firewalld 显示独立“卸载”；卸载确认信息和取消流程正常，未执行真实卸载。
- 测试服务器已升级到 `v0.1.0-test.14`，`one.service` 为 active，
  `/health/ready` 返回数据库和 WebUI 正常。
- 升级前版本已备份至 `/var/backups/oneinstack-panel/20260728-194732`。

### 4.7 2026-07-27 Center 管理后台与 Panel 联调部署

状态：测试服务器原生部署和核心链路验收完成

完成内容：

- `192.168.1.6` 已原生部署新版 Center 管理后台和 Panel，不使用 Docker。
- Center 监听 `8189`，Panel 监听 `8089`；firewalld 已永久放行两个 TCP 端口。
- Panel 以 `v0.1.0-test.6` 运行，保留原配置、数据库、软件安装状态和运行日志。
- 修复 `server start` 未启动 Center 软件目录同步任务的问题。
- Panel 已固定信任 Center 密钥 `cf70d6abecf739d1`，商城状态为 `center`，同步 5 个应用和 7 个版本，快照未过期。
- Panel 版本更新来源已切换到 Center；真实 `one update check` 返回 `source=center`、版本兼容且当前无更新。
- Center 与 Panel 的账号密码登录、HttpOnly 会话、首页 HTML、就绪探针和 systemd 自启动均已验证。
- 升级前二进制、配置和 Panel 数据库已保存到 `/var/backups/oneinstack-center` 与 `/var/backups/oneinstack-panel`。

测试环境继续使用 HTTP 和服务器 IP 访问；正式环境仍需要 HTTPS、独立强密码、API Token 轮换和恢复演练。

### 4.8 生产状态

当前版本仍不可直接部署到生产环境。

鉴权、服务生命周期、健康探针、默认 IP/HTTP 与可选 HTTPS 面板访问、可信代理/配置事务、文件安全边界、回收站、容量保护、终端默认关闭、服务端会话吊销、首次登录强制改密、TOTP/恢复码、实例 JWT/凭据密钥、静态页面嵌入、安全安装器、持久化软件任务、四组件服务状态与结构化配置、网站配置补偿事务、SSL/ACME、网站安全删除与整站备份、MySQL 数据库生命周期/备份恢复、计划任务安全模板与主动取消、防火墙安全闭环、签名更新/失败回滚、持久化防篡改审计、监控通知告警、实时运行日志和供应链发布制品已完成第一轮整改。访问配置的 systemd 自动重启/失败恢复、配置历史人工恢复、组件健康告警、高级 Shell 低权限隔离、剩余软件脚本、发布密钥轮换运维、防火墙/更新实机矩阵、真实公共 CA/Nginx/MySQL 矩阵和真实干净系统完整安装首轮验收等 P0/P1 问题仍未清除。

### DEV-P1-16：Center 统一控制 Panel 版本

状态：核心控制链路代码完成，生产 HA 与实机升级矩阵待完成

完成内容：

- Center 新增不可变 Panel 发布仓库、Ed25519 签名防篡改索引和草稿、双架构制品上传、发布、灰度更新、永久撤回 API。
- 版本解析同时校验渠道、新旧版本、最低直升版本、Linux 架构、时间窗、实例白名单/黑名单和确定性百分比。
- Center 使用当前 Ed25519 密钥动态签名 Panel schema-v1 更新清单；Panel 只接受配置中固定的可信公钥。
- 组件包和 Panel 版本索引均使用 Ed25519 防篡改签名，启动时严格验证；下载制品前重新核对文件类型、容量与 SHA-256。
- Panel 新增稳定匿名实例 ID 和 Center resolve 客户端；无分配时保持当前版本，静态清单继续用于兼容/离线模式。
- Center 内置管理控制台、viewer/publisher/admin scoped Token、签名哈希链审计与启动完整性验证。
- GitHub Release 工作流自动向 Center 发布 amd64/arm64 制品，首次灰度默认 0%，由 Center 再逐步放量。
- 前端更新页显示 Center 管控来源、实例 ID 和 Center 分配版本；生产 ZIP 已同步到单二进制资源。

验证结果：

- Center 全量 Go 测试、核心包 race、vet 和 Linux amd64/arm64 静态编译通过；发布生命周期、签名、灰度、撤回、持久化、Token 权限和审计篡改测试通过。
- Panel 全量 Go 测试、更新模块 race 和 vet 通过；Center 请求格式、204 无更新、可信/非可信签名和实例 ID 持久化/并发测试通过。
- 前端类型检查、Vitest、生产构建通过；最新 `webui/app.zip` SHA-256 为 `1bc2ee4b5e4d9bde4bc48e9acf1afab93521e5171bb3a25c49101dddde30714f`。
- 临时 Center 真实 HTTP 联调通过双架构发布、重复发布脚本幂等、签名解析和下载 SHA-256 一致性验证。

待完成：

- Center PostgreSQL/S3 后端、OIDC 和审批流、密钥轮换/撤销体系、限流、指标、追踪和恢复演练。
- Ubuntu 真实 systemd 灰度更新、最低版本拦截、健康失败回滚、中断和断电恢复矩阵。
- 发布策略的自动健康门禁、暂停/回退编排、SBOM 和来源证明强制校验。

### DEV-P1-15：默认 IP/HTTP 与可选 HTTPS 面板访问

状态：首轮闭环已完成，systemd 自动重启与实机代理矩阵待完成

完成内容：

- HTTP 固定为默认可用入口，默认监听 `0.0.0.0:8089`；无需域名，用户直接访问服务器 IP。
- HTTPS 默认关闭，启用后使用独立端口和 TLS 1.2+；不关闭、不重定向 HTTP，也不发送 host-wide HSTS。
- 保存前校验绑定 IP、HTTP/HTTPS 端口、证书私钥匹配、有效期、可信代理 IP/CIDR和候选端口占用。
- 新端口预先放行主机防火墙；配置通过 `0600` 临时文件同步并原子替换，失败回滚候选监听和本次新增规则。
- 可信代理默认空；Cookie、Gin 客户端地址与审计来源只接受已配置代理的转发头。
- 新增安全响应头和管理员设置页面，显示 HTTP/HTTPS 地址、证书 DNS/IP SAN、待重启状态与代理风险提示。
- 前端生产包已重新构建并同步到后端嵌入资源。

待完成：

- 当前配置保存后由管理员重启 Panel；后续增加 systemd 协调、启动失败自动恢复上一配置和无损监听切换。
- 需要在 Ubuntu 22.04/24.04 上验证公网 IP、IPv6、IP SAN 证书、自签名证书及 Nginx/Caddy 代理矩阵。

### DEV-P1-16：四组件服务控制与运行状态

状态：首轮代码闭环已完成，真实 systemd 矩阵待验收

完成内容：

- Center 与 Panel 的严格组件清单新增 `status/start/stop/restart` 和可选 `reload` 动作及独立超时；服务动作必须成组声明。
- Nginx、MySQL 8、PHP-FPM、Redis 均提供固定 systemd 单元的状态、启动、停止和重启脚本；Nginx/PHP-FPM 的 reload 会先执行配置语法检查。
- MySQL/Redis 不声明不可靠的 reload，API 会拒绝并提示使用重启。
- 状态脚本只输出固定键值字段；Panel 限制输出为 64 KiB，并校验组件身份、服务名、systemd 状态、布尔值和运行版本。
- 服务变更复用现有持久化任务、组件互斥、结构化进度、SSE、日志、取消、停服中断和重启核验。
- Script Manager 区分安装生命周期和运行控制，服务动作成功或失败都不会修改软件安装标记、版本或原安装日志。
- 新增管理员状态/单组件查询/服务动作 API；所有接口继续经过鉴权、CSRF、限流和审计。
- 前端软件卡片展示运行状态与实际版本，支持启动、停止、重启、平滑重载、刷新和后台任务抽屉。
- Center 包重新可复现打包并同步到 Panel 离线包；前端重新构建并同步进单二进制资源。

验收结果：

- Panel 全量 Go 测试、重点包 race、vet、生产二进制构建、内置包校验和 Shell 语法检查通过。
- Center 全量 Go 测试、vet、CLI 构建、组件包校验和 Shell 语法检查通过。
- 当前共有 224 个 Go 测试函数、14 个 Bats 契约和 2 个前端 Vitest 用例；前端类型检查、Vitest 和生产构建通过。
- 嵌入前端 `webui/app.zip` SHA-256 为 `547a5fc9d5eb7d478cdf186abeb659590df714b7e71be1545f2233fefea0e878`。

待完成：

- 在 Ubuntu 22.04/24.04 amd64 真实 systemd 环境验证运行/停止/失败状态、取消、超时、Panel 重启和四组件启停恢复。
- 将服务状态接入监控告警；组件配置闭环已进入 `DEV-P1-17`。
- OpenResty、Java、phpMyAdmin 仍需独立服务动作包。

### DEV-P1-17：四组件结构化配置与安全发布

状态：首轮代码闭环已完成，真实 systemd/组件语法矩阵待验收

完成内容：

- Center 与 Panel 严格清单新增配对的 `configGet/configApply` 动作和独立超时；只执行已验证组件包中的固定脚本。
- Nginx、MySQL 8、PHP-FPM、Redis 分别提供独立配置脚本，配置读取只输出白名单字段、SHA-256 版本指纹和 `reload/restart` 生效方式。
- HTTP API 不接受任意配置文本或 Shell；Panel 对字段数量、类型、范围、枚举和 PHP-FPM 跨字段关系做二次校验。
- 读取输出限制为 64 KiB，并拒绝未知、重复、缺失、身份不符或格式异常字段；Redis 密码、监听地址、路径和网站配置不会返回前端。
- 发布前要求当前指纹与预览版本一致，避免并发覆盖；版本冲突、候选语法错误和一般发布失败使用独立任务错误码。
- 每次变更在组件状态目录创建 `0700` 版本快照并保留最近 20 份；候选文件先通过组件原生语法检查，再在原目录原子替换。
- 运行中的 Nginx/PHP-FPM 使用 reload，MySQL/Redis 使用 restart；原本已停止的服务不会被配置任务自动启动。
- 发布、重载或重启失败以及正常取消/终止会自动恢复发布前快照；PHP 双文件发布在第一步替换前即进入回滚状态。
- 配置发布复用持久化软件任务、组件互斥、SSE、增量日志、取消、停服中断、重启核验和现有敏感操作审计链，不修改软件安装记录。
- 前端软件卡片新增配置入口、动态字段表单、当前指纹/脚本来源、字段级差异预览、重载/重启提示和统一任务进度抽屉。
- Center 包已重新可复现打包并同步到 Panel 离线包；前端生产 ZIP 已同步进单二进制资源。

验收结果：

- Panel `go test ./...` 全量通过，当前共有 232 个 Go 测试函数；覆盖严格配置解析、未知/重复字段拒绝、范围与跨字段校验、差异预览、版本冲突、错误分类、任务持久化、安装状态保护和管理员权限。
- Center `go test ./...` 全量通过；四组件源脚本和 Panel 内置脚本均通过 `bash -n`，清单配对/超时与动作权限通过验证。
- 前端 `npm run typecheck`、2 个 Vitest 用例和 `npm run build` 通过。
- 嵌入前端 `webui/app.zip` SHA-256 为 `ab6eb6c6324361fbc76b4271f4a78b0af3047184c38e1ed1602588019e47351f`。

待完成：

- 当前开发机没有真实 systemd 和四组件运行环境；必须在 Ubuntu 22.04/24.04 验证实际语法检查、reload/restart、取消、服务失败与回滚。
- 自动快照已可用于失败回滚，但尚未提供历史快照列表、差异查看和管理员手动恢复页面。
- 下一项目将配置历史恢复与四组件服务健康告警接入同一运维闭环。

软件安装基础架构已新增独立 `Oneinstack-Center` 项目，完成脚本包上传、不可变版本、发布、兼容性解析、下载、撤销、Ed25519 签名和 Panel 端可信校验/缓存/本地回退。Nginx、MySQL 8.0、PHP 和 Redis 已重构为首批组件候选包；Java、OpenResty 和 phpMyAdmin 仍使用兼容路径，四个候选包也尚待真实系统矩阵验收，因此不能把“中心链路可用”等同于“所有软件均可生产安装”。

### DEV-P1-01：OneinStack 脚本服务中心

状态：基础闭环和首批生产组件候选包已完成，真实系统矩阵验收中

完成内容：

- 新建同级仓库 `Oneinstack-Center`，包含服务端、组件打包 CLI、容器部署、systemd 模板、API 与开发计划。
- 独立组件清单区分“脚本包版本”和“目标软件版本”，支持 stable/beta/development、系统版本、架构、Panel 版本、依赖、冲突、参数和动作超时。
- 服务端实现 draft/published/revoked 生命周期，同一组件版本不可覆盖。
- 上传时校验安全归档路径、文件数量、解压容量、可执行动作和 `files.sha256`，发布元数据使用 Ed25519 签名。
- Panel 只用 Center 就绪接口判断在线状态，拒绝未预置信任的公钥、跨源下载、大小/摘要/签名/文件校验异常及危险归档。
- 已验证的远程包进入受限缓存；网络或远程解析失败时回退到随发布包安装的本地组件目录，最后临时兼容旧内置脚本。
- 组件执行顺序统一为 `precheck → install → configure → verify`，失败调用 `rollback`；参数通过环境变量传入，不再写入远程脚本内容。
- Panel 发布包和安装器会创建并安装 `script-registry/cache` 与 `script-registry/bundled`。
- Panel 安装、升级和卸载统一使用 Center 组件动作；同一组件并发操作被拒绝，动作按清单超时强制结束，保留失败回滚。
- 清单参数执行前做必填、端口、整数、布尔和规范化绝对路径校验，并禁止 `PATH`、`BASH_ENV`、`LD_PRELOAD` 等环境注入变量。
- 已完成 Nginx 1.28.2、MySQL 8.0.45、PHP 8.1/8.2/8.3 和 Redis 7.4.8 四套独立组件包。
- 四套包均包含预检、安装、配置、验证、升级、回滚和安全卸载动作，使用 HTTPS 与固定 SHA-256 下载上游制品。
- Panel 发布树已内置四套离线基线包；Center 不可达时按目标系统、架构和软件版本选择本地包。
- 软件目录自动补充 Nginx 1.28.2、Redis 7.4.8、PHP 8.2 和 PHP 8.3，同时保留旧版本记录。
- 卸载成功后才更新数据库状态；失败时保留已安装标记和错误日志，MySQL/Redis/网站数据默认不删除。

验收结果：

- Center 全量 Go 测试通过；Panel 新增 Center 包、执行锁、超时、参数与软件目录测试通过。
- Center 真实 HTTP 烟雾测试完成上传、发布、公钥查询、兼容性解析、下载和 SHA-256 一致性验证。
- Panel 单元测试验证了可信远程包下载/缓存以及不可信签名拒绝。
- 四套生产组件源码通过 Bash 语法检查、Center 清单校验、执行权限校验、文件 SHA-256 覆盖和可复现打包。
- 尚未在 Ubuntu 22.04/24.04 干净虚拟机完成真实安装矩阵，因此当前只能称为“可发布候选包”，不能称为生产验收完成。

下一步：

- 按 Nginx → Redis → PHP → MySQL 顺序完成 Ubuntu 22.04/24.04 amd64 的干净安装、重复安装、升级、故障回滚、重启恢复和卸载数据保留测试。
- 补充结构化操作审计，并继续把服务配置等高权限变更接入统一任务模型。
- 开发 OpenResty、Java 和 phpMyAdmin 独立组件包，再扩展 Debian 与 arm64。
- Center 增加 PostgreSQL/S3、OIDC/RBAC、审批审计、离线根密钥/在线轮换密钥和签名撤销清单。

### DEV-P1-02：安装任务与实时进度

状态：任务生命周期闭环已完成，四组件真实完整安装矩阵等待远程 CI 首次运行

完成内容：

- 已完成[安装进度系统设计](INSTALL_PROGRESS_DESIGN.md)，确定使用持久化任务、结构化事件、SSE 断线续传和增量日志接口。
- 新增安装任务、结构化事件和组件操作锁数据模型，Task Runner 使用双 Worker 执行队列。
- 安装接口返回 HTTP 202 和任务 ID；已实现任务快照、活动列表、SSE、增量日志和取消 API。
- 总体进度由 Panel 编排器按阶段权重计算，独立 Shell 脚本通过 FD 3 上报阶段内部 JSON 进度，不从普通日志文本猜测百分比。
- 任务状态覆盖排队、Center 包解析、预检、安装、配置、验证、取消、回滚、成功、失败和重启中断。
- Script Manager 已支持调用方 Context、进程组终止、结构化进度管道和安装日志精确值脱敏。
- 前端以全局任务 Store 为状态源，提供安装任务抽屉、软件卡片进度、任务入口、刷新恢复和 SSE 轮询降级。
- 软件只有在安装和健康检查成功后才更新为已安装，移除 `localStorage` 提前写入安装结果的逻辑。
- 密码不写入任务表和事件表；跨分块日志脱敏、任务取消、重启中断和 SSE 终态事件已有自动化测试。
- Center 的 Nginx、MySQL 8.0、PHP 和 Redis 生产脚本已加入 FD 3 步骤进度，Panel 内置包及哈希清单已重新生成。
- 任务摘要、事件和日志采用分层保留策略：摘要默认 90 天，事件和日志默认 30 天；清理 Cron 在服务启动时注册并支持安全停止。
- 新增完整日志下载和 1～3650 天窗口的任务统计 API，普通用户只能读取自己的任务，管理员可查看全局统计。
- Panel 启动时会组合软件安装记录、记录版本和实际进程状态写入重启核验结果，但任务仍保持 `interrupted`，不会盲目续跑或误判成功。
- Panel 优雅关闭会取消运行中脚本的 Context、终止进程组并将任务记为 `PANEL_SHUTDOWN` 中断，等待下次启动核验。
- 前端任务中心已加载历史任务和 30 天成功率，任务抽屉支持重启核验提示和完整日志下载。
- 软件卸载接口已迁移到同一持久化任务模型，返回 HTTP 202 和任务 ID，支持组件互斥、SSE 进度、取消、日志和历史统计。
- 卸载脚本成功后才原子更新面板软件状态；失败或取消时保留已安装记录和版本，避免界面误报“已卸载”。
- 前端已提供卸载确认、后台进度、卸载专用步骤条和动态取消提示；软件卡片可直接重新打开活动卸载任务。
- 新增四组件故障契约，覆盖非法版本、过宽路径、端口、密码、网络失败、摘要失败、FD 3 JSON 单调进度和回滚恢复。
- 新增 Ubuntu 22.04/24.04 × Nginx/MySQL/PHP/Redis 的真实安装、配置、验证、卸载工作流，并作为标签发布的前置门禁。
- 前端类型检查、Vitest、生产构建和 Panel 嵌入包校验通过，Go 全量测试通过。
- Center 全量测试、四套脚本 Bash 语法、进度 JSON 和内置包 SHA-256 校验通过。
- Ubuntu 22.04/24.04 amd64 本地容器中的四组件故障契约均通过（各 7/7）；完整 systemd 安装生命周期仍由远程 CI 门禁验收。

下一步：

- 推送开发分支并取得 GitHub Hosted Runner 上 8 组真实组件安装结果；本地容器故障契约不能替代 systemd 完整安装验收。
- 需要跨重启续跑含密码任务时，再增加实例级秘密参数信封加密。

### DEV-P1-03：网站配置安全事务

状态：HTTP 与 HTTPS/ACME 配置事务闭环已完成，真实公共 CA/Nginx 矩阵待执行

完成内容：

- PHP、静态和反向代理站点统一校验域名、监听端口、站点类型、根目录、代理协议、代理目标和 Host 请求头。
- 站点配置写入 Nginx 实际加载的 `conf/conf.d`；PHP 站点与组件脚本统一使用 `/dev/shm/php-cgi.sock`。
- 所有配置先写同目录临时文件并 `fsync`、原子替换，再执行全局 `nginx -t`，通过后才 reload。
- `nginx -t`、reload 或数据库提交失败时恢复旧配置，再次校验并重载旧配置。
- 网站新增、改名和删除与数据库写入组成补偿事务；新建空目录在失败时安全移除。
- 删除站点只移除数据库记录和 Nginx 配置，业务目录与网站文件默认保留，避免误删数据。
- 增加配置注入、目录穿越、代理输入、Nginx 校验失败、发布补偿、站点改名和删除保留数据测试。
- ACME HTTP-01 路由已进入所有站点模板；纯 Go ACME 客户端管理账户密钥、订单、授权、CSR 和证书链，不依赖额外安装 certbot。
- 证书签发/续签使用持久化异步任务，支持进度、日志、取消、重启中断恢复和同网站互斥。
- 证书与私钥部署前校验匹配关系、有效期和全部域名；文件限制在受管目录，私钥权限为 `0600`。
- HTTPS 配置启用 TLS 1.2/1.3，可选强制跳转和 HSTS；`nginx -t` 或 reload 失败会保留旧证书与旧配置。
- 自动续签按配置调度，默认提前 30 天；失败保留当前证书并延迟 24 小时重试，过期/即将过期状态进入网站列表。
- 前端新增 SSL 证书抽屉，可申请、重新签发、立即续签、关闭 SSL、查看实时进度和任务日志。

下一步：

- 在 Ubuntu 22.04/24.04 真实 Nginx 与公共 ACME 测试域名环境验证签发、强制跳转、续签、过期和失败回滚。
- 后续增加 DNS-01 提供商，以支持通配符证书。

### DEV-P1-04：数据库安全与备份恢复

状态：MySQL 核心生命周期已通过 Ubuntu 24.04 实机验收，备份恢复故障矩阵待执行

完成内容：

- 数据库实例密码和库用户密码使用实例级 AES-256-GCM 密钥加密；旧明文记录在启动迁移时事务化加密，密钥缺失或密文损坏时拒绝启动。
- 数据库实例和库列表改用专用响应 DTO，API 与前端均不再返回、显示或复制密码，仅显示是否已配置。
- 数据库路由统一要求数据库中的真实 `is_admin` 角色，不再假定只有用户 ID 1 是管理员。
- 新增连接测试、空密码保留原密码的连接编辑、连接移除、MySQL/Redis 类型边界和 Redis 无副作用 `PING` 检测。
- MySQL 创建数据库、创建同名专用用户和授权已真实执行；账户使用受 MySQL
  监听地址约束的 `用户@%`，创建和密码轮换后均强制执行真实登录验证，失败时
  补偿删除资源或恢复旧密码。
- MySQL 删除要求精确输入数据库名，拒绝删除系统库，并删除数据库及专用用户；移除连接只删除面板记录，不触碰远端数据库。
- Redis 库和键读取使用独立 DB 客户端，去除测试写入；Redis 不支持的库创建/删除会明确拒绝。
- 新增持久化数据库备份/恢复任务、同库互斥锁、取消、进度、增量日志、历史列表和 Panel 重启中断恢复。
- MySQL 备份使用 `mysqldump` 单事务导出并 gzip 压缩，凭据只写入 `0600` 临时 option file，不进入命令行、环境变量、日志或任务表。
- 备份产物记录大小和 SHA-256，下载与恢复前重新校验普通文件、受限路径、大小和摘要。
- 恢复前强制创建并登记安全备份；恢复源必须属于同一数据库。
- 备份前检查磁盘预留空间；备份默认保留 30 天并由 `0 4 * * *` 调度自动清理，均可通过配置或环境变量覆盖。
- 前端新增数据库备份管理抽屉，支持立即备份、进度轮询、取消、下载、精确确认恢复和删除；恢复前安全备份在列表中单独标记。
- MySQL 远程连接页支持测试、同步、编辑和只移除面板连接；Redis 页面修复首次服务器/逻辑库自动选择。

验收结果：

- 后端 `go test ./...`、数据库重点包 `go test -race` 和 `go vet ./...` 通过；当前全仓共有 206 个 Go 测试函数。
- 前端 `vue-tsc --noEmit` 和 Vite 生产构建通过，最新制品已同步到后端嵌入包。
- 自动化测试覆盖凭据加密迁移、响应脱敏、数据库/用户创建删除编排、管理员角色、任务成功/取消/重启中断、恢复前安全备份、摘要篡改拒绝和 option file 权限/转义。

下一步：

- 已在 Ubuntu 24.04 + MySQL 8.0.45 完成安装、自动登记、建库、授权、密码查看、
  随机轮换、真实登录和删除验收；仍需补充 Ubuntu 22.04 以及备份、覆盖恢复、
  取消、磁盘不足和损坏备份矩阵。
- 增加数据库定时备份计划、保留份数与 S3 远端目标；当前已具备手动任务和按天清理基础。
- 执行真实 MySQL 8 集成矩阵，并在真实 systemd 主机验收签名更新与失败自动回滚。

### DEV-P1-05：计划任务可靠执行

状态：可靠执行与安全模板核心闭环已完成，真实 Linux 进程/systemctl 矩阵待验收

完成内容：

- 修复启用和停用调用同一更新逻辑的问题；数据库状态与内存调度器现在同步更新。
- 新增任务名称、命令长度、描述、Cron 表达式数量/格式、1～86400 秒超时和并发策略校验。
- 当前明确只支持 `forbid` 并发策略；同一任务前一次未结束时，新调度记录为 `skipped`，不会重叠执行。
- 新增手动立即执行 API 与前端入口，禁用的任务仍可由管理员显式手动执行。
- 执行使用固定 `/bin/bash --noprofile --norc`、最小环境变量和受控 PATH，不继承 Panel 的 JWT、凭据密钥和其他服务秘密。
- 超时或 Panel 停止时终止整个命令进程组，避免只结束父 Shell 而遗留后台子进程。
- 执行记录增加触发来源、错误码、耗时、退出码和输出截断标记；单次输出上限 1 MiB，按天保留并设置每任务 10000 条安全上限。
- 批量删除先检查全部任务和运行状态，再事务化删除任务及日志，避免部分删除。
- 计划任务全部要求真实管理员角色，并加入敏感操作审计标记；调度器在 Panel 优雅关闭时停止并等待执行收敛。
- 前端新增超时/并发配置、立即执行、真实任务 ID 日志页，以及成功、失败、超时、取消、跳过和输出截断状态展示。
- 修复搜索参数、上次执行时间和周日 Cron 值错误。
- 新增三种不经过 Shell 的白名单模板、结构化参数校验和高级 Shell 显式风险确认。
- 新增按执行 ID 主动取消、重启遗留执行恢复、输出凭据脱敏、状态/时间筛选、CSV 导出和默认 30 天自动清理。
- 任务失败或超时可选进入现有监控通知中心，复用加密通道、告警事件和投递记录。

验收结果：

- 新增启停持久化、调度器同步、环境秘密隔离、手动执行、并发跳过、超时进程终止和输出上限测试。
- `go test -race ./internal/services/cron` 通过；前端类型检查和 Vitest 通过。
- 当前全仓共有 206 个 Go 测试函数。

下一步：

- 增加数据库/网站备份、证书检查和受控 HTTP 探测模板，继续降低高级 Shell 的使用比例。
- 将高级 Shell 和特权模板迁移到独立低权限/白名单执行器，增加资源限额和系统调用隔离。
- 在 Ubuntu 22.04/24.04 验证 systemctl 服务名、进程组 TERM/KILL、重启恢复、磁盘写满和失败通知矩阵。

### DEV-P1-06：网站 SSL/ACME 生命周期

状态：核心闭环已完成，真实公共 CA/Nginx 环境验收中

完成内容：

- 新增证书、证书任务和网站操作锁持久化模型，Panel 重启会将未完成任务标记为中断。
- 使用 `golang.org/x/crypto/acme` 完成 HTTP-01 签发，不在命令行、任务表或 API 暴露账户私钥与网站私钥。
- ACME 账户密钥持久化为 `0600`；网站证书按不可变版本目录保存，私钥只允许受管普通文件。
- 签发前自动发布 `/.well-known/acme-challenge/`，代理站点和强制 HTTPS 站点也能完成验证。
- 证书部署执行 Nginx 全局配置校验和原子 reload；部署或数据库提交失败时回滚到上一份配置。
- 自动续签、过期状态扫描、失败延迟重试、立即续签、任务取消和关闭 SSL 已接入服务生命周期。
- SSL API 全部要求真实管理员角色并进入敏感操作审计。
- 前端提供证书状态、到期时间、自动续签、强制 HTTPS、任务进度和日志管理。
- 安装器创建受控证书与 ACME WebRoot 目录，前端生产包已重新构建并同步到后端。

验收结果：

- `go test ./...`、`go vet ./...` 和证书/网站/路由重点包竞态测试通过。
- 自动化覆盖账户密钥权限、危险 HTTP-01 输入、签发部署、Nginx 失败不落库、取消、关闭 SSL、自动续签、过期状态和 TLS 模板。
- 前端类型检查、Vitest、生产构建及后端嵌入包测试通过。

下一步：

- 使用真实测试域名完成 Let's Encrypt/公共 ACME 签发和续签矩阵。
- 增加 DNS-01 插件与通配符证书。
- 执行真实公共 CA/Nginx 环境矩阵。

### DEV-P1-07：防火墙安全闭环

状态：代码闭环已完成，Linux 实机命令矩阵待执行

完成内容：

- UFW、firewalld 和 iptables 进入统一服务、模型、状态接口和前端页面；旧的空 firewalld 实现和分裂调用路径已移除。
- 方向、协议、策略、IPv4/CIDR、端口及端口范围执行严格白名单校验，所有系统参数以独立 argv 传递，不经 Shell。
- 新增、修改和删除采用系统命令与数据库补偿事务；批量命令、持久化或数据库失败均会逆序撤销。
- firewalld 入站使用永久 rich rule，出站使用永久 direct rule 并 reload；iptables 只有检测到 `netfilter-persistent` 才允许修改。
- 防火墙启用前写入不可编辑/删除的面板 TCP 端口保护规则；入站拒绝规则不能覆盖面板端口。
- 面板端口修改前先创建新端口保护规则；配置读取或写入失败会回滚新规则。
- 关闭防火墙要求固定确认文本；iptables 整体启停明确不支持，不再执行不确定 toggle。
- UFW ICMP 配置使用同目录临时文件、`fsync` 和原子替换；reload 失败恢复原文件并再次加载。
- 防火墙路由全部要求数据库中的管理员角色，新增修改、启停、Ping 和安装入口进入敏感操作审计。
- 未检测到受支持的防火墙时，安全页默认提供 firewalld 安装；安装接口只提交固定 `firewalld@1.0.0` 持久化任务，不接受包名或任意 Shell。
- 新增独立 firewalld 组件包，覆盖 Debian/Ubuntu/RHEL/CentOS/Rocky/AlmaLinux/Fedora/openSUSE/SLES 与 amd64/arm64，使用宿主包管理器安装。
- 安装脚本通过 FD 3 上报实时进度，并在面板端口尚未保护前保持 firewalld 关闭；首次启用仍由安全服务先写入管理端口规则。
- 前端展示后端、持久化、规则数和面板端口保护；危险关闭有精确确认，系统规则不可编辑，开关请求使用明确目标状态。
- 前端未检测到防火墙时展示默认安装卡片，复用软件任务抽屉显示阶段、百分比、实时日志、取消和失败回滚状态。
- 最新前端生产制品已同步到后端 `webui/app.zip`。

验收结果：

- 新增输入攻击、部分失败回滚、更新恢复、保护规则、启用顺序、关闭确认、端口变更回滚、UFW 文件恢复、iptables 持久化和 firewalld 命令测试。
- 后端 `go test ./...`、防火墙/系统/路由重点包 `go test -race` 和 `go vet ./...` 通过；当前全仓共有 206 个 Go 测试函数。
- 前端 TypeScript 检查、Vitest 和 Vite 生产构建通过；后端嵌入包校验通过。

下一步：

- 在 Ubuntu 22.04/24.04 验证 UFW 启停、规则 CRUD、Ping 和管理端口变更。
- 在 Rocky/CentOS 实机验证 firewalld rich/direct rule 和离线端口预置；验证 iptables 持久化恢复。
- 后续支持 IPv6、nftables 以及面板外部规则的发现、导入和对账。
- 转入签名更新与失败自动回滚开发。

### DEV-P1-08：面板签名更新与失败自动回滚

状态：代码闭环已完成，真实 systemd 升级与故障矩阵待执行

完成内容：

- 新增 schema v1 更新清单，使用 Ed25519 对版本、渠道、发布时间、最低直升版本及 Linux amd64/arm64 制品元数据整体签名。
- 清单与制品下载限制为 HTTPS（仅回环测试允许 HTTP），拒绝跨主机重定向、用户信息 URL、超限清单、大小不符和 SHA-256 不符。
- 安全解压限制条目数和全包展开容量，拒绝绝对路径、目录穿越、反斜线、符号链接及非普通文件，只提取面板二进制和内置组件脚本。
- 候选二进制在停服前完成签名、摘要和版本验证；停服后使用配置/SQLite 副本运行候选版本的数据库迁移预检。
- 更新前保存配置、SQLite/WAL/SHM、旧二进制和脚本快照；首次升级自动建立 legacy release，后续通过 `current` 符号链接原子切换版本。
- 新版本只有在 `one.service` 启动且 `/health/ready` 通过后才提交成功；失败会恢复版本指针、数据库、配置和内置脚本并再次检查旧服务。
- 活动事务日志在停服前持久化；检测到中断事务时拒绝新更新，`one update rollback --yes` 可完成恢复。
- 失败 release 带事务所有权标记，仅清理由当前事务创建且已不再激活的目录，回滚后允许安全重试同一版本。
- 新增 `one update check/apply/status/rollback`、独立 `one-update.service`、管理员更新 API、敏感操作审计标识和设置页更新状态/进度。
- 新增 `cmd/update-keygen` 和 `cmd/update-manifest`，发布私钥文件要求 `0600`；GitHub Release 使用私钥 Secret 自动生成并上传签名清单。
- 更新配置支持渠道、超时、包/展开容量、健康超时、备份保留和多可信公钥，启用时验证 HTTPS 与 Base64 Ed25519 公钥。
- 前端更新页完成检查、精确确认、离线重连、终态提示；生产构建已同步到后端 `webui/app.zip`。

验收结果：

- 后端 `go test -count=1 ./...`、`go vet ./...` 和 `go build ./...` 通过，当前共有 206 个 Go 测试函数。
- 更新服务、CLI 和路由重点包 `go test -race` 通过。
- 测试覆盖签名篡改、未知字段、归档穿越/容量、迁移预检、版本切换、健康失败全量恢复、同版本重试、中断事务恢复和密钥文件权限。
- 前端 `npm run typecheck`、`npm test`、`npm run buildNocnd` 及后端嵌入包测试通过。
- Shell 语法、Git diff whitespace 和 Secret Scan 通过。
- 当前开发机未安装 Bats，本轮未重复执行 14 个 Bats 用例；CI 中仍保留安装器契约门禁。

下一步：

- 在 Ubuntu 22.04/24.04 真实 systemd 主机执行 vA → vB 成功升级、数据库迁移失败、启动失败、健康失败、进程中断、断电恢复和同版本重试矩阵。
- 完成离线根密钥、在线发布密钥轮换/撤销演练，并增加 GitHub Artifact Attestation/SLSA 来源证明。
- 持久化审计日志、会话安全、监控通知告警、实时运行日志、计划任务安全模板、网站整站备份、面板访问配置、四组件服务控制和结构化配置发布均已完成首轮闭环；当前入口转为配置历史恢复与组件健康告警。

### DEV-P1-09：持久化防篡改审计日志

状态：核心闭环已完成

完成内容：

- 新增只追加审计事件、签名保留检查点和签名链头模型，普通 ORM 更新/删除会被拒绝。
- 使用实例凭据密钥派生独立 HMAC-SHA-256 子密钥；每条记录绑定单调序号、前置摘要和规范化字段。
- 链校验可发现内容篡改、序号/摘要断裂、检查点篡改和最新记录截断；链异常时拒绝追加与保留清理。
- 审计中间件位于限流、鉴权和 CSRF 之前，记录登录、未认证、权限拒绝、限流失败、所有变更请求和敏感读取。
- 成功的普通 GET/HEAD/OPTIONS 不持久化，避免系统监控轮询造成噪声；所有失败请求仍持久化。
- 不读取或保存请求正文与查询字符串；字符串字段限制长度并移除控制字符，来源 IP 只信任本机反向代理转发。
- 管理员 API 支持分页、详情、统计、完整性校验、时间/用户/结果/方法/状态/敏感级别/关键词筛选和 CSV 导出。
- CSV 导出限制最大行数并防止公式注入；审计 API 使用数据库真实管理员角色。
- 默认保留 365 天、每天 `04:45` 清理；清理前校验全链，删除前缀后写入 HMAC 签名检查点。
- 前端新增管理员可见导航、统计卡、组合筛选、分页、详情抽屉、链校验结果和 CSV 下载。

主要代码：

- `internal/models/audit.go`
- `internal/services/audit/manager.go`
- `internal/services/audit/integrity.go`
- `internal/services/audit/query.go`
- `internal/services/audit/cleaner.go`
- `router/middleware/permission.go`
- `router/handler/audit/audit.go`
- `router/router.go`
- `cmd/main.go`
- `../Oneinstack-Panel-Web/src/views/pages/log/index.vue`

验收结果：

- 后端 `go test ./...` 全量通过，当前全仓共有 206 个 Go 测试函数。
- 审计服务、中间件、审计处理器和路由 `go test -race` 通过；全量 `go vet ./...` 通过。
- 测试覆盖链连续性、记录篡改、尾部删除、保留检查点、链异常拒绝追加、查询通配符、正文/查询不落库、管理员权限与 CSV 公式注入。
- 前端 `npm run typecheck`、`npm test` 和 `npm run build` 通过；生产页面可直接打包。
- 当前开发机未安装 Bats，本轮未重复执行现有 14 个安装器契约；本项目未修改 Shell 安装脚本。

后续增强：

- 把每日链头签名发送到外部不可变存储或安全中心，进一步识别整库快照回滚。
- 增加资源对象 ID 和经脱敏的变更前后摘要，不保存密码、Token、私钥或请求正文。
- 增加 S3 Object Lock、远程 Syslog/SIEM 归档和归档恢复校验。

### DEV-P1-10：会话安全与首次登录保护

状态：核心闭环已完成

完成内容：

- 新增服务端会话模型，保存随机会话 ID、账号、来源 IP、User-Agent、创建/最近活动/到期时间和吊销原因，不保存 JWT。
- JWT 增加会话 ID 和用户安全版本；认证同时校验 JWT 签名/签发者、数据库用户版本和服务端会话状态。
- 旧无状态 JWT 被明确拒绝；退出会吊销当前会话，前端 Cookie 同时失效。
- 提供有效会话列表、单会话吊销和一键吊销其他会话 API，前端安全页可直接管理登录设备。
- 密码变更要求当前密码；密码哈希、安全版本、首次登录标记和全部会话吊销在同一数据库事务内更新。
- 安装初始化和新建账号默认要求首次登录改密；在改密前仅能访问退出、改密和只读安全状态。
- 实现标准 RFC 6238 TOTP 注册、确认、登录校验、停用和恢复码重新生成。
- TOTP Secret 使用实例 AES-256-GCM 凭据密钥加密；恢复码使用用途隔离的 HMAC-SHA-256 摘要，每条只允许使用一次。
- 服务端记录并原子更新最后使用的 TOTP 计数器，拒绝在同一时间窗口重放动态口令。
- 启用 TOTP 或重新生成恢复码会吊销其他会话；停用 TOTP 会增加安全版本并吊销全部会话。
- 登录页完成账号密码与二次认证两步流程；新增首次登录强制改密页和包含二维码、恢复码、会话表的账号安全页面。
- 会话与 MFA 变更路由加入敏感操作审计，既有审计中间件不会读取密码、动态口令、恢复码或 TOTP Secret。

主要代码：

- `internal/models/security.go`
- `internal/services/security/session.go`
- `internal/services/security/totp.go`
- `router/middleware/auth.go`
- `router/handler/security/security.go`
- `router/handler/user/user.go`
- `router/handler/system/system.go`
- `../Oneinstack-Panel-Web/src/views/first-login/index.vue`
- `../Oneinstack-Panel-Web/src/views/pages/setting/components/account-security.vue`
- `../Oneinstack-Panel-Web/src/views/login/index.vue`

验收结果：

- 后端 `go test ./...` 全量通过，当前全仓共有 206 个 Go 测试函数。
- 会话服务、路由与鉴权中间件重点包 `go test -race` 通过；全量 `go vet ./...` 通过。
- 测试覆盖服务端会话即时吊销、旧 JWT 拒绝、退出后重用拒绝、改密后全会话失效、首次登录路由限制、TOTP 加密、重放拒绝和恢复码单次消费。
- 前端 `npm run typecheck`、2 个 Vitest 用例和 `npm run build` 通过。
- 前端生产 ZIP 已原子同步到 `webui/app.zip`，两端 SHA-256 均为 `1ca9c1f4bb0e16ee6ce1a8b169b70ca47a8c03ae0057033b323c41408dfd13a7`。
- 当前开发机未安装 Bats，本轮未重复执行 14 个安装器契约；本项目未修改安装 Shell。

后续增强：

- v1.1 增加多管理员独立角色、组织级强制 MFA 和管理员远程重置 MFA 流程。
- 增加 Passkey/WebAuthn 和可信设备策略。
- 完成实例凭据密钥备份、轮换和灾难恢复演练，避免 TOTP 密钥因实例密钥丢失而不可恢复。

### DEV-P1-11：监控与通知告警闭环

状态：核心闭环已完成，真实 Linux 长时间运行与外部通知矩阵待验收

完成内容：

- 新增 CPU、内存、根分区、1/5/15 分钟负载、网络收发和磁盘读写速率的持久化分钟级采集。
- 指标默认保留 30 天，告警事件与投递记录默认保留 365 天；两类数据按独立配置和 Cron 计划清理。
- 新增百分比、负载和字节速率规则，支持 `gt/gte/lt/lte`、连续采样、恢复滞回、级别和重复提醒冷却。
- 实现 `normal → pending → firing` 状态机，持久化触发、持续提醒和恢复事件；滞回区内仍维持告警并按冷却发送提醒。
- 静默只抑制外部发送，不丢弃状态和事件；规则修改会清除旧状态，避免沿用已经失效的阈值判断。
- 通知通道完整配置使用实例 AES-256-GCM 凭据密钥加密，列表只返回目标主机提示和是否配置签名密钥。
- 通用 Webhook 仅允许公网 HTTPS，禁止凭据 URL、重定向、代理、本机/私网/保留地址，并在连接时重新校验 DNS 解析结果。
- 通知载荷支持可选 HMAC-SHA256 签名；失败记录不保存完整 URL 或查询令牌，管理员可测试发送和显式清除签名密钥。
- 监控 API 统一放在管理员路由组；规则、静默和通道变更全部进入既有敏感操作审计。
- 前端新增监控告警页，提供 24 小时趋势、摘要卡、当前规则状态、事件筛选、静默、通道管理、测试发送与投递记录。

主要代码：

- `internal/models/monitoring.go`
- `internal/services/monitoring/collector.go`
- `internal/services/monitoring/manager.go`
- `internal/services/monitoring/notifier.go`
- `router/handler/monitoring/monitoring.go`
- `router/router.go`
- `cmd/main.go`
- `../Oneinstack-Panel-Web/src/views/pages/monitor/index.vue`

验收结果：

- 后端 `go test -count=1 ./...` 全量通过，当前全仓共有 206 个 Go 测试函数。
- 监控服务与路由 `go test -race`、全量 `go vet ./...` 和 `go build ./...` 通过。
- 测试覆盖连续触发、滞回区提醒、恢复、静默、当前状态、加密落库、私网/保留地址拒绝、独立保留清理和最新历史窗口。
- 前端 `npm run typecheck`、2 个 Vitest 用例和 `npm run buildNocnd` 通过。
- 前端生产 ZIP 已同步到 `webui/app.zip`，两端 SHA-256 均为 `d95da0d37a35e4aa42e5566586c8d4d8ba219993a5bfd5afcf6a39dcbae65614`。
- 当前开发机未安装 Bats，本轮未重复执行 14 个安装器契约；本项目未修改安装 Shell。

后续增强：

- 在 Ubuntu 22.04/24.04 主机完成 7×24 小时采集、数据库增长、重启恢复、网络异常和慢/失败通知端点矩阵。
- 增加邮件、钉钉、飞书和企业微信原生适配以及持久化重试/死信队列。
- 把服务状态、证书到期、网站可用性、磁盘剩余字节和安装/备份任务失败接入同一规则引擎。

### DEV-P1-12：实时日志中心可靠性闭环

状态：核心闭环已完成，真实 Linux 长连接与外部日志源矩阵待验收

完成内容：

- 删除未接入主安装任务的旧 WebSocket 日志原型和废弃增强安装器；软件安装进度继续使用既有持久化任务与 SSE，不再维护两套协议。
- 新增独立运行日志模型和管理器，使用 SQLite、有界异步队列、100 条/100ms 批量写入、优雅停止刷新和丢弃计数。
- 面板标准库日志与 Gin HTTP 访问摘要接入统一来源；HTTP 记录不包含查询字符串，健康探针及日志 API 自身被排除。
- 单条日志写入前清理控制字符、截断到 4096 字节，并统一脱敏 Authorization、Bearer、Cookie、密码、Token 与 Secret。
- 查询 API 支持最新页、`beforeId` 向前翻页、`afterId` 增量读取、级别/来源/关键词/RFC3339 时间范围筛选、统计和来源聚合。
- 实时 API 使用管理员认证的 SSE，先订阅再回填数据库以关闭竞态；慢客户端断开后用 `Last-Event-ID` 从持久化游标恢复。
- 默认保留 30 天并每天 `05:10` 清理，可通过配置或环境变量设置 1～3650 天和 Cron 表达式。
- 前端新增管理员“运行日志”页面，支持实时/重连/暂停状态、历史查询、加载更早、自动滚动、复制、导出和浏览器端 2000 条上限。

主要代码：

- `internal/models/runtime_log.go`
- `internal/services/log/runtime_manager.go`
- `router/handler/log/log_handler.go`
- `router/router.go`
- `cmd/main.go`
- `../Oneinstack-Panel-Web/src/views/runtime-log/index.vue`

验收结果：

- 后端 `go test -count=1 ./...` 全量通过，当前共有 206 个 Go 测试函数。
- 日志服务、日志处理器、路由和鉴权重点包 `go test -race`、全量 `go vet ./...` 及生产二进制构建通过。
- 新增测试覆盖凭据脱敏、字面 LIKE 筛选、稳定游标、优雅刷新、级别识别、慢订阅恢复、保留清理、统计、SSE 续传、管理员权限和 HTTP 查询参数不落日志。
- 前端 `npm run typecheck`、2 个 Vitest 用例和 `npm run build` 通过。
- 前端生产 ZIP 已同步到 `webui/app.zip`，两端 SHA-256 均为 `b5a6d8fd5eed96c02a52c9728101efee093be3f47a6563f53c0292e6fe96d0ec`。
- 当前开发机未安装 Bats，本轮未重复执行 14 个安装器契约；本项目未修改安装 Shell。

后续增强：

- 为 journald、Nginx、PHP、MySQL 和组件脚本增加白名单日志源适配器，禁止前端传入任意服务器路径。
- 在真实 Linux 与 Nginx 反向代理完成长连接、缓冲关闭、突发流量、磁盘写满、重启续传和保留增长矩阵。
- 增加请求追踪 ID、脱敏诊断包、压缩归档及 Loki/OpenSearch 等远端日志出口。

### DEV-P1-13：计划任务安全模板与主动取消

状态：核心闭环已完成，低权限隔离与真实 Linux 矩阵待验收

完成内容：

- 在兼容原有高级 Shell 任务的基础上新增安全模板任务类型；创建或修改高级 Shell 时必须由管理员显式确认风险。
- 新增磁盘使用报告、服务状态检查、网站目录容量三个模板；模板固定可执行文件并对白名单参数做结构化校验，不把用户输入拼接到 Shell。
- 服务状态仅允许受控服务名；网站目录仅允许配置的网站根目录下的单层安全名称，拒绝绝对路径、穿越和额外参数。
- 运行实例在启动前登记独立执行 ID，前端可精确取消；取消和超时都终止整个进程组，关闭启动竞态。
- Panel 启动时将遗留 `running` 记录恢复为 `canceled/PANEL_RESTARTED`，停服使用 `SERVICE_STOPPING`，用户取消使用 `USER_CANCELED`。
- 执行输出继续限制为 1 MiB，并在入库前移除控制字符、密码、Cookie、Bearer、Token 和 Secret。
- 日志接口新增状态和时间筛选、最多 500 条 CSV 导出、默认 30 天自动清理与管理员手动清理；清理不会删除运行中记录。
- CSV 对以 `= + - @` 开头的单元格做公式注入防护，导出使用 UTF-8 BOM 方便常用表格软件打开。
- 失败和超时可按任务配置进入现有监控通知中心；仅发送任务/执行元数据，不发送可能包含业务内容的执行输出。
- 前端完成元数据驱动的模板表单、结构化参数、高级 Shell 风险确认、失败通知开关、真实运行状态、主动取消、日志筛选/导出/清理。

主要代码：

- `internal/models/cron.go`
- `internal/services/cron/templates.go`
- `internal/services/cron/crons.go`
- `internal/services/monitoring/manager.go`
- `router/handler/cron/cron.go`
- `router/input/cron.go`
- `../Oneinstack-Panel-Web/src/views/pages/task/components/add-task.vue`
- `../Oneinstack-Panel-Web/src/views/pages/task/components/scheduled.vue`
- `../Oneinstack-Panel-Web/src/views/pages/task/log.vue`

验收结果：

- 后端 `go test -count=1 ./...` 全量通过，当前共有 206 个 Go 测试函数。
- 计划任务、监控、处理器和中间件重点包 `go test -race`、全量 `go vet ./...` 和生产二进制构建通过。
- 新增测试覆盖模板无 Shell 执行、未知/注入参数拒绝、结构化参数持久化、主动取消、重复取消冲突、重启恢复、保留清理、凭据脱敏、失败通知和 CSV 公式注入防护。
- 前端 `npm run typecheck`、2 个 Vitest 用例和 `npm run build` 通过。
- 前端生产 ZIP 已同步到 `webui/app.zip`，两端 SHA-256 均为 `93e395a19471dd3a18d4dc6fa180861d172b8efd43e496abd8bd33587007cad3`。
- 当前开发机未安装 Bats，本轮未重复执行 14 个安装器契约；本项目未修改安装 Shell。

后续增强：

- 增加数据库/网站备份、证书检查和受控 HTTP 探测模板，并为模板声明资源、权限和预估容量。
- 将高级 Shell 与特权模板迁移到独立执行服务，使用专用用户、资源限额和更强进程隔离。
- 在 Ubuntu 22.04/24.04 实机完成 systemctl 服务差异、长进程取消、重启恢复、磁盘写满和通知端点故障矩阵。

### DEV-P1-14：网站删除安全与整站备份

状态：核心闭环已完成，真实 Nginx/MySQL 与断电故障矩阵待验收

完成内容：

- 网站删除由同步接口改为持久化异步任务，必须精确确认网站名，并且只有删除前整站快照成功且整包 SHA-256 校验通过后才执行删除。
- 默认删除 Nginx/证书状态但保留网站目录；勾选删除文件时，后端再次验证网站根目录严格位于配置 Web 根下，拒绝根目录、越界路径和符号链接路径组件。
- 网站与证书目录先原子移动到根目录内的私有暂存区；数据库事务或 `nginx -t`/reload 失败会回移目录并恢复数据库记录。
- 证书目录删除错误不再被忽略；路径与目录类型不符合预期时拒绝操作，不执行模糊路径或跟随符号链接的递归删除。
- 新增 `WebsiteTask`、`WebsiteBackup` 和 `WebsiteOperationLock`，覆盖备份、恢复、安全删除、进度、日志、取消、资源互斥和 Panel 重启中断恢复。
- 整站包包含网站元数据、Nginx 配置快照、网站文件与安全相对符号链接，以及一个可选 MySQL 数据库转储；证书私钥因归档未加密而明确排除。
- 每个普通文件、数据库转储和整包均记录 SHA-256；恢复前重新校验整包、清单、逐文件元数据与摘要，并拒绝重复、未知和未声明条目。
- 打包/解包完全使用 Go 标准库，不通过 Shell；拒绝绝对路径、目录穿越、危险符号链接、特殊文件、条目父级符号链接和归档路径覆盖。
- 默认限制 20 GiB 展开容量和 20 万条目，执行前检查磁盘最低预留；数据库操作复用现有 MySQL option file，凭据不进入参数、任务、日志或归档。
- 恢复现有站点前强制创建安全快照；文件使用同文件系统暂存目录原子切换，Nginx 配置通过当前安全渲染器重新生成，不直接安装归档中的原始文本。
- 默认保留 30 天、每天 `04:15` 清理；活动恢复引用的备份受保护，下载、恢复、删除、取消和安全删除均为管理员敏感审计操作。
- 前端新增站点级和全局备份管理抽屉，支持关联 MySQL、进度、取消、日志、下载、恢复、删除，以及已删除网站从全局快照重建。

主要代码：

- `internal/models/website_backup.go`
- `internal/services/website/website.go`
- `internal/services/websitetask/archive.go`
- `internal/services/websitetask/manager.go`
- `internal/services/websitetask/repository.go`
- `internal/services/websitetask/cleaner.go`
- `router/handler/website/backup.go`
- `../Oneinstack-Panel-Web/src/views/pages/website/components/WebsiteBackupDrawer.vue`
- `../Oneinstack-Panel-Web/src/views/pages/website/index.vue`

验收结果：

- 后端 `go test -count=1 ./...` 全量通过，当前共有 206 个 Go 测试函数。
- 网站、网站任务、网站处理器和审计中间件重点包 `go test -race`、全量 `go vet ./...` 和生产二进制构建通过。
- 新增测试覆盖整站备份—恢复—删除—再恢复、关联数据库、主动取消、重启中断、目录穿越/危险符号链接拒绝、篡改拒绝、清理保护活动恢复、Nginx 失败目录回移和精确删除确认。
- 前端 `npm run typecheck`、2 个 Vitest 用例和 `npm run build` 通过。
- 前端生产 ZIP 已同步到 `webui/app.zip`，两端 SHA-256 均为 `81f9731019c730a6e10c4c993c8f7fe9cb6f0757998772593ddd501e4c2b2e8e`。
- 当前开发机未安装 Bats，本轮未重复执行 14 个安装器契约；本项目未修改安装 Shell。

后续增强：

- 当前一个整站包最多关联一个 MySQL 库；多库依赖图、外部对象存储和数据库失败自动回滚进入后续版本。
- 证书私钥需在备份加密与密钥恢复流程完成后再纳入；当前恢复后必须重新签发证书。
- 在 Ubuntu 22.04/24.04 + Nginx + MySQL 8 完成大站点、长任务取消、磁盘写满、符号链接、反向代理和断电故障注入矩阵。

## 5. 已完成开发

### DEV-P0-01：路由鉴权

状态：已完成

完成内容：

- 将 `/v1/login` 和 `/v1/sys/getbaseinfo` 定义为显式公开白名单。
- 其余 `/v1` API 统一进入受保护路由组。
- 所有管理 API 默认应用：
  - API 限流。
  - JWT 鉴权。
  - 审计中间件。
- 数据库、文件、软件、日志、网站、防火墙、SSH 和计划任务不再各自重复挂载鉴权。
- SSH 路由不能再只依赖 URL 查询参数中的 Token 绕过统一鉴权。

主要代码：

- `router/router.go`
- `router/middleware/auth.go`
- `router/middleware/permission.go`
- `router/router_test.go`

验收结果：

- 未携带 Token 访问任意管理路由均返回 `401`。
- 合法 Bearer Token 可以访问受保护接口。
- 新增管理路由如果遗漏统一鉴权，路由遍历测试会失败。

### DEV-P0-02：服务入口和配置

状态：已完成

完成内容：

- 删除 `app` 包导入时自动创建 `/usr/local/one` 和初始化数据库的副作用。
- 新增显式 `app.Initialize()`。
- 配置文件在应用启动时加载。
- 支持：
  - `ONEINSTACK_BASE_PATH`
  - `ONEINSTACK_CONFIG_PATH`
  - `ONEINSTACK_SYSTEM_PORT`
- 默认配置文件以 `0600` 权限创建。
- `one server start` 改为前台阻塞模式。
- 服务监听配置中的端口，不再固定为 `8089`。
- 支持 SIGINT/SIGTERM 和最长 10 秒的优雅关闭。
- HTTP Server 增加 Header、Idle 和最大 Header 大小约束。
- PID 文件移动到面板数据目录。
- Cron 调度器在数据库初始化后显式启动。
- systemd 不再通过 `ExecStop` 启动第二个面板进程。

主要代码：

- `app/app.go`
- `app/db.go`
- `app/viper.go`
- `cmd/main.go`
- `server/http.go`
- `router/handler/cron/cron.go`
- `install.sh`
- `install-ubuntu.sh`
- `install-cent.sh`

验收结果：

- 配置端口范围为 `1–65535`。
- 服务收到取消信号后完成优雅关闭。
- 普通测试导入后端包时不会写入 `/usr/local`。
- 初始化可以在独立数据目录中创建配置和 SQLite 数据库。

### DEV-P0-03：用户上下文和敏感响应

状态：已完成

完成内容：

- 统一认证上下文键：
  - `username`
  - `userId`
  - `tokenClaims`
- 提供类型安全的当前用户 ID 读取方法。
- 修复用户名修改和密码重置读取错误上下文键的问题。
- 用户修改接口改用专用请求结构，避免直接绑定数据库模型。
- 密码重置只使用当前登录用户 ID，不接受客户端指定用户 ID。
- 系统设置接口改用专用用户响应 DTO。
- `models.User.Password` 禁止 JSON 序列化。

主要代码：

- `router/middleware/auth.go`
- `router/handler/system/system.go`
- `router/input/user.go`
- `router/output/sys.go`
- `internal/models/user.go`
- `internal/services/system/sys_service.go`

验收结果：

- 登录用户可以修改自己的用户名。
- 登录用户可以设置符合强度要求的新密码。
- 系统设置接口不包含密码字段和密码哈希。

### DEV-P0-04：文件服务安全与回收站

状态：已完成

完成内容：

- 文件 API 不再接受服务器物理路径；前端继续使用 `/`、`/sites/...` 等虚拟路径。
- 虚拟 `/` 由后端映射到 `system.defaultPath` 授权根目录。
- 基于 Go `os.Root` 建立抗路径穿越、抗越界符号链接和抗符号链接切换竞态的文件访问层。
- 拒绝 `..`、反斜杠、NUL、控制字符和非法文件名。
- 列表、创建、上传、下载、删除、属性修改、内容读取、目录树、保存和 URL 下载全部迁移到安全访问层。
- 删除和属性修改禁止作用于授权根目录。
- 上传使用独占创建，默认不覆盖同名文件，单文件限制为 `100 MiB`，失败时清理残留文件。
- 在线读取和编辑仅允许普通文件，单文件限制为 `10 MiB`，并支持保存空文件。
- API 响应只返回虚拟路径，不泄漏服务器授权根目录的物理路径。
- 原删除接口改为默认移动到回收站，不再立即永久删除。
- 回收站数据和元数据保存在授权根目录内的内部目录，普通文件 API 无法解析、列出或递归修改该目录。
- Linux 使用 `renameat2(RENAME_NOREPLACE)`，macOS 使用 `renameatx_np(RENAME_EXCL)`，删除和恢复均采用同文件系统原子移动且禁止覆盖。
- 回收站支持列表、恢复、彻底删除、确认清空和按时间清理服务。
- 恢复时如果原位置已有新文件会返回冲突，不覆盖现有数据；缺失的原父目录会安全重建。
- 目录和符号链接以目录项本身移动，回收站操作不会跟随链接访问授权根目录之外。
- URL 下载：
  - 仅允许 HTTP/HTTPS。
  - 禁止 URL 用户凭据。
  - DNS 解析和建立连接时同时验证地址。
  - 禁止本机、私网、链路本地、组播、保留地址、云元数据和测试网段。
  - 最多跟随 3 次重定向，并重新验证每个目标。
  - 总超时 5 分钟、连接超时 10 秒、响应头超时 30 秒。
- 最大响应 `100 MiB`，拒绝非 2xx 状态，失败时删除部分文件。
- 文件上传、在线编辑、URL 下载统一读取配置化大小限制。
- 支持授权根目录容量配额；`0` 表示不限制配额。
- 写入前检查文件系统可用空间并保留最低空闲空间，默认预留 `1 GiB`。
- 并发上传和保存通过进程内容量预留锁计算未完成写入，避免多个请求同时绕过配额。
- 容量统计包含回收站文件，并返回已用、配额、磁盘总量、可用、已预留、可写和目录项数量。
- 回收站保留期和清理 Cron 表达式进入配置，默认保留 `30` 天、每天 `03:00` 清理。
- 后台清理器在服务启动时显式启动，并在服务退出时等待停止。
- 前端文件页显示容量状态并提供完整回收站页面，支持列表、刷新、恢复、彻底删除和确认清空。

主要代码：

- `internal/services/filemanager/manager.go`
- `internal/services/filemanager/download.go`
- `internal/services/filemanager/trash.go`
- `internal/services/filemanager/capacity.go`
- `internal/services/filemanager/cleaner.go`
- `internal/services/filemanager/rename_linux.go`
- `internal/services/filemanager/rename_darwin.go`
- `router/handler/ftp/ftp.go`
- `router/router.go`
- `internal/services/filemanager/manager_test.go`
- `internal/services/filemanager/download_test.go`
- `internal/services/filemanager/trash_test.go`
- `internal/services/filemanager/capacity_test.go`
- `internal/services/filemanager/cleaner_test.go`
- `router/handler/ftp/ftp_test.go`
- `../Oneinstack-Panel-Web/src/views/pages/file/components/trash-list.vue`

验收结果：

- `../`、物理绝对路径和越界符号链接不能逃逸授权根目录。
- 授权根目录不能被删除。
- 文件列表和内容接口不返回物理根目录。
- 删除后的文件可以列出并恢复，永久删除后不能再恢复。
- 原路径冲突时恢复不会覆盖新文件。
- 回收站内部目录不可通过普通文件 API 访问。
- URL 下载会拒绝 localhost、环回、私网、链路本地和云元数据地址。
- 超大响应、异常状态和超时不会留下部分文件。
- 配额不足和磁盘预留空间不足返回 `507 Insufficient Storage`，原文件不被修改。
- 并发容量预留不能超过配额，释放后容量可再次使用。
- 过期回收站条目可以手动或按配置计划自动清理。
- 前端生产构建包含可直接操作的回收站页面。

后续增强：

- 大文件分片上传、断点续传和哈希校验。
- 按站点/用户的独立配额与文件数量限制。
- 文件对象 ID、批量条目数和经脱敏的变更差异摘要。

### DEV-P0-05：终端和前端安全

状态：已完成首轮安全闭环

完成内容：

- Web 终端默认关闭，需要显式设置 `terminalEnabled: true`。
- 启用终端后，管理员必须再次输入当前密码进行二次认证。
- 二次认证成功后只签发 30 秒有效、绑定用户和来源 IP、只能使用一次的随机票据。
- 前端 API、上传、WebSocket 全部使用当前页面同源地址，不再包含固定 IP、演示域名和明文外部 WS。
- WebSocket 严格校验 Origin，限制输入大小、终端尺寸、空闲时间和最大会话时间。
- 终端使用固定参数启动 shell，不接受客户端命令参数；连接结束会终止并回收子进程。
- 登录 JWT 不再返回前端或进入 LocalStorage/普通 Cookie。
- 浏览器登录改用 `HttpOnly`、`SameSite=Strict` 会话 Cookie；退出会清除服务端 Cookie。
- Cookie 认证的写请求强制进行同源 Origin 校验；Bearer Token 客户端保持兼容。
- 每台机器首次启动生成独立 JWT 密钥，以 `0600` 写入配置；已知旧示例密钥会自动轮换。
- JWT 组件不再自动写当前工作目录，也不再向控制台打印密钥。

主要代码：

- `internal/services/ssh/ssh.go`
- `internal/services/ssh/ticket.go`
- `router/handler/ssh/ssh.go`
- `router/middleware/auth.go`
- `router/session/cookie.go`
- `utils/jwt.go`
- `../Oneinstack-Panel-Web/src/views/pages/terminal/components/terminal.vue`
- `../Oneinstack-Panel-Web/src/utils/HttpConfig.ts`
- `../Oneinstack-Panel-Web/src/sstore/sconfig.ts`

验收结果：

- URL 中的旧 JWT 不能访问终端，一次性票据不能复用或跨 IP 使用。
- 非同源 WebSocket 和跨站 Cookie 写请求被拒绝。
- 登录响应中没有 Token，Cookie 不能被 JavaScript 读取。
- 终端关闭时无法创建票据或打开会话。
- 前端 TypeScript 检查和生产构建通过。

## 6. 自动化测试进度

当前共有 232 个 Go 测试函数、14 个 Bats 用例和 2 个前端 Vitest 单元用例。

| 测试范围 | 已覆盖 |
| --- | --- |
| 路由安全 | 全部管理路由无 Token 返回 401 |
| 公开路由 | 登录和基础信息不要求认证 |
| SSH Token | 查询参数 Token 不能绕过统一鉴权 |
| Bearer Token | 合法 Token 可访问受保护接口 |
| 用户设置 | 用户名修改、密码重置 |
| 敏感响应 | 系统信息不返回密码 |
| 配置 | 默认配置、0600 权限、环境变量覆盖 |
| 初始化 | 指定数据目录、配置和 SQLite 创建 |
| 端口 | 合法端口和非法端口 |
| HTTP 生命周期 | 启动、响应请求、取消后优雅关闭 |
| 健康与版本 | 存活、数据库/前端就绪检查和构建元数据 |
| 文件路径 | 虚拟路径、父目录穿越、物理绝对路径和符号链接逃逸 |
| 文件 API | 列表、内容、空文件保存、根目录删除保护和上传目标 |
| URL 下载 | 协议/地址校验、SSRF、状态码、大小、超时和残留清理 |
| 文件回收站 | 删除、列表、恢复、冲突保护、永久删除、清空、过期清理和内部目录隔离 |
| 文件容量 | 配额统计、磁盘预留、并发预留、回收站计量和 507 API |
| 回收站调度 | 保留期清理、Cron 配置和启动/停止生命周期 |
| 终端安全 | 票据绑定、单次消费、过期、同源 Origin |
| 浏览器会话 | HttpOnly Cookie、登录不返回 Token、退出清理和 CSRF 拒绝 |
| 服务端会话安全 | JWT 会话绑定、即时吊销、设备列表、改密全失效、首次登录保护、TOTP/恢复码和重放拒绝 |
| 持久化审计 | HMAC 链、链头截断检测、保留检查点、脱敏采集、管理员查询/导出、CSV 注入防护和清理调度 |
| 实时运行日志 | 写入脱敏、稳定游标、停服刷新、慢消费者恢复、SSE 续传、管理员权限、保留清理和 HTTP 查询参数不落日志 |
| JWT 密钥 | 首次生成、稳定重载、旧示例密钥轮换和配置权限 |
| 前端静态交付 | 嵌入首页、SPA fallback、静态资源 404、API JSON 404 和路径穿越 |
| 软件任务 | 安装/升级/卸载/启停/重启/重载进度、取消、关闭中断、重启核验、组件互斥、日志下载权限、统计和分层清理 |
| 组件脚本 | 参数拒绝、FD 3 JSON 进度、网络/摘要失败、回滚恢复、严格状态探测、服务动作和运行控制不改安装记录 |
| 组件配置 | 白名单解析、范围/跨字段校验、差异预览、版本冲突、错误分类、配置任务、安装状态保护、清单配对和管理员权限 |
| 网站配置 | 输入注入、目录边界、原子发布、`nginx -t`、reload/数据库失败补偿和数据保留 |

最近一次验证结果：

```text
go vet ./...       通过
go test ./...      通过
日志/处理器/路由重点包 race
                    通过
Linux amd64/arm64 静态构建
                    通过
双架构发布包 SHA-256 和内容校验
                    通过
bash -n install.sh install-ubuntu.sh install-cent.sh
                    通过
ShellCheck error gate
                    通过
Bats 安装器契约    通过（7/7）
四组件故障契约     Ubuntu 22.04/24.04 本地 amd64 容器均通过（各 7/7）
Secret Scan        前后端通过
Go CycloneDX SBOM  通过（1.6）
```

前端本轮验证：

```text
npm run typecheck   通过
npm test            通过（2 个 Vitest 用例）
npm run build       通过（Vite production build）
npm CycloneDX SBOM  通过（1.5）
```

前端已有最小工具单元测试，仍缺组件测试和 E2E 测试。

## 7. 当前模块状态

| 模块 | 当前状态 | 下一步 |
| --- | --- | --- |
| 登录和鉴权 | HttpOnly Cookie、服务端会话吊销、CSRF、首次登录强制改密、TOTP/恢复码和实例密钥已完成 | Passkey/WebAuthn、多管理员设备策略和凭据密钥轮换演练 |
| 服务启动 | 默认 IP/HTTP、可选 HTTPS、双监听预绑定和健康探针已完成 | systemd 探针联动、配置自动重启与失败恢复 |
| 用户设置 | 当前密码确认、改密全会话失效、首次登录保护、TOTP 与设备会话管理已完成 | 多管理员角色、强制 MFA 策略和管理员重置 MFA |
| 系统监控与告警 | 分钟级历史、阈值状态机、静默/恢复、加密 Webhook、投递记录和前端告警中心已完成 | Linux 长时间运行、原生通知通道、服务/证书/网站可用性规则 |
| 网站管理 | HTTP/HTTPS、ACME、原子发布、安全删除和整站备份恢复已完成 | 真实公共 CA/Nginx/MySQL 矩阵、重写、访问策略和站点日志 |
| 文件管理 | 安全边界、容量保护、回收站和路由级审计已完成 | 分片上传、站点级配额和对象级差异审计 |
| 数据库 | MySQL 核心闭环已完成 | 真实 MySQL 8 矩阵、定时备份和远端存储 |
| 防火墙 | UFW/firewalld/iptables 代码闭环已完成 | Linux 实机矩阵、IPv6/nftables、外部规则对账 |
| 计划任务 | 安全模板与主动取消闭环已完成 | 更多模板、低权限执行器和 Linux 实机矩阵 |
| 软件管理 | Center/内置包、安装/升级/卸载/启停/重启/重载/结构化配置持久化任务、状态/版本探测、配置差异/快照/自动回滚、进度、取消、恢复和统计已完成 | 真实 systemd/组件语法矩阵、配置历史手动恢复、服务健康告警和更多组件 |
| Web 终端 | 默认关闭；二次认证和一次性票据已完成 | 特权隔离、命令级审计和并发限额 |
| 审计日志 | HMAC 防篡改链、查询/导出/保留和前端页面已完成 | 外部不可变锚点、对象级差异摘要和远端归档 |
| 实时运行日志 | SQLite + SSE 核心闭环已完成 | journald/Nginx/PHP/MySQL 适配、诊断包、追踪 ID 和 Linux 长连接/流量矩阵 |
| SSL | HTTP-01 签发、部署、续签、回滚和状态告警闭环已完成 | DNS-01、通配符证书和真实公共 CA 矩阵 |
| 面板访问 | 默认 IP/HTTP、独立可选 HTTPS、原子配置、可信代理、安全头和前端设置已完成 | systemd 自动应用/失败恢复、真实 IP SAN/反向代理/IPv6 矩阵 |
| 更新与回滚 | 签名、迁移预检、双版本切换、健康确认和失败/中断恢复代码闭环 | Ubuntu 真实 systemd 故障矩阵、密钥轮换/撤销演练、来源证明 |
| Docker | 本地测试部署已完成 | 继续补充镜像发布、升级与多架构流水线 |
| 备份迁移 | 数据库本地备份和网站文件/配置/可选 MySQL 整站备份已完成 | 面板配置、加密证书备份、多数据库和 S3 |
| 插件系统 | 未开始 | Manifest、权限、签名和事件 |

## 8. 下一阶段任务

### DEV-P0-04：文件服务安全

状态：已完成。

已完成：

- 所有文件操作限定在授权根目录。
- 阻止物理绝对路径、`..`、软链接和符号链接竞态逃逸。
- 授权根目录之外的面板目录、系统伪文件系统、设备文件和密钥目录不可访问。
- 上传和在线编辑限制单文件大小。
- 删除默认进入可恢复的回收站。
- 支持回收站列表、恢复、彻底删除、确认清空和按时间清理服务。
- 恢复冲突不会覆盖原位置的新文件。
- URL 下载限制协议、目标、重定向、大小和超时。
- 阻止访问本机、内网、云元数据和 Unix Socket。
- 已增加正常读写、路径逃逸、软链接、根目录保护、SSRF、超大响应和超时测试。
- 已增加容量配额、磁盘预留空间、并发写入预留和容量状态 API。
- 已将回收站保留天数及清理时间接入后台调度器。
- 已完成前端回收站管理和容量状态界面。

### DEV-P0-05：终端和前端安全

状态：已完成首轮安全闭环。

- 生产配置默认关闭 Web 终端。
- 已删除固定 WebSocket/API 地址、URL JWT 和 Token 控制台输出。
- 已完成同源运行时地址、二次认证、一次性终端票据和 WebSocket Origin 校验。
- 已迁移到 HttpOnly SameSite 会话 Cookie，并增加 CSRF 防护。
- 已生成实例独立 JWT 密钥并自动轮换旧示例密钥。

### DEV-P0-06：前端嵌入和发布

状态：已完成首轮单二进制交付。

- 已删除未接通的 base64 UI 生成器和约 `7.5 MiB` 的旧源码常量。
- 已删除可能为空的 `GetFile`/`FileExists` 全局函数指针。
- 前端生产 ZIP 通过 `go:embed` 进入后端，单个 Linux 二进制同时提供 API 和 SPA。
- `scripts/sync-webui.sh` 会校验 ZIP 和 `index.html` 后原子同步构建产物。
- `make build-ui` 已接到真实前端仓库构建和同步流程，不再创建演示首页。
- 未知 `/v1/*` 返回 JSON 404，不会回退到 SPA。
- 只有无扩展名页面路由执行 SPA fallback；缺失静态资源保持 404。
- HTML 禁止缓存，静态资源使用长期缓存，并发送 `nosniff` 和同源 Referrer Policy。
- 版本命令已预留后端版本、构建时间、提交和前端版本注入字段。

### DEV-P0-07：CI 和质量门禁

状态：第二批代码完成，等待 GitHub Ubuntu 矩阵首次远程验收。

已完成：

- 后端 CI 强制执行 gofmt、vet、全量 race test、Shell 语法和 Linux amd64/arm64 构建。
- ShellCheck 的 error 级问题作为后端 CI 阻断条件。
- 前端 CI 强制执行 TypeScript typecheck、Vitest 单元测试和生产构建。
- 前后端新增 CodeQL 周期扫描及 Dependabot 依赖更新配置。
- 新增 `/health/live`、`/health/ready` 和认证后的 `/v1/sys/version`。
- 构建元数据统一进入 `internal/buildinfo`，`one version` 不再触发数据库或文件初始化。
- 删除重复、失效且会在 main 自动发布 Beta/改写仓库的旧工作流。
- 重写 Makefile、BUILD.md、标签发布流程、发布打包和 SHA-256/内容校验脚本。
- 清理全部前端环境的固定域名、固定 IP 和通配 CORS 开发配置。
- 升级 gopsutil/purego，修复 macOS ARM64 race runtime 崩溃。
- 将三套行为冲突的安装脚本收敛为统一 `install.sh`；Ubuntu/CentOS 文件只保留兼容转发入口。
- 安装仅使用已校验发布包内的二进制和配置，不再替换软件源、关闭防火墙、修改内核参数或执行系统全量升级。
- 支持隔离测试根目录、配置保留更新、显式配置替换、systemd 服务、就绪检查、普通卸载和 `--purge --yes` 双确认清理。
- 初始管理员密码支持 `--password-file`，不会进入进程参数或安装日志；初始化可在失败后安全重试。
- 新增 7 个 Bats 安装器契约用例，已在本机使用 Bats 1.13.0 全部通过。
- 新增 7 组四组件故障/进度契约，并进入 Ubuntu 22.04/24.04 CI。
- CI 新增 Ubuntu 22.04/24.04 矩阵，执行发布包“安装 → 管理员初始化 → 启动 → live/ready → 停止 → 卸载”。
- Release 新增 2 个 Ubuntu 版本 × 4 个组件的真实生命周期门禁，保存进度 JSON 和完整安装日志证据。
- 后端与前端均加入高置信 Secret Scan。
- 后端 Release 生成 CycloneDX 1.6 Go SBOM、模块清单、许可证证据表及校验和。
- 前端 CI 生成 CycloneDX npm SBOM、完整依赖树、许可证表及校验和。

待完成：

- 合并或推送后观察 GitHub Ubuntu 22.04/24.04 矩阵首次真实运行结果。
- 将 ShellCheck warning/style 级存量问题逐步收敛；当前 error 级门禁已通过。
- 在真实 systemd 主机验收签名清单、双版本更新、失败自动恢复和安装器更新单元。
- 为发布流程增加来源证明及发布密钥轮换/撤销演练。

## 9. 0.1 版本剩余阻断项

- Web 终端启用后仍运行面板进程权限，尚缺独立低权限/特权执行隔离和命令级审计。
- 会话吊销、首次登录强制改密、TOTP、恢复码和前端设备管理已完成；生产仍需实例凭据密钥备份/轮换演练。
- 数据库连接和库用户密码已完成实例级加密和 API 脱敏；仍需制定凭据密钥备份/轮换运维流程。
- 审计日志已持久化并具备 HMAC 链、尾部截断检测、管理员查询/导出和保留清理；仍需外部不可变链头锚点防御整库快照回滚。
- 安装矩阵已进入 CI 配置，但尚未在当前未提交分支获得 GitHub Hosted Runner 的首次结果。
- 签名更新、备份、迁移预检和失败/中断恢复已完成代码闭环，但尚未取得真实 systemd 升级与断电故障矩阵结果。
- 发布包已有 SHA-256、签名更新清单、SBOM 和许可证证据，尚缺来源证明、密钥轮换/撤销演练和变更日志自动校验。

这些问题关闭前，不发布 0.1 测试版。

## 10. 版本里程碑

| 版本 | 目标 | 状态 |
| --- | --- | --- |
| 0.1 | 安全可运行内核 | 开发中 |
| 0.2 | OneinStack Shell 任务引擎 | 开发中（安装与卸载任务闭环完成，远程矩阵待完成） |
| 0.3 | 网站、数据库、SSL、文件、备份闭环 | 开发中（网站 HTTP/SSL、安全删除、整站备份、文件和 MySQL 数据库闭环完成，实机验收待完成） |
| 0.4 | 监控、安全、更新、回滚和审计 | 开发中（签名更新/回滚、审计、会话安全、监控告警、实时日志和计划任务安全闭环完成，实机验收待完成） |
| 0.5 | Docker、更多运行时、FTP 和插件 | 未开始 |
| 0.9 | 公开测试和系统兼容矩阵 | 未开始 |
| 1.0 | 首个生产稳定版 | 未开始 |

## 11. 下一开发入口

当前代码开发入口转为组件配置历史恢复与服务健康告警闭环；远程验收入口为四组件服务控制/配置发布、面板 IP/HTTP/可选 HTTPS、网站整站备份/计划任务/实时日志/监控长时间运行、签名更新/回滚、防火墙、公共 ACME/Nginx、Ubuntu 组件安装矩阵和 MySQL 8 数据库闭环矩阵。

实施顺序：

1. 将当前批次推送开发分支，观察 Ubuntu 22.04/24.04 的 Panel 生命周期和 8 组组件真实安装门禁。
2. 对失败矩阵只修复组件脚本、兼容性和回滚，不以跳过测试方式放行。
3. 在真实 MySQL 8 环境验收数据库创建、授权、备份和恢复矩阵。
4. 在真实测试域名验证网站 HTTPS/ACME 证书签发、部署、续签与回滚。
5. 在 Ubuntu/Rocky 实机验证防火墙管理端口保护、规则补偿回滚与重启持久化。

### 2026-07-26 面板访问配置开发记录

- 默认面板入口确定为 `http://服务器IP:8089`，监听 `0.0.0.0`；HTTP 不提供关闭开关。
- HTTPS 作为独立可选监听器，校验证书/私钥、有效期和 SAN，最低 TLS 1.2，不强制跳转 HTTP。
- 新增候选端口预检、防火墙预保护、`0600` 配置事务文件、同步与原子替换；失败执行补偿回滚。
- 可信代理默认空，转发协议和来源 IP 只有在 socket peer 命中可信 IP/CIDR 时才被接受。
- 新增 CSP、frame、MIME、Referrer 与 Permissions 响应头；为保留 HTTP 入口不发送 HSTS。
- 前端新增访问设置页、访问地址、证书状态、IP SAN/反代提示和重启待生效状态。
- 该面板访问批次完成时累计 216 个 Go 测试函数；四组件服务控制批次完成后当前累计 224 个。
- 前端嵌入 ZIP SHA-256 更新为 `4a071581b2d3cfd3b3bbd1372b421946a730a177f0f7aa6ac9323a1c0349b7e3`。
6. 在 Ubuntu 真实 systemd 主机验收签名更新、迁移预检、健康失败回滚与中断恢复。
7. 四组件状态、服务控制和结构化配置安全发布已完成；下一步提供配置历史列表/差异/手动恢复，并把服务异常接入监控告警。

### 2026-07-27 Docker 测试部署与前端 CSP 白屏修复

- 新增多阶段 `Dockerfile`、Compose 配置、Secret 管理员初始化、健康检查和持久化数据卷。
- 生成并校验 `0.1.0-dev.20260727` Linux amd64/arm64 发布包。
- 本机 Docker 测试实例使用 HTTP `18089` 端口，容器状态和 `/health/ready` 均为 healthy。
- 浏览器实测发现 `tools-javascript 1.2.9` 使用 `new Function`，被严格 CSP 正确阻止，导致 Vue 挂载前白屏。
- 前端构建增加 CSP 安全转换，将原型方法复制改为直接复用函数对象；未增加 `unsafe-eval`。
- 修复后完成前端 TypeScript、Vitest、生产构建、嵌入 ZIP、Go WebUI/安全响应头测试及双架构发布包校验。
- 浏览器复验入口脚本更新为 `index-CsyE5ccp.js`，`#app` 已挂载并显示账号、密码和登录控件。
- 当前嵌入 ZIP SHA-256：`aacf75d856ae59d391beb12ecba14e1cb19f84f069f60bb48d575ad6194a4d09`。

### 2026-07-27 全界面现代化改造

- 建立浅色/深色双主题设计令牌，统一页面背景、卡片、文本、边框、圆角、阴影、状态色与焦点反馈。
- 全局重构 Element Plus 的按钮、输入框、选择器、文本域、表格、分页、标签、提示、下拉菜单、弹窗和抽屉视觉规范。
- 重做桌面与移动端登录页，移除外部 Spline 脚本依赖，改为 CSP 兼容的本地 CSS 服务器状态视觉。
- 重做首次登录安全设置页、扫码登录页、顶部栏、用户菜单、服务状态、可折叠侧边栏与移动端布局。
- 重做首页概览、监控卡片、资源状态、软件商店卡片、面板设置、数据库提示、文件导航、监控告警、审计日志与运行日志。
- 统一 `card-tabs`、`custom-table`、`custom-form`、`custom-dialog`、`custom-drawer` 和 `search-input`，使全部业务页同步应用新样式。
- 修复运行日志页面位于主布局之外的问题，将其移动到 `/pages/runtime-log` 并纳入统一顶部栏和侧边栏。
- 浏览器逐页检查首页、网站、数据库、监控、安全、文件、审计日志、运行日志、计划任务、软件商店和面板设置；登录页、首页、软件商店、设置页、监控页和深色模式完成截图验收。
- 前端 `vue-tsc --noEmit` 通过，Vitest 2/2 通过，生产构建成功。
- 最终嵌入 ZIP 已同步到 `webui/app.zip`，SHA-256：`fea0db61ddaa5984b2b2a8267a4095ce233a7e23fa55cc2f5d8647531db86622`。
- Docker 现有测试实例保持 healthy；最终两个布局修复已进入嵌入包，镜像重建因本地 Docker 授权检查超时待再次执行。

### 2026-07-27 firewalld 默认安装与布局修复

- 安全模块在未检测到 UFW、firewalld 或持久化 iptables 时，显示“安装 firewalld”默认操作，不再只有不支持提示。
- `/v1/safe/install` 复用统一持久化软件任务，固定解析经过清单和 SHA-256 校验的 firewalld 独立脚本包。
- firewalld 脚本支持常用 Debian/RPM/zypper 系统与 amd64/arm64，包含预检、安装、验证、失败回滚和卸载动作。
- 安装过程保持防火墙关闭；用户首次启用时，现有安全服务先写入不可删除的面板管理端口规则，再启动 firewalld。
- 前端接入统一任务抽屉，可查看安装阶段、百分比、实时日志、取消与失败状态。
- 运行日志源码移入 `/pages/runtime-log`，重新进入统一顶部栏和侧边栏；移除侧栏底部 `OneinStack / Panel Console` 品牌卡。
- 后端 `go test ./...` 全量通过；firewalld 脚本 `bash -n` 与内置包校验通过；前端类型检查、2 个 Vitest 用例和生产构建通过。
- 最新嵌入前端 `webui/app.zip` SHA-256 已由后续任务反馈修复批次更新。
- 待完成：在 Ubuntu、Debian、Rocky/AlmaLinux 实机验证包管理器安装、保持关闭、面板端口预保护、启用与重启持久化。

### 2026-07-27 全局任务反馈与 firewalld 安装验证修复

- 修复前端底层传输把 HTTP `202 Accepted` 当作失败的问题；任务创建不再显示“红叉 + 操作成功”，调用方可以立即接收任务 ID 并进入追踪状态。
- 对软件安装、卸载、服务动作、配置发布、网站/数据库备份恢复、证书任务、防火墙安装和计划任务执行等异步接口统一增加成功 2xx 适配。
- 顶栏新增当前运行任务数字框和任务列表，展示组件、操作、阶段、进度；点击任务可打开统一实时日志抽屉。
- 任务结束时右上角显示成功或失败通知；通知可点击回看任务详情，安全页在终态后自动刷新防火墙状态和规则。
- 软件任务日志读取增加 single-flight 合并，消除 SSE 事件与轮询并发请求相同游标时的重复日志。
- firewalld 验证不再执行依赖系统 D-Bus 的 `firewall-cmd --version`，改用离线 Python 模块读取版本；修复软件包已经安装却被验证误判并回滚的问题。
- 前端类型检查与生产构建通过；后端重点 Go 测试、firewalld Shell 语法和文件哈希全量校验通过。
- Docker 测试容器重新构建后保持 healthy；在 Debian bookworm arm64 中实装 firewalld `1.3.3` 成功，验证脚本通过且服务保持关闭。
- 浏览器完整复验任务数字 `0 → 1 → 0`、顶栏打开任务、实时日志、完成通知、安全页免刷新更新；控制台无错误。
- 成功消息实测类名为 `el-message--success` 且 SVG 为对勾路径；不存在旧的错误类型提示。
- 最新嵌入前端 `webui/app.zip` SHA-256：`054dfb8cb2cea4972332838d89c9e4a996fbe8fdced8e6eb574655e1d372c8e0`。
- 待完成：把网站、数据库与计划任务的独立任务模型聚合进顶栏任务中心；继续执行真实 Linux systemd 防火墙矩阵。

### 2026-07-27 Center 联调、Nginx/防火墙修复与安装界面统一

- 启动本地 Oneinstack-Center，修复非 root Center 无法写入新 Docker 数据卷的问题，导入并发布稳定组件包。
- 将 Debian 12 / ARM64 / 容器运行支持后的 Nginx 作为 Center 包 `1.0.1` 发布；Panel 通过固定 Ed25519 公钥完成解析、下载、摘要、签名、逐文件校验和缓存。
- 修复 Nginx 任务标题为 1.28.2、旧回退脚本却安装 1.26.2 的不一致；正式包现在覆盖当前 Docker 目标，不再进入旧兼容路径。
- 修复 APT HTTP 下载超时：使用 HTTPS 临时软件源、5 次重试和缺失归档补拉。
- 修复 firewalld 因系统缺少 `/etc/protocols` 导致 `INVALID_PROTOCOL: esp`；安装依赖、协议解析和完整离线配置现在同时校验。
- 区分“firewalld 配置需修复”和“当前环境没有 systemd”；Docker 页面禁用危险开关并给出明确说明，真实 Linux 主机仍执行端口预保护后启用。
- 普通弹窗全局居中；软件安装任务抽屉完成现代化重构并保留实时进度、日志、取消、下载和后台运行能力。

### 2026-07-27 Ubuntu 24.04 原生 systemd 实机验收

- 后续功能验收从 Docker 切换到局域网 Ubuntu 24.04.1 LTS、x86_64、systemd
  测试服务器；Panel、Center 和组件均使用原生二进制与 systemd 服务。
- 原生 Center 监听 `127.0.0.1:8189`，使用 DynamicUser 和独立状态目录运行；
  Nginx、MySQL、PHP、Redis 稳定包已导入、签名并发布。Panel 通过固定
  Ed25519 公钥完成 Center 解析、下载、摘要、签名、逐文件校验和缓存。
- 原生 Panel 监听 `0.0.0.0:8089`，首次管理员初始化、强制改密、会话、
  `/health/ready`、嵌入前端和局域网 HTTP 访问均通过。
- firewalld `1.0.0` 通过持久化软件任务安装成功，
  `firewall-offline-cmd --check-config` 无错误，确认
  `INVALID_PROTOCOL: esp` 修复在真实 Ubuntu 主机有效。
- 防火墙选择改为“已启用后端优先；全部关闭时 firewalld 优先”，避免 Ubuntu
  预装但未启用的 UFW 抢占刚安装的 firewalld；新增回归测试。
- firewalld 由 Panel API 先写入不可删除的 8089 保护规则再启动；80 和 5244
  规则也通过 Panel 写入。外部验证 SSH 22、Panel 8089、Nginx 80 和 Alist
  5244 均可访问，安全页返回 `backend=firewalld`、
  `panelPortProtected=true`。
- Nginx 首次实机安装发现 systemd `ExecStart` 把 `-g` 参数错误拆分，任务在
  verify 阶段失败并自动回滚成功；Center 包 `1.0.2` 移除无必要的 `-g`
  参数后重新发布。
- Nginx `1.0.2` 包重新安装成功，任务进度 100%，Nginx 1.28.2 的配置检查、
  systemd 启动、运行状态探测和本机/局域网 HTTP 健康检查全部通过。
- 旧 `/usr/local/one` 和 `/usr/local/nginx` 已分别移动到带时间戳的 legacy
  备份目录；GitLab 服务已停止并禁用开机启动，数据和配置未删除。

尚未完成：

- 在同一原生环境继续完成 PHP 8.1/8.2/8.3、Redis 7.4.8 的安装、服务控制、
  配置发布、失败回滚和数据保留矩阵；MySQL 仍需补齐重复安装、配置发布、
  备份恢复和故障注入矩阵。
- 完成服务器重启后的 Panel、Center、Nginx、firewalld 开机恢复及规则持久化
  验收。

实际验收：

- Center：`http://127.0.0.1:8189/health/ready` 正常，公开目录可返回已发布包。
- Panel：`http://127.0.0.1:18089/health/ready` 正常，容器内可访问 Center。
- Nginx 1.28.2：从 Center `1.0.1` 包安装成功，配置校验和 HTTP 健康检查通过，浏览器任务用时约 19 秒。
- firewalld：安装任务成功，`getent protocols esp` 返回 `esp 50 IPSEC-ESP`，`firewall-offline-cmd --check-config` 返回成功。
- 前端：浏览器确认备忘录、版本选择、确认框均居中；安装抽屉与主界面风格一致。
- 内嵌前端 SHA-256：`746f6c699779023fd0c45a45eec1f645296e4d04e100f6c885190550ad2f722e`。

当前边界：

- Docker 测试容器不运行 systemd，也不应直接管理 macOS/Docker Desktop 宿主机防火墙；真实启用验收必须在 Linux 主机完成。
- 软件商城目录已在 2026-07-27 后续批次改为由 Center 控制；本地
  `softwares` 表仅保存经过签名校验的可信快照和本机安装状态。
- 远程 Center 失败时，Panel 依次回退到随 Panel 发布的受校验 bundled 包，再对未迁移组件使用旧兼容脚本。

### 2026-07-27 软件商城状态一致性与 MySQL 零配置闭环

- 修复软件商城按名称聚合时混用任意版本行的问题；安装状态、安装版本和底部
  操作按钮现在统一来自已安装记录，Nginx 不再同时显示“已安装”和“未安装”。
- MySQL 安装界面不再要求端口、用户名或密码；后端固定使用 `3306`、`root`
  并生成 24 位密码，密码不进入任务参数或日志。
- MySQL 安装成功后自动创建 `127.0.0.1:3306` 本机受管连接，root 密码使用
  实例级 AES-256-GCM 凭据密钥加密保存，列表接口只返回
  `passwordConfigured`。
- 新建数据库只填写数据库名、字符集和连接；用户名固定为数据库名，密码由
  服务端安全随机生成。创建结果只在当前会话明确展示一次。
- 数据库列表新增“查看账户”和“修改密码”；两项操作均要求输入当前面板密码，
  空的新密码由服务端重新生成。普通列表始终不包含密码字段。
- 数据库账户统一为 `数据库名@%`，实际可达范围仍由 MySQL 的
  `bind-address` 和防火墙控制；创建、密码轮换后都会用新账户真实连接目标库，
  避免接口成功但运行态未生效。
- Center MySQL 脚本包迭代至 `1.0.6`：适配 Ubuntu 24.04
  `libaio1t64`、兼容 SONAME、校验下载缓存、等待 systemd socket，并创建和
  验证仅用于 Panel 管理的 `root@127.0.0.1`。
- Ubuntu 24.04 amd64 实机上，MySQL 8.0.45 已安装并设置为 active/enabled，
  监听 `127.0.0.1:3306`；Panel 自动连接登记成功。
- 实机验收完整通过：创建 `codex_acceptance_db`、生成同名账户与 24 位密码、
  二次认证查看、随机密码轮换、MySQL CLI 真实登录、删除数据库及账户。验收
  数据已清理。
- 早期包的失败均由组件回滚流程处理；1.0.5 已初始化但未登记的全新测试数据
  未删除，保留在组件状态目录的隔离快照中。
- Panel 已部署 `test-20260727.4` 到原生 systemd 测试服务；部署前二进制
  SHA-256 为 `ddb0271ed6c493aee74e01018a6708552c87766879a9e4febd30e365dff45801`。

### 2026-07-27 Center 控制软件商城目录

完成：

- Center 新增独立软件商城目录，默认包含 Nginx、MySQL 8.0、PHP
  8.1/8.2/8.3、Redis 7.4.8 和 firewalld；目录与安装脚本包保持分层，
  商城条目通过 `component` 指向独立组件包。
- Center 管理台可新增、编辑、隐藏、暂停安装和移除商城应用，并控制版本、
  推荐版本、通道、排序、标签和更新说明。每次变更都会生成新目录摘要、使用
  Center Ed25519 密钥签名并写入管理审计。
- Panel 启动时立即获取 `/v1/software/catalog`，默认每 15 分钟同步；支持
  ETag，未变化目录返回 304，不重复写库。
- Panel 严格校验固定公钥、Ed25519 签名、SHA-256 目录摘要、字段范围、
  重复版本和每通道唯一推荐版本，校验失败不会覆盖现有目录。
- 可信目录在单个 SQLite 事务中更新。本次目录未包含的软件会停止新装并从
  商城隐藏；已经安装的软件仍保留管理和卸载入口。
- Center 离线或响应异常时继续使用最后一次可信快照，并在商城顶部明确显示
  `Center 可信缓存`、最近同步时间、过期状态和错误；尚未完成首次同步时才
  使用本地内置目录。
- 安装任务会再次检查当前可信目录，只允许安装 Center 已启用的应用和版本；
  商城 Key 与实际脚本组件不再依赖固定 switch 映射。
- 软件商城“全部、已安装、可升级”三个标签统一使用真实数据；Center 推荐
  版本高于本机版本时显示升级按钮，暂停安装时按钮即时变为“已停用”。
- 删除旧 `/v1/sys/update` 商城同步逻辑，避免 Center 和本地两套目录来源
  互相覆盖。

验证：

- Center `go test ./...` 全量通过，覆盖默认目录、持久化、篡改拒绝、
  推荐版本约束、公开 ETag API 和管理员增删接口。
- Panel `go test ./...` 全量通过，覆盖签名同步、离线可信缓存、删除目录项
  后隐藏、保留已安装状态和篡改响应拒绝。
- 前端 `npm run typecheck`、2 个 Vitest 用例和 `npm run buildNocnd`
  通过；生产 ZIP 已同步到 `webui/app.zip`，两端 SHA-256 均为
  `c13eb7bae63344bdc3da6d2a50d5a81197432ebd78157e39c2bc400fe4b7feeb`。

部署要求：

- 在 Panel `scriptCenter` 中启用 Center、配置通道和固定的 Center Ed25519
  公钥；生产环境使用 HTTPS，本机开发 HTTP 必须显式开启
  `allowInsecureHTTP`。
- Center 的签名私钥和 `data/software-catalog/catalog.json` 必须纳入备份。
  丢失私钥后，现有 Panel 不会接受新签名目录。

尚未完成：

- 当前管理台适合首个单节点版本；批量版本编辑、跨通道提升、变更审批和定时
  发布进入后续 Center 运营增强。
- 已在 Ubuntu 24.04 原生测试机完成 Center 在线同步；断网缓存、隐藏应用、
  暂停安装和恢复同步仍需继续完成浏览器端到端矩阵。

### 2026-07-27 OneinStack 完整组件脚本包

完成：

- 对照固定 OneinStack 上游提交，生成 Nginx、Tengine、OpenResty、Caddy、
  Apache、PHP、OpenJDK、Tomcat、MySQL、MariaDB、Percona、PostgreSQL、
  MongoDB、Node.js、Pure-FTPd、phpMyAdmin、Memcached 和 Redis 共 18 个
  独立组件包。
- PHP 覆盖 5.3–5.6、7.0–7.4、8.0–8.5；OpenJDK 覆盖 8、11、17、18；
  MySQL 系数据库覆盖 OneinStack 当前全部版本线。商城共发布 50 个开发通道
  软件版本。
- 所有包固定上游提交和归档 SHA-256，提供独立生命周期动作；Panel 到组件
  入口的数据库密码不进入任务记录或组件状态文件，PostgreSQL/MongoDB 默认
  限制为本机监听。
- Center 默认商城基线扩展为完整矩阵，新部署实例不再只有最初五个软件。
- Panel 安装器增加通用密码参数映射，Center 新增 MariaDB、Percona、
  PostgreSQL 和 MongoDB 时不需要继续增加固定软件 Key。
- 18 个包已上传并签名发布到 `192.168.1.6:8189` 的 development 通道；
  50 个版本的公共解析请求全部通过，原有 firewalld 目录项保留。

验证：

- Center 与 Panel `go test ./...` 全量通过。
- 所有 Shell 动作通过 `bash -n`。
- 18 个归档通过清单、权限、安全路径、逐文件校验和验证，并通过两次构建
  内容一致性检查。

尚未完成：

- 本批属于 development，不是生产稳定包；必须逐个完成干净系统安装、
  重复安装、故障回滚、重启恢复、服务控制和数据保留矩阵。
- 当前只声明 amd64；arm64 需要单独开发和实机验证。
- Tomcat 6 在官方固定提交中缺少实际安装文件，本批只发布 Tomcat 7–11。
- OneinStack 上游数据库初始化命令仍需完成进程参数凭据清理，修复和泄漏
  测试完成前不能提升为 stable。
- 测试 Panel 已部署本批通用数据库密码映射代码；MariaDB、Percona、
  PostgreSQL 和 MongoDB 仍需逐个完成界面安装与数据库增删改验收。

### 2026-07-27 原生测试环境部署

完成：

- Center 与 Panel 已原生部署到 Ubuntu 24.04 amd64 测试服务器，不使用
  Docker；两个 systemd 服务均为 `active`，局域网 HTTP 页面和就绪接口均
  返回 200。
- Panel 更新为 `v0.1.0-test.8`，脚本中心通道切换为 `development`；
  Center 管理台与现有已发布组件包保持不变。
- 修复切换 stable/development 通道后 ETag 返回 304，导致 Panel 沿用旧
  通道商城快照的问题。Panel 现在持久化已同步通道，通道变化时强制重新获取
  并应用相同修订号的目录。
- Panel 已校验同步 Center 修订
  `62497a724f643286e04f8173f2ef5066057b33e4b6a0f21c196962b207ca6765`，
  状态为 18 个产品、50 个版本、无同步错误。
- 部署前备份位于 `/var/backups/oneinstack-center/20260727-200811`、
  `/var/backups/oneinstack-panel/20260727-200811` 和
  `/var/backups/oneinstack-panel/20260727-201300`。

验证：

- Panel `go test ./...` 全量通过，新增通道变化后重新应用相同目录修订的
  回归测试。
- Center、Panel 健康检查与页面访问均通过；Panel 重启后的 warning 日志
  为空。

### 2026-07-27 组件配置抽屉视觉优化

完成：

- 重构 Nginx 等受管组件的配置抽屉，新增组件标识、服务配置标题区、安全模式
  提示、配置版本/脚本来源/生效方式信息卡和独立运行参数面板。
- 参数表单改为响应式卡片网格，统一标签、输入框、说明文字、聚焦状态和数字
  输入对齐；窄屏自动切换为单列。
- 变更预览增加当前值/新值的视觉区分、待发布数量和发布前校验说明；底部操作
  栏增加当前状态提示并固定在抽屉底部。
- 抽屉统一使用页面背景、圆角卡片、轻量阴影、主题色和现代关闭按钮，移除原
  界面从顶部直接堆叠表单及大面积无层次留白的问题。
- 新前端已嵌入 Panel `v0.1.0-test.9` 并部署到原生测试服务器，部署前备份
  位于 `/var/backups/oneinstack-panel/20260727-202718`。

验证：

- 前端 TypeScript 类型检查、2 个 Vitest 用例和生产构建通过。
- 后端嵌入页面测试通过，新 `webui/app.zip` SHA-256 为
  `29fed2ae364d875eb796b14758e9dc7b2536e67cc12ee0694964bf673b42914b`。
- 使用真实 Vue 组件与 Nginx 配置模拟数据完成桌面视觉和变更预览交互验收；
  抽屉内容滚动、固定底栏和禁用/可发布状态均正常。

## 12. 进度维护规则

每完成一个开发批次，需要更新：

1. 文档顶部日期和当前阶段。
2. 任务状态。
3. 完成内容和主要文件。
4. 新增测试。
5. 最近一次验证结果。
6. 新发现的风险或阻断项。
7. 下一开发入口。

任务只有通过测试、构建和对应验收条件后才能标记为“已完成”。
