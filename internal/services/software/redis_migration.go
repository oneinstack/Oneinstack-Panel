package software

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services/componentstate"
	"oneinstack/internal/services/legacyoneinstack"
	"oneinstack/internal/services/scriptregistry"
	"oneinstack/router/input"
	"oneinstack/utils"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const redisMigrationUnit = "/etc/systemd/system/redis.service"
const redisMigrationPackageMinimum = "1.0.27"

func requireMigratedRedisPackage(component, version string) error {
	if component != "redis" || scriptregistry.ComparePackageVersions(version, redisMigrationPackageMinimum) >= 0 {
		return nil
	}
	if _, err := os.Lstat(filepath.Join(redisMigrationStateDir(), "migration-task")); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return redisMigrationConflict("迁移后的 Redis 需要组件包 1.0.27 或更新版")
}

type RedisMigrationPlan struct {
	Revision string                     `json:"revision"`
	Version  string                     `json:"version"`
	Package  *scriptregistry.PackagePin `json:"package"`
	Config   string                     `json:"config"`
	DataDir  string                     `json:"dataDir"`
	StateDir string                     `json:"stateDir"`
	Port     string                     `json:"port"`
}

type redisMigrationSource struct {
	RedisMigrationPlan
	id         int
	parameters map[string]string
	config     []byte
	unit       []byte
	password   string
}

func redisMigrationConflict(reason string) error {
	return fmt.Errorf("REDIS_MIGRATION_UNSUPPORTED: %s", reason)
}

func redisMigrationStateDir() string {
	root := strings.TrimSpace(os.Getenv("ONEINSTACK_COMPONENT_STATE"))
	if root == "" {
		root = defaultComponentStateRoot
	}
	return filepath.Join(root, "redis")
}

