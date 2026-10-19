package software

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/buildinfo"
	"oneinstack/internal/models"
	"oneinstack/internal/services/scriptregistry"
	softwareService "oneinstack/internal/services/software"
	"oneinstack/internal/services/translation"
	"oneinstack/router/input"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shirou/gopsutil/v4/disk"
	"gorm.io/gorm"
)

var automationMu sync.Mutex
var automationID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var automationHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Dedicated credentials are opt-in, never accepted by normal management APIs.
func AutomationAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") || len(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")) < 32 {
			automationError(c, 401, "AUTOMATION_MISSING_TOKEN", "请提供复测凭证 / automation credential required")
			return
		}
		path := os.Getenv("ONEINSTACK_AUTOMATION_TOKEN_FILE")
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			automationError(c, 503, "AUTOMATION_DISABLED", "自动复测未配置 / automation disabled")
			return
		}
		secret, err := os.ReadFile(path)
		provided := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		want, got := sha256.Sum256([]byte(strings.TrimSpace(string(secret)))), sha256.Sum256([]byte(provided))
		if err != nil || len(strings.TrimSpace(string(secret))) < 32 || subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
			automationError(c, 401, "AUTOMATION_UNAUTHORIZED", "复测凭证无效 / invalid automation credential")
			return
		}
		peer, _, _ := net.SplitHostPort(c.Request.RemoteAddr)
		if c.Request.TLS == nil && !net.ParseIP(peer).IsLoopback() {
			automationError(c, 403, "AUTOMATION_SECURE_TRANSPORT_REQUIRED", "复测接口需要 HTTPS 或 SSH 本地转发 / HTTPS or loopback tunnel required")
			return
		}
		actor, err := strconv.ParseInt(os.Getenv("ONEINSTACK_AUTOMATION_USER_ID"), 10, 64)
		var user models.User
		if err != nil || actor <= 0 || app.DB() == nil || app.DB().First(&user, actor).Error != nil || !user.IsAdmin || user.MustChangePassword {
			automationError(c, 403, "AUTOMATION_ACTOR_INVALID", "复测审计用户不可用 / automation audit actor unavailable")
			return
		}
		c.Set("automationActor", actor)
		c.Set("userId", actor)
		c.Set("username", user.Username)
		c.Set("authMode", "automation")
		c.Next()
	}
}

func automationError(c *gin.Context, status int, code, message string) {
	numeric := core.ErrInternalError
	switch status {
	case 400:
		numeric = core.ErrInvalidParameter
	case 401:
		numeric = core.ErrInvalidToken
	case 403:
		numeric = core.ErrForbidden
	case 404:
		numeric = core.ErrNotFound
	case 409:
		numeric = core.ErrConflict
	case 503:
		numeric = core.ErrServiceUnavailable
	}
	if code == "AUTOMATION_MISSING_TOKEN" {
		numeric = core.ErrMissingToken
	}
	err := core.NewError(numeric, message)
	err.StableCode = code
	core.HandleErrorWithStatus(c, status, err)
	c.Abort()
}

func automationOK(c *gin.Context, status int, data any) {
	c.JSON(status, core.SuccessResponseForLocale(c.GetString("locale"), data))
}

func automationIdentity(ctx context.Context) (*translation.SignedPanelClient, error) {
	t := app.ONE_CONFIG.Translation
	return translation.NewSignedPanelClient(ctx, translation.SignedPanelClientConfig{
		BaseURL: app.ONE_CONFIG.ScriptCenter.URL, InstallDir: app.GetBasePath(),
		IdentityPath: t.IdentityPath, ActivationCodeFile: t.ActivationCodeFile, Timeout: 30 * time.Second})
}

func automationRedisPing(parameters map[string]string) bool {
	port := parameters["REDIS_PORT"]
	if port == "" {
		port = "6379"
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return false
	}
	connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 3*time.Second)
	if err != nil {
		return false
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = io.WriteString(connection, "*1\r\n$4\r\nPING\r\n"); err != nil {
		return false
	}
	response, err := bufio.NewReader(io.LimitReader(connection, 512)).ReadString('\n')
	return err == nil && response == "+PONG\r\n"
}

