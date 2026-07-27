<h1 align="center">Oneinstack 服务器管理面板</h1>

[![GitHub forks](https://img.shields.io/github/forks/oneinstack/Oneinstack-Panel)](https://github.com/oneinstack/Oneinstack-Panel/network)
[![GitHub stars](https://img.shields.io/github/stars/oneinstack/Oneinstack-Panel)](https://github.com/oneinstack/Oneinstack-Panel/stargazers)
[![GitHub license](https://img.shields.io/github/license/oneinstack/Oneinstack-Panel)](https://github.com/oneinstack/Oneinstack-Panel/blob/main/LICENSE)
![GitHub release](https://img.shields.io/github/v/release/oneinstack/Oneinstack-Panel)

> 一款开源的 Linux 服务器运维管理面板，让服务器管理更简单、更安全、更高效

## 🚀 功能特性

- 🛡️ 可视化服务器状态监控（CPU/内存/磁盘/网络）
- 🔧 一键安装常用服务/软件（Nginx/MySQL/Redis 等）
- 🔐 自动防火墙配置与管理
- 🧾 HMAC 防篡改操作审计、筛选、完整性校验和 CSV 导出
- 🌐 网站/FTP
- 🔄 定时任务管理（Crontab）
- [ x ] 📊 实时日志查看与分析
- [ x ] 数据库可视化管理
- [ x ] ⚡ 内置 BBR 网络加速优化
- [ x ] 📡 支持多语言操作界面

## 📦 快速安装

### 已验证系统要求

- 操作系统：Ubuntu 22.04 LTS、Ubuntu 24.04 LTS
- 架构：首发生产矩阵为 amd64；arm64 已构建发布包，但暂未进入首发生产兼容矩阵
- 内存：推荐 1GB 以上
- 磁盘空间：至少 20GB 可用空间
- 需要 root 权限

下载匹配架构的发布包及对应 `.sha256` 文件，验证并解压。初始管理员密码通过权限受控的文件传入，不写入 Shell 历史：

```bash
sha256sum -c one-linux-amd64-v0.1.0.tar.gz.sha256
tar -xzf one-linux-amd64-v0.1.0.tar.gz
cd one-linux-amd64-v0.1.0
read -r -s -p "请输入初始管理员密码: " PANEL_PASSWORD
printf '\n'
sudo install -m 0600 /dev/null /run/one-admin-password
printf '%s\n' "$PANEL_PASSWORD" | sudo tee /run/one-admin-password >/dev/null
unset PANEL_PASSWORD
sudo ./install.sh --admin-user admin \
  --admin-password-file /run/one-admin-password
sudo rm -f /run/one-admin-password
```

安装完成后访问：`http://服务器IP:8089`

HTTP 是默认且始终保留的面板入口，默认监听 `0.0.0.0`，无需域名即可通过服务器 IP 访问。管理员可以在“设置 → 面板访问方式”中另行启用独立 HTTPS 端口并配置证书；启用 HTTPS 不会关闭 HTTP，也不会强制跳转。若使用反向代理，可信代理列表默认留空，只应加入实际受控的代理 IP 或 CIDR。

安装器只使用已经校验的发布包内二进制和配置，不会替换软件源、关闭防火墙、修改内核参数或执行系统全量升级。

使用另一个已校验并解压的发布包更新，默认保留现有配置：

```bash
sudo ./install.sh --force
```

配置 Center 地址和可信更新公钥后，也可以在“设置 → 面板更新”中检查并安装 Center 分配的签名版本，或使用：

```bash
sudo one update check
sudo one update apply --yes
sudo one update status
```

Center 统一控制稳定版、测试版、灰度百分比、实例白名单和版本撤回；Panel 不直接信任 Center 的网络响应，而是再次校验固定 Ed25519 公钥、清单、制品大小和 SHA-256，再执行数据库迁移预检、版本原子切换和健康检查。失败时自动恢复旧二进制、数据库、配置和内置脚本。密钥配置、手工恢复和发布流程见 [BUILD.md](BUILD.md)。

普通卸载会保留配置、数据库、日志和备份：

```bash
sudo ./install.sh uninstall
```

永久删除必须同时提供两个破坏性确认参数：

```bash
sudo ./install.sh uninstall --purge --yes
```

## 🖥️ 管理功能

### 服务器管理

- 实时资源监控

![alt 属性文本](img/1.png)

- 防火墙规则配置

![alt 属性文本](img/2.png)

- SSH 端口管理

- 系统服务管理
- 定时任务管理

![alt 属性文本](img/3.png)

- 系统更新提醒

### 应用管理

- 一键安装：
  - Web 服务器：Nginx
  - 数据库：MySQL/Redis
  - 运行环境：PHP/JAVA

### 网站管理

- 静态代理
- 反向代理

## 🛠️ 技术架构

- 核心语言：Go
- 前端框架：Vue.js
- 数据库：SQLite
- 进程管理：Systemd

## 🤝 参与贡献

我们欢迎任何形式的贡献！

## 📄 开源协议

本项目采用 [Apache License 2.0](LICENSE) 开源协议。

---

> 🌍 官网地址：[https://oneinstack.com](https://oneinstack.com)  
> 🐛 问题反馈：[GitHub Issues](https://github.com/oneinstack/Oneinstack-Panel/issues)