func inspectRedisMigration(ctx context.Context, db *gorm.DB) (redisMigrationSource, error) {
	var source redisMigrationSource
	row, err := legacyoneinstack.Installed(db, "redis")
	if err != nil {
		return source, err
	}
	if row == nil {
		return source, redisMigrationConflict("没有待迁移的脚本接管 Redis")
	}
	component, err := legacyoneinstack.DecodeComponent(row.LegacyMetadata)
	if err != nil {
		return source, redisMigrationConflict("接管记录中的 Redis 路径无效，请重新扫描")
	}
	if component.ID != "redis" || component.Service != "redis" || component.Binary != filepath.Join(component.InstallDir, "bin/redis-server") ||
		component.Config != filepath.Join(component.InstallDir, "etc/redis.conf") ||
		(!strings.HasPrefix(component.InstallDir, "/usr/local/") && !strings.HasPrefix(component.InstallDir, "/opt/")) {
		return source, redisMigrationConflict("仅支持具有独立安装目录并由 redis.service 运行的源码 Redis")
	}
	cliPath := filepath.Join(component.InstallDir, "bin/redis-cli")
	for _, path := range []string{component.Binary, cliPath, component.Config, redisMigrationUnit} {
		if err := validateRedisMigrationRootOwned(path); err != nil {
			return source, err
		}
	}
	if err := validateRedisMigrationInstallDir(component.InstallDir); err != nil {
		return source, err
	}
	unit, err := legacyoneinstack.VerifyService(ctx, component)
	if err != nil {
		return source, err
	}
	if unit["Id"] != "redis.service" || unit["ActiveState"] != "active" || unit["FragmentPath"] != redisMigrationUnit ||
		unit["SourcePath"] != "" || unit["DropInPaths"] != "" || unit["User"] != "redis" || unit["Group"] != "redis" {
		return source, redisMigrationConflict("需要正在运行、无自定义 drop-in、以 redis:redis 运行的独立 redis.service")
	}
	command := "path=" + component.Binary + " ; argv[]=" + component.Binary + " " + component.Config + " ;"
	pid, pidErr := strconv.Atoi(unit["MainPID"])
	if pidErr != nil || pid <= 1 || !strings.Contains(unit["ExecStart"], command) || strings.Count(unit["ExecStart"], "argv[]=") != 1 {
		return source, redisMigrationConflict("Redis 启动命令与接管记录不一致，或包含额外参数")
	}
	executable, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil || executable != component.Binary {
		return source, redisMigrationConflict("Redis 当前进程与接管的可执行文件不一致")
	}
	version := legacyoneinstack.RuntimeVersion(ctx, component)
	if version == "" || version != row.InstallVersion {
		return source, redisMigrationConflict("Redis 版本已变化，请重新扫描")
	}
	config, err := legacyoneinstack.ReadFile(component.Config)
	if err != nil {
		return source, redisMigrationConflict("无法安全读取 Redis 配置文件")
	}
	settings, err := redisMigrationSettings(config)
	if err != nil {
		return source, err
	}
	dataDir := settings["dir"]
	if legacyoneinstack.ValidatePath(dataDir) != nil || !strings.HasPrefix(dataDir, "/data/") ||
		strings.ContainsAny(dataDir, " \t") || (component.DataDir != "" && component.DataDir != dataDir) {
		return source, redisMigrationConflict("需要 /data 下与接管记录一致的独立数据目录")
	}
	if err := validateRedisMigrationDataDir(dataDir, settings); err != nil {
		return source, err
	}
	password := settings["requirepass"]
	probe := func(args ...string) (string, error) {
		runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(runCtx, cliPath, append([]string{"--raw", "-h", "127.0.0.1", "-p", settings["port"]}, args...)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "REDISCLI_AUTH=" + password}
		output, err := cmd.Output()
		return strings.ReplaceAll(strings.TrimSpace(string(output)), "\r\n", "\n"), err
	}
	if pong, err := probe("PING"); err != nil || pong != "PONG" {
		return source, redisMigrationConflict("无法使用原配置凭据通过 Redis PING")
	}
	server, err := probe("INFO", "server")
	server = "\n" + server + "\n"
	if err != nil || !strings.Contains(server, "\nprocess_id:"+strconv.Itoa(pid)+"\n") ||
		!strings.Contains(server, "\nconfig_file:"+component.Config+"\n") || !strings.Contains(server, "\nredis_version:"+version+"\n") {
		return source, redisMigrationConflict("Redis TCP 监听未绑定到接管的进程和配置")
	}
	replication, err := probe("INFO", "replication")
	replication = "\n" + replication + "\n"
	if err != nil || !strings.Contains(replication, "\nrole:master\n") || !strings.Contains(replication, "\nconnected_slaves:0\n") {
		return source, redisMigrationConflict("当前只支持独立 Redis，存在主从复制的实例暂不可迁移")
	}
	for key, expected := range map[string]string{
		"dir": dataDir, "port": settings["port"], "bind": settings["bind"], "appendonly": settings["appendonly"],
		"save": settings["save"], "dbfilename": settings["dbfilename"], "appendfilename": settings["appendfilename"], "appenddirname": settings["appenddirname"],
	} {
		current, err := probe("CONFIG", "GET", key)
		if err != nil || current != strings.TrimSuffix(key+"\n"+expected, "\n") {
			return source, redisMigrationConflict("Redis 运行参数与磁盘配置不一致，请检查运行态配置")
		}
	}
	unitBytes, err := legacyoneinstack.ReadFile(redisMigrationUnit)
	if err != nil {
		return source, redisMigrationConflict("无法保存原 Redis 服务单元")
	}
	stateDir := redisMigrationStateDir()
	if legacyoneinstack.ValidatePath(filepath.Dir(stateDir)) != nil ||
		strings.HasPrefix(stateDir, component.InstallDir+"/") || strings.HasPrefix(stateDir, dataDir+"/") {
		return source, redisMigrationConflict("组件状态目录不可用")
	}
	if _, err := os.Lstat(stateDir); !errors.Is(err, os.ErrNotExist) {
		return source, redisMigrationConflict("已存在 Redis 管理状态，请先检查历史任务与组件归属")
	}
	token := sha256.Sum256([]byte(fmt.Sprintf("%d\n%s\n%s\n%d\n%s\n%s", row.Id, row.LegacyMetadata, stateDir, pid, config, unitBytes)))
	source.RedisMigrationPlan = RedisMigrationPlan{
		Revision: fmt.Sprintf("%x", token), Version: version, Config: component.Config, DataDir: dataDir, StateDir: stateDir, Port: settings["port"],
	}
	source.id, source.config, source.unit, source.password = row.Id, config, unitBytes, password
	source.parameters = map[string]string{
		"install-dir": component.InstallDir, "data-dir": dataDir, "redis-port": settings["port"],
		"redis-bind": settings["bind"], "redis-username": "default", "component-state-dir": filepath.Dir(stateDir),
	}
	return source, nil
}

func validateRedisMigrationRootOwned(path string) error {
	if legacyoneinstack.ValidatePath(path) != nil {
		return redisMigrationConflict("Redis 配置、服务或程序路径包含链接或越界")
	}
	for current := path; current != "/"; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return redisMigrationConflict("Redis 配置、服务或程序无法访问")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0022 != 0 ||
			(current == path && !info.Mode().IsRegular()) {
			return redisMigrationConflict("Redis 配置、程序和服务路径必须由 root 持有，且不可由其他用户写入")
		}
	}
	return nil
}

