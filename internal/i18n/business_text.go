package i18n

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

// LocalizeBusinessText translates backend-generated display text. User input,
// machine-readable status/action values, paths, and raw logs must not be passed
// through this function.
func LocalizeBusinessText(locale, value string) string {
	if Canonical(locale) != LocaleEnUS || strings.TrimSpace(value) == "" {
		return value
	}
	if translated, ok := englishBusinessTexts[value]; ok {
		return translated
	}
	if translated, ok := englishOperationPreviewBusinessTexts[value]; ok {
		return translated
	}
	if translated := translateWebServerPreviewText(value); translated != "" {
		return translated
	}
	if translated := translateWebsiteRuntimePreviewText(value); translated != "" {
		return translated
	}
	if translated := translateTomcatUpgradePreviewText(value); translated != "" {
		return translated
	}
	if translated := translateSoftwareLifecyclePreviewText(value); translated != "" {
		return translated
	}
	if strings.HasPrefix(value, "计划任务：") {
		return "Scheduled task: " + strings.TrimSpace(strings.TrimPrefix(value, "计划任务："))
	}
	if strings.HasPrefix(value, "组件服务：") {
		return "Component service: " + strings.TrimSpace(strings.TrimPrefix(value, "组件服务："))
	}
	if strings.HasPrefix(value, "计划任务 #") {
		return "The scheduled-task execution failed or timed out. Check the execution record for details."
	}
	if strings.HasPrefix(value, "正在验证域名 ") {
		return "Validating domain " + strings.TrimSpace(strings.TrimPrefix(value, "正在验证域名 "))
	}
	for zh, en := range softwareOperationNames {
		switch value {
		case "任务已进入" + zh + "队列":
			return "The " + en + " task has been queued"
		case "正在取消" + zh + "任务":
			return "Canceling the " + en + " task"
		case zh + "任务已取消", "排队中的" + zh + "任务已取消":
			return "The " + en + " task was canceled"
		case "正在解析并校验" + zh + "脚本包":
			return "Resolving and validating the " + en + " script package"
		}
	}
	for _, suffix := range []struct{ zh, en string }{
		{" 服务已恢复运行", " service has recovered"},
		{" 服务仍然异常", " service remains unhealthy"},
		{" 服务连续探测异常", " service failed consecutive health checks"},
	} {
		if strings.Contains(value, suffix.zh) {
			name := strings.TrimSpace(strings.SplitN(value, suffix.zh, 2)[0])
			return name + suffix.en
		}
	}
	for _, prefix := range englishBusinessPrefixes {
		if strings.HasPrefix(value, prefix.zh) {
			return prefix.en
		}
	}
	return value
}

// LocalizeComponentConfigurationText localizes component configuration
// labels, descriptions, and units in both directions. Component manifests
// use stable English text, while older built-in definitions use Chinese; the
// API boundary must support either source representation.
func LocalizeComponentConfigurationText(locale, component, key, value string) string {
	if strings.TrimSpace(value) == "" {
		return value
	}
	if Canonical(locale) == LocaleEnUS {
		return LocalizeBusinessText(locale, value)
	}
	if strings.EqualFold(strings.TrimSpace(component), "redis") {
		if translated, ok := redisConfigurationChineseText[strings.TrimSpace(value)]; ok {
			return translated
		}
		if translated, ok := redisConfigurationChineseText[strings.TrimSpace(key)]; ok {
			return translated
		}
	}
	if strings.EqualFold(strings.TrimSpace(component), "adminer") {
		if translated, ok := adminerConfigurationChineseText[strings.TrimSpace(value)]; ok {
			return translated
		}
		if translated, ok := adminerConfigurationChineseText[strings.TrimSpace(key)]; ok {
			return translated
		}
	}
	return value
}

// LocalizeComponentInstallParameterLabel gives install parameters a human
// readable label without changing their stable machine key.
func LocalizeComponentInstallParameterLabel(locale, component, key, fallback string) string {
	if Canonical(locale) != LocaleEnUS {
		if translated, ok := softwareInstallParameterChineseLabels[fallback]; ok {
			return translated
		}
	}
	if strings.EqualFold(strings.TrimSpace(component), "redis") {
		if translated, ok := redisInstallParameterChineseLabels[strings.ToUpper(strings.TrimSpace(key))]; ok &&
			Canonical(locale) != LocaleEnUS {
			return translated
		}
	}
	if strings.EqualFold(strings.TrimSpace(component), "adminer") {
		if translated, ok := adminerInstallParameterChineseLabels[strings.ToUpper(strings.TrimSpace(key))]; ok &&
			Canonical(locale) != LocaleEnUS {
			return translated
		}
	}
	return LocalizeComponentConfigurationText(locale, component, key, fallback)
}

var softwareInstallParameterChineseLabels = map[string]string{
	"Endpoint":   "服务端点",
	"Bucket":     "存储桶",
	"Region":     "区域",
	"Access key": "访问密钥 ID",
	"Secret key": "访问密钥",
}

var adminerConfigurationChineseText = map[string]string{
	"publicPath":              "公开访问路径",
	"accessPolicy":            "访问策略",
	"allowedCidrs":            "允许访问的 CIDR",
	"defaultDriver":           "默认数据库驱动",
	"defaultServer":           "默认数据库服务器",
	"Public path":             "公开访问路径",
	"Access policy":           "访问策略",
	"Allowed CIDRs":           "允许访问的 CIDR",
	"Default database driver": "默认数据库驱动",
	"Default database server": "默认数据库服务器",
	"Single-segment URL path. The old route is removed only after the new route passes HTTP verification.": "单层 URL 路径；新路径通过 HTTP 验证后才会移除旧路径。",
	"Public allows remote access; local and allowlist are recommended for production.":                     "public 允许远程访问；生产环境推荐使用 local 或 allowlist。",
	"Comma-separated IPv4/IPv6 CIDRs; required when the access policy is allowlist.":                       "逗号分隔的 IPv4/IPv6 CIDR；访问策略为 allowlist 时必填。",
	"MariaDB uses the mysql driver.":                                                           "MariaDB 使用 mysql 驱动。",
	"Host or IP with an optional port; credentials and URLs are forbidden.":                    "主机名或 IP，可带端口；禁止填写凭据或 URL。",
	"Exact Adminer version published by Center; only 6.1.0 is supported.":                      "Center 发布的 Adminer 精确版本；仅支持 6.1.0。",
	"Single-segment public URL path; must not conflict with another web route.":                "单层公开 URL 路径；不能与现有 Web 路由冲突。",
	"Access policy: public, local, or allowlist. Public exposes Adminer to remote clients.":    "访问策略可选 public、local 或 allowlist；public 会向远程客户端公开 Adminer。",
	"Comma-separated IPv4/IPv6 CIDRs; required when access policy is allowlist.":               "逗号分隔的 IPv4/IPv6 CIDR；访问策略为 allowlist 时必填。",
	"Default login driver: mysql or pgsql. MariaDB uses mysql.":                                "默认登录驱动可选 mysql 或 pgsql；MariaDB 使用 mysql。",
	"Default database host or IP, optionally with a port; credentials and URLs are forbidden.": "默认数据库主机名或 IP，可带端口；禁止填写凭据或 URL。",
}

