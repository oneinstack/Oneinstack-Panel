# OneinStack Panel Docker 部署

该配置用于快速验证面板 Web UI、HTTP API、账户安全、任务进度、文件管理、
回收站、监控和配置管理等功能。默认访问端口为 `18089`。

## 启动

首次启动前，在 `docker/secrets/one-admin-password.txt` 写入强密码并确保该文件
不被提交到版本库，然后执行：

```bash
docker compose up -d --build
docker compose ps
```

健康检查：

```bash
curl http://127.0.0.1:18089/health/ready
```

访问地址：

```text
http://127.0.0.1:18089
```

初始用户名默认为 `admin`。初始密码只在首次创建管理员时使用；首次登录后面板会
要求更换密码。

## 日常操作

```bash
docker compose logs -f panel
docker compose restart panel
docker compose down
```

`docker compose down` 不删除数据。只有显式执行
`docker compose down --volumes` 才会删除面板状态和 `/data` 数据卷。

## 数据持久化

- `panel_state`：SQLite 数据库、配置、凭据加密密钥、证书、任务记录。
- `panel_data`：网站、日志、数据库备份和文件管理数据。

## 边界说明

普通 Docker 容器与宿主机服务隔离，因此该模式不会直接管理 Docker Desktop
宿主机上的 systemd、Nginx、MySQL、PHP、Redis 或防火墙。不要为了绕过隔离而
挂载 Docker socket 或宿主机根目录。需要完整管理 Linux 服务器软件时，应在
目标 Linux 主机使用项目发布包和 `install.sh` 原生部署。
