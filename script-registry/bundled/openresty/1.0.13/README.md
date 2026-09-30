# OpenResty 稳定组件包

组件包版本：1.0.13。支持 OpenResty 1.27.1.2、1.31.1.1，推荐版本为 1.31.1.1。

组件直接构建官方源码，原生二进制位于 /usr/local/openresty/nginx/sbin/nginx，主配置位于
/usr/local/openresty/nginx/conf/nginx.conf。源码归档、分离签名、SHA-256 与发布密钥完整指纹
25451EB088460026195BD62CB550E09EA0E98066 均为固定输入。完整发布公钥随组件包分发，
避免 CentOS 7、Rocky Linux 9 等系统的 GnuPG 拒绝导入或忽略缺少 User ID 的远端公钥。
CentOS 7 构建会为内置 lua-cjson 显式启用 GNU C99，并使用 EPEL 的
openssl11-devel，避免系统 OpenSSL 1.0.2 缺少 SSL_get_client_random 导致最终链接失败。

## 服务器支持矩阵

| 系统 | 版本 | 包管理器 | 架构 |
| --- | --- | --- | --- |
| Ubuntu | 22.04、24.04、26.04 | apt | amd64、arm64 |
| Debian | 11、12、13 | apt | amd64、arm64 |
| RHEL / Rocky / AlmaLinux / Oracle Linux | 8、9、10 | dnf | amd64、arm64 |
| CentOS | 7、8、9、10 | 7 使用 yum，其余优先 dnf | amd64、arm64 |
| Fedora | * | dnf | amd64、arm64 |
| Amazon Linux | 2023 | dnf | amd64、arm64 |
| SLES | 15、16 | zypper | amd64、arm64 |
| openSUSE Leap / Tumbleweed / openSUSE | * | zypper | amd64、arm64 |

该矩阵是兼容性声明。只有为对应系统、版本、架构完成在线、完全离线、升级、
回滚、监听端口与 HTTP 实机验收后，才能将该组合标记为“已验证”。

## 安装模式

- Center 在线模式下载所选官方源码与签名，使用组件包内固定的完整指纹公钥验证，
  并从系统仓库安装已声明的编译依赖。
- 完全离线模式只接受与软件版本、OS、OS 版本、架构完全一致的 Bundle；校验
  files.sha256、源码 SHA-256 和 PGP 签名，仅安装 Bundle 内的本地依赖包，
  不刷新仓库、不访问上游、不允许网络回退。

## 安装前参数

| 参数 | 默认值 | 约束 |
| --- | --- | --- |
| SOFTWARE_VERSION | 1.31.1.1 | 仅允许 1.27.1.2、1.31.1.1 |
| OPENRESTY_PORT | 80 | 1–65535，预检与部署前复核占用 |
| PHP_FPM_SOCKET | /dev/shm/php-cgi.sock | 规范化绝对路径 |
| INSTALL_DIR | /usr/local/openresty | 规范化绝对路径，安装后不可在线迁移 |
| WEB_ROOT | /data/wwwroot | 受管绝对路径 |
| LOG_DIR | /data/wwwlogs | 受管绝对路径 |
| WEB_VHOST_ROOT | /usr/local/one/vhost | Panel 后端注入，不接受请求覆盖 |
| RUN_USER / RUN_GROUP | www | 合法系统账号标识 |

ONEINSTACK_INSTALL_MODE、ONEINSTACK_OFFLINE_PACKAGE_PATH、
ONEINSTACK_COMPONENT_STATE、UNINSTALL_DATA_POLICY 和
UNINSTALL_CONFIRM_DATA_DELETION 是后端控制字段，不向请求方开放覆盖。

## 安装后配置参数

configGet/configApply 使用 revision 乐观锁，发布候选配置前后均执行原生
nginx -t，原子替换后按 reload 生效；失败恢复主配置、默认站点和服务状态。

| 参数 | 默认值或范围 |
| --- | --- |
| workerProcesses | auto，或 1–99 |
| workerConnections | 4096，范围 512–65535 |
| keepaliveTimeout | 65 秒，范围 5–300 |
| clientMaxBodySize | 1 MB，范围 1–10240 |
| openrestyPort | 80，范围 1–65535 |
| phpFpmSocket | /dev/shm/php-cgi.sock |
| installDir | /usr/local/openresty，只读约束为不可在线迁移 |
| webRoot / logDir | /data/wwwroot、/data/wwwlogs |
| runUser / runGroup | www、www |

配置读取同时返回 runtime.port、runtime.installDir、runtime.logDir、
runtime.runUser、runtime.runGroup；状态输出 can_reload=true。

## 生命周期与 phpMyAdmin

仅允许 1.27.1.2 到 1.31.1.1 的前向升级，拒绝降级。安装目录、配置、service unit、
安装参数与启停/启用状态会进入回滚点；验证精确版本、配置语法、systemd、
目标进程、实际端口和本机 HTTP 2xx 后才提交版本状态并删除回滚点。

检测到 phpMyAdmin 但 PHP-FPM 服务或 socket 不可用时，安装保持成功并向
stderr 和进度事件发出：

```text
WARNING: OPENRESTY_PHPMYADMIN_INTEGRATION_PENDING: phpMyAdmin was detected, but PHP-FPM socket /dev/shm/php-cgi.sock is unavailable. OpenResty installation succeeded; phpMyAdmin access was not verified.
```

PHP-FPM 可用时继续探测临时 PHP 页面与 /phpMyAdmin/index.php；任一探测失败
仍使用同一告警码，不能宣称 phpMyAdmin 已验证。
