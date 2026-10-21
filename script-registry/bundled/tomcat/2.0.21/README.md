# Apache Tomcat 2.0.18

该组件使用独立的 Tomcat 生命周期脚本，不调用历史 OneinStack Java/Tomcat 安装器。

- Tomcat 7/8.5 固定 Temurin JDK 8，Tomcat 9/10.1/11 固定 Temurin JDK 17。
- 在线模式只下载 Manifest 中声明的固定归档并校验 SHA-256。
- 离线模式只读取 Bundle 中的脚本、归档和目标系统依赖，不刷新仓库，也不会网络回退。
- 归档 JDK 的动态库目录由受管 ldconfig 配置和 systemd 环境同时注入，避免 `libjli.so` 无法装载。
- 安装时统一修正 JDK 文件权限，确保受管 `tomcat` 用户可以读取 Java 动态库和运行时文件。
- 启动前以受管 `tomcat` 用户执行 Java 预检，并对受限 `/data` 父目录只增加最小 `--x` ACL；失败回滚和卸载只移除本组件写入且未被外部修改的 ACL。
- SELinux 启用时使用系统 `restorecon` 恢复 Tomcat、JDK、unit 和动态库配置的标准上下文，避免从状态目录移动制品后保留错误标签。
- Tomcat 程序文件由 root 管理并设为只读可读，`bin` 脚本保持可执行，确保运行用户能够加载全部 JAR。
- 程序目录为 `/usr/local/tomcat`，配置、应用和状态目录为 `/data/tomcat`。
- 安装或升级会把旧版遗留的 `/data/tomcat`、受管子目录或 `server.xml` 符号链接复制为组件自有实体目录；外部链接目标保持不变，失败时恢复原链接。
- 安装、升级和回滚会同步保存及恢复 `server.xml`、`setenv.sh`、systemd unit 和安装状态，避免服务失败后留下半更新配置。
- 默认卸载保留 `/data/tomcat`；删除数据必须显式传入 `UNINSTALL_CONFIRM_DATA_DELETION=true`。

离线 Bundle 使用：

```text
scripts/build-offline-bundle.sh OUTPUT SOFTWARE_VERSION OS_ID OS_VERSION ARCH SOURCE_DIR PACKAGE_DIR
```

Bundle 建议由与目标系统、版本、架构一致的主机生成，`PACKAGE_DIR` 只放该平台依赖闭包。
