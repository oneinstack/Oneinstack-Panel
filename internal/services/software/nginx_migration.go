package software

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services/legacyoneinstack"
	"oneinstack/internal/services/script"
	"oneinstack/internal/services/scriptregistry"
	"oneinstack/router/input"
)

const nginxMigrationPackageMinimum = "1.0.34"

type NginxMigrationPlan struct {
	Revision        string                     `json:"revision"`
	Version         string                     `json:"version"`
	PreviousVersion string                     `json:"previousVersion"`
	InstallDir      string                     `json:"installDir"`
	Config          string                     `json:"config"`
	Modules         []string                   `json:"modules"`
	Package         *scriptregistry.PackagePin `json:"package"`
}

type nginxMigrationSource struct {
	NginxMigrationPlan
	params *input.InstallParams
}

func nginxSingleInlineSite(config []byte) bool {
	return !regexp.MustCompile(`(?m)^\s*include\s+[^;]*conf\.d/\*\.conf\s*;`).Match(config) &&
		len(regexp.MustCompile(`(?m)^\s*server\s*\{`).FindAll(config, -1)) == 1 &&
		len(regexp.MustCompile(`(?m)^\s*listen\s+[^;]+;`).FindAll(config, -1)) == 1 &&
		len(regexp.MustCompile(`(?m)^\s*root\s+[^;]+;`).FindAll(config, -1)) == 1
}

func nginxMigrationError(reason string) error {
	return fmt.Errorf("NGINX_MIGRATION_UNSUPPORTED: %s", reason)
}