var adminerInstallParameterChineseLabels = map[string]string{
	"SOFTWARE-VERSION":       "Adminer 版本",
	"ADMINER-PUBLIC-PATH":    "公开访问路径",
	"ADMINER-ACCESS-POLICY":  "访问策略",
	"ADMINER-ALLOWED-CIDRS":  "允许访问的 CIDR",
	"ADMINER-DEFAULT-DRIVER": "默认数据库驱动",
	"ADMINER-DEFAULT-SERVER": "默认数据库服务器",
}

var redisConfigurationChineseText = map[string]string{
	"port":                           "监听端口",
	"maxmemory":                      "最大内存",
	"maxmemoryPolicy":                "内存淘汰策略",
	"appendonly":                     "AOF 持久化",
	"timeout":                        "空闲连接超时",
	"tcpKeepalive":                   "TCP 保活",
	"Max memory":                     "最大内存",
	"Max memory policy":              "内存淘汰策略",
	"AOF persistence":                "AOF 持久化",
	"Idle connection timeout":        "空闲连接超时",
	"TCP keepalive":                  "TCP 保活",
	"TCP Keepalive":                  "TCP 保活",
	"Redis listener port":            "监听端口",
	"0 means no Redis memory limit.": "0 表示不设置 Redis 内存上限。",
	"How Redis handles new writes after reaching the memory limit.":        "达到内存上限后 Redis 处理新写入的方式。",
	"Appends write operations to the AOF file.":                            "将写操作追加到 AOF 文件。",
	"0 means idle clients are not disconnected automatically.":             "0 表示不主动断开空闲客户端。",
	"TCP keepalive probe interval; 0 disables it.":                         "TCP 保活探测间隔；0 表示关闭。",
	"Redis software version: 7.4.8 or 8.4.0.":                              "Redis 软件版本：7.4.8 或 8.4.0。",
	"Managed Redis program directory.":                                     "受管 Redis 程序目录。",
	"Persistent RDB/AOF data directory; preserved on uninstall.":           "持久化 RDB/AOF 数据目录；卸载时保留。",
	"Redis TCP listener port.":                                             "Redis TCP 监听端口。",
	"Space-separated IPv4/IPv6 listener addresses; local-only by default.": "以空格分隔的 IPv4/IPv6 监听地址；默认仅本机访问。",
	"Redis ACL login username.":                                            "Redis ACL 登录用户名。",
	"Optional Redis password; never returned in task output.":              "可选 Redis 密码；任务输出中不会返回明文。",
	"Managed component state and rollback directory.":                      "受管组件状态及回滚目录。",
	"seconds": "秒",
}

var redisInstallParameterChineseLabels = map[string]string{
	"SOFTWARE_VERSION":           "Redis 版本",
	"INSTALL_DIR":                "安装目录",
	"DATA_DIR":                   "数据目录",
	"REDIS_PORT":                 "监听端口",
	"REDIS_BIND":                 "监听地址",
	"REDIS_USERNAME":             "Redis 登录用户",
	"REDIS_PASSWORD":             "Redis 密码",
	"ONEINSTACK_COMPONENT_STATE": "组件状态目录",
}

func translateWebServerPreviewText(value string) string {
	const (
		syntaxSuffix   = " 配置语法"
		validatePrefix = "校验 "
		validateSuffix = " 配置"
		reloadPrefix   = "重新加载 "
	)
	if strings.HasSuffix(value, syntaxSuffix) {
		name := strings.TrimSpace(strings.TrimSuffix(value, syntaxSuffix))
		if name == "Web Server" {
			return name + " configuration syntax"
		}
	}
	if strings.HasPrefix(value, validatePrefix) && strings.HasSuffix(value, validateSuffix) {
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, validatePrefix), validateSuffix))
		if name == "Web Server" {
			return "Validate " + name + " configuration"
		}
	}
	if strings.HasPrefix(value, reloadPrefix) {
		name := strings.TrimSpace(strings.TrimPrefix(value, reloadPrefix))
		if name == "Web Server" {
			return "Reload " + name
		}
	}
	return ""
}

func translateWebsiteRuntimePreviewText(value string) string {
	const (
		prefix = "网站 "
		open   = "（"
		close  = "）"
		suffix = "将修改运行配置或流量路径，执行前需要确认"
	)
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, suffix) {
		return ""
	}
	body := strings.TrimSuffix(strings.TrimPrefix(value, prefix), suffix)
	openIndex := strings.Index(body, open)
	if openIndex <= 0 || !strings.HasSuffix(body, close) {
		return ""
	}
	domainEnd := len(body) - len(close)
	if domainEnd <= openIndex+len(open) {
		return ""
	}
	name := body[:openIndex]
	domain := body[openIndex+len(open) : domainEnd]
	return "Website " + name + " (" + domain + ") will modify the runtime configuration or traffic path. Confirmation is required before execution"
}

var (
	tomcatMajorUpgradePreviewPattern    = regexp.MustCompile(`^Tomcat (.+?) → (.+?) 为跨大版本升级；服务启动成功不代表应用兼容，请先备份应用并验证业务功能$`)
	tomcatRecordedUpgradePreviewPattern = regexp.MustCompile(`^安装记录显示 Tomcat (.+?) → (.+?) 可能跨大版本，但未能回读实际运行版本；请核对版本、备份应用并验证兼容性$`)
	tomcatRollbackPreviewPattern        = regexp.MustCompile(`^失败时脚本尝试从 (.+?) 恢复；旧程序：(.+?)；配置快照：(.+?)。/data/tomcat/webapps 不在此快照中，请升级前单独备份应用与数据；下次安装或升级会覆盖快照$`)
)

// Match complete backend templates so versions and configured snapshot paths
// are preserved without translating arbitrary content embedded in them.
func translateTomcatUpgradePreviewText(value string) string {
	if match := tomcatMajorUpgradePreviewPattern.FindStringSubmatch(value); match != nil {
		return fmt.Sprintf("Tomcat %s → %s is a major-version upgrade. A successful service start does not guarantee application compatibility. Back up applications and verify their functionality first", match[1], match[2])
	}
	if match := tomcatRecordedUpgradePreviewPattern.FindStringSubmatch(value); match != nil {
		return fmt.Sprintf("Installation records indicate that Tomcat %s → %s may cross major versions, but the running version could not be read. Check the actual version, back up applications, and verify compatibility", match[1], match[2])
	}
	if match := tomcatRollbackPreviewPattern.FindStringSubmatch(value); match != nil {
		return fmt.Sprintf("On failure, the script attempts to restore from %s; previous program: %s; configuration snapshot: %s. /data/tomcat/webapps is excluded from this snapshot. Back up applications and data separately before upgrading; the next installation or upgrade overwrites the snapshot", match[1], match[2], match[3])
	}
	return ""
}

var softwareLifecyclePreviewTemplates = []struct {
	pattern *regexp.Regexp
	format  string
}{
	{regexp.MustCompile(`^组件包 (.+?) 与 SHA-256 已固定，执行阶段使用 ResolveFixed$`), "Component package %s and its SHA-256 are fixed; execution uses ResolveFixed"},
	{regexp.MustCompile(`^卸载 (.+?) (.+?) 将按目标主机执行已列出的服务、软件包和目录变更$`), "Uninstalling %s %s will apply the listed service, package, and directory changes on the target host"},
	{regexp.MustCompile(`^卸载 (.+?) (.+?) 会执行旧版组件包声明的卸载动作并移除受管程序文件，执行前需要确认$`), "Uninstalling %s %s will run the uninstall action declared by the legacy component package and remove managed program files. Confirmation is required before execution"},
	{regexp.MustCompile(`^将对 (.+?) 执行 (start|stop|restart|reload)，目标服务和组件包已固定$`), "%s: run service action %s using the fixed target services and component package"},
	{regexp.MustCompile(`^已确认组件 (.+?)，软件版本 (.+?)，数据策略 (preserve|delete)$`), "Confirmed component %s, software version %s, data policy %s"},
	{regexp.MustCompile(`^对 (.+?) 执行 (start|stop|restart|reload)$`), "Run service action on %s: %s"},
	{regexp.MustCompile(`^停止并禁用 (.+)$`), "Stop and disable %s"},
	{regexp.MustCompile(`^卸载 (.+) ([0-9A-Za-z][0-9A-Za-z._+-]*)$`), "Uninstall %s %s"},
}

