# Panel 多节点管理

Center 只负责 Panel 版本、组件脚本包和软件商城目录发布，不参与节点运行时管理。用户可以把任意一个 Panel 设为主控 Panel，再把其他独立部署的 Panel 注册为节点。

## 注册流程

1. 主控管理员调用 `POST /v1/cluster/nodes`，提交节点名称、Panel 地址、分组和标签。
2. 主控端只保存节点令牌的 SHA-256 哈希，并在响应中返回明文令牌（仅此响应展示）。令牌应通过 HTTPS 安全交给被管理 Panel。
3. 被管理 Panel 调用主控端 `POST /cluster/agent/register`，提交令牌和自身版本/系统信息。
4. 被管理 Panel 按固定周期调用 `POST /cluster/agent/heartbeat`，提交 CPU、内存、磁盘、网络和运行时长。
5. 主控端把最新快照写入节点记录，同时保留历史指标到 `cluster_node_metrics`，供后续负载路由和趋势图使用。

## 主控端接口

控制端接口均位于 `/v1` 下，并要求超级管理员权限。节点模式配置也可以直接在 Web 的“多节点管理”页面完成：

- `GET /v1/cluster/agent/settings`：读取本机控制端/节点端配置（令牌只返回是否已配置）。
- `PUT /v1/cluster/agent/settings`：保存角色、开关、主控地址、令牌和轮询参数。

- `GET /v1/cluster/nodes`：超级管理员查看节点列表。
- `GET /v1/cluster/nodes/:id`：查看节点详情。
- `GET /v1/cluster/nodes/:id/metrics`：查看最近指标快照。
- `POST /v1/cluster/nodes`：创建节点并生成节点令牌。
- `PUT /v1/cluster/nodes/:id`：修改地址、分组、标签或启用状态。
- `POST /v1/cluster/nodes/:id/token/rotate`：撤销旧令牌并生成新令牌。
- `GET /v1/cluster/nodes/:id/tasks`：查看节点任务记录。
- `POST /v1/cluster/tasks`：向指定节点创建任务（支持 `idempotencyKey`）。
- `POST /v1/cluster/website/dispatch`：按固定节点、标签或最低负载选择节点并创建网站同步任务。
- `DELETE /v1/cluster/nodes/:id`：移除节点记录。

## Agent 接口

Agent 接口不依赖 Panel 登录会话，只接受节点令牌。令牌可放在 JSON 的 `token` 字段中，也可放在 `Authorization: Bearer <token>` 请求头中。

- `POST /cluster/agent/register`
- `POST /cluster/agent/heartbeat`
- `POST /cluster/agent/tasks/next`
- `POST /cluster/agent/tasks/complete`

任务 Agent 使用 `Authorization: Bearer <token>` 领取任务；回执提交任务 ID、`succeeded` 或 `failed` 状态及结果。失败任务在达到 `maxAttempts` 前会自动重新排队。

任务队列支持幂等键、回执和失败重试；网站下发、任务编排和按标签/负载调度均由主控 Panel 负责，Center 不进入调用链。

## 被管理 Panel 启用 Agent（兼容手工配置）

在被管理 Panel 的 `config.yaml` 中配置（文件权限应保持为 `0600`）：

```yaml
clusterAgent:
  enabled: true
  controllerUrl: "https://主控Panel.example.com/v1"
  token: "创建节点时返回的令牌"
  intervalSeconds: 30
  requestTimeoutSeconds: 10
```

通过 Web 保存后，运行中的 Agent Supervisor 会自动启动、停止或重载代理；不需要手动重启服务。也可以直接编辑配置文件，适用于无人值守部署。

Agent 会自动注册并按配置周期上报资源指标。令牌轮换后，在节点端页面更新令牌即可。

## 任务执行与限制

控制端通过 `POST /v1/cluster/tasks` 向指定节点创建结构化任务，失败任务会在达到 `maxAttempts` 前自动重试。支持软件安装/卸载、组件服务启停、受限系统命令、管理目录文件上传、MySQL/MariaDB/PostgreSQL Dump 导入和网站内容同步。

网站内容同步使用 SHA-256 校验的 tar.gz 归档，最大 64 MiB；文件上传最大 16 MiB。系统命令仅允许受控诊断命令，不接受 Shell、下载器或脚本解释器。数据库密码、节点令牌等敏感字段应通过 HTTPS 传输。
