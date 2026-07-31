package system

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/services/panelbackup"
	"oneinstack/internal/services/panelupdate"
)

const panelRestoreConfirmation = "RESTORE PANEL"

var (
	panelBackupManagerMu sync.Mutex
	panelBackupManagerDB *gorm.DB
	panelBackupManager   *panelbackup.Manager
)

func getPanelBackupManager() (*panelbackup.Manager, error) {
	panelBackupManagerMu.Lock()
	defer panelBackupManagerMu.Unlock()
	database := app.DB()
	if database == nil {
		return nil, errors.New("database is not initialized")
	}
	if panelBackupManager == nil || panelBackupManagerDB != database {
		manager, err := panelbackup.NewApplicationManager(database)
		if err != nil {
			return nil, err
		}
		panelBackupManager = manager
		panelBackupManagerDB = database
	}
	return panelBackupManager, nil
}

func CreatePanelBackup(c *gin.Context) {
	var request struct {
		Passphrase          string `json:"passphrase" binding:"required"`
		IncludeCertificates bool   `json:"includeCertificates"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "请输入备份加密密码"))
		return
	}
	manager, ok := panelBackupManagerForRequest(c)
	if !ok {
		return
	}
	info, err := manager.Create(c.Request.Context(), panelbackup.CreateOptions{
		Passphrase: request.Passphrase, IncludeCertificates: request.IncludeCertificates,
	})
	request.Passphrase = ""
	if err != nil {
		handlePanelBackupError(c, err, "创建 Panel 备份失败")
		return
	}
	core.HandleSuccess(c, info)
}

func ImportPanelBackup(c *gin.Context) {
	manager, ok := panelBackupManagerForRequest(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, manager.MaxBackupBytes()+(2<<20))
	if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "备份上传请求无效"))
		return
	}
	passphrase := c.Request.FormValue("passphrase")
	file, _, err := c.Request.FormFile("backup")
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "请选择 .onebak 备份文件"))
		return
	}
	defer file.Close()
	info, err := manager.Import(c.Request.Context(), file, passphrase)
	passphrase = ""
	if err != nil {
		handlePanelBackupError(c, err, "导入 Panel 备份失败")
		return
	}
	core.HandleSuccess(c, info)
}

func ListPanelBackups(c *gin.Context) {
	manager, ok := panelBackupManagerForRequest(c)
	if !ok {
		return
	}
	backups, err := manager.List()
	if err != nil {
		handlePanelBackupError(c, err, "读取 Panel 备份失败")
		return
	}
	core.HandleSuccess(c, gin.H{"backups": backups})
}

func DownloadPanelBackup(c *gin.Context) {
	manager, ok := panelBackupManagerForRequest(c)
	if !ok {
		return
	}
	file, info, err := manager.Open(c.Param("id"))
	if err != nil {
		handlePanelBackupError(c, err, "读取 Panel 备份失败")
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		handlePanelBackupError(c, err, "读取 Panel 备份失败")
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", strconv.FormatInt(stat.Size(), 10))
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": info.FileName,
	}))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "no-store")
	http.ServeContent(c.Writer, c.Request, info.FileName, info.CreatedAt, file)
}

func DeletePanelBackup(c *gin.Context) {
	manager, ok := panelBackupManagerForRequest(c)
	if !ok {
		return
	}
	var request struct {
		Confirm bool `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || !request.Confirm {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "删除备份需要明确确认"))
		return
	}
	if err := manager.Delete(c.Param("id")); err != nil {
		handlePanelBackupError(c, err, "删除 Panel 备份失败")
		return
	}
	core.HandleSuccess(c, gin.H{"deleted": true})
}