func translateSoftwareLifecyclePreviewText(value string) string {
	for _, template := range softwareLifecyclePreviewTemplates {
		match := template.pattern.FindStringSubmatch(value)
		if match == nil {
			continue
		}
		arguments := make([]any, len(match)-1)
		for index := 1; index < len(match); index++ {
			arguments[index-1] = match[index]
		}
		return fmt.Sprintf(template.format, arguments...)
	}
	return ""
}

func LocalizeStatusText(locale, value string, failed bool) string {
	translated := LocalizeBusinessText(locale, value)
	if Canonical(locale) != LocaleEnUS || !failed || !ContainsHan(translated) {
		return translated
	}
	translated = LocalizeText(locale, value)
	if ContainsHan(translated) {
		return "The operation failed. Check the related task or service logs for details."
	}
	return translated
}

// LocalizeResponseData keeps legacy task/status records compatible while new
// records are gradually migrated to stable message keys. It only translates
// well-known backend message fields and scalar success messages; names,
// descriptions, reasons, paths, and arbitrary user content are left untouched.
func LocalizeResponseData(locale string, data any) any {
	if Canonical(locale) != LocaleEnUS || data == nil {
		return data
	}
	localized := localizeResponseValue(locale, reflect.ValueOf(data), "", make(map[responseVisit]reflect.Value))
	if !localized.IsValid() {
		return data
	}
	return localized.Interface()
}

type responseVisit struct {
	typ     reflect.Type
	pointer uintptr
}

func localizeResponseValue(locale string, value reflect.Value, field string, visited map[responseVisit]reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		localized := localizeResponseValue(locale, value.Elem(), field, visited)
		result := reflect.New(value.Type()).Elem()
		if localized.IsValid() && localized.Type().AssignableTo(value.Elem().Type()) {
			result.Set(localized)
			return result
		}
		result.Set(value)
		return result
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := responseVisit{typ: value.Type(), pointer: value.Pointer()}
		if previous, ok := visited[visit]; ok {
			return previous
		}
		result := reflect.New(value.Type().Elem())
		visited[visit] = result
		result.Elem().Set(localizeResponseValue(locale, value.Elem(), field, visited))
		return result
	case reflect.Struct:
		result := reflect.New(value.Type()).Elem()
		result.Set(value)
		failed := structRepresentsFailure(value)
		for index := 0; index < value.NumField(); index++ {
			structField := value.Type().Field(index)
			if structField.PkgPath != "" || !result.Field(index).CanSet() {
				continue
			}
			name := responseFieldName(structField)
			if failed && (strings.EqualFold(name, "message") || strings.EqualFold(name, "statusMessage")) {
				name = "error"
			}
			result.Field(index).Set(localizeResponseValue(locale, value.Field(index), name, visited))
		}
		return result
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		failed := mapRepresentsFailure(value)
		iterator := value.MapRange()
		for iterator.Next() {
			key := iterator.Key()
			name := field
			if key.Kind() == reflect.String {
				name = key.String()
			}
			if failed && (strings.EqualFold(name, "message") || strings.EqualFold(name, "statusMessage")) {
				name = "error"
			}
			result.SetMapIndex(key, localizeResponseValue(locale, iterator.Value(), name, visited))
		}
		return result
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(localizeResponseValue(locale, value.Index(index), field, visited))
		}
		return result
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(localizeResponseValue(locale, value.Index(index), field, visited))
		}
		return result
	case reflect.String:
		if isAccessURLField(field) {
			localizedURL := strings.ReplaceAll(value.String(), "服务器IP", "your-server-ip")
			return reflect.ValueOf(localizedURL).Convert(value.Type())
		}
		// Diagnostic error fields contain the actionable cause of a failed
		// operation (for example, Docker's stderr). Keep them intact so
		// response localization does not replace the cause with a generic
		// failure message. User-facing summary fields are localized below.
		if isRawDiagnosticErrorField(field) {
			// Some task models use errorMessage for a safe, persisted summary
			// rather than raw diagnostics. Translate known summaries while
			// preserving unknown diagnostic text verbatim.
			if translated := LocalizeText(locale, value.String()); translated != value.String() {
				return reflect.ValueOf(translated).Convert(value.Type())
			}
			return value
		}
		if isBackendErrorField(field) {
			translated := LocalizeBusinessText(locale, value.String())
			if translated == value.String() {
				translated = LocalizeText(locale, value.String())
			}
			if ContainsHan(translated) {
				translated = "The operation failed. Check the related service status and server logs."
			}
			return reflect.ValueOf(translated).Convert(value.Type())
		}
		if field == "" || isBackendMessageField(field) {
			return reflect.ValueOf(LocalizeBusinessText(locale, value.String())).Convert(value.Type())
		}
	}
	return value
}

func structRepresentsFailure(value reflect.Value) bool {
	for index := 0; index < value.NumField(); index++ {
		field := value.Type().Field(index)
		name := strings.ToLower(responseFieldName(field))
		if (name == "errorcode" || name == "error_code") && value.Field(index).Kind() == reflect.String && strings.TrimSpace(value.Field(index).String()) != "" {
			return true
		}
		if (name == "status" || name == "state") && value.Field(index).Kind() == reflect.String {
			status := strings.ToLower(strings.TrimSpace(value.Field(index).String()))
			if status == "failed" || status == "error" || status == "interrupted" || status == "rollback_failed" || status == "recovery_required" {
				return true
			}
		}
	}
	return false
}

func mapRepresentsFailure(value reflect.Value) bool {
	iterator := value.MapRange()
	for iterator.Next() {
		key, item := iterator.Key(), iterator.Value()
		if key.Kind() != reflect.String {
			continue
		}
		name := strings.ToLower(key.String())
		if item.Kind() == reflect.Interface && !item.IsNil() {
			item = item.Elem()
		}
		if (name == "errorcode" || name == "error_code") && item.IsValid() && item.Kind() == reflect.String && strings.TrimSpace(item.String()) != "" {
			return true
		}
		if (name == "status" || name == "state") && item.IsValid() && item.Kind() == reflect.String {
			status := strings.ToLower(strings.TrimSpace(item.String()))
			if status == "failed" || status == "error" || status == "interrupted" || status == "rollback_failed" || status == "recovery_required" {
				return true
			}
		}
	}
	return false
}

func isRawDiagnosticErrorField(field string) bool {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "errormessage", "error_message":
		return true
	default:
		return false
	}
}

func isBackendErrorField(field string) bool {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "error", "lasterror", "last_error", "failuremessage", "failure_message", "recoverymessage", "recovery_message":
		return true
	default:
		return false
	}
}