// Only this inspected, confirmed migration workflow may bypass the legacy
// installation guard. The flag has no JSON representation and is never taken
// from the generic user parameter map.
func inspectNginxMigration(ctx context.Context, version string) (nginxMigrationSource, error) {
	var source nginxMigrationSource
	if version != "1.30.7" && version != "1.31.9" {
		return source, nginxMigrationError("请选择已修复的 Nginx 精确版本")
	}
	row, err := legacyoneinstack.Installed(app.DB(), "webserver")
	if err != nil {
		return source, err
	}
	if row == nil {
		row, err = legacyoneinstack.Installed(app.DB(), "nginx")
	}
	if err != nil {
		return source, err
	}
	if row == nil {
		return source, nginxMigrationError("没有待迁移的脚本接管 Nginx")
	}
	c, err := legacyoneinstack.DecodeComponent(row.LegacyMetadata)
	if err != nil || c.ID != "nginx" || c.Service != "nginx" || c.Binary != filepath.Join(c.InstallDir, "sbin/nginx") || c.Config != filepath.Join(c.InstallDir, "conf/nginx.conf") ||
		(!strings.HasPrefix(c.InstallDir, "/usr/local/") && !strings.HasPrefix(c.InstallDir, "/opt/")) {
		return source, nginxMigrationError("需要独立目录、标准配置布局和 nginx.service 的源码 Nginx")
	}
	for _, p := range []string{c.Binary, c.Config} {
		if err := validateRedisMigrationRootOwned(p); err != nil {
			return source, nginxMigrationError("程序和主配置必须由 root 持有且不可被其他用户写入")
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := []string{"show", "nginx.service"}
	for _, p := range []string{"MainPID", "ExecStart", "ActiveState", "FragmentPath", "DropInPaths", "SourcePath", "User", "Group"} {
		args = append(args, "-p", p)
	}
	data, err := exec.CommandContext(probeCtx, "systemctl", args...).Output()
	if err != nil {
		return source, nginxMigrationError("无法探测 nginx.service")
	}
	unit := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			unit[k] = v
		}
	}
	if unit["ActiveState"] != "active" || unit["DropInPaths"] != "" || unit["SourcePath"] != "" || (unit["User"] != "" && unit["User"] != "root") {
		return source, nginxMigrationError("需要正在运行且没有自定义 drop-in 的 root 主服务")
	}
	command := regexp.MustCompile(`argv\[\]=(.*?) ;`).FindStringSubmatch(unit["ExecStart"])
	if len(command) != 2 || (command[1] != c.Binary && command[1] != c.Binary+" -c "+c.Config) || strings.Count(unit["ExecStart"], "argv[]=") != 1 {
		return source, nginxMigrationError("服务启动参数不在可迁移范围内")
	}
	pid, err := strconv.Atoi(unit["MainPID"])
	if err != nil || pid <= 1 {
		return source, nginxMigrationError("原服务主进程不可用")
	}
	exe, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil || exe != c.Binary {
		return source, nginxMigrationError("运行进程与原程序不一致")
	}
	unitPath, err := filepath.EvalSymlinks(unit["FragmentPath"])
	if err != nil {
		return source, nginxMigrationError("原服务单元不可用")
	}
	if err = validateRedisMigrationRootOwned(unitPath); err != nil {
		return source, nginxMigrationError("原服务单元权限不安全")
	}
	unitBytes, err := legacyoneinstack.ReadFile(unitPath)
	if err != nil {
		return source, err
	}
	build, err := exec.CommandContext(probeCtx, c.Binary, "-V").CombinedOutput()
	if err != nil || !strings.Contains(string(build), "nginx version: nginx/") {
		return source, nginxMigrationError("无法确认上游 Nginx 模块与版本")
	}
	previous := legacyoneinstack.RuntimeVersion(probeCtx, c)
	if previous == "" || previous != row.InstallVersion {
		return source, nginxMigrationError("原版本已变化，请重新扫描")
	}
	if scriptregistry.ComparePackageVersions(version, previous) < 0 {
		return source, nginxMigrationError("迁移不允许降低运行版本")
	}
	mainConfig, err := legacyoneinstack.ReadFile(c.Config)
	if err != nil {
		return source, err
	}
	if !nginxSingleInlineSite(mainConfig) && !regexp.MustCompile(`(?m)^\s*include\s+[^;]*conf\.d/\*\.conf\s*;`).Match(mainConfig) {
		return source, nginxMigrationError("默认站点布局暂不可安全迁移；需要标准 conf.d 或单一内联默认站点")
	}
	config, err := exec.CommandContext(probeCtx, c.Binary, "-T").Output()
	if err != nil || len(config) > 4<<20 {
		return source, nginxMigrationError("原配置校验失败或超出检查范围")
	}
	// The revision binds every included config plus certificate bytes, binary,
	// service identity and running process. None of those bytes leave this host.
	digest := sha256.New()
	fmt.Fprintf(digest, "%d\n%d\n%s\n%s\n%s\n", row.Id, pid, row.LegacyMetadata, data, unitBytes)
	digest.Write(build)
	digest.Write(config)
	binary, err := os.ReadFile(c.Binary)
	if err != nil {
		return source, err
	}
	digest.Write(binary)
	for _, m := range regexp.MustCompile(`(?m)^\s*ssl_certificate(?:_key)?\s+([^;]+);`).FindAllStringSubmatch(string(config), -1) {
		p := strings.Trim(strings.TrimSpace(m[1]), `"'`)
		if strings.Contains(p, "$") {
			return source, nginxMigrationError("动态证书路径需要单独处理")
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(c.InstallDir, "conf", p)
		}
		b, e := legacyoneinstack.ReadFile(p)
		if e != nil {
			return source, nginxMigrationError("无法安全读取原证书以固定迁移版本")
		}
		digest.Write(b)
	}
	run := regexp.MustCompile(`(?m)^\s*user\s+([a-z_][a-z0-9_-]*)(?:\s+([a-z_][a-z0-9_-]*))?\s*;`).FindStringSubmatch(string(config))
	if len(run) != 3 {
		return source, nginxMigrationError("无法确定原运行用户")
	}
	group := run[2]
	if group == "" {
		group = run[1]
	}
	port := c.Port
	if port == "" {
		port = "80"
	}
	if !regexp.MustCompile(`(?m)\blisten\s+(?:[^\s;]+:)?` + regexp.QuoteMeta(port) + `(?:\s|;)`).Match(config) {
		return source, nginxMigrationError("原监听端口与接管记录不符")
	}
	web := strings.TrimSuffix(c.WebRoot, "/default")
	if web == "" {
		return source, nginxMigrationError("原站点目录缺失")
	}
	logs := c.LogRoot
	if logs == "" {
		logs = "/data/wwwlogs"
	}
	socket := "/dev/shm/php-cgi.sock"
	if m := regexp.MustCompile(`fastcgi_pass\s+unix:([^;]+);`).FindStringSubmatch(string(config)); len(m) == 2 {
		socket = strings.TrimSpace(m[1])
	}
	for _, p := range []string{web, logs} {
		if err := legacyoneinstack.ValidatePath(p); err != nil {
			return source, nginxMigrationError("原安装参数路径无法安全迁移")
		}
	}
	if !regexp.MustCompile(`^/dev/shm/[a-zA-Z0-9._-]+\.sock$`).MatchString(socket) {
		if err := legacyoneinstack.ValidatePath(socket); err != nil {
			return source, nginxMigrationError("原 PHP Socket 路径不可迁移")
		}
	}
	modules := []string{}
	if strings.Contains(string(build), "ngx_brotli") {
		modules = append(modules, "brotli")
	}
	if strings.Contains(string(build), "--with-http_v3_module") {
		modules = append(modules, "http3")
	}
	source.NginxMigrationPlan = NginxMigrationPlan{Revision: fmt.Sprintf("%x", digest.Sum(nil)), Version: version, PreviousVersion: previous, InstallDir: c.InstallDir, Config: c.Config, Modules: modules}
	source.params = &input.InstallParams{Key: "webserver", Version: version, Port: port, Username: run[1], MigrateExistingNginx: true, InstallMode: "center", Parameters: map[string]string{"install-dir": c.InstallDir, "web-root": web, "log-dir": logs, "run-user": run[1], "run-group": group, "nginx-port": port, "php-fpm-socket": socket}}
	return source, nil
}

