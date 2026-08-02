# 软件商城安装审计

> 审计日期：2026-08-01  
> 测试环境：Ubuntu 24.04 amd64，`192.168.1.6` 物理/虚拟机原生部署  
> Panel：`v0.1.0-test.39`  
> Center：`127.0.0.1:8189`，development 通道

## 结论

- 软件商城 18 个 development 组件均能从 Center 正常解析，解析接口全部返回 HTTP 200。
- 14 类软件完成了原生安装或已有安装的运行验证；Java 4 个版本和 Tomcat 5 个版本均逐版验证。
- MariaDB、Percona 因测试机保留 MySQL 数据，没有执行会替换数据库服务的破坏性安装；已验证新加入的互斥保护。
- MongoDB 8.0 下载链路已修复，但测试机 CPU 不提供 AVX，不能运行 MongoDB 8.0；`1.0.5` 包现在会在下载前明确拒绝。
- firewalld 已作为 stable 组件发布到 Center；测试机本身正在运行 firewalld，因此没有重复卸载重装。

## 组件结果

| 组件 | 软件版本 | 结果 | 说明 |
| --- | --- | --- | --- |
| Nginx | 1.28.2、1.31.0 | 通过 | 两版均完成安装、版本、服务和卸载验证；1.31.0 增加官方源兜底 |
| Tengine | 3.1.0 | 通过 | 完成安装、运行和卸载验证 |
| OpenResty | 1.27.1.2 | 通过 | 完成安装、运行和卸载验证 |
| Caddy | 2.10.2 | 通过 | 修复系统用户和 systemd 服务后通过 |
| Apache | 2.4.66 | 通过 | 修复 nghttp2 下载后通过 |
| PHP | 8.3 / 8.3.29 | 通过 | 完成源码编译、PHP-FPM、配置检查和卸载验证 |
| OpenJDK | 8、11、17、18 | 通过 | 四个版本逐版安装、版本切换和卸载验证 |
| Tomcat | 7.0.109、8.5.96、9.0.113、10.1.50、11.0.15 | 通过 | 五版逐版启动并返回 HTTP 200，卸载通过 |
| MySQL | 8.0 / 8.0.45 | 通过 | 已完成真实安装、回环登录、服务和版本验证，测试机继续保留 |
| MariaDB | 10.11 / 10.11.15 | 有条件通过 | 官方包已缓存并修复脚本；因现有 MySQL 未进行替换安装，互斥提示通过 |
| Percona | 8.0 / 8.0.33-25 | 有条件通过 | 官方 1.65GB 包地址可用；因现有 MySQL 未进行替换安装，互斥提示通过 |
| PostgreSQL | 18.1 | 通过 | 修复失效镜像后完成安装、5432 本机监听、卸载及数据保留验证 |
| MongoDB | 8.0.17 | 环境不兼容 | 官方包链路已修复；测试机无 AVX，1.0.5 包会在预检阶段明确阻止 |
| Node.js | 22.12.0 | 通过 | 已完成安装和运行验证，测试机继续保留 |
| Pure-FTPd | 1.0.51 | 通过 | 完成安装、21 端口、进程和卸载验证 |
| phpMyAdmin | 5.2.3 | 通过 | 在 PHP 8.3/MySQL 环境完成文件、权限、密钥和卸载验证 |
| Memcached | 1.6.40 | 通过 | 完成版本、服务、11211 本机监听和卸载验证 |
| Redis | 8.4.0 | 通过 | 已完成安装、服务和管理连接验证，测试机继续保留 |
| firewalld | 1.0.0 | 通过 | Center stable 包可解析；测试机现有服务保持 active |

## 本次修复

- Panel 持久化每条商城版本的真实 channel，解析脚本包时不再错误使用全局 channel。
- Panel 增加 Web 服务和 MySQL 兼容数据库的互斥安装保护，任务入队前返回清晰提示。
- Nginx 1.31.0、Apache nghttp2、Java 18、Tomcat、PHP 8.3、PostgreSQL 18.1、
  MongoDB 8.0.17 和 MariaDB 10.11 增加官方来源、固定 SHA-256 与持久缓存。
- MongoDB 官方 Ubuntu 24.04 包会转换为 OneinStack 期待的归档目录名，并补充 mongosh。
- MongoDB 8.0 增加 AVX CPU 预检，避免以 `SIGILL` 崩溃。
- Java 修复多版本切换、卸载标记和 `/usr/lib/jvm` 权限。
- Tomcat 修复 OpenSSL 路径、APR/NIO 协议、启动健康检查和卸载残留。
- firewalld 正式组件包加入 Center，安全页在本地无 bundled 包时仍可从 Center 解析。

## 尚需补测

- 在一台没有 MySQL/MariaDB/Percona 数据的干净 Ubuntu 24.04 主机上，对 MariaDB 10.11、
  Percona 8.0 做真实安装、初始化、登录和卸载测试。
- 在提供 AVX 的 amd64 主机上完成 MongoDB 8.0.17 的启动、用户认证和卸载测试。
- PHP 5.3—8.5 的全部小版本矩阵尚未逐版编译；本轮验证代表推荐的 PHP 8.3 安装链路。

离线源包与校验值见
[`artifacts/software-source-packages`](./artifacts/software-source-packages/README.md)。