func responseFieldName(field reflect.StructField) string {
	if tag := strings.Split(field.Tag.Get("json"), ",")[0]; tag != "" && tag != "-" {
		return tag
	}
	return field.Name
}

func isBackendMessageField(field string) bool {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "message", "errormessage", "error_message", "recoverymessage", "recovery_message",
		"failuremessage", "failure_message", "statusmessage", "status_message", "rulename", "rule_name":
		return true
	default:
		return false
	}
}

func isAccessURLField(field string) bool {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "httpaccessurl", "httpsaccessurl", "panelaccessurl", "httpurl", "httpsurl":
		return true
	default:
		return false
	}
}

var englishBusinessPrefixes = []struct {
	zh string
	en string
}{
	{"新访问配置启动或健康检查失败: 重启后读取待应用配置:", "The Panel restarted, but the applied access configuration could not be read for verification."},
	{"新访问配置启动或健康检查失败: 重启后待应用配置已被其他操作替换", "The Panel restarted, but the access configuration changed before verification completed."},
	{"重启后读取待应用配置:", "The applied access configuration could not be read after the Panel restarted."},
	{"更新失败且自动回滚失败：", "The update failed, and automatic rollback also failed. Check the update service logs."},
	{"更新失败，已自动恢复旧版本：", "The update failed, and the previous version was restored automatically."},
	{"面板已从 ", "The Panel update completed successfully."},
	{"已解析组件脚本包", "The component script package was resolved."},
	{"回滚失败：", "Rollback failed. Check the task log for details."},
	{"Panel 正在关闭，", "The task was interrupted safely because the Panel is shutting down. Its component state will be verified on the next startup."},
	{"Panel 重启导致任务中断；", "The task was interrupted because the Panel restarted. Check the detected component state and task logs."},
	{"安全备份：", "Creating the safety backup."},
	{"删除前快照：", "Creating the pre-deletion snapshot."},
	{"恢复前快照：", "Creating the pre-restore snapshot."},
}

var softwareOperationNames = map[string]string{
	"安装": "installation",
	"升级": "upgrade",
	"卸载": "uninstall",
	"启动": "service start",
	"停止": "service stop",
	"重启": "service restart",
	"重载": "service reload",
	"配置": "configuration",
}

