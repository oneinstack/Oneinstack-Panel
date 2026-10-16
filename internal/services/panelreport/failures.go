package panelreport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"oneinstack/internal/models"
	"oneinstack/internal/services/translation"
)

const failureSyncPath = "/v1/panel/failures/sync"
const failureTimestamp = "2006-01-02T15:04:05.000000000Z"

type failureOutboxRow struct {
	ID        string `gorm:"column:id"`
	TaskType  string `gorm:"column:task_type"`
	TaskID    string `gorm:"column:task_id"`
	CreatedAt string `gorm:"column:created_at"`
	Attempts  int    `gorm:"column:attempts"`
}

type failureEvent struct {
	ID             string    `json:"id"`
	TaskType       string    `json:"taskType"`
	TaskID         string    `json:"taskId"`
	Operation      string    `json:"operation"`
	Component      string    `json:"component,omitempty"`
	RequestedBy    int64     `json:"requestedBy"`
	Status         string    `json:"status"`
	FailurePhase   string    `json:"failurePhase,omitempty"`
	ErrorCode      string    `json:"errorCode,omitempty"`
	ErrorMessage   string    `json:"errorMessage,omitempty"`
	RollbackStatus string    `json:"rollbackStatus,omitempty"`
	Version        string    `json:"version,omitempty"`
	PackageVersion string    `json:"packageVersion,omitempty"`
	OSID           string    `json:"osId,omitempty"`
	OSVersion      string    `json:"osVersion,omitempty"`
	Architecture   string    `json:"architecture,omitempty"`
	Evidence       []string  `json:"evidence,omitempty"`
	FinishedAt     time.Time `json:"finishedAt"`
}

var (
	failureSensitiveLine = regexp.MustCompile(`(?i)password|passwd|\bpwd\b|token|secret|authorization|cookie|credential|private.key|api[_-]?key|\bdsn\b|jdbc`)
	failureURL           = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://\S+`)
	failureIPv4          = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	failureIPv6          = regexp.MustCompile(`(?i)(?:\[[0-9a-f:]{2,}\]|\b(?:[0-9a-f]{1,4}:){2,}[0-9a-f:]{1,}\b)`)
	failureEmail         = regexp.MustCompile(`\b[^\s@]+@[^\s@]+\.[^\s@]+\b`)
	failurePath          = regexp.MustCompile(`(^|[\s(])/(?:[^/\s:;,)]+/)*[^/\s:;,)]+`)
	failureWindowsPath   = regexp.MustCompile(`(?i)\b[a-z]:\\(?:[^\\\s]+\\)*[^\\\s]+`)
	failureLongValue     = regexp.MustCompile(`\b[A-Za-z0-9_+/=-]{24,}\b`)
)

func cleanFailureText(value string, limit int) string {
	value = strings.ReplaceAll(value, "\x00", "")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	if failureSensitiveLine.MatchString(value) {
		return "[敏感信息已省略，请查看受控任务详情]"
	}
	value = failureURL.ReplaceAllString(value, "[url]")
	value = failureIPv4.ReplaceAllString(value, "[ip]")
	value = failureIPv6.ReplaceAllString(value, "[ip]")
	value = failureEmail.ReplaceAllString(value, "[email]")
	value = failurePath.ReplaceAllString(value, "${1}[path]")
	value = failureWindowsPath.ReplaceAllString(value, "[path]")
	value = failureLongValue.ReplaceAllString(value, "[value]")
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}

func failureEvidence(value string) []string {
	lines := strings.Split(value, "\n")
	result := make([]string, 0, 10)
	bytesUsed := 0
	for i := len(lines) - 1; i >= 0 && len(result) < 10; i-- {
		line := strings.TrimSpace(lines[i])
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "error") && !strings.Contains(lower, "fail") &&
			!strings.Contains(lower, "denied") && !strings.Contains(lower, "timeout") &&
			!strings.Contains(lower, "错误") && !strings.Contains(lower, "失败") {
			continue
		}
		line = cleanFailureText(line, 256)
		if line == "" || strings.HasPrefix(line, "[敏感信息") || bytesUsed+len(line) > 2048 {
			continue
		}
		result = append([]string{line}, result...)
		bytesUsed += len(line)
	}
	return result
}

