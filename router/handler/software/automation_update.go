package software

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/gin-gonic/gin"
	"oneinstack/app"
	"oneinstack/internal/buildinfo"
	"oneinstack/internal/models"
	"oneinstack/internal/services/panelupdate"
)

type automationPanelUpdate struct {
	InstanceID    string `json:"instanceId"`
	Version       string `json:"version"`
	ArchiveSHA256 string `json:"archiveSHA256"`
	BinarySHA256  string `json:"binarySHA256"`
	PlanDigest    string `json:"planDigest"`
	SourceCommit  string `json:"sourceCommit"`
	Channel       string `json:"channel"`
}

func automationBinarySHA() (string, error) {
	file, err := os.Executable()
	if runtime.GOOS == "linux" {
		file = "/proc/self/exe"
		err = nil
	}
	if err != nil {
		return "", err
	}
	stream, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, stream); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func AutomationPanelUpdate(c *gin.Context) {
	automationMu.Lock()
	defer automationMu.Unlock()
	job := c.Param("jobID")
	if !automationID.MatchString(job) {
		automationError(c, 400, "AUTOMATION_INVALID_PLAN", "复测编号无效 / invalid retest identity")
		return
	}
	file := filepath.Join(app.GetBasePath(), "automation", "panel-updates", job+".json")
	var plan automationPanelUpdate
	if c.Request.Method == http.MethodGet {
		data, err := os.ReadFile(file)
		if err != nil || json.Unmarshal(data, &plan) != nil {
			automationError(c, 404, "AUTOMATION_NOT_FOUND", "更新记录不存在 / update request not found")
			return
		}
	} else {
		decoder := json.NewDecoder(io.LimitReader(c.Request.Body, 16384))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&plan) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			automationError(c, 400, "AUTOMATION_INVALID_PLAN", "更新方案无效 / invalid update plan")
			return
		}
	}
	identity, err := automationIdentity(c.Request.Context())
	if err != nil || identity.InstanceID() != plan.InstanceID || !automationHash.MatchString(plan.ArchiveSHA256) || !automationHash.MatchString(plan.BinarySHA256) || !automationHash.MatchString(plan.PlanDigest) ||
		!regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-retest\.[0-9]{14})?$`).MatchString(plan.Version) || !regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`).MatchString(plan.SourceCommit) ||
		(plan.Channel != "development" && plan.Channel != "stable") || (plan.Channel == "stable" && strings.Contains(plan.Version, "-")) || (plan.Channel == "development" && !strings.Contains(plan.Version, "-retest.")) {
		automationError(c, 409, "AUTOMATION_PIN_MISMATCH", "实际实例或固定更新身份不匹配 / instance or pinned update mismatch")
		return
	}
	encoded, _ := json.Marshal(plan)
	if data, readErr := os.ReadFile(file); readErr == nil {
		var prior automationPanelUpdate
		if json.Unmarshal(data, &prior) != nil || prior != plan {
			automationError(c, 409, "AUTOMATION_PLAN_CHANGED", "更新方案不可覆盖 / immutable update request")
			return
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		automationError(c, 503, "AUTOMATION_STATE_UNAVAILABLE", "更新记录不可用 / update state unavailable")
		return
	}
	manager, err := panelupdate.NewApplicationManager("")
	if err != nil {
		automationError(c, 503, "AUTOMATION_UPDATE_UNAVAILABLE", "受控更新不可用 / controlled update unavailable")
		return
	}
	status, statusErr := manager.Status()
	if c.Request.Method == http.MethodGet {
		if statusErr != nil {
			automationError(c, 503, "AUTOMATION_UPDATE_UNAVAILABLE", "更新状态不可用 / update status unavailable")
			return
		}
		automationOK(c, 200, gin.H{"instanceId": plan.InstanceID, "request": plan, "status": status})
		return
	}
	if buildinfo.Current().Version == plan.Version {
		digest, err := automationBinarySHA()
		if err != nil || digest != plan.BinarySHA256 || buildinfo.Current().CommitHash != plan.SourceCommit {
			automationError(c, 409, "AUTOMATION_BINARY_MISMATCH", "运行中的更新字节或源码提交不匹配 / running update identity mismatch")
			return
		}
		automationOK(c, 200, gin.H{"instanceId": plan.InstanceID, "accepted": true, "alreadyApplied": true})
		return
	}
	var active int64
	if app.DB().Model(&models.SoftwareTask{}).Where("status IN ?", models.ActiveSoftwareTaskStatuses()).Count(&active).Error != nil || active != 0 {
		automationError(c, 409, "AUTOMATION_ACTIVE_TASKS", "存在活动组件任务，不能重启 Panel / active component tasks prevent update")
		return
	}
	if err := writeAutomationFile(file, encoded); err != nil {
		automationError(c, 503, "AUTOMATION_STATE_UNAVAILABLE", "无法保存更新方案 / cannot persist update plan")
		return
	}
	result, err := manager.QueueAutomationUpdate(c.Request.Context(), plan.Version, plan.ArchiveSHA256, plan.Channel)
	if err != nil {
		automationError(c, 409, "AUTOMATION_UPDATE_REJECTED", "固定更新未受理，请检查签名目标、更新服务和恢复状态 / pinned update rejected; inspect assignment, updater and recovery state")
		return
	}
	automationOK(c, 202, gin.H{"instanceId": plan.InstanceID, "accepted": true, "version": result.LatestVersion})
}
