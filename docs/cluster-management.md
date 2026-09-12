# 多节点管理使用说明

多节点模式由一个 OneinStack Panel 作为控制端，其他独立部署的 Panel 作为被管理节点。Center 不参与节点通信，只负责版本和软件商城。

## 1. 在控制端创建节点

登录控制端 Web 的“多节点管理”，点击“添加节点”，填写被管理 Panel 地址。保存后复制一次性显示的节点令牌。

节点必须先完成注册并上报心跳，状态才会变为 `online`。控制端只允许超级管理员访问节点管理 API。

## 2. 配置被管理 Panel Agent

在被管理 Panel 的配置文件中设置：

```yaml
clusterAgent:
  enabled: true
  controllerUrl: "https://controller.example.com:8089/v1"
  token: "控制端生成的节点令牌"
  intervalSeconds: 30
  requestTimeoutSeconds: 10
```

`controllerUrl` 必须包含控制端 API 前缀 `/v1`。修改后重启被管理 Panel 服务，Agent 会立即注册，之后按间隔发送 CPU、内存、磁盘、网络和运行时长指标。

## 3. 网站配置下发

控制端“网站配置下发”支持最低负载、指定节点和按标签三种策略。任务由目标节点 Agent 自动领取并执行：

- 新网站在目标节点按本地 Web 根目录创建；
- 已存在网站按名称匹配并更新；
- 网站结构化设置（重写、代理、访问控制、限流等）一并同步；
- 网站启用/停用状态同步；
- 目标节点本地 Web Server 负责校验并发布配置，失败会回报任务错误；
- Agent 中断时，控制端会在 15 分钟后回收运行中的任务并按最大尝试次数重试。

源节点的网站文件内容不会通过任务队列复制。静态文件或代码需要通过文件同步、镜像仓库或对象存储单独分发。

## 4. 通用节点任务

控制端的任务接口还支持以下结构化任务类型：

- `software.install` / `software.uninstall`：复用本机组件安装、卸载流程；
- `service.start` / `service.stop` / `service.restart` / `service.reload`：仅允许已登记的 OneinStack 组件服务；
- `system.command`：使用参数数组执行受限诊断命令，禁止 shell、下载器和解释器；
- `file.upload`：Base64 文件上传，单文件最大 16 MiB，路径必须位于 Panel 管理目录；
- `database.sync`：将 MySQL/MariaDB 或 PostgreSQL dump 导入目标数据库，dump 最大 64 MiB；
- `website.content_sync`：同步网站配置及网站目录内容，总大小最大 64 MiB，自动校验 SHA-256 并拒绝路径穿越。

软件密码、数据库密码等敏感字段只应通过 HTTPS 和短生命周期任务传递，生产环境建议使用专用同步账号，避免使用 root 密码。

## 5. 排障

- `pending`：节点尚未使用令牌成功注册；
- `offline`：超过两分钟没有心跳；
- 任务长期 `queued`：检查 Agent 是否启用、控制端地址是否包含 `/v1`、防火墙是否放行 8089；
- 任务 `failed`：在节点详情的“指标/任务”中查看错误信息，修复目标节点 Web Server 配置后可重新下发。