var englishBusinessTexts = map[string]string{
	"Adminer 版本必须选择 Center 发布的精确版本 6.1.0":             "Select the exact Adminer 6.1.0 version published by Center.",
	"Adminer 公开路径必须是单层安全 URL 路径，例如 /adminer/":         "Adminer public path must be one safe URL segment, such as /adminer/.",
	"Adminer 访问策略必须是 public、local 或 allowlist":        "Adminer access policy must be public, local, or allowlist.",
	"Adminer 访问策略为 allowlist 时必须填写允许访问的 CIDR":         "Allowed CIDRs are required when the Adminer access policy is allowlist.",
	"Adminer 最多允许配置 64 个 IPv4/IPv6 CIDR":              "Adminer allows at most 64 IPv4/IPv6 CIDRs.",
	"Adminer 允许访问的 CIDR 格式无效，请使用逗号分隔的 IPv4/IPv6 CIDR": "Adminer allowed CIDRs are invalid; use comma-separated IPv4/IPv6 CIDRs.",
	"Adminer 默认数据库驱动必须是 mysql 或 pgsql":                "Adminer default database driver must be mysql or pgsql.",
	"Adminer 默认数据库服务器必须是主机名或 IP，可带端口，且不能包含凭据或 URL":    "Adminer default database server must be a host or IP with an optional port and must not contain credentials or a URL.",
	"SSH 登录防护":         "SSH login protection",
	"检测 SSH 密码和认证失败事件": "Detects failed SSH password and authentication attempts",
	"Panel 登录防护":       "Panel login protection",
	"根据 Panel 安全审计中的连续登录失败生成事件": "Creates incidents from repeated Panel login failures in the security audit log",
	"Nginx HTTP 认证防护":      "Nginx HTTP authentication protection",
	"检测 Nginx HTTP 基础认证失败": "Detects failed Nginx HTTP basic-authentication attempts",
	"Nginx 恶意扫描防护":         "Nginx malicious scan protection",
	"检测针对常见敏感路径的恶意扫描":      "Detects malicious scans for common sensitive paths",
	"MySQL 登录防护":           "MySQL login protection",
	"Redis 认证防护":           "Redis authentication protection",
	"FTP 登录防护":             "FTP login protection",
	"SSH 登录防护（由旧自动封禁迁移）":   "SSH login protection (migrated from legacy automatic banning)",
	"服务器尚未安装 Fail2ban 组件":  "The Fail2ban component is not installed",
	"Fail2ban 服务未运行或无法响应":  "The Fail2ban service is not running or is not responding",
	"规则变更已排队":              "The policy change has been queued",
	"IP 处置任务已排队":           "The IP enforcement task has been queued",
	"任务已进入持久队列":            "The task has entered the durable queue",
	"服务重启后重新排队":            "The task was requeued after the service restarted",
	"正在校验实际状态":             "Validating the actual system state",
	"开始执行安全任务":             "The security task has started",
	"安全任务执行成功":             "The security task completed successfully",
	"安全任务执行失败":             "The security task failed",
	"成功":                   "Succeeded",
	"操作成功":                 "Operation succeeded",
	"重启后待应用配置已被其他操作替换":     "The access configuration changed after the Panel restarted.",
	"创建成功":                 "Created successfully",
	"更新成功":                 "Updated successfully",
	"删除成功":                 "Deleted successfully",
	"彻底删除成功":               "Permanently deleted successfully",
	"修改成功":                 "Updated successfully",
	"保存成功":                 "Saved successfully",
	"连接成功":                 "Connection succeeded",
	"已取消收藏":                "Favorite removed",
	"已取消外链分享":              "Public link revoked",
	"任务已进入队列":              "The task has been queued",
	"网站任务已进入队列":            "The website task has been queued",
	"网站任务开始执行":             "The website task has started",
	"数据库任务开始执行":            "The database task has started",
	"证书任务开始执行":             "The certificate task has started",
	"正在取消网站任务":             "Canceling the website task",
	"正在取消数据库任务":            "Canceling the database task",
	"正在取消容器任务":             "Canceling the container task",
	"正在取消证书任务":             "Canceling the certificate task",
	"任务已中断":                "The task was interrupted",
	"Panel 重启，网站任务已中断":     "The website task was interrupted because the Panel restarted",
	"Panel 停止，排队任务已中断":     "The queued task was interrupted because the Panel stopped",
	"Panel 重启，数据库任务已中断":    "The database task was interrupted because the Panel restarted",
	"Panel 重启，证书任务已中断":     "The certificate task was interrupted because the Panel restarted",
	"Panel 停止，网站任务已中断":     "The website task was interrupted because the Panel stopped",
	"Panel 停止，数据库任务已中断":    "The database task was interrupted because the Panel stopped",
	"Panel 停止，证书任务已中断":     "The certificate task was interrupted because the Panel stopped",
	"网站任务已取消":              "The website task was canceled",
	"数据库任务已取消":             "The database task was canceled",
	"证书任务已取消":              "The certificate task was canceled",
	"数据库恢复任务已进入队列":         "The database restore task has been queued",
	"证书签发超时":               "Certificate issuance timed out",
	"已解析组件脚本包":             "The component script package was resolved",
	"正在获取安装包":              "Retrieving the installation package",
	"获取安装包完成":              "Installation package retrieval completed",
	"正在环境预检":               "Running environment prechecks",
	"环境预检完成":               "Environment prechecks completed",
	"正在安装软件":               "Installing software",
	"安装软件完成":               "Software installation completed",
	"正在升级软件":               "Upgrading software",
	"升级软件完成":               "Software upgrade completed",
	"正在卸载软件":               "Uninstalling software",
	"卸载软件完成":               "Software uninstall completed",
	"正在启动服务":               "Starting the service",
	"启动服务完成":               "Service start completed",
	"正在停止服务":               "Stopping the service",
	"停止服务完成":               "Service stop completed",
	"正在重启服务":               "Restarting the service",
	"重启服务完成":               "Service restart completed",
	"正在重载服务":               "Reloading the service",
	"重载服务完成":               "Service reload completed",
	"正在写入配置":               "Writing configuration",
	"写入配置完成":               "Configuration write completed",
	"正在安全发布组件配置":           "Publishing component configuration safely",
	"安全发布组件配置完成":           "Safe component configuration publication completed",
	"正在启动并验证":              "Starting and validating",
	"启动并验证完成":              "Start and validation completed",
	"正在保存任务状态":             "Saving task status",
	"保存任务状态完成":             "Task status saved",
	"已收到取消请求":              "The cancellation request was received",
	"安装失败，正在回滚变更":          "Installation failed; rolling back changes",
	"回滚完成":                 "Rollback completed",
	"卸载完成，软件数据按组件策略保留":     "Uninstall completed; software data was retained according to component policy",
	"服务启动成功":               "The service started successfully",
	"服务停止成功":               "The service stopped successfully",
	"服务重启成功":               "The service restarted successfully",
	"服务重载成功":               "The service reloaded successfully",
	"配置已安全发布并验证成功":         "The configuration was published safely and validated successfully",
	"升级并验证成功":              "Upgrade and validation completed successfully",
	"安装并验证成功":              "Installation and validation completed successfully",
	"正在加载 ACME 账户":         "Loading the ACME account",
	"正在创建证书订单":             "Creating the certificate order",
	"域名验证完成，正在生成证书私钥":      "Domain validation completed; generating the certificate private key",
	"正在签发证书":               "Issuing the certificate",
	"证书签发完成，正在校验证书":        "Certificate issuance completed; validating the certificate",
	"正在安全写入证书文件":           "Writing certificate files safely",
	"正在验证并重新加载 Nginx":      "Validating and reloading Nginx",
	"正在准备数据库备份":            "Preparing the database backup",
	"备份文件校验完成":             "Backup file validation completed",
	"恢复源校验完成，正在创建恢复前安全备份":  "Restore source validation completed; creating a pre-restore safety backup",
	"安全备份已完成，开始恢复数据库":      "Safety backup completed; restoring the database",
	"数据库恢复完成":              "Database restore completed",
	"正在创建删除前强制快照":          "Creating the required pre-deletion snapshot",
	"快照已验证，正在删除网站配置":       "Snapshot validated; deleting the website configuration",
	"网站已安全删除，恢复快照已保留":      "The website was deleted safely and the recovery snapshot was retained",
	"正在校验并解压网站备份":          "Validating and extracting the website backup",
	"正在创建恢复前安全快照":          "Creating a pre-restore safety snapshot",
	"正在恢复网站文件":             "Restoring website files",
	"正在重新生成并发布 Nginx 配置":   "Regenerating and publishing the Nginx configuration",
	"正在恢复关联数据库":            "Restoring the associated database",
	"网站文件、配置和数据库恢复完成":      "Website files, configuration, and database restore completed",
	"正在导出关联数据库":            "Exporting the associated database",
	"正在打包网站文件与配置":          "Packaging website files and configuration",
	"网站备份已完成并通过完整性校验":      "Website backup completed and passed integrity validation",
	"正在连接 MySQL":           "Connecting to MySQL",
	"正在导出并压缩数据库":           "Exporting and compressing the database",
	"正在发布备份文件":             "Publishing the backup file",
	"数据库备份导出完成":            "Database backup export completed",
	"正在恢复数据库，请勿中断服务":       "Restoring the database; do not interrupt the service",
	"未配置组件状态探测器":           "No component status probe is configured",
	"容器任务失败":               "The container task failed",
	"容器任务执行成功":             "The container task completed successfully",
	"正在准备 Compose 项目":      "Preparing the Compose project",
	"正在创建 Compose 项目":      "Creating the Compose project",
	"正在保存 Compose 配置":      "Saving the Compose configuration",
	"正在拉取 Compose 镜像":      "Pulling Compose images",
	"正在启动 Compose 服务":      "Starting Compose services",
	"正在重启 Compose 服务":      "Restarting Compose services",
	"正在停止 Compose 服务":      "Stopping Compose services",
	"正在删除 Compose 资源":      "Deleting Compose resources",
	"Compose 项目创建并启动成功":    "The Compose project was created and started successfully",
	"Compose 配置保存成功":       "The Compose configuration was saved successfully",
	"Compose 服务启动成功":       "Compose services started successfully",
	"Compose 服务停止成功":       "Compose services stopped successfully",
	"Compose 服务重启成功":       "Compose services restarted successfully",
	"Compose 项目更新成功":       "The Compose project was updated successfully",
	"Compose 项目删除成功":       "The Compose project was deleted successfully",
	"Compose 配置校验失败":       "Compose configuration validation failed",
	"Compose 项目不存在":        "The Compose project does not exist",
	"Compose 项目已存在或状态冲突":   "The Compose project already exists or its state conflicts with this operation",
	"Compose 项目已有进行中的任务":   "Another task is already running for this Compose project",
	"Compose 预览已失效，请重新预览":  "The Compose preview is stale; create a new preview",
	"当前 Compose 项目使用多个配置文件，暂不支持编辑":  "This Compose project uses multiple configuration files and cannot be edited as a single YAML file",
	"Docker Compose 操作超时":           "The Docker Compose operation timed out",
	"Docker Compose 插件不可用":          "The Docker Compose plugin is unavailable",
	"Docker 运行时不可用":                 "The Docker runtime is unavailable",
	"Docker 未安装或不在 PATH 中":          "Docker is not installed or is not available in PATH",
	"容器日志追踪已中断，请检查容器状态和 Docker 运行时": "Container log streaming was interrupted. Check the container and Docker runtime status",
	"容器日志追踪已结束":                     "Container log streaming has ended",
	"审计链完整":                         "The audit chain is intact",
	"Oneinstack Panel 通知通道测试成功":     "Oneinstack Panel notification channel test succeeded",
	"通知通道测试":                        "Notification channel test",
	"只读观察员":                         "Read-only observer",
	"可查看概览、运行日志和本人任务":               "Can view the dashboard, runtime logs, and own tasks",
	"网站管理员":                         "Website administrator",
	"负责网站资源与相关任务":                   "Manages website resources and related tasks",
	"数据库管理员":                        "Database administrator",
	"负责数据库资源与相关任务":                  "Manages database resources and related tasks",
	"系统运维":                          "System operator",
	"负责文件、软件、监控、计划任务与系统访问配置":        "Manages files, software, monitoring, scheduled tasks, and system access settings",
	"安全审计员":                         "Security auditor",
	"可查看登录审计、运行日志和审批记录":             "Can view login audits, runtime logs, and approval records",
	"操作审批员":                         "Operation approver",
	"负责审核并执行高风险操作":                  "Reviews and executes high-risk operations",
	"容器运维":                          "Container operator",
	"负责 Docker、容器和 Compose 资源管理":    "Manages Docker, containers, and Compose resources",
	"磁盘空间报告":                        "Disk space report",
	"使用 df 输出所有挂载点的容量和使用率，不执行 Shell。": "Uses df to report capacity and usage for all mount points without executing arbitrary shell commands.",
	"服务状态检查": "Service status check",
	"检查受支持的 OneinStack 服务是否处于 active 状态。": "Checks whether supported OneinStack services are active.",
	"服务":       "Service",
	"网站目录容量报告": "Website directory size report",
	"统计受管网站根目录下单个站点的占用空间，不接受任意路径。": "Reports disk usage for one site under the managed website root and does not accept arbitrary paths.",
	"站点目录名": "Site directory name",
	"只能填写网站根目录下的单级目录名。":                  "Enter a single directory name directly under the website root.",
	"防火墙状态检查":                            "Firewall status check",
	"检查主机 firewalld 服务状态，适用于计划任务安全模板验证。": "Checks the host firewalld service for scheduled-task security validation.",
	"CPU 使用率": "CPU usage",
	"内存使用率":   "Memory usage",
	"1 分钟负载":  "1-minute load",
	"5 分钟负载":  "5-minute load",
	"15 分钟负载": "15-minute load",
	"网络接收":    "Network receive",
	"网络发送":    "Network send",
	"磁盘使用率":   "Disk usage",
	"磁盘读取":    "Disk read",
	"磁盘写入":    "Disk write",

	"软件版本":                  "Software version",
	"安装目录":                  "Installation directory",
	"数据目录":                  "Data directory",
	"日志目录":                  "Log directory",
	"运行账号":                  "Running account",
	"运行用户组":                 "Running user group",
	"监听端口":                  "Listener port",
	"绑定地址":                  "Bind address",
	"MySQL 端口":              "MySQL port",
	"MySQL端口":               "MySQL port",
	"MySQL 登录用户":            "MySQL login user",
	"MySQL 管理密码":            "MySQL administrator password",
	"接管外部 MySQL":            "Take over external MySQL",
	"确认接管外部 MySQL":          "Confirm takeover of external MySQL",
	"重置已有 MySQL root 密码":    "Reset the existing MySQL root password",
	"确认重置已有 MySQL root 密码":  "Confirm resetting the existing MySQL root password",
	"Tomcat HTTP 端口":        "Tomcat HTTP port",
	"Tomcat 绑定地址":           "Tomcat bind address",
	"JVM 初始堆（MB）":           "JVM initial heap (MB)",
	"JVM 最大堆（MB）":           "JVM maximum heap (MB)",
	"最大线程数":                 "Maximum threads",
	"等待队列长度":                "Accept queue length",
	"连接超时（毫秒）":              "Connection timeout (milliseconds)",
	"URI 编码":                "URI encoding",
	"网站根目录":                 "Website root directory",
	"虚拟主机目录":                "Virtual host directory",
	"管理员密码":                 "Administrator password",
	"管理员用户名":                "Administrator username",
	"发现冲突时备份并接管":            "Back up and take over conflicting resources",
	"Docker Engine 版本":      "Docker Engine version",
	"组件状态目录":                "Component state directory",
	"Docker Compose 版本":     "Docker Compose version",
	"Fail2ban 版本":           "Fail2ban version",
	"默认最大重试次数":              "Default maximum retries",
	"默认统计窗口（秒）":             "Default observation window (seconds)",
	"默认封禁时间（秒）":             "Default ban duration (seconds)",
	"忽略 IP":                 "Ignored IP addresses",
	"Redis 版本":              "Redis version",
	"监听地址":                  "Listener addresses",
	"Redis 登录用户":            "Redis login user",
	"Redis 密码":              "Redis password",
	"Halo 版本":               "Halo version",
	"HTTP 监听端口":             "HTTP listener port",
	"外部访问地址":                "External access URL",
	"配置目录":                  "Configuration directory",
	"运行用户":                  "Runtime user",
	"JVM 初始堆内存（MB）":         "JVM initial heap (MB)",
	"JVM 最大堆内存（MB）":         "JVM maximum heap (MB)",
	"数据库类型":                 "Database type",
	"数据库主机":                 "Database host",
	"数据库端口":                 "Database port",
	"数据库名称":                 "Database name",
	"数据库用户名":                "Database username",
	"数据库密码":                 "Database password",
	"自动迁移 /root/.halo2 旧站点": "Automatically migrate the legacy site from /root/.halo2",
	"Adminer 版本":            "Adminer version",
	"公开访问路径":                "Public access path",
	"访问策略":                  "Access policy",
	"允许访问的 CIDR":            "Allowed CIDRs",
	"默认数据库驱动":               "Default database driver",
	"默认数据库服务器":              "Default database server",
	"MongoDB 连接地址":          "MongoDB connection URL",
	"MinIO 版本":              "MinIO version",
	"Root 用户名":              "Root username",
	"Root 密码":               "Root password",
	"API 监听地址":              "API bind address",
	"API 端口":                "API port",
	"Console 监听地址":          "Console bind address",
	"Console 端口":            "Console port",
	"Console 外部跳转 URL":      "Console external redirect URL",
	"WebDAV 版本":             "WebDAV version",
	"登录账号":                  "Login account",
	"登录密码":                  "Login password",
	"ClamAV 版本":             "ClamAV version",
	"病毒库目录":                 "Virus database directory",
	"OpenSearch 初始管理员密码":    "OpenSearch initial administrator password",
	"HTTPS 监听端口":            "HTTPS listener port",
	"集群名称":                  "Cluster name",
	"节点名称":                  "Node name",
	"JVM 堆大小（MB）":           "JVM heap size (MB)",
	"接管外部MySQL":             "Take over external MySQL",
	"确认接管外部MySQL":           "Confirm takeover of external MySQL",
	"状态探测范围":                "Status probe scope",
	"Panel端口":               "Panel port",
	"迁移外部防火墙后端":             "Migrate the external firewall backend",
	"卸载数据策略":                "Uninstall data policy",
	"确认删除数据":                "Confirm data deletion",
	"PHP 内存限制":              "PHP memory limit",
	"迁移外部 PHP-FPM":          "Migrate external PHP-FPM",
	"确认迁移外部 PHP-FPM":        "Confirm migration of external PHP-FPM",
	"确认删除 PHP 组件数据":         "Confirm deletion of PHP component data",

	"工作进程数": "Worker processes",
	"建议保持 auto；手动设置范围为 1–99。": "Keep auto unless manual tuning is required; valid range: 1-99.",
	"单进程连接数":                  "Connections per worker",
	"每个工作进程允许的最大连接数。":         "Maximum connections allowed per worker process.",
	"长连接超时":                   "Keep-alive timeout",
	"客户端 Keep-Alive 空闲超时。":    "Client Keep-Alive idle timeout.",
	"请求体上限":                   "Request body limit",
	"上传请求允许的最大请求体。":           "Maximum request body allowed for uploads.",
	"最大连接数":                   "Maximum connections",
	"MySQL 同时接受的客户端连接上限。":     "Maximum number of concurrent MySQL client connections.",
	"数据包上限":                   "Packet size limit",
	"单个通信数据包允许的最大大小。":         "Maximum size allowed for one protocol packet.",
	"InnoDB 缓冲池":              "InnoDB buffer pool",
	"建议根据服务器内存和数据库负载设置。":      "Set according to server memory and database workload.",
	"慢查询日志":                   "Slow query log",
	"记录执行时间超过阈值的 SQL。":        "Records SQL statements whose execution time exceeds the threshold.",
	"慢查询阈值":                   "Slow query threshold",
	"超过该时长的查询会进入慢查询日志。":       "Queries exceeding this duration are written to the slow query log.",
	"脚本内存上限":                  "Script memory limit",
	"单个 PHP 请求允许使用的最大内存。":     "Maximum memory available to one PHP request.",
	"单文件上传上限":                 "Per-file upload limit",
	"单个上传文件允许的最大大小。":          "Maximum size allowed for one uploaded file.",
	"POST 数据上限":               "POST data limit",
	"必须不小于单文件上传上限。":           "Must not be lower than the per-file upload limit.",
	"脚本执行超时":                  "Script execution timeout",
	"单个 PHP 脚本的最长执行时间。":       "Maximum execution time for one PHP script.",
	"最大子进程数":                  "Maximum child processes",
	"PHP-FPM 可同时运行的最大工作进程数。":  "Maximum PHP-FPM worker processes that can run concurrently.",
	"启动进程数":                   "Startup processes",
	"PHP-FPM 启动时创建的工作进程数。":    "Worker processes created when PHP-FPM starts.",
	"最小空闲进程":                  "Minimum idle processes",
	"PHP-FPM 保持的最少空闲工作进程。":    "Minimum idle worker processes maintained by PHP-FPM.",
	"最大空闲进程":                  "Maximum idle processes",
	"PHP-FPM 保持的最多空闲工作进程。":    "Maximum idle worker processes maintained by PHP-FPM.",
	"最大内存":                    "Maximum memory",
	"0 表示不设置 Redis 内存上限。":     "0 means no Redis memory limit.",
	"内存淘汰策略":                  "Memory eviction policy",
	"达到内存上限后 Redis 处理新写入的方式。": "How Redis handles new writes after reaching the memory limit.",
	"AOF 持久化":                 "AOF persistence",
	"将写操作追加到 AOF 文件。":         "Appends write operations to the AOF file.",
	"空闲连接超时":                  "Idle connection timeout",
	"0 表示不主动断开空闲客户端。":         "0 means idle clients are not disconnected automatically.",
	"TCP 保活探测间隔；0 表示关闭。":      "TCP keepalive probe interval; 0 disables it.",
	"秒": "seconds",
	"该操作会改变系统状态，执行前需要确认": "This operation changes system state and requires confirmation before execution",
	"实际系统状态":                      "Actual system state",
	"执行阶段将重新探测并校验":                "The actual state will be detected and validated again during execution",
	"执行失败时由对应业务事务或任务流程尝试恢复":       "The corresponding transaction or task workflow will attempt recovery if execution fails",
	"受管 Nginx 虚拟主机目录/<name>.conf": "Managed Nginx virtual-host directory/<name>.conf",
	"写入网站虚拟主机配置":                  "Write the website virtual-host configuration",
	"Nginx 配置语法":                  "Nginx configuration syntax",
	"校验 Nginx 配置":                 "Validate the Nginx configuration",
	"重新加载 Nginx":                  "Reload Nginx",
	"OpenResty 配置语法":              "OpenResty configuration syntax",
	"校验 OpenResty 配置":             "Validate the OpenResty configuration",
	"重新加载 OpenResty":              "Reload OpenResty",
	"Tengine 配置语法":                "Tengine configuration syntax",
	"校验 Tengine 配置":               "Validate the Tengine configuration",
	"重新加载 Tengine":                "Reload Tengine",
	"Apache 配置语法":                 "Apache configuration syntax",
	"校验 Apache 配置":                "Validate the Apache configuration",
	"重新加载 Apache":                 "Reload Apache",
	"Caddy 配置语法":                  "Caddy configuration syntax",
	"校验 Caddy 配置":                 "Validate the Caddy configuration",
	"重新加载 Caddy":                  "Reload Caddy",
	"启用或停用网站虚拟主机配置":               "Enable or disable the website virtual-host configuration",
	"执行受控软件安装动作":                  "Run the controlled software installation action",
	"执行受控软件升级动作":                  "Run the controlled software upgrade action",
	"由组件安装器按软件 key 和版本执行":         "Executed by the component installer using the software key and version",
	"安装后验证服务状态":                   "Verify service status after installation",
	"升级后验证服务状态":                   "Verify service status after upgrade",
	"由组件探测器确定":                    "Determined by the component probe",
	"任务失败时由软件任务执行器按组件策略回滚或保留失败现场": "On failure, the software task runner rolls back according to component policy or preserves the failure state",
	"执行受控软件卸载动作":                   "Run the controlled software uninstall action",
	"由组件卸载器按软件 key 和版本执行":          "Executed by the component uninstaller using the software key and version",
	"执行组件服务动作":                     "Run the component service action",
	"由组件参数确定":                      "Determined by component parameters",
	"组件受管配置文件":                     "Component-managed configuration file",
	"应用组件配置并保存历史":                  "Apply component configuration and save history",
	"校验并应用组件配置":                    "Validate and apply component configuration",
	"由组件配置动作执行":                    "Executed by the component configuration action",
	"修改防火墙 Ping 响应":                "Change the firewall ping response",
	"由检测到的防火墙后端执行受控 ICMP 规则动作":     "Run a controlled ICMP rule action through the detected firewall backend",
	"可通过反向设置 Ping 响应状态恢复":          "Can be restored by reversing the ping response setting",
	"修改防火墙规则":                      "Change firewall rules",
	"由检测到的防火墙后端执行受控规则动作":           "Run a controlled rule action through the detected firewall backend",
	"失败时恢复已应用的规则操作和持久化状态":          "Restore applied rules and persisted state if the operation fails",
	"修改端口转发":                       "Change port forwarding",
	"由检测到的防火墙后端执行受控转发动作":           "Run a controlled forwarding action through the detected firewall backend",
	"切换防火墙状态":                      "Change firewall state",
	"由检测到的防火墙后端执行启停动作":             "Start or stop the detected firewall backend",
	"防火墙启停可能导致当前连接中断，请确认后执行":       "Starting or stopping the firewall may interrupt the current connection; confirm before execution",
	"已断开的外部连接":                     "External connections that were interrupted",
	"面板受管配置文件":                     "Panel-managed configuration file",
	"更新面板监听、TLS、安全入口和可信代理配置":       "Update Panel listening, TLS, secure-entry, and trusted-proxy settings",
	"应用面板访问配置":                     "Apply Panel access settings",
	"由面板网络配置事务执行":                  "Executed by the Panel network-configuration transaction",
	"同步面板端口规则":                     "Synchronize Panel port rules",
	"由受控防火墙适配器执行":                  "Executed by the controlled firewall adapter",
	"由面板网络事务恢复配置文件和已准备的端口规则":       "The Panel network transaction restores the configuration file and prepared port rules",
	"Fail2ban 的 OneinStack 受管规则文件": "OneinStack-managed Fail2ban policy file",
	"原子应用固定模板规则":                   "Atomically apply a fixed-template policy",
	"校验 Fail2ban 配置":               "Validate the Fail2ban configuration",
	"重新加载 Fail2ban":                "Reload Fail2ban",
	"通过受管 Fail2ban jail 处置单个 IP":   "Enforce one IP through a managed Fail2ban jail",
	"可通过对应的解封或重新封禁任务恢复":            "Can be reversed by the corresponding unban or ban task",
	"恢复请求已通过预检，等待独立恢复服务接管":         "The restore request passed preflight checks and is waiting for the dedicated restore service",
	"恢复任务已交给独立 systemd 单元，面板将短暂离线并自动完成健康检查": "The restore task was handed to a dedicated systemd unit. The Panel will briefly go offline and run health checks automatically",
	"更新任务已交给独立 systemd 单元执行，面板将在完成或回滚后恢复服务": "The update task was handed to a dedicated systemd unit. The Panel will resume after completion or rollback",
}

