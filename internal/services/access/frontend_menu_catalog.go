package access

// builtinButtonItem describes a frontend button menu entry. The frontend key
// is kept as the action target (button.<action>) so the backend remains the
// single source of truth for menu visibility and button authorization.
type builtinButtonItem struct {
	Action string
	Name   string
}

type builtinButtonDefinition struct {
	Action     string
	Name       string
	Permission string
}

// builtinFrontendButtonDefinitions is the canonical button catalog consumed
// by the current frontend permission contract. Module tabs and resource types
// intentionally share the same action keys where the contract defines a
// common operation.
func builtinFrontendButtonDefinitions() []builtinButtonDefinition {
	item := func(action, name string) builtinButtonItem {
		return builtinButtonItem{Action: action, Name: name}
	}
	definitions := make([]builtinButtonDefinition, 0, 208)
	add := func(permission string, items ...builtinButtonItem) {
		for _, item := range items {
			definitions = append(definitions, builtinButtonDefinition{
				Action: item.Action, Name: item.Name, Permission: permission,
			})
		}
	}

	add(PermissionWebsiteRead,
		item("website.read", "查看"),
		item("website.webserver.read", "Web服务-查看状态"),
		item("website.webserver.config.read", "Web服务-查看配置"),
	)
	add(PermissionWebsiteWrite,
		item("website.create", "添加站点"),
		item("website.update", "编辑站点"),
		item("website.toggle", "启停站点"),
		item("website.ssl", "SSL管理"),
		item("website.backup", "备份"),
		item("website.restore", "恢复备份"),
		item("website.delete", "删除站点"),
		item("website.settings", "站点设置"),
		item("website.config", "配置文件"),
		item("website.webserver.manage", "Web服务-启停重载"),
		item("website.webserver.config.write", "Web服务-修改配置"),
	)

	add(PermissionDatabaseRead,
		item("database.mysql.read", "MySQL-查看"),
		item("database.mysql.account.read", "MySQL-查看账号"),
		item("database.redis.read", "Redis-查看"),
		item("database.remote.read", "远程数据库-查看"),
	)
	add(PermissionDatabaseWrite,
		item("database.mysql.create", "MySQL-添加数据库"),
		item("database.mysql.account.update", "MySQL-修改密码"),
		item("database.mysql.backup", "MySQL-备份"),
		item("database.mysql.restore", "MySQL-恢复备份"),
		item("database.mysql.delete", "MySQL-删除数据库"),
		item("database.mysql.phpmyadmin", "MySQL-phpMyAdmin管理"),
		item("database.remote.create", "远程数据库-添加连接"),
		item("database.remote.update", "远程数据库-编辑连接"),
		item("database.remote.test", "远程数据库-测试连接"),
		item("database.remote.sync", "远程数据库-同步数据"),
		item("database.remote.delete", "远程数据库-移除连接"),
	)

	add(PermissionSoftwareRead,
		item("software.read", "软件商店-查看"),
	)
	add(PermissionServiceRead,
		item("software.service.read", "软件服务-查看状态"),
		item("software.service.config.read", "软件服务-查看配置"),
	)
	add(PermissionTaskReadSelf,
		item("software.task.read", "软件任务-查看进度"),
		item("software.task.log", "软件任务-查看日志"),
	)
	add(PermissionSoftwareWrite,
		item("software.catalog.sync", "软件商店-同步软件目录"),
		item("software.install", "软件商店-安装软件"),
		item("software.update", "软件商店-更新软件"),
		item("software.uninstall", "软件商店-卸载软件"),
		item("software.task.retry", "软件任务-重试任务"),
	)
	add(PermissionServiceWrite,
		item("software.service.manage", "软件服务-启停重启重载"),
		item("software.service.config.write", "软件服务-修改配置"),
	)
	add(PermissionTaskCancelSelf, item("software.task.cancel", "软件任务-取消任务"))

	add(PermissionContainerRead,
		item("container.read", "容器管理-查看"),
		item("container.networkRead", "容器管理-查看网络"),
		item("container.volumeRead", "容器管理-查看存储卷"),
		item("container.composeRead", "容器管理-查看 Compose 项目"),
		item("container.templateRead", "容器管理-查看模板"),
		item("container.registryRead", "容器管理-查看镜像仓库"),
		item("container.configRead", "容器管理-查看 Docker 配置"),
		item("container.imageDetail", "容器管理-镜像详情"),
		item("container.exportImage", "容器管理-导出镜像"),
	)
	add(PermissionContainerWrite,
		item("container.create", "容器管理-创建容器"),
		item("container.update", "容器管理-编辑容器"),
		item("container.start", "容器管理-启动"),
		item("container.stop", "容器管理-停止"),
		item("container.restart", "容器管理-重启"),
		item("container.pause", "容器管理-暂停"),
		item("container.unpause", "容器管理-恢复运行"),
	)
	add(PermissionContainerDelete,
		item("container.delete", "容器管理-删除容器"),
		item("container.deleteImage", "容器管理-删除镜像"),
	)
	add(PermissionContainerForceAction, item("container.kill", "容器管理-强制停止"))
	add(PermissionContainerLogsRead, item("container.logs", "容器管理-查看日志"))
	add(PermissionContainerImageWrite,
		item("container.pullImage", "容器管理-拉取镜像"),
		item("container.importImage", "容器管理-导入镜像"),
		item("container.buildImage", "容器管理-构建镜像"),
		item("container.tagImage", "容器管理-镜像标签"),
		item("container.pushImage", "容器管理-推送镜像"),
	)
	add(PermissionContainerNetworkWrite,
		item("container.networkConnect", "容器管理-连接网络"),
		item("container.networkCreate", "容器管理-创建网络"),
		item("container.networkDelete", "容器管理-删除网络"),
	)
	add(PermissionContainerVolumeWrite,
		item("container.volumeCreate", "容器管理-创建存储卷"),
		item("container.volumeDelete", "容器管理-删除存储卷"),
	)
	add(PermissionContainerComposeWrite,
		item("container.composeCreate", "容器管理-创建 Compose 项目"),
		item("container.composeUpdate", "容器管理-编辑 Compose 项目"),
		item("container.composeStart", "容器管理-启动 Compose 项目"),
		item("container.composeStop", "容器管理-停止 Compose 项目"),
		item("container.composeRestart", "容器管理-重启 Compose 项目"),
		item("container.composeUpdateProject", "容器管理-更新 Compose 项目"),
		item("container.composeDelete", "容器管理-删除 Compose 项目"),
		item("container.templateCreate", "容器管理-创建模板"),
		item("container.templateUpdate", "容器管理-编辑模板"),
		item("container.templateDelete", "容器管理-删除模板"),
		item("container.templateDeploy", "容器管理-模板部署"),
	)
	add(PermissionContainerRegistryWrite,
		item("container.registryCreate", "容器管理-添加镜像仓库"),
		item("container.registryUpdate", "容器管理-编辑镜像仓库"),
		item("container.registryDelete", "容器管理-删除镜像仓库"),
		item("container.registryTest", "容器管理-测试镜像仓库"),
	)
	add(PermissionContainerConfigWrite, item("container.configWrite", "容器管理-修改 Docker 配置"))
	add(PermissionContainerDangerousCleanup,
		item("container.cleanupImage", "容器管理-清理镜像"),
		item("container.cleanupBuildCache", "容器管理-清理构建缓存"),
		item("container.cleanup", "容器管理-清理资源"),
	)

	add(PermissionFileRead, item("file.read", "文件管理-查看文件"))
	add(PermissionFileCreate, item("file.create", "文件管理-上传及新建"))
	add(PermissionFileEdit, item("file.edit", "文件管理-编辑文件"))
	add(PermissionFileModify, item("file.modify", "文件管理-修改权限及属性"))
	add(PermissionFileMove, item("file.move", "文件管理-移动文件"))
	add(PermissionFileDelete, item("file.delete", "文件管理-删除文件"))
	add(PermissionFileArchive, item("file.archive", "文件管理-压缩及解压文件"))
	add(PermissionFileShare, item("file.share", "文件管理-分享文件"))

	add(PermissionCronRead,
		item("task.read", "查看计划任务列表"),
		item("task.log.read", "查看计划任务日志"),
	)
	add(PermissionCronWrite,
		item("task.create", "添加计划任务"),
		item("task.update", "修改计划任务"),
		item("task.toggle", "启用或禁用计划任务"),
		item("task.execute", "立即执行计划任务"),
		item("task.delete", "删除计划任务"),
		item("task.batch.start", "批量启动计划任务"),
		item("task.batch.stop", "批量停止计划任务"),
		item("task.batch.delete", "批量删除计划任务"),
	)

	add(PermissionMonitoringRead,
		item("monitor.rule.read", "告警规则-查看"),
		item("monitor.event.read", "告警事件-查看"),
		item("monitor.channel.read", "通知通道-查看"),
		item("monitor.record.read", "投递记录-查看"),
	)
	add(PermissionMonitoringWrite,
		item("monitor.rule.create", "告警规则-新建"),
		item("monitor.rule.update", "告警规则-编辑"),
		item("monitor.rule.delete", "告警规则-删除"),
		item("monitor.rule.silence", "告警规则-静默"),
		item("monitor.event.handle", "告警事件-处理"),
		item("monitor.channel.create", "通知通道-新建"),
		item("monitor.channel.update", "通知通道-编辑"),
		item("monitor.channel.delete", "通知通道-删除"),
		item("monitor.channel.test", "通知通道-测试"),
	)

	add(PermissionBastionRead,
		item("bastion.read", "查看堡垒机资源"),
		item("bastion.server.test", "测试服务器连接"),
		item("bastion.server.detail", "查看服务器详情"),
		item("bastion.session.read", "查看堡垒机会话"),
	)
	add(PermissionBastionWrite,
		item("bastion.server.create", "添加服务器"),
		item("bastion.server.update", "修改服务器"),
		item("bastion.server.delete", "删除服务器"),
		item("bastion.server.collect.update", "修改服务器采集设置"),
	)
	add(PermissionBastionIdentityRead, item("bastion.session.access", "发起堡垒机连接"))

	add(PermissionSecurityRead,
		item("security.firewall.read", "系统防火墙-查看"),
		item("security.firewall.port-rule.read", "端口规则-查看"),
		item("security.intrusion.read", "入侵防御-查看"),
	)
	add(PermissionSecurityWrite,
		item("security.firewall.toggle", "系统防火墙-启用或关闭"),
		item("security.firewall.ping.update", "系统防火墙-修改Ping响应策略"),
		item("security.firewall.cache.clear", "系统防火墙-清理缓存"),
		item("security.firewall.port-rule.create", "端口规则-添加"),
		item("security.firewall.port-rule.update", "端口规则-编辑"),
		item("security.firewall.port-rule.delete", "端口规则-删除"),
		item("security.firewall.port-rule.import", "端口规则-导入"),
		item("security.firewall.port-rule.export", "端口规则-导出"),
		item("security.firewall.ip-rule.manage", "IP规则-管理"),
		item("security.firewall.port-forward.manage", "端口转发-管理"),
		item("security.firewall.region-rule.manage", "地区规则-管理"),
		item("security.firewall.malicious-ip.manage", "恶意IP自动封禁-管理"),
		item("security.intrusion.manage", "入侵防御-配置管理"),
	)

	add(PermissionCertificateRead,
		item("certificate.cert.read", "证书-查看"),
		item("certificate.cert.detail", "证书-查看详情"),
		item("certificate.task.read", "任务-查看"),
		item("certificate.dns-account.read", "DNS账号-查看"),
	)
	add(PermissionCertificateWrite,
		item("certificate.cert.apply", "证书-申请"),
		item("certificate.cert.upload", "证书-上传"),
		item("certificate.cert.create.self-signed", "证书-创建自签证书"),
		item("certificate.cert.renew", "证书-续签"),
		item("certificate.cert.bind.website", "证书-绑定网站"),
		item("certificate.cert.download", "证书-下载"),
		item("certificate.cert.delete", "证书-删除"),
		item("certificate.task.manage", "任务-管理"),
		item("certificate.dns-account.manage", "DNS账号-管理"),
	)

	add(PermissionApprovalRead,
		item("approval.read.mine", "查看我的申请"),
		item("approval.read.all", "查看全部申请"),
		item("approval.detail", "查看审批详情"),
		item("approval.payload.read", "查看申请参数快照与执行结果"),
	)
	add(PermissionApprovalReview,
		item("approval.approve", "审批通过"),
		item("approval.reject", "审批拒绝"),
	)

	add(PermissionConfigSnapshotRead,
		item("config.snapshot.read", "查看快照列表"),
		item("config.snapshot.detail", "查看快照详情"),
		item("config.snapshot.diff.read", "查看配置差异"),
		item("config.snapshot.resource.read", "查看可快照资源"),
		item("config.snapshot.restore.preview", "查看回滚预览"),
	)
	add(PermissionConfigSnapshotWrite,
		item("config.snapshot.create", "创建配置快照"),
		item("config.snapshot.restore", "执行配置回滚"),
		item("config.snapshot.restore.force", "强制配置回滚"),
		item("config.snapshot.delete", "删除配置快照"),
	)

	add(PermissionSystemRead,
		item("user.user.read", "用户管理-查看列表"),
		item("user.permission.read", "权限管理-查看角色列表"),
		item("panel.appearance.read", "界面设置-查看"),
		item("panel.settings.read", "面板设置-查看"),
		item("panel.network.read", "访问方式-查看"),
		item("panel.account-security.read", "安全设置-查看"),
		item("panel.backup.read", "备份还原-查看"),
		item("panel.update.read", "面板更新-查看更新状态"),
	)
	add(PermissionSystemWrite,
		item("user.user.create", "用户管理-新建用户"),
		item("user.user.role.assign", "用户管理-分配角色"),
		item("user.user.password.reset", "用户管理-重置密码"),
		item("user.user.delete", "用户管理-删除用户"),
		item("user.permission.role.create", "权限管理-新建角色"),
		item("user.permission.role.update", "权限管理-编辑角色"),
		item("user.permission.role.assign", "权限管理-配置菜单与权限码"),
		item("user.permission.role.delete", "权限管理-删除角色"),
		item("panel.appearance.update", "界面设置-修改外观"),
		item("panel.settings.alias.update", "面板设置-修改别名"),
		item("panel.settings.username.update", "面板设置-修改登录账号"),
		item("panel.settings.password.update", "面板设置-修改面板密码"),
		item("panel.settings.entry.update", "面板设置-修改安全入口"),
		item("panel.network.update", "访问方式-修改监听与访问配置"),
		item("panel.account-security.totp.setup", "安全设置-启用双因素认证"),
		item("panel.account-security.totp.disable", "安全设置-关闭双因素认证"),
		item("panel.account-security.recovery-codes.regenerate", "安全设置-重新生成恢复码"),
		item("panel.account-security.session.revoke", "安全设置-注销登录会话"),
		item("panel.backup.create", "备份还原-创建面板备份"),
		item("panel.backup.import", "备份还原-导入面板备份"),
		item("panel.backup.restore", "备份还原-恢复面板备份"),
		item("panel.backup.delete", "备份还原-删除面板备份"),
		item("panel.update.check", "面板更新-检查更新"),
		item("panel.update.apply", "面板更新-执行更新"),
	)
	add(PermissionSystemRead, item("panel.backup.download", "备份还原-下载面板备份"))

	return definitions
}

func builtinActionPermissionCatalog() map[string]string {
	definitions := builtinFrontendButtonDefinitions()
	result := make(map[string]string, len(definitions))
	for _, definition := range definitions {
		result[definition.Action] = definition.Permission
	}
	return result
}

// BuiltinButtonPermissions exposes the canonical frontend button contract to
// the authorization matrix without exposing the mutable internal map.
func BuiltinButtonPermissions() map[string]string {
	return builtinActionPermissionCatalog()
}

func lookupBuiltinActionLabel(action string) (builtinActionLabel, bool) {
	for _, definition := range builtinFrontendButtonDefinitions() {
		if definition.Action == action {
			label := builtinActionLabel{Name: definition.Name, NameEn: definition.Action}
			if existing, ok := builtinActionLabels[action]; ok {
				label.NameEn = existing.NameEn
			}
			return label, true
		}
	}
	return builtinActionLabel{}, false
}