func AutomationFacts(c *gin.Context) {
	registry, err := scriptregistry.New(app.ONE_CONFIG.ScriptCenter)
	if err != nil {
		automationError(c, 503, "AUTOMATION_HOST_UNAVAILABLE", "无法检查主机 / host unavailable")
		return
	}
	identity, err := automationIdentity(c.Request.Context())
	if err != nil {
		automationError(c, 503, "AUTOMATION_IDENTITY_UNAVAILABLE", "实例身份不可用 / instance identity unavailable")
		return
	}
	var active, installed int64
	definition, err := softwareService.ResolveServiceComponent(app.DB(), c.Query("component"))
	if err != nil {
		automationError(c, 400, "AUTOMATION_COMPONENT_UNSUPPORTED", "缺少受支持服务定义 / supported service definition required")
		return
	}
	if app.DB().Model(&models.SoftwareTask{}).Where("status IN ?", models.ActiveSoftwareTaskStatuses()).Count(&active).Error != nil ||
		app.DB().Model(&models.Software{}).Where("(`key` = ? OR component = ?) AND installed = ?", definition.SoftwareKey, c.Query("component"), true).Count(&installed).Error != nil {
		automationError(c, 503, "AUTOMATION_INVENTORY_UNAVAILABLE", "任务或组件清单不可用 / inventory unavailable")
		return
	}
	usage, err := disk.Usage(app.GetBasePath())
	if err != nil {
		automationError(c, 503, "AUTOMATION_DISK_UNAVAILABLE", "磁盘信息不可用 / disk unavailable")
		return
	}
	if runtime.GOOS == "linux" {
		isolationDisk, diskErr := disk.Usage("/opt")
		if diskErr != nil {
			automationError(c, 503, "AUTOMATION_DISK_UNAVAILABLE", "隔离目录磁盘不可用 / isolation filesystem unavailable")
			return
		}
		if isolationDisk.Free < usage.Free {
			usage.Free = isolationDisk.Free
		}
	}
	executable, err := os.Executable()
	if runtime.GOOS == "linux" {
		executable = "/proc/self/exe"
	}
	image, openErr := os.Open(executable)
	if err != nil || openErr != nil {
		automationError(c, 503, "AUTOMATION_BINARY_UNAVAILABLE", "无法确认运行中程序 / running binary unavailable")
		return
	}
	hash := sha256.New()
	_, hashErr := io.Copy(hash, image)
	image.Close()
	if hashErr != nil {
		automationError(c, 503, "AUTOMATION_BINARY_UNAVAILABLE", "无法校验运行中程序 / running binary unreadable")
		return
	}
	osRelease := map[string]string{}
	if raw, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok && (key == "ID" || key == "VERSION_ID" || key == "VERSION" || key == "PRETTY_NAME") {
				osRelease[key] = strings.Trim(value, "\"'")
			}
		}
	}
	var websites, databases int64
	if app.DB().Model(&models.Website{}).Count(&websites).Error != nil || app.DB().Model(&models.Software{}).Where("`key` IN ? AND installed = ?", models.DatabaseSoftwareKeys, true).Count(&databases).Error != nil {
		automationError(c, 503, "AUTOMATION_INVENTORY_UNAVAILABLE", "业务清单不可用 / business inventory unavailable")
		return
	}
	automationOK(c, 200, gin.H{"schemaVersion": 1, "adapterVersion": 2, "instanceId": identity.InstanceID(), "host": registry.Host(),
		"build": buildinfo.Current(), "activeTasks": active, "installedComponentRows": installed, "freeBytes": usage.Free,
		"binarySHA256": hex.EncodeToString(hash.Sum(nil)), "osRelease": osRelease, "websiteCount": websites, "databaseSoftwareCount": databases,
		"checkedAt": time.Now().UTC(), "actions": []string{"install", "verify", "start", "stop", "restart", "reload"}})
}