func PreviewNginxMigration(ctx context.Context, version string) (NginxMigrationPlan, error) {
	if strings.TrimSpace(version) == "" {
		var target models.Software
		if err := app.DB().WithContext(ctx).Where("component = ? AND catalog_managed = ? AND catalog_visible = ? AND installable = ? AND version IN ?", "nginx", true, true, true, []string{"1.30.7", "1.31.9"}).Order("recommended DESC, version_order ASC, id ASC").First(&target).Error; err != nil {
			return NginxMigrationPlan{}, nginxMigrationError("没有已发布且适用于当前主机的迁移目标，请刷新软件目录")
		}
		version = target.Version
	}
	source, err := inspectNginxMigration(ctx, version)
	if err != nil {
		return NginxMigrationPlan{}, err
	}
	info, err := NewInstaller().resolveInstallParams(ctx, source.params)
	if err != nil {
		return NginxMigrationPlan{}, err
	}
	if info.PackagePin == nil || scriptregistry.ComparePackageVersions(info.PackageVersion, nginxMigrationPackageMinimum) < 0 {
		return NginxMigrationPlan{}, nginxMigrationError("需要包含模块保留能力的组件包 1.0.34 或更新版")
	}
	source.Package = info.PackagePin
	return source.NginxMigrationPlan, nil
}

func ValidateNginxMigrationRevision(ctx context.Context, revision string) error {
	source, err := inspectNginxMigration(ctx, "1.31.9")
	if err != nil {
		return err
	}
	if revision == "" || source.Revision != revision {
		return errors.New("CONFIG_REVISION_CONFLICT: Nginx 配置、证书或进程已变化，请重新预览")
	}
	return nil
}

func ApplyNginxMigration(ctx context.Context, revision, version string, pin *scriptregistry.PackagePin, logPath string, observer script.ExecutionObserver) error {
	if pin == nil || scriptregistry.ComparePackageVersions(pin.ResolvedVersion, nginxMigrationPackageMinimum) < 0 {
		return nginxMigrationError("缺少已确认的迁移组件包")
	}
	source, err := inspectNginxMigration(ctx, version)
	if err != nil {
		return err
	}
	if revision == "" || source.Revision != revision {
		return errors.New("CONFIG_REVISION_CONFLICT: Nginx 迁移源发生变化")
	}
	source.params.ResolvedPackage = pin
	_, err = NewInstaller().InstallTask(ctx, source.params, logPath, observer)
	if err == nil {
		if o, ok := observer.(interface{ OnRuntimeVersion(string) }); ok {
			o.OnRuntimeVersion(version)
		}
	}
	return err
}

// MarshalMigrationPlan is shared by the confirmed preview and automation entry.
func MarshalNginxMigrationPlan(plan NginxMigrationPlan) ([]byte, error) { return json.Marshal(plan) }
