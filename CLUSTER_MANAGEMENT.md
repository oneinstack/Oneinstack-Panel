# Panel 多节点管理（第一阶段）

Center 只负责 Panel 版本、组件脚本包和软件商城目录发布，不参与节点运行时管理。用户可以把任意一个 Panel 设为主控 Panel，再把其他独立部署的 Panel 注册为节点。

## 注册流程

1. 主控管理员调用 `POST /v1/cluster/nodes`，提交节点名称、Panel 地址、分组和标签。
2. 主控端只保存节点令牌的 SHA-256 哈希，并在响应中返回明文令牌（仅此响应展示）。令牌应通过 HTTPS 安全交给被管理 Panel。
3. 被管理 Panel 调用主控端 `POST /cluster/agent/register`，提交令牌和自身版本/系统信息。
4. 被管理 Panel 按固定周期调用 `POST /cluster/agent/heartbeat`，提交 CPU、内存、磁盘、网络和运行时长。
5. 主控端把最新快照写入节点记录，同时保留历史指标到 `cluster_node_metrics`，供后续负载路由和趋势图使用。

## 主控端接口

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

当前阶段只完成节点身份、心跳和指标采集。网站下发、任务编排和按标签/负载调度应在此基础上增加任务队列、幂等键、回执和失败重试，且仍由主控 Panel 负责，Center 不进入调用链。

## 被管理 Panel 启用 Agent

在被管理 Panel 的 `config.yaml` 中配置（文件权限应保持为 `0600`）：

```yaml
clusterAgent:
  enabled: true
  controllerUrl: "https://主控Panel.example.com"
  token: "创建节点时返回的令牌"
  intervalSeconds: 30
  requestTimeoutSeconds: 10
```

重启 Panel 后，Agent 会自动注册并每 30 秒上报一次资源指标。令牌轮换后，需同步修改被管理 Panel 的配置并重启服务。