func validateRedisMigrationInstallDir(root string) error {
	// Upgrade and uninstall move the whole install tree. Only accept a tree
	// dedicated to the Redis binaries and configuration that we can identify.
	allowed := map[string][]string{
		"":    {"bin", "etc"},
		"bin": {"redis-server", "redis-cli", "redis-benchmark", "redis-sentinel", "redis-check-rdb", "redis-check-aof"},
		"etc": {"redis.conf"},
	}
	for dir, names := range allowed {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			return redisMigrationConflict("无法验证 Redis 安装目录")
		}
		for _, entry := range entries {
			if !slices.Contains(names, entry.Name()) {
				return redisMigrationConflict("Redis 安装目录包含额外文件，无法确认由本实例独占")
			}
			if dir == "" && entry.IsDir() || dir != "" && entry.Type().IsRegular() {
				continue
			}
			if dir == "bin" && entry.Type() == os.ModeSymlink {
				target, err := os.Readlink(filepath.Join(root, dir, entry.Name()))
				if err == nil && (target == "redis-server" || target == filepath.Join(root, dir, "redis-server")) {
					continue
				}
			}
			return redisMigrationConflict("Redis 安装目录包含无法安全识别的文件类型")
		}
	}
	return nil
}

func redisMigrationSettings(config []byte) (map[string]string, error) {
	settings := map[string]string{}
	for _, line := range strings.Split(string(config), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		words := strings.Fields(line)
		key := strings.ToLower(words[0])
		switch key {
		case "include", "aclfile", "user", "loadmodule", "replicaof", "slaveof", "unixsocket", "tls-port", "rename-command",
			"cluster-config-file", "sentinel", "logfile", "pidfile":
			// Default output to journald does not leave an external path for the managed unit.
			if (key == "logfile" && len(words) == 2 && words[1] == "\"\"") || (key == "tls-port" && len(words) == 2 && words[1] == "0") {
				continue
			}
			return nil, redisMigrationConflict("Redis 配置包含暂不支持的外部依赖或拓扑指令：" + key)
		case "bind", "dir", "port", "requirepass", "cluster-enabled", "daemonize", "supervised", "dbfilename", "appendfilename", "appenddirname", "appendonly", "save":
			_, alreadySet := settings[key]
			if key == "save" && alreadySet {
				settings[key] += " " + strings.Join(words[1:], " ")
				continue
			}
			if len(words) < 2 || (key != "bind" && key != "save" && len(words) != 2) || alreadySet {
				return nil, redisMigrationConflict("Redis 配置项无法唯一识别：" + key)
			}
			// Quoted simple names and secrets are accepted; escape and inline-comment
			// syntax is deliberately excluded from this first migration contract.
			value := strings.Trim(strings.Join(words[1:], " "), "\"")
			if strings.ContainsAny(value, "\"'\\#") || ((key == "dir" || key == "port" || key == "requirepass") && words[1] != value) {
				return nil, redisMigrationConflict("Redis 配置项需要人工核对：" + key)
			}
			settings[key] = value
		}
	}
	for key, fallback := range map[string]string{"appendonly": "no", "save": "3600 1 300 100 60 10000", "dbfilename": "dump.rdb", "appendfilename": "appendonly.aof", "appenddirname": "appendonlydir"} {
		if _, present := settings[key]; !present {
			settings[key] = fallback
		}
	}
	if settings["appendonly"] != "yes" && settings["save"] == "" {
		return nil, redisMigrationConflict("当前 Redis 未启用持久化，无法保证后续升级时的数据完整性")
	}
	port, err := strconv.Atoi(settings["port"])
	if err != nil || port < 1 || port > 65535 || settings["dir"] == "" ||
		(settings["cluster-enabled"] != "" && settings["cluster-enabled"] != "no") ||
		(settings["daemonize"] != "" && settings["daemonize"] != "no") ||
		(settings["supervised"] != "" && settings["supervised"] != "no" && settings["supervised"] != "systemd") {
		return nil, redisMigrationConflict("需要静态端口、数据目录及非集群前台运行配置")
	}
	hasIPv4 := false
	for _, field := range strings.Fields(settings["bind"]) {
		ip := net.ParseIP(strings.TrimPrefix(field, "-"))
		if ip == nil || !ip.IsLoopback() {
			return nil, redisMigrationConflict("首版迁移只支持回环地址监听")
		}
		hasIPv4 = hasIPv4 || field == "127.0.0.1"
	}
	password := settings["requirepass"]
	if !hasIPv4 || (password != "" && (len(password) < 8 || len(password) > 128 || !managedRedisPasswordPattern.MatchString(password))) {
		return nil, redisMigrationConflict("需要 IPv4 回环监听及受支持的原有密码格式")
	}
	for _, key := range []string{"dbfilename", "appendfilename", "appenddirname"} {
		if value := settings[key]; value == "." || value == ".." || (value != "" && filepath.Base(value) != value) {
			return nil, redisMigrationConflict("持久化文件必须位于该实例的数据目录内")
		}
	}
	return settings, nil
}

