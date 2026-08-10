package configsnapshot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/internal/services/configsnapshot"
	safeservice "oneinstack/internal/services/safe"
	systemservice "oneinstack/internal/services/system"
	websiteService "oneinstack/internal/services/website"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func List(c *gin.Context) {
	userID := snapshotUser(c)
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 {
		core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "page 必须是大于等于 1 的整数", "page"))
		return
	}
	pageSize, err := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if err != nil || pageSize < 1 || pageSize > 100 {
		core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "pageSize 必须是 1 到 100 之间的整数", "pageSize"))
		return
	}
	result, err := configsnapshot.Default().List(c.Query("resourceType"), c.Query("resourceId"), c.Query("status"), page, pageSize, userID)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取配置快照失败"))
		return
	}
	core.HandleSuccess(c, result)
}

// ListNginxResources returns the safe resource identifiers accepted by nginx
// configuration snapshots. An unavailable web server is represented as an
// empty list so the snapshot form can still render without a special case.
func ListNginxResources(c *gin.Context) {
	manager, err := websiteService.NewDefaultWebServerConfigManager()
	if err != nil {
		if errors.Is(err, websiteService.ErrWebServerUnavailable) {
			core.HandleSuccess(c, gin.H{"items": []websiteService.WebServerConfigFile{}})
			return
		}
		core.HandleError(c, core.WrapError(err, core.ErrConfigError, "读取可选 Nginx 配置失败"))
		return
	}
	files, err := manager.List()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrConfigError, "读取可选 Nginx 配置失败"))
		return
	}

	items := make([]gin.H, 0, len(files))
	for _, file := range files {
		items = append(items, gin.H{
			"resourceId": file.Path,
			"name":       file.Name,
			"path":       file.Path,
			"main":       file.Main,
			"site":       file.Site,
		})
	}
	core.HandleSuccess(c, gin.H{"items": items})
}

type createRequest struct {
	ResourceType  string `json:"resourceType" binding:"required"`
	ResourceID    string `json:"resourceId" binding:"required"`
	Name          string `json:"name"`
	Version       string `json:"version"`
	BackupAccount string `json:"backupAccount"`
	Description   string `json:"description"`
}

// Create captures the current real resource state. The client cannot provide
// before/after content, preventing forged snapshots or arbitrary file writes.
func Create(c *gin.Context) {
	var request createRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "创建快照参数错误"))
		return
	}
	userID := snapshotUser(c)
	current, _, err := currentResource(request.ResourceType, request.ResourceID)
	if err != nil {
		handleSnapshotError(c, err, "读取当前实际配置失败")
		return
	}
	artifact, artifactName := snapshotArtifact(request.ResourceType, current)
	snapshot, err := configsnapshot.Default().Create(configsnapshot.CreateInput{
		ResourceType: request.ResourceType, ResourceID: request.ResourceID,
		Operation: "create", Before: current, After: current, RequestedBy: userID,
		Name: request.Name, Version: request.Version, BackupAccount: request.BackupAccount,
		Description: request.Description, Artifact: artifact, ArtifactName: artifactName,
	})
	if err != nil {
		handleSnapshotError(c, err, "创建配置快照失败")
		return
	}
	if err := configsnapshot.Default().Mark(snapshot.ID, "succeeded", ""); err != nil {
		handleSnapshotError(c, err, "保存配置快照状态失败")
		return
	}
	RecordAudit(c, snapshot, "succeeded", "手动创建配置快照")
	c.JSON(http.StatusCreated, core.SuccessResponseForContext(c, gin.H{"snapshot": snapshot}))
}

func Get(c *gin.Context) {
	document, err := configsnapshot.Default().Get(c.Param("id"), snapshotUser(c))
	if err != nil {
		handleSnapshotError(c, err, "读取配置快照失败")
		return
	}
	core.HandleSuccess(c, document)
}

func Diff(c *gin.Context) {
	document, err := configsnapshot.Default().Get(c.Param("id"), snapshotUser(c))
	if err != nil {
		handleSnapshotError(c, err, "读取配置差异失败")
		return
	}
	core.HandleSuccess(c, gin.H{"snapshot": document.Snapshot, "diff": document.Diff})
}

func RestorePreview(c *gin.Context) {
	document, err := configsnapshot.Default().Get(c.Param("id"), snapshotUser(c))
	if err != nil {
		handleSnapshotError(c, err, "读取回滚预览失败")
		return
	}
	current, _, err := currentResource(document.Snapshot.ResourceType, document.Snapshot.ResourceID)
	if err != nil {
		handleSnapshotError(c, err, "读取当前配置失败")
		return
	}
	drift := !configsnapshot.Equal(current, document.After)
	core.HandleSuccess(c, gin.H{"snapshot": document.Snapshot, "current": current, "target": document.Before, "diff": document.Diff, "hasDrift": drift, "requiresForce": drift})
}