func (m *Manager) readFailureLog(path string) []string {
	if path == "" {
		return nil
	}
	root, err := filepath.Abs(m.config.LogDir)
	if err != nil {
		return nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil || !strings.HasPrefix(absolute, root+string(os.PathSeparator)) {
		return nil
	}
	if info, err := os.Lstat(absolute); err != nil || !info.Mode().IsRegular() {
		return nil
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil
	}
	defer file.Close()
	if info, statErr := file.Stat(); statErr == nil && info.Size() > 64<<10 {
		_, _ = file.Seek(-64<<10, io.SeekEnd)
	}
	contents, _ := io.ReadAll(io.LimitReader(file, 64<<10))
	return failureEvidence(string(contents))
}

func (m *Manager) buildFailure(row failureOutboxRow, server serverSnapshot) (failureEvent, error) {
	event := failureEvent{ID: row.ID, TaskType: row.TaskType, TaskID: row.TaskID, Status: "failed", OSID: server.OSID, OSVersion: server.OSVersion, Architecture: server.Architecture}
	finish := func(at *time.Time) {
		if at != nil {
			event.FinishedAt = at.UTC()
			return
		}
		if captured, err := time.Parse(time.RFC3339Nano, row.CreatedAt); err == nil {
			event.FinishedAt = captured
			return
		}
		event.FinishedAt = time.Now().UTC()
	}
	switch row.TaskType {
	case "software":
		var task models.SoftwareTask
		if err := m.db.First(&task, "id = ?", row.TaskID).Error; err != nil {
			return event, err
		}
		event.Operation, event.Component, event.RequestedBy = task.Operation, task.Component, task.RequestedBy
		event.FailurePhase, event.ErrorCode, event.ErrorMessage = task.FailurePhase, task.ErrorCode, task.ErrorMessage
		event.RollbackStatus, event.Version, event.PackageVersion = task.RollbackStatus, task.RequestedVersion, task.ResolvedVersion
		if task.TargetOS != "" {
			event.OSID, event.OSVersion, event.Architecture = task.TargetOS, task.TargetOSVersion, task.TargetArch
		}
		if path, err := m.safeLogPath(task); err == nil {
			event.Evidence = m.readFailureLog(path)
		}
		finish(task.FinishedAt)
	case "container":
		var task models.ContainerTask
		if err := m.db.First(&task, "id = ?", row.TaskID).Error; err != nil {
			return event, err
		}
		event.Operation, event.Component, event.RequestedBy = task.Operation, task.Name, task.RequestedBy
		event.FailurePhase, event.ErrorCode, event.ErrorMessage = task.FailurePhase, task.ErrorCode, task.ErrorMessage
		finish(task.FinishedAt)
	case "cluster":
		var task models.ClusterTask
		if err := m.db.First(&task, "id = ?", row.TaskID).Error; err != nil {
			return event, err
		}
		event.Operation, event.Component, event.RequestedBy = task.Type, fmt.Sprintf("node#%d", task.NodeID), task.RequestedBy
		event.FailurePhase, event.ErrorCode, event.ErrorMessage = task.Stage, "CLUSTER_TASK_FAILED", task.Error
		event.OSID, event.OSVersion, event.Architecture = "", "", ""
		var node models.ClusterNode
		if err := m.db.Select("system_id", "system_version", "architecture").First(&node, task.NodeID).Error; err == nil {
			event.OSID, event.OSVersion, event.Architecture = node.SystemID, node.SystemVersion, node.Architecture
		}
		finish(task.FinishedAt)
	case "certificate":
		var task models.CertificateTask
		if err := m.db.First(&task, "id = ?", row.TaskID).Error; err != nil {
			return event, err
		}
		event.Operation, event.Component, event.RequestedBy = task.Operation, fmt.Sprintf("certificate#%d", task.WebsiteID), task.RequestedBy
		event.ErrorCode, event.ErrorMessage, event.Evidence = task.ErrorCode, task.ErrorMessage, failureEvidence(task.LogText)
		finish(task.FinishedAt)
	case "database":
		var task models.DatabaseTask
		if err := m.db.First(&task, "id = ?", row.TaskID).Error; err != nil {
			return event, err
		}
		event.Operation, event.Component, event.RequestedBy = task.Operation, fmt.Sprintf("database#%d", task.LibraryID), task.RequestedBy
		event.ErrorCode, event.ErrorMessage = task.ErrorCode, task.ErrorMessage
		finish(task.FinishedAt)
	case "website":
		var task models.WebsiteTask
		if err := m.db.First(&task, "id = ?", row.TaskID).Error; err != nil {
			return event, err
		}
		event.Operation, event.Component, event.RequestedBy = task.Operation, fmt.Sprintf("website#%d", task.WebsiteID), task.RequestedBy
		event.ErrorCode, event.ErrorMessage = task.ErrorCode, task.ErrorMessage
		finish(task.FinishedAt)
	case "file_archive":
		var task models.FileArchiveTask
		if err := m.db.First(&task, "id = ?", row.TaskID).Error; err != nil {
			return event, err
		}
		event.Operation, event.Component, event.RequestedBy = task.Operation, "file_archive", task.RequestedBy
		event.ErrorCode, event.ErrorMessage = task.ErrorCode, task.Message
		finish(task.FinishedAt)
	case "fail2ban":
		var task models.Fail2banTask
		if err := m.db.First(&task, "id = ?", row.TaskID).Error; err != nil {
			return event, err
		}
		event.Operation, event.Component, event.RequestedBy = task.Operation, "fail2ban", task.RequestedBy
		event.FailurePhase, event.ErrorCode, event.ErrorMessage = task.Phase, task.ErrorCode, task.ErrorMessage
		finish(task.FinishedAt)
	case "cron":
		id, err := strconv.ParseUint(row.TaskID, 10, 64)
		if err != nil {
			return event, err
		}
		var task models.JobExecution
		if err := m.db.First(&task, "id = ?", id).Error; err != nil {
			return event, err
		}
		event.Operation, event.Component, event.ErrorCode = "run", fmt.Sprintf("cron#%d", task.CronJobID), task.ErrorCode
		event.Status, event.ErrorMessage, event.Evidence = task.Status, fmt.Sprintf("退出码 %d", task.ExitCode), failureEvidence(task.Output)
		if task.EndTime.IsZero() {
			finish(nil)
		} else {
			event.FinishedAt = task.EndTime.UTC()
		}
	case "artifact_cleanup":
		var task models.ArtifactCleanupTask
		if err := m.db.First(&task, "id = ?", row.TaskID).Error; err != nil {
			return event, err
		}
		event.Operation, event.Component, event.RequestedBy = "cleanup", task.Component, task.RequestedBy
		event.ErrorCode, event.ErrorMessage = task.ErrorCode, task.ErrorText
		finish(task.FinishedAt)
	default:
		return event, fmt.Errorf("unsupported failure task type")
	}
	event.ErrorMessage = cleanFailureText(event.ErrorMessage, 512)
	event.Operation = cleanFailureText(event.Operation, 80)
	event.Component = cleanFailureText(event.Component, 80)
	event.FailurePhase = cleanFailureText(event.FailurePhase, 64)
	event.ErrorCode = cleanFailureText(event.ErrorCode, 64)
	event.RollbackStatus = cleanFailureText(event.RollbackStatus, 40)
	event.Version = cleanFailureText(event.Version, 64)
	event.PackageVersion = cleanFailureText(event.PackageVersion, 64)
	event.OSID = cleanFailureText(event.OSID, 32)
	event.OSVersion = cleanFailureText(event.OSVersion, 32)
	event.Architecture = cleanFailureText(event.Architecture, 24)
	if event.ErrorCode == "" {
		event.ErrorCode = "TASK_FAILED"
	}
	return event, nil
}

func (m *Manager) syncFailures(ctx context.Context, client *translation.SignedPanelClient) {
	now := time.Now().UTC()
	var rows []failureOutboxRow
	if err := m.db.Table("failure_outbox").Where("reported_at IS NULL AND (next_attempt_at IS NULL OR next_attempt_at <= ?)", now.Format(failureTimestamp)).Order("created_at ASC").Limit(30).Scan(&rows).Error; err != nil || len(rows) == 0 {
		return
	}
	server := collectServerSnapshot(ctx)
	events := make([]failureEvent, 0, len(rows))
	for _, row := range rows {
		event, err := m.buildFailure(row, server)
		if err != nil {
			m.deferFailure(row, "task unavailable")
			continue
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		return
	}
	body, err := json.Marshal(map[string]any{"schemaVersion": 1, "instanceId": client.InstanceID(), "failures": events})
	if err != nil {
		return
	}
	request, err := client.NewRequest(ctx, http.MethodPost, failureSyncPath, signatureDomain, body)
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		for _, row := range rows {
			m.deferFailure(row, "Center unreachable")
		}
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		for _, row := range rows {
			m.deferFailure(row, fmt.Sprintf("Center HTTP %d", response.StatusCode))
		}
		return
	}
	var accepted struct {
		AcceptedIDs []string `json:"acceptedIds"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&accepted); err != nil {
		return
	}
	allowed := make(map[string]bool, len(events))
	for _, event := range events {
		allowed[event.ID] = true
	}
	for _, id := range accepted.AcceptedIDs {
		if allowed[id] {
			_ = m.db.Table("failure_outbox").Where("id = ? AND reported_at IS NULL", id).Updates(map[string]any{"reported_at": now.Format(failureTimestamp), "last_error": ""}).Error
		}
	}
}

func (m *Manager) deferFailure(row failureOutboxRow, reason string) {
	delay := time.Duration(1<<min(row.Attempts, 8)) * time.Minute
	_ = m.db.Table("failure_outbox").Where("id = ? AND reported_at IS NULL", row.ID).Updates(map[string]any{
		"attempts": row.Attempts + 1, "next_attempt_at": time.Now().UTC().Add(delay).Format(failureTimestamp), "last_error": reason,
	}).Error
}