type automationRetest struct {
	InstanceID      string            `json:"instanceId"`
	Component       string            `json:"component"`
	SoftwareVersion string            `json:"softwareVersion"`
	PackageVersion  string            `json:"packageVersion"`
	SHA256          string            `json:"sha256"`
	PlanDigest      string            `json:"planDigest"`
	Parameters      map[string]string `json:"parameters"`
	Steps           []string          `json:"steps"`
	PublicPackage   bool              `json:"publicPackage,omitempty"`
}

func resolveAutomationRetest(c *gin.Context, registry *scriptregistry.Registry, job string, value automationRetest, identity *translation.SignedPanelClient) (scriptregistry.Package, error) {
	if value.PublicPackage {
		return registry.ResolvePublishedAutomation(c.Request.Context(), value.Component, value.SoftwareVersion, value.PackageVersion, value.SHA256)
	}
	return registry.ResolveAutomation(c.Request.Context(), job, value.Component, value.SoftwareVersion, value.PackageVersion, value.SHA256, value.PlanDigest, identity)
}

func retestFile(job string) string {
	return filepath.Join(app.GetBasePath(), "automation", "retests", job+".json")
}

func writeAutomationFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".retest-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	return err
}
func retestKey(job string, step int, value automationRetest) string {
	data, _ := json.Marshal([]any{job, step, value})
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
func retestTaskID(job string, step int, value automationRetest) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("oneinstack-retest:"+retestKey(job, step, value))).String()
}

