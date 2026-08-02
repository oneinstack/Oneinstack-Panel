# 软件安装离线源包

这里保存软件商城原生安装审计中发现镜像不可用后，从软件官方来源下载的源码包或二进制包。
这些文件不是 Center 组件脚本包；上传到自建源时应保持文件名不变，并使用同目录
`SHA256SUMS` 校验上传前后的内容。

当前总容量约 879 MiB，包含：

- Nginx 1.31.0 官方源码包。
- Apache 2.4.66 所需 nghttp2 1.64.0。
- Eclipse Temurin OpenJDK 18.0.2.1。
- MariaDB 10.11.15 官方 systemd 二进制包。
- MongoDB 8.0.17 Ubuntu 24.04 二进制包与 mongosh 2.3.1。
- PHP 8.3.29 以及本次安装使用的依赖源码包。
- PostgreSQL 18.1 官方源码包。
- Tomcat 7、8、9、10、11 对应的官方归档包。

MongoDB 官方文件名包含 `ubuntu2404`。Center 组件包会在安装工作目录中安全地重打包为
OneinStack 期待的 `mongodb-linux-x86_64-8.0.17.tgz`，不会修改这里保存的原始官方包。

校验命令：

```bash
cd artifacts/software-source-packages
shasum -a 256 -c SHA256SUMS
```