func Restore(c *gin.Context) {
	var request struct {
		Force bool `json:"force"`
	}
	if err := c.ShouldBindJSON(&request); err != nil && !errors.Is(err, http.ErrNotSupported) {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "回滚请求格式错误"))
		return
	}
	document, err := configsnapshot.Default().Get(c.Param("id"), snapshotUser(c))
	if err != nil {
		handleSnapshotError(c, err, "读取配置快照失败")
		return
	}
	current, _, err := currentResource(document.Snapshot.ResourceType, document.Snapshot.ResourceID)
	if err != nil {
		handleSnapshotError(c, err, "读取当前配置失败")
		return
	}
	drift := !configsnapshot.Equal(current, document.After)
	if drift && !request.Force {
		core.HandleError(c, core.NewError(core.ErrConflict, "检测到配置已被外部修改，请先确认强制回滚"))
		return
	}
	userID := snapshotUser(c)
	restoreSnapshot, err := configsnapshot.Default().Create(configsnapshot.CreateInput{ResourceType: document.Snapshot.ResourceType, ResourceID: document.Snapshot.ResourceID, Operation: "restore", Before: current, After: document.Before, RequestedBy: userID})
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "创建回滚快照失败"))
		return
	}
	if err := applyResource(document.Snapshot.ResourceType, document.Snapshot.ResourceID, document.Before); err != nil {
		_ = configsnapshot.Default().Mark(restoreSnapshot.ID, "failed", err.Error())
		RecordAudit(c, restoreSnapshot, "failed", err.Error())
		core.HandleError(c, core.WrapError(err, core.ErrConfigError, "执行配置回滚失败"))
		return
	}
	_ = configsnapshot.Default().Mark(restoreSnapshot.ID, "succeeded", "")
	RecordAudit(c, restoreSnapshot, "succeeded", "配置快照已回滚")
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, gin.H{"snapshotId": restoreSnapshot.ID, "status": "succeeded", "operation": "restore"}))
}

func currentResource(resourceType, resourceID string) (any, bool, error) {
	switch resourceType {
	case "website":
		id, err := strconv.ParseInt(resourceID, 10, 64)
		if err != nil {
			return nil, false, err
		}
		service, err := websiteService.DefaultService()
		if err != nil {
			return nil, false, err
		}
		current, err := service.GetSettings(id)
		if err != nil {
			return nil, false, err
		}
		return current.Settings, false, nil
	case "firewall":
		rules, err := safeservice.NewDefaultService().ExportRules("")
		return rules, false, err
	case "nginx":
		manager, err := websiteService.NewDefaultWebServerConfigManager()
		if err != nil {
			return nil, false, err
		}
		document, err := manager.Read(resourceID)
		return document, false, err
	case "panel_access":
		settings, err := systemservice.GetPanelNetworkSettings()
		if err != nil {
			return nil, false, err
		}
		return systemservice.UpdatePanelNetworkRequest{BindAddress: settings.BindAddress, HTTPPort: settings.HTTPPort, HTTPSEnabled: settings.HTTPSEnabled, HTTPSPort: settings.HTTPSPort, HTTPSCertificateFile: settings.HTTPSCertificateFile, HTTPSPrivateKeyFile: settings.HTTPSPrivateKeyFile, TrustedProxies: settings.TrustedProxies, PanelEntryEnabled: settings.PanelEntryEnabled, PanelEntryPath: settings.PanelEntryPath}, false, nil
	default:
		return nil, false, errors.New("不支持的快照资源类型")
	}
}

func applyResource(resourceType, resourceID string, target any) error {
	switch resourceType {
	case "website":
		id, err := strconv.ParseInt(resourceID, 10, 64)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(target)
		if err != nil {
			return err
		}
		var settings websiteService.WebsiteSettings
		if err := json.Unmarshal(payload, &settings); err != nil {
			return err
		}
		service, err := websiteService.DefaultService()
		if err != nil {
			return err
		}
		_, err = service.UpdateSettings(context.Background(), id, settings)
		return err
	case "firewall":
		payload, err := json.Marshal(target)
		if err != nil {
			return err
		}
		var rules []models.IptablesRule
		if err := json.Unmarshal(payload, &rules); err != nil {
			return err
		}
		if len(rules) == 0 {
			return errors.New("不能恢复为空的防火墙规则集")
		}
		return safeservice.NewDefaultService().ReplaceRules(context.Background(), rules)
	case "nginx":
		payload, err := json.Marshal(target)
		if err != nil {
			return err
		}
		var document websiteService.WebServerConfigDocument
		if err := json.Unmarshal(payload, &document); err != nil {
			return err
		}
		manager, err := websiteService.NewDefaultWebServerConfigManager()
		if err != nil {
			return err
		}
		_, err = manager.Update(context.Background(), websiteService.WebServerConfigUpdate{Path: document.Path, Content: document.Content, Revision: document.Revision})
		return err
	case "panel_access":
		payload, err := json.Marshal(target)
		if err != nil {
			return err
		}
		var request systemservice.UpdatePanelNetworkRequest
		if err := json.Unmarshal(payload, &request); err != nil {
			return err
		}
		_, err = systemservice.UpdatePanelNetwork(request)
		return err
	default:
		return errors.New("不支持的快照资源类型")
	}
}

func snapshotArtifact(resourceType string, current any) ([]byte, string) {
	if resourceType == "nginx" {
		if document, ok := current.(websiteService.WebServerConfigDocument); ok {
			return []byte(document.Content), "nginx.conf"
		}
	}
	data, err := json.Marshal(current)
	if err != nil {
		return nil, ""
	}
	return data, resourceType + ".json"
}

func Delete(c *gin.Context) {
	document, err := configsnapshot.Default().Get(c.Param("id"), snapshotUser(c))
	if err != nil {
		handleSnapshotError(c, err, "读取配置快照失败")
		return
	}
	if err := configsnapshot.Default().Delete(c.Param("id"), snapshotUser(c)); err != nil {
		handleSnapshotError(c, err, "删除配置快照失败")
		return
	}
	deleted := document.Snapshot
	deleted.Operation = "delete"
	RecordAudit(c, &deleted, "succeeded", "配置快照已删除")
	c.Status(http.StatusNoContent)
}

func snapshotUser(c *gin.Context) int64 { id, _ := middleware.AuthenticatedUserID(c); return id }

func handleSnapshotError(c *gin.Context, err error, message string) {
	status := core.ErrInternalError
	if errors.Is(err, configsnapshot.ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound) {
		status = core.ErrNotFound
	}
	core.HandleError(c, core.WrapError(err, status, message))
}