func AutomationRetest(c *gin.Context) {
	automationMu.Lock()
	defer automationMu.Unlock()
	job := c.Param("jobID")
	step, err := strconv.Atoi(c.Param("step"))
	if !automationID.MatchString(job) || err != nil || step < 0 || step > 15 {
		automationError(c, 400, "AUTOMATION_INVALID_STEP", "复测步骤无效 / invalid retest step")
		return
	}
	var value automationRetest
	file := retestFile(job)
	if c.Request.Method == http.MethodGet {
		data, err := os.ReadFile(file)
		if err != nil || json.Unmarshal(data, &value) != nil {
			automationError(c, 404, "AUTOMATION_NOT_FOUND", "复测不存在 / retest not found")
			return
		}
	} else {
		decoder := json.NewDecoder(io.LimitReader(c.Request.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			automationError(c, 400, "AUTOMATION_INVALID_PLAN", "复测方案无效 / invalid retest plan")
			return
		}
	}
	if !automationHash.MatchString(value.SHA256) || !automationHash.MatchString(value.PlanDigest) || len(value.Steps) < 2 || len(value.Steps) > 16 || step >= len(value.Steps) || value.Steps[0] != "install" || value.Steps[len(value.Steps)-1] != "verify" {
		automationError(c, 400, "AUTOMATION_INVALID_PLAN", "需要安装及最终功能验证 / install and final verification required")
		return
	}
	for i, action := range value.Steps {
		if action != "verify" && action != "start" && action != "stop" && action != "restart" && action != "reload" && !(action == "install" && i == 0) {
			// Uninstall is intentionally excluded until a data-preserving cleanup receipt is supplied.
			automationError(c, 400, "AUTOMATION_ACTION_UNSUPPORTED", "复测动作未获支持 / unsupported retest action")
			return
		}
	}
	identity, err := automationIdentity(c.Request.Context())
	if err != nil || identity.InstanceID() != value.InstanceID {
		automationError(c, 409, "AUTOMATION_INSTANCE_MISMATCH", "实际实例不匹配 / instance mismatch")
		return
	}
	encoded, _ := json.Marshal(value)
	if old, err := os.ReadFile(file); err == nil {
		var prior automationRetest
		if json.Unmarshal(old, &prior) != nil {
			automationError(c, 500, "AUTOMATION_STATE_INVALID", "复测记录损坏 / invalid retest state")
			return
		}
		normal, _ := json.Marshal(prior)
		if string(normal) != string(encoded) {
			automationError(c, 409, "AUTOMATION_PLAN_CHANGED", "复测方案不可覆盖 / immutable retest plan")
			return
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		automationError(c, 500, "AUTOMATION_STATE_UNAVAILABLE", "复测记录不可用 / retest state unavailable")
		return
	}
	manager, err := getTaskManager()
	if err != nil {
		automationError(c, 503, "AUTOMATION_TASK_UNAVAILABLE", "任务服务不可用 / task manager unavailable")
		return
	}
	// Each completed predecessor is required. Polling a live install never starts another task.
	for previous := 0; previous < step; previous++ {
		if value.Steps[previous] == "verify" {
			data, err := os.ReadFile(retestFile(job) + "." + strconv.Itoa(previous) + ".verify")
			var receipt struct {
				Verified   bool   `json:"verified"`
				SHA256     string `json:"sha256"`
				PlanDigest string `json:"planDigest"`
			}
			if err != nil || json.Unmarshal(data, &receipt) != nil || !receipt.Verified || receipt.SHA256 != value.SHA256 || receipt.PlanDigest != value.PlanDigest {
				automationError(c, 409, "AUTOMATION_PREDECESSOR_INCOMPLETE", "前序功能探测未通过 / predecessor probe incomplete")
				return
			}
			continue
		}
		task, err := manager.Get(retestTaskID(job, previous, value))
		if err != nil || task.Status != models.SoftwareTaskStatusSucceeded || task.PackageSHA256 != value.SHA256 {
			automationError(c, 409, "AUTOMATION_PREDECESSOR_INCOMPLETE", "前序任务未通过 / predecessor incomplete")
			return
		}
	}
	if value.Steps[step] == "verify" {
		receiptFile := retestFile(job) + "." + strconv.Itoa(step) + ".verify"
		if c.Request.Method == http.MethodGet {
			data, err := os.ReadFile(receiptFile)
			var receipt map[string]any
			if err != nil || json.Unmarshal(data, &receipt) != nil {
				automationError(c, 404, "AUTOMATION_PROBE_NOT_FOUND", "尚无功能探测回执 / probe receipt not found")
				return
			}
			automationOK(c, 200, receipt)
			return
		}
		registry, err := scriptregistry.New(app.ONE_CONFIG.ScriptCenter)
		if err != nil {
			automationError(c, 503, "AUTOMATION_REGISTRY_UNAVAILABLE", "组件注册表不可用 / registry unavailable")
			return
		}
		pkg, err := resolveAutomationRetest(c, registry, job, value, identity)
		// After the same immutable candidate is formally published, private draft
		// access is revoked. Re-probe the installed bytes through signed public
		// metadata, keeping the original task/plan identity unchanged.
		if err != nil && !value.PublicPackage {
			pkg, err = registry.ResolvePublishedAutomation(c.Request.Context(), value.Component, value.SoftwareVersion, value.PackageVersion, value.SHA256)
		}
		if err != nil {
			automationError(c, 409, "AUTOMATION_PIN_UNAVAILABLE", "固定签名包不可用 / fixed signed package unavailable")
			return
		}
		var latest models.SoftwareTask
		if app.DB().Where("component = ? AND status = ?", value.Component, models.SoftwareTaskStatusSucceeded).Order("updated_at DESC").First(&latest).Error != nil || latest.PackageSHA256 != value.SHA256 {
			automationError(c, 409, "AUTOMATION_RUNTIME_CHANGED", "当前成功任务的包身份已改变 / installed task identity changed")
			return
		}
		probe, err := softwareService.NewInstaller().InspectServiceWithPackage(c.Request.Context(), value.Component, value.SoftwareVersion, pkg.AutomationPin(value.SoftwareVersion))
		if err != nil || probe.ActiveState != "active" || probe.RuntimeVersion != value.SoftwareVersion {
			automationError(c, 409, "AUTOMATION_PROBE_FAILED", "真实服务或原生版本探测未通过 / runtime probe failed")
			return
		}
		if value.Component == "redis" && !automationRedisPing(value.Parameters) {
			automationError(c, 409, "AUTOMATION_NATIVE_PROBE_FAILED", "Redis PING 功能探测未通过 / Redis PING failed")
			return
		}
		receipt := gin.H{"instanceId": value.InstanceID, "verified": true, "probe": probe, "sha256": value.SHA256, "planDigest": value.PlanDigest, "checkedAt": time.Now().UTC()}
		data, _ := json.Marshal(receipt)
		if writeAutomationFile(receiptFile, data) != nil {
			automationError(c, 500, "AUTOMATION_STATE_UNAVAILABLE", "无法保存功能回执 / cannot persist probe receipt")
			return
		}
		automationOK(c, 200, receipt)
		return
	}
	if task, err := manager.Get(retestTaskID(job, step, value)); err == nil {
		if task.PackageSHA256 != value.SHA256 {
			automationError(c, 409, "AUTOMATION_TASK_MISMATCH", "任务包身份不匹配 / task identity mismatch")
			return
		}
		response := gin.H{"instanceId": value.InstanceID, "task": task}
		if task.Status == models.SoftwareTaskStatusFailed || task.Status == models.SoftwareTaskStatusInterrupted {
			if log, logErr := manager.ReadLog(task.ID, 0, 65536); logErr == nil {
				response["log"] = log
			}
		}
		automationOK(c, 200, response)
		return
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		automationError(c, 503, "AUTOMATION_TASK_UNAVAILABLE", "无法核实已有任务 / cannot inspect existing task")
		return
	}
	if c.Request.Method == http.MethodGet {
		automationError(c, 404, "AUTOMATION_TASK_NOT_FOUND", "复测任务未创建 / retest task not found")
		return
	}
	registry, err := scriptregistry.New(app.ONE_CONFIG.ScriptCenter)
	if err != nil {
		automationError(c, 503, "AUTOMATION_CENTER_UNAVAILABLE", "组件注册表不可用 / registry unavailable")
		return
	}
	pkg, err := resolveAutomationRetest(c, registry, job, value, identity)
	if err != nil {
		automationError(c, 409, "AUTOMATION_PACKAGE_UNAVAILABLE", "签名隔离包解析或校验失败 / isolated package verification failed")
		return
	}
	if step == 0 {
		var installed, active int64
		definition, definitionErr := softwareService.ResolveServiceComponent(app.DB(), value.Component)
		if definitionErr != nil || app.DB().Model(&models.Software{}).Where("(`key` = ? OR component = ?) AND installed = ?", definition.SoftwareKey, value.Component, true).Count(&installed).Error != nil || installed != 0 {
			automationError(c, 409, "AUTOMATION_EXISTING_COMPONENT", "已安装组件不可作为空白隔离对象 / existing component is protected")
			return
		}
		if app.DB().Model(&models.SoftwareTask{}).Where("status IN ?", models.ActiveSoftwareTaskStatuses()).Count(&active).Error != nil || active != 0 {
			automationError(c, 409, "AUTOMATION_ACTIVE_TASKS", "主机存在活动任务 / host has active tasks")
			return
		}
		// Preserve existing services even when they are not recorded in the Panel database.
		if pkg.Manifest.ServiceName == "" {
			automationError(c, 409, "AUTOMATION_SERVICE_UNKNOWN", "缺少明确服务身份 / service identity missing")
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		unit, unitErr := exec.CommandContext(ctx, "systemctl", "show", pkg.Manifest.ServiceName, "-p", "LoadState", "--value").Output()
		cancel()
		if strings.TrimSpace(string(unit)) != "not-found" || (unitErr != nil && len(unit) == 0) {
			automationError(c, 409, "AUTOMATION_EXISTING_SERVICE", "已有系统服务受保护 / existing system service protected")
			return
		}
		for _, parameter := range pkg.Manifest.Parameters {
			if parameter.Secret && value.Parameters[parameter.Name] != "" {
				automationError(c, 400, "AUTOMATION_SECRET_PARAMETER", "复测方案不得包含明文凭证 / plaintext secret prohibited")
				return
			}
			if parameter.Name == "ONEINSTACK_COMPONENT_STATE" || parameter.Env == "ONEINSTACK_COMPONENT_STATE" {
				root := strings.TrimSpace(os.Getenv("ONEINSTACK_COMPONENT_STATE"))
				if root == "" {
					root = "/var/lib/oneinstack/components"
				}
				if supplied := value.Parameters[parameter.Name]; supplied != "" && supplied != root {
					automationError(c, 400, "AUTOMATION_STATE_OVERRIDE", "复测不能覆盖 Panel 的组件状态目录 / component state override prohibited")
					return
				}
				if !filepath.IsAbs(root) {
					automationError(c, 409, "AUTOMATION_STATE_ROOT_UNKNOWN", "组件状态目录不明确 / component state directory unknown")
					return
				}
				if _, err := os.Lstat(filepath.Join(root, value.Component)); !errors.Is(err, os.ErrNotExist) {
					automationError(c, 409, "AUTOMATION_EXISTING_STATE", "已有组件状态或恢复现场受保护 / existing component state is protected")
					return
				}
			}
			if parameter.Name == "INSTALL_DIR" || parameter.Env == "INSTALL_DIR" || parameter.Name == "DATA_DIR" || parameter.Env == "DATA_DIR" {
				path := value.Parameters[parameter.Name]
				base := filepath.Join("/opt/oneinstack-retest", job)
				rel, err := filepath.Rel(base, path)
				if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.Clean(path) != path {
					automationError(c, 400, "AUTOMATION_PATH_NOT_ISOLATED", "需要独立复测目录 / isolated retest paths required")
					return
				}
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					automationError(c, 409, "AUTOMATION_PATH_EXISTS", "复测目录已有内容 / retest path already exists")
					return
				}
				for parent := filepath.Dir(path); parent != "/"; parent = filepath.Dir(parent) {
					if info, err := os.Lstat(parent); err == nil && info.Mode()&os.ModeSymlink != 0 {
						automationError(c, 400, "AUTOMATION_SYMLINK_PARENT", "复测目录父路径不可为符号链接 / symlink parent prohibited")
						return
					} else if err != nil && !errors.Is(err, os.ErrNotExist) {
						automationError(c, 409, "AUTOMATION_PATH_UNAVAILABLE", "无法核实隔离目录 / cannot verify isolated directory")
						return
					}
				}
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		automationError(c, 500, "AUTOMATION_STATE_UNAVAILABLE", "无法保存复测方案 / cannot store retest plan")
		return
	}
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		if err := writeAutomationFile(file, encoded); err != nil {
			automationError(c, 500, "AUTOMATION_STATE_UNAVAILABLE", "无法保存复测方案 / cannot store retest plan")
			return
		}
	}
	pin := pkg.AutomationPin(value.SoftwareVersion)
	actor := c.GetInt64("automationActor")
	var task *models.SoftwareTask
	if value.Steps[step] == "install" {
		task, err = SubmitInstallationTask(input.InstallParams{Key: value.Component, Version: value.SoftwareVersion, Parameters: value.Parameters, ResolvedPackage: &pin, IdempotencyKey: retestKey(job, step, value)}, actor)
	} else {
		task, err = manager.SubmitServiceActionWithResolvedPackage(value.Component, value.Steps[step], false, "", &pin, actor, retestKey(job, step, value))
	}
	if err != nil {
		automationError(c, 409, "AUTOMATION_TASK_REJECTED", fmt.Sprintf("复测任务未受理 / retest task rejected (%s)", value.Steps[step]))
		return
	}
	automationOK(c, 202, gin.H{"instanceId": value.InstanceID, "task": task})
}