func PreflightPanelBackup(c *gin.Context) {
	manager, ok := panelBackupManagerForRequest(c)
	if !ok {
		return
	}
	var request struct {
		Passphrase string `json:"passphrase" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "请输入备份加密密码"))
		return
	}
	result, err := manager.Preflight(c.Request.Context(), c.Param("id"), request.Passphrase)
	request.Passphrase = ""
	if err != nil {
		handlePanelBackupError(c, err, "备份恢复预检失败")
		return
	}
	core.HandleSuccess(c, result)
}

func RestorePanelBackup(c *gin.Context) {
	manager, ok := panelBackupManagerForRequest(c)
	if !ok {
		return
	}
	var request struct {
		Passphrase string `json:"passphrase" binding:"required"`
		Confirm    string `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || request.Confirm != panelRestoreConfirmation {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "确认文本必须为 "+panelRestoreConfirmation))
		return
	}
	recoveryNeeded, err := manager.NeedsRecovery()
	if err != nil {
		handlePanelBackupError(c, err, "检查恢复事务失败")
		return
	}
	if recoveryNeeded {
		handlePanelBackupError(c, panelbackup.ErrRecoveryNeeded, "存在中断的恢复事务")
		return
	}
	if _, err := manager.Preflight(c.Request.Context(), c.Param("id"), request.Passphrase); err != nil {
		handlePanelBackupError(c, err, "备份恢复预检失败")
		return
	}
	runner := panelupdate.OSCommandRunner{}
	if _, err := runner.Run(c.Request.Context(), panelupdate.Command{
		Name: "systemctl", Args: []string{"is-active", "--quiet", "one-panel-restore.service"},
	}); err == nil {
		handlePanelBackupError(c, panelbackup.ErrRestoreBusy, "已有 Panel 恢复任务正在执行")
		return
	}
	if err := manager.WriteRestoreRequest(panelbackup.RestoreRequest{
		BackupID: c.Param("id"), Passphrase: request.Passphrase,
	}); err != nil {
		handlePanelBackupError(c, err, "创建恢复请求失败")
		return
	}
	request.Passphrase = ""
	now := time.Now().UTC()
	_ = manager.WriteStatus(panelbackup.RestoreStatus{
		State: panelbackup.StatusValidating, BackupID: c.Param("id"),
		Message:   "恢复请求已通过预检，等待独立恢复服务接管",
		StartedAt: &now, UpdatedAt: now,
	})
	if _, err := runner.Run(c.Request.Context(), panelupdate.Command{
		Name: "systemctl", Args: []string{"start", "--no-block", "one-panel-restore.service"},
	}); err != nil {
		manager.RemoveRestoreRequest()
		handlePanelBackupError(c, fmt.Errorf("start restore service: %w", err), "启动独立恢复服务失败")
		return
	}
	core.HandleSuccess(c, gin.H{
		"accepted": true,
		"message":  "恢复任务已交给独立 systemd 单元，面板将短暂离线并自动完成健康检查",
	})
}

func GetPanelRestoreStatus(c *gin.Context) {
	manager, ok := panelBackupManagerForRequest(c)
	if !ok {
		return
	}
	status, err := manager.Status()
	if err != nil {
		handlePanelBackupError(c, err, "读取 Panel 恢复状态失败")
		return
	}
	core.HandleSuccess(c, status)
}

func panelBackupManagerForRequest(c *gin.Context) (*panelbackup.Manager, bool) {
	manager, err := getPanelBackupManager()
	if err != nil {
		handlePanelBackupError(c, err, "Panel 备份服务不可用")
		return nil, false
	}
	return manager, true
}

func handlePanelBackupError(c *gin.Context, err error, message string) {
	switch {
	case errors.Is(err, panelbackup.ErrNotFound):
		core.HandleError(c, core.WrapError(err, core.ErrNotFound, "Panel 备份不存在"))
	case errors.Is(err, panelbackup.ErrInvalidPassphrase):
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "备份密码错误或不符合要求"))
	case errors.Is(err, panelbackup.ErrInvalidBackup):
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "备份包格式、完整性或兼容性校验失败"))
	case errors.Is(err, panelbackup.ErrRestoreBusy), errors.Is(err, panelbackup.ErrRecoveryNeeded):
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, message))
	case errors.Is(err, context.Canceled):
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "操作已取消"))
	default:
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, message))
	}
}