func validateRedisMigrationDataDir(path string, settings map[string]string) error {
	account, err := user.Lookup("redis")
	if err != nil {
		return redisMigrationConflict("缺少 Redis 系统运行账号")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return redisMigrationConflict("Redis 数据目录不存在或无法访问")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || strconv.FormatUint(uint64(stat.Uid), 10) != account.Uid || info.Mode().Perm()&0002 != 0 {
		return redisMigrationConflict("数据目录需要由 Redis 账号独占并禁止其他用户写入")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return redisMigrationConflict("无法读取 Redis 数据目录")
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == settings["appenddirname"] && entry.IsDir() {
			files, err := os.ReadDir(filepath.Join(path, name))
			if err != nil {
				return redisMigrationConflict("无法读取 Redis 持久化目录")
			}
			for _, file := range files {
				if !file.Type().IsRegular() {
					return redisMigrationConflict("Redis AOF 目录包含链接或非普通文件")
				}
			}
			continue
		}
		if !entry.Type().IsRegular() || (name != settings["dbfilename"] && name != settings["appendfilename"]) {
			return redisMigrationConflict("数据目录包含其他文件，无法确认由本 Redis 实例独占")
		}
	}
	return nil
}

func PreviewRedisMigration(ctx context.Context) (RedisMigrationPlan, error) {
	source, err := inspectRedisMigration(ctx, app.DB())
	if err != nil {
		return RedisMigrationPlan{}, err
	}
	if _, err := redisMigrationCatalogRow(app.DB(), source); err != nil {
		return RedisMigrationPlan{}, err
	}
	info, err := NewInstaller().getInstallScriptForPreview(ctx, &input.InstallParams{Key: "redis", Version: source.Version}, "verify")
	if err != nil {
		return RedisMigrationPlan{}, err
	}
	if info.PackagePin == nil || info.PackagePin.PackageSHA256 == "" || scriptregistry.ComparePackageVersions(info.PackageVersion, redisMigrationPackageMinimum) < 0 {
		return RedisMigrationPlan{}, redisMigrationConflict("需要匹配当前 Redis 版本且包含可靠回滚的 Center 组件包 1.0.27 或更新版")
	}
	source.Package = info.PackagePin
	return source.RedisMigrationPlan, nil
}

func ValidateRedisMigrationRevision(ctx context.Context, revision string) error {
	source, err := inspectRedisMigration(ctx, app.DB())
	if err != nil {
		return err
	}
	if revision == "" || revision != source.Revision {
		return errors.New("CONFIG_REVISION_CONFLICT: Redis 源配置或运行进程已变化，请重新预览")
	}
	return nil
}

func redisMigrationCatalogRow(db *gorm.DB, source redisMigrationSource) (models.Software, error) {
	var catalog models.Software
	err := db.Where("`key` = ? AND component = ? AND version = ? AND catalog_managed = ? AND installable = ? AND id <> ?",
		"redis", "redis", source.Version, true, true, source.id).First(&catalog).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = redisMigrationConflict("当前 Redis 版本缺少可接收归属的已同步软件目录条目")
	}
	return catalog, err
}

func ApplyRedisMigration(ctx context.Context, plan RedisMigrationPlan, taskID string) error {
	if plan.Revision == "" || plan.Version == "" || plan.Package == nil {
		return redisMigrationConflict("缺少已预览的源版本或组件包")
	}
	info, err := NewInstaller().getInstallScript(ctx, &input.InstallParams{Key: "redis", Version: plan.Version, ResolvedPackage: plan.Package}, "verify")
	if err != nil {
		return err
	}
	if scriptregistry.ComparePackageVersions(info.PackageVersion, redisMigrationPackageMinimum) < 0 {
		return redisMigrationConflict("固定组件包缺少可靠的数据回滚能力")
	}
	source, err := inspectRedisMigration(ctx, app.DB())
	if err != nil {
		return err
	}
	if plan.Revision != source.Revision || plan.Version != source.Version {
		return errors.New("CONFIG_REVISION_CONFLICT: Redis 源配置或运行进程已变化，请重新预览")
	}
	return source.commit(ctx, app.DB(), info.PackageVersion, taskID)
}