var englishOperationPreviewBusinessTexts = map[string]string{
	"卸载后保留":    "Preserve after uninstall",
	"移除受管程序文件": "Remove managed program files",
	"按删除策略移除":  "Remove according to the deletion policy",
	"替换受管 Adminer 程序并保留事务回滚副本": "Replace the managed Adminer program and keep a transaction rollback copy",
	"重建受管 Adminer 公开访问入口":      "Rebuild the managed Adminer public access route",
	"Adminer 公开访问入口":           "Adminer public access route",
	"当前访问入口无法可靠探测；升级任务会按运行中的 Web 服务重新检查并验证入口": "The current access route cannot be reliably detected. The upgrade task will recheck and verify it using the running Web service",
	"创建受管 Adminer 程序目录":         "Create the managed Adminer program directory",
	"更新受管 Adminer 安装状态":         "Update the managed Adminer installation state",
	"验证 Adminer Web 访问入口":       "Verify the Adminer Web access route",
	"更新受管 Caddy 安装状态":           "Update the managed Caddy installation state",
	"更新 firewalld 受管配置及防火墙规则":   "Update managed firewalld configuration and firewall rules",
	"保存规则及回滚状态":                 "Save rules and rollback state",
	"按现有服务状态重载或启动 firewalld":    "Reload or start firewalld according to its current service state",
	"迁移当前活动的 UFW 或 nftables 后端": "Migrate the currently active UFW or nftables backend",
	"先保护面板端口，再切换防火墙后端":          "Protect the Panel port before switching the firewall backend",
	"失败时尝试恢复受管程序与配置；数据库内容不在回滚快照中，新初始化的数据会保留为失败现场":                           "On failure, attempt to restore managed programs and configuration. Database contents are excluded from the rollback snapshot; newly initialized data is retained for failure diagnosis",
	"仅当已有 MySQL 数据目录认证失败时，通过隔离本地实例重置 root 密码":                               "Reset the root password through an isolated local instance only if authentication fails for the existing MySQL data directory",
	"若执行中已变更 root 密码，失败回滚会尝试恢复受管程序和配置并保留数据目录，但不会撤销密码变更或自动恢复旧密码；新密码按一次性凭据交付": "If execution changes the root password, failure rollback attempts to restore managed programs and configuration and preserves the data directory, but does not undo the password change or automatically restore the old password. The new password is delivered as a one-time credential",
	"移除系统软件包":                   "Remove system packages",
	"卸载后保留目录":                   "Preserve the directory after uninstall",
	"移除受管目录":                    "Remove the managed directory",
	"按删除策略移除目录":                 "Remove the directory according to the deletion policy",
	"移除组件状态内容":                  "Remove component state contents",
	"仅移除组件管理的目录内容":              "Remove only directory contents managed by the component",
	"组件预览契约":                    "Component preview contract",
	"服务、软件包和目录来自已签名组件 Manifest": "Services, packages, and directories come from the signed component manifest",
	"固定组件包":                     "Fixed component package",
	"执行阶段将重新检查服务和受管资源状态":        "Execution will recheck service and managed resource state",
	"由已验证的受管组件包执行卸载":            "Run uninstall through the verified managed component package",
	"删除组件数据":                    "Delete component data",
	"仅在删除策略和确认字段同时有效时执行":        "Run only when both the deletion policy and confirmation are valid",
	"数据删除不可自动恢复":                "Deleted data cannot be restored automatically",
	"数据目录和日志目录删除后不可由卸载任务恢复":     "The uninstall task cannot restore deleted data and log directories",
	"保留组件数据和配置":                 "Preserve component data and configuration",
	"卸载程序文件，保留受管数据与配置":          "Uninstall program files and preserve managed data and configuration",
	"卸载完成后不自动恢复已移除的程序文件；保留的数据和配置可供重新安装后接管": "Removed program files are not automatically restored after uninstall. Preserved data and configuration can be adopted after reinstallation",
	"卸载目标":    "Uninstall target",
	"服务与文件状态": "Service and file state",
	"执行阶段将重新检查服务、受管目录和已固定卸载包": "Execution will recheck services, managed directories, and the fixed uninstall package",
	"恢复历史组件配置并保存历史":           "Restore historical component configuration and save history",
	"创建或更新受管程序文件":             "Create or update managed program files",
	"安装参数":                    "Installation parameters",
	"版本、端口及组件清单参数校验通过":        "Parameter validation for version, port, and component manifest passed",
	"Web 应用兼容性":               "Web application compatibility",
	"Panel 无法验证应用兼容性；Tomcat 9 及更早版本到 10+ 需迁移 javax.* 到 jakarta.*，Tomcat 11 需 Java 17，并检查新版配置与应用功能": "Panel cannot verify application compatibility. Upgrading from Tomcat 9 or earlier to 10+ requires migrating javax.* to jakarta.*. Tomcat 11 requires Java 17. Check the new configuration and application functionality",
	"无法回读当前 Tomcat 运行版本；升级前请核对实际版本、备份应用并验证兼容性":                                                     "The current running Tomcat version could not be read. Check the actual version, back up applications, and verify compatibility before upgrading",
	"替换 Tomcat 程序目录；旧目录移至组件回滚快照":                                                                   "Replace the Tomcat program directory and move the previous directory to the component rollback snapshot",
	"更新受管 Connector 配置": "Update the managed Connector configuration",
	"更新受管 JVM 启动参数":     "Update the managed JVM startup parameters",
	"更新并重启 Tomcat 服务":   "Update and restart the Tomcat service",
	"保存本次升级的程序、配置及服务回滚快照；下次安装或升级会覆盖": "Save a rollback snapshot of the program, configuration, and service for this upgrade; the next installation or upgrade overwrites it",
	"创建或复用规范化后的网站根目录":                "Create or reuse the normalized website root directory",
	"按需放行网站端口":                      "Allow website ports as needed",
	"更新 Web 服务器受管配置":                "Update the managed Web Server configuration",
	"更新网站受管虚拟主机配置":                  "Update the managed website virtual-host configuration",
	"网站配置版本":                        "Website configuration revision",
	"预览基于当前网站和运行配置生成":               "The preview was generated from the current website and runtime configuration",
	"执行阶段将重新校验":                     "The execution stage will validate again",
	"执行前创建配置快照；发布或重载失败时恢复原配置":       "Create a configuration snapshot before execution; restore the original configuration if publication or reload fails",
	"创建或复用 Caddy 访问日志文件并授予运行用户写入权限": "Create or reuse the Caddy access-log file and grant the runtime user write access",
	"启用或停用网站虚拟主机配置；网站数据和网站文件保留":     "Enable or disable the website virtual-host configuration; website data and files are retained",
}