func (source redisMigrationSource) commit(ctx context.Context, db *gorm.DB, packageVersion, taskID string) error {
	params, err := json.Marshal(source.parameters)
	if err != nil {
		return err
	}
	credentials := ""
	if source.password != "" {
		plain, err := json.Marshal(map[string]string{"redis-password": source.password})
		if err != nil {
			return err
		}
		credentials, err = utils.EncryptCredential(string(plain), utils.CredentialPurposeSoftwareInstall)
		if err != nil {
			return err
		}
	}
	parent := filepath.Dir(source.StateDir)
	if err := os.MkdirAll(parent, 0750); err != nil {
		return err
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return errors.New("invalid Redis migration task ID")
	}
	stage := filepath.Join(parent, ".redis-migration-"+taskID)
	if err := os.Mkdir(stage, 0700); err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for name, content := range map[string][]byte{
		"version": []byte(source.Version + "\n"), "install-dir": []byte(source.parameters["install-dir"] + "\n"),
		"data-dir": []byte(source.DataDir + "\n"), "install-mode": []byte("center\n"),
		"adopted-config.backup": source.config, "adopted-unit.backup": source.unit, "migration-task": []byte(taskID),
	} {
		if err := os.WriteFile(filepath.Join(stage, name), content, 0600); err != nil {
			return err
		}
	}
	if err := componentstate.Write(stage, &componentstate.Ownership{
		SchemaVersion: componentstate.OwnershipSchemaVersion, Component: "redis", SoftwareVersion: source.Version,
		PackageVersion: packageVersion, Parameters: source.parameters,
		PurgePaths: []string{source.parameters["install-dir"], source.DataDir},
	}); err != nil {
		return err
	}
	published := false
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		catalog, err := redisMigrationCatalogRow(tx, source)
		if err != nil {
			return err
		}
		old := tx.Model(&models.Software{}).Where("id = ? AND installed = ? AND origin = ?", source.id, true, legacyoneinstack.Origin).
			Updates(map[string]any{"installed": false, "catalog_visible": false})
		if old.Error != nil {
			return old.Error
		}
		if old.RowsAffected != 1 {
			return errors.New("CONFIG_REVISION_CONFLICT: Redis 接管记录已变化")
		}
		if err := tx.Model(&catalog).Updates(map[string]any{
			"installed": true, "install_version": source.Version, "runtime_version": source.Version,
			"installed_package_version": packageVersion, "runtime_params": string(params), "credential_params_ciphertext": credentials,
			"http_port": source.Port, "status": models.Soft_Status_Suc, "install_time": time.Now(),
		}).Error; err != nil {
			return err
		}
		if _, err := os.Lstat(source.StateDir); !errors.Is(err, os.ErrNotExist) {
			return redisMigrationConflict("Redis 组件状态目录已变化")
		}
		if err := os.Rename(stage, source.StateDir); err != nil {
			return err
		}
		published = true
		return nil
	})
	if err != nil && published {
		return errors.Join(err, os.RemoveAll(source.StateDir))
	}
	return err
}

// RecoverRedisMigration removes state published by an interrupted task only
// when the durable component row still belongs to the OneinStack script.
func RecoverRedisMigration(ctx context.Context, db *gorm.DB, taskID string) (string, string) {
	stateDir := redisMigrationStateDir()
	if _, err := uuid.Parse(taskID); err != nil || legacyoneinstack.ValidatePath(stateDir) != nil {
		return "unknown", "Redis 迁移状态路径无法验证"
	}
	if err := os.RemoveAll(filepath.Join(filepath.Dir(stateDir), ".redis-migration-"+taskID)); err != nil {
		return "unknown", "Redis 迁移的临时状态无法清理"
	}
	row, err := legacyoneinstack.Installed(db.WithContext(ctx), "redis")
	if err != nil {
		return "unknown", "无法读取 Redis 接管归属"
	}
	marker, err := legacyoneinstack.ReadFile(filepath.Join(stateDir, "migration-task"))
	if errors.Is(err, os.ErrNotExist) {
		return "unchanged", "Redis 服务和配置仍由原环境管理"
	}
	if err != nil || string(marker) != taskID {
		return "unknown", "Redis 状态归属不明确，已保留现场"
	}
	if row == nil {
		return "managed", "Redis 受管归属已保存"
	}
	if err := os.RemoveAll(stateDir); err != nil {
		return "unknown", "无法撤销本次迁移写入的 Redis 状态"
	}
	return "rolled_back", "已撤销 Redis 受管状态，原服务、配置和数据保留"
}
