package configsnapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
	websiteService "oneinstack/internal/services/website"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	maxSnapshotJSONBytes = 4 << 20
	maxSnapshotFileBytes = 16 << 20
)

var (
	ErrNotFound        = errors.New("configuration snapshot not found")
	ErrDriftDetected   = errors.New("managed configuration drift detected")
	ErrInvalidSnapshot = errors.New("invalid configuration snapshot")
)

type CreateInput struct {
	ResourceType   string
	ResourceID     string
	Operation      string
	BeforeRevision string
	AfterRevision  string
	Before         any
	After          any
	RequestedBy    int64
	Artifact       []byte
	ArtifactName   string
	TaskID         string
	Name           string
	Version        string
	BackupAccount  string
	Description    string
}

type Diff struct {
	Added   []string `json:"added"`
	Changed []string `json:"changed"`
	Removed []string `json:"removed"`
	Summary string   `json:"summary"`
}

type Document struct {
	Snapshot models.ConfigurationSnapshot `json:"snapshot"`
	Before   any                          `json:"before"`
	After    any                          `json:"after"`
	Diff     Diff                         `json:"diff"`
}

type Page struct {
	Items    []ListItem `json:"items"`
	Total    int64      `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
}

type ListItem struct {
	models.ConfigurationSnapshot
	ResourceName        string `json:"resourceName,omitempty"`
	ResourceDisplayName string `json:"resourceDisplayName"`
	ConfigPath          string `json:"configPath,omitempty"`
	ResourceMissing     bool   `json:"resourceMissing,omitempty"`
	OperationLabel      string `json:"operationLabel"`
	StatusLabel         string `json:"statusLabel"`
	Version             string `json:"version"`
	Description         string `json:"description"`
	SizeBytes           int64  `json:"sizeBytes"`
	ArtifactSHA256      string `json:"artifactSha256"`
}

func New(database *gorm.DB) *Service { return &Service{db: database} }

type Service struct{ db *gorm.DB }

func Default() *Service { return New(app.DB()) }

func (s *Service) Create(input CreateInput) (*models.ConfigurationSnapshot, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("snapshot database is not initialized")
	}
	if input.RequestedBy <= 0 || strings.TrimSpace(input.ResourceType) == "" || strings.TrimSpace(input.ResourceID) == "" {
		return nil, ErrInvalidSnapshot
	}
	before, err := normalizeJSON(input.Before)
	if err != nil {
		return nil, fmt.Errorf("encode snapshot before: %w", err)
	}
	after, err := normalizeJSON(input.After)
	if err != nil {
		return nil, fmt.Errorf("encode snapshot after: %w", err)
	}
	diff, err := buildDiff(before, after)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.BeforeRevision) == "" {
		input.BeforeRevision = revision(before)
	}
	if strings.TrimSpace(input.AfterRevision) == "" {
		input.AfterRevision = revision(after)
	}
	if strings.TrimSpace(input.Version) == "" {
		input.Version = revisionLabel(input.AfterRevision)
	}
	now := time.Now().UTC()
	snapshot := &models.ConfigurationSnapshot{
		ID: uuid.NewString(), ResourceType: strings.TrimSpace(input.ResourceType), ResourceID: strings.TrimSpace(input.ResourceID),
		Operation: strings.TrimSpace(input.Operation), Status: models.ConfigurationSnapshotStatusPending,
		BeforeRevision: strings.TrimSpace(input.BeforeRevision), AfterRevision: strings.TrimSpace(input.AfterRevision),
		BeforeJSON: string(before), AfterJSON: string(after), DiffJSON: string(diff), TaskID: strings.TrimSpace(input.TaskID),
		RequestedBy: input.RequestedBy, CreatedAt: now,
		Name: strings.TrimSpace(input.Name), Version: strings.TrimSpace(input.Version),
		BackupAccount: strings.TrimSpace(input.BackupAccount), Description: truncate(input.Description, 255),
	}
	if snapshot.Name == "" {
		snapshot.Name = snapshot.ResourceID
	}
	if snapshot.BackupAccount == "" {
		snapshot.BackupAccount = "local"
	}
	if len(input.Artifact) > maxSnapshotFileBytes {
		return nil, errors.New("snapshot artifact exceeds size limit")
	}
	if len(input.Artifact) > 0 {
		path, digest, err := writeArtifact(snapshot.ID, input.Artifact, input.ArtifactName)
		if err != nil {
			return nil, err
		}
		snapshot.ArtifactPath, snapshot.ArtifactSHA256 = path, digest
		snapshot.SizeBytes = int64(len(input.Artifact))
	}
	if err := s.db.Create(snapshot).Error; err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *Service) Mark(id, status, failure string) error {
	updates := map[string]any{"status": strings.TrimSpace(status), "failure_message": truncate(failure, 1024), "updated_at": time.Now().UTC()}
	if status == models.ConfigurationSnapshotStatusSucceeded || strings.HasSuffix(status, "failed") || status == models.ConfigurationSnapshotStatusRolledBack {
		now := time.Now().UTC()
		updates["finished_at"] = &now
	}
	result := s.db.Model(&models.ConfigurationSnapshot{}).Where("id = ?", strings.TrimSpace(id)).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) MarkWithAfter(id string, after any, status, failure string) error {
	afterJSON, err := normalizeJSON(after)
	if err != nil {
		return err
	}
	var row models.ConfigurationSnapshot
	if err := s.db.Where("id = ?", strings.TrimSpace(id)).First(&row).Error; err != nil {
		return err
	}
	diffJSON, err := buildDiff([]byte(row.BeforeJSON), afterJSON)
	if err != nil {
		return err
	}
	updates := map[string]any{"after_json": string(afterJSON), "diff_json": string(diffJSON), "status": strings.TrimSpace(status), "failure_message": truncate(failure, 1024), "updated_at": time.Now().UTC()}
	if status == models.ConfigurationSnapshotStatusSucceeded || strings.HasSuffix(status, "failed") || status == models.ConfigurationSnapshotStatusRolledBack {
		now := time.Now().UTC()
		updates["finished_at"] = &now
	}
	return s.db.Model(&models.ConfigurationSnapshot{}).Where("id = ?", row.ID).Updates(updates).Error
}

func (s *Service) Get(id string, userID int64) (Document, error) {
	var row models.ConfigurationSnapshot
	q := s.db.Where("id = ?", strings.TrimSpace(id))
	if userID > 0 {
		q = q.Where("requested_by = ?", userID)
	}
	if err := q.First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Document{}, ErrNotFound
		}
		return Document{}, err
	}
	return decodeDocument(row)
}

func (s *Service) List(resourceType, resourceID, status, locale string, page, pageSize int, userID int64) (Page, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	q := s.db.Model(&models.ConfigurationSnapshot{})
	if userID > 0 {
		q = q.Where("requested_by = ?", userID)
	}
	if v := strings.TrimSpace(resourceType); v != "" {
		q = q.Where("resource_type = ?", v)
	}
	if v := strings.TrimSpace(resourceID); v != "" {
		q = q.Where("resource_id = ?", v)
	}
	if v := strings.TrimSpace(status); v != "" {
		q = q.Where("status = ?", v)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return Page{}, err
	}
	var rows []models.ConfigurationSnapshot
	if err := q.Order("created_at DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&rows).Error; err != nil {
		return Page{}, err
	}
	items := make([]ListItem, 0, len(rows))
	var sites map[string]models.Website
	websiteIDs := make([]int64, 0)
	for _, row := range rows {
		if row.ResourceType == models.ConfigurationSnapshotResourceWebsite {
			if id, err := strconv.ParseInt(row.ResourceID, 10, 64); err == nil && id > 0 {
				websiteIDs = append(websiteIDs, id)
			}
		}
	}
	if len(websiteIDs) > 0 {
		sites = make(map[string]models.Website, len(websiteIDs))
		var records []models.Website
		if err := s.db.Where("id IN ?", websiteIDs).Find(&records).Error; err != nil {
			return Page{}, err
		}
		for _, site := range records {
			sites[strconv.FormatInt(site.ID, 10)] = site
		}
	}
	var webService *websiteService.Service
	if len(sites) > 0 {
		webService, _ = websiteService.DefaultService()
	}
	for _, row := range rows {
		item := ListItem{
			ConfigurationSnapshot: row,
			OperationLabel:        operationLabel(locale, row.Operation),
			StatusLabel:           statusLabel(locale, row.Status),
			Version:               row.Version,
			Description:           row.Description,
			SizeBytes:             row.SizeBytes,
			ArtifactSHA256:        row.ArtifactSHA256,
		}
		if item.BeforeRevision == "" {
			item.BeforeRevision = revision([]byte(row.BeforeJSON))
		}
		if item.AfterRevision == "" {
			item.AfterRevision = revision([]byte(row.AfterJSON))
		}
		if strings.TrimSpace(item.Version) == "" {
			item.Version = revisionLabel(item.AfterRevision)
		}
		item.ResourceDisplayName = resourceFallback(row.ResourceType, row.ResourceID)
		if row.ResourceType == models.ConfigurationSnapshotResourceWebsite {
			if site, ok := sites[row.ResourceID]; ok {
				item.ResourceName = firstNonEmpty(site.Domain, site.Name)
				item.ResourceDisplayName = firstNonEmpty(site.Name, site.Domain, "网站 #"+row.ResourceID)
				if strings.TrimSpace(item.Description) == "" {
					item.Description = strings.TrimSpace(site.Remark)
				}
				if webService != nil {
					item.ConfigPath, _ = webService.ConfigFile(&site)
				}
			} else {
				item.ResourceMissing = true
			}
		} else if row.ResourceType == models.ConfigurationSnapshotResourceNginx {
			item.ConfigPath = row.ResourceID
			item.ResourceName = row.ResourceID
			item.ResourceDisplayName = row.ResourceID
		}
		if strings.TrimSpace(item.Description) == "" {
			item.Description = item.ResourceDisplayName
		}
		items = append(items, item)
	}
	return Page{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (s *Service) Delete(id string, userID int64) error {
	var row models.ConfigurationSnapshot
	q := s.db.Where("id = ?", strings.TrimSpace(id))
	if userID > 0 {
		q = q.Where("requested_by = ?", userID)
	}
	if err := q.First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	if row.Status == models.ConfigurationSnapshotStatusApplying || row.Status == models.ConfigurationSnapshotStatusPending {
		return errors.New("active snapshot cannot be deleted")
	}
	if strings.TrimSpace(row.ArtifactPath) != "" {
		_ = os.Remove(row.ArtifactPath)
	}
	return s.db.Delete(&row).Error
}

func revision(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func revisionLabel(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "sha256:") {
		value = strings.TrimPrefix(value, "sha256:")
	}
	if len(value) > 12 {
		value = value[:12]
	}
	if value == "" {
		return ""
	}
	return "rev-" + value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func resourceFallback(resourceType, resourceID string) string {
	labels := map[string]string{
		models.ConfigurationSnapshotResourceWebsite:     "网站",
		models.ConfigurationSnapshotResourceNginx:       "Nginx 配置",
		models.ConfigurationSnapshotResourceFirewall:    "防火墙",
		models.ConfigurationSnapshotResourcePanelAccess: "面板访问配置",
	}
	label := firstNonEmpty(labels[resourceType], resourceType)
	return label + " #" + strings.TrimSpace(resourceID)
}

func operationLabel(locale, operation string) string {
	labels := map[string][2]string{
		"create":          {"创建配置快照", "Create configuration snapshot"},
		"update":          {"更新配置", "Update configuration"},
		"settings.update": {"更新网站设置", "Update website settings"},
		"config.update":   {"更新网站配置", "Update website configuration"},
		"restore":         {"回滚配置", "Restore configuration"},
		"delete":          {"删除配置", "Delete configuration"},
		"toggle":          {"切换网站状态", "Toggle website status"},
	}
	value, ok := labels[strings.TrimSpace(operation)]
	if !ok {
		value = [2]string{"配置变更", "Configuration change"}
	}
	if strings.EqualFold(strings.TrimSpace(locale), "en-us") || strings.EqualFold(strings.TrimSpace(locale), "en") {
		return value[1]
	}
	return value[0]
}

func statusLabel(locale, status string) string {
	labels := map[string][2]string{
		models.ConfigurationSnapshotStatusPending:        {"等待执行", "Pending"},
		models.ConfigurationSnapshotStatusApplying:       {"执行中", "Applying"},
		models.ConfigurationSnapshotStatusSucceeded:      {"成功", "Succeeded"},
		models.ConfigurationSnapshotStatusFailed:         {"失败", "Failed"},
		models.ConfigurationSnapshotStatusRolledBack:     {"已回滚", "Rolled back"},
		models.ConfigurationSnapshotStatusRollbackFailed: {"回滚失败", "Rollback failed"},
	}
	value, ok := labels[strings.TrimSpace(status)]
	if !ok {
		value = [2]string{"未知", "Unknown"}
	}
	if strings.EqualFold(strings.TrimSpace(locale), "en-us") || strings.EqualFold(strings.TrimSpace(locale), "en") {
		return value[1]
	}
	return value[0]
}

func Equal(a, b any) bool {
	x, err := normalizeJSON(a)
	if err != nil {
		return false
	}
	y, err := normalizeJSON(b)
	if err != nil {
		return false
	}
	return string(x) == string(y)
}

func decodeDocument(row models.ConfigurationSnapshot) (Document, error) {
	var before, after any
	if err := json.Unmarshal([]byte(row.BeforeJSON), &before); err != nil {
		return Document{}, err
	}
	if err := json.Unmarshal([]byte(row.AfterJSON), &after); err != nil {
		return Document{}, err
	}
	var diff Diff
	if err := json.Unmarshal([]byte(row.DiffJSON), &diff); err != nil {
		return Document{}, err
	}
	return Document{Snapshot: row, Before: before, After: after, Diff: diff}, nil
}

func normalizeJSON(value any) ([]byte, error) {
	b, err := json.Marshal(redact(value))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSnapshotJSONBytes {
		return nil, errors.New("snapshot JSON exceeds size limit")
	}
	return b, nil
}

func redact(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			lower := strings.ToLower(k)
			if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "privatekey") || strings.Contains(lower, "private_key") {
				out[k] = "[REDACTED]"
			} else {
				out[k] = redact(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = redact(v[i])
		}
		return out
	default:
		return value
	}
}

func buildDiff(before, after []byte) ([]byte, error) {
	var left, right any
	if err := json.Unmarshal(before, &left); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(after, &right); err != nil {
		return nil, err
	}
	d := Diff{}
	leftMap, leftOK := left.(map[string]any)
	rightMap, rightOK := right.(map[string]any)
	if leftOK && rightOK {
		collectDiff("", leftMap, rightMap, &d)
	} else if !equalJSON(left, right) {
		d.Changed = []string{"$"}
	}
	if len(d.Added)+len(d.Changed)+len(d.Removed) == 0 {
		d.Summary = "无配置变化"
	} else {
		d.Summary = fmt.Sprintf("新增 %d 项，修改 %d 项，删除 %d 项", len(d.Added), len(d.Changed), len(d.Removed))
	}
	return json.Marshal(d)
}

func collectDiff(prefix string, left, right map[string]any, d *Diff) {
	keys := make(map[string]struct{}, len(left)+len(right))
	for k := range left {
		keys[k] = struct{}{}
	}
	for k := range right {
		keys[k] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		lv, lok := left[key]
		rv, rok := right[key]
		if !lok {
			d.Added = append(d.Added, path)
			continue
		}
		if !rok {
			d.Removed = append(d.Removed, path)
			continue
		}
		if equalJSON(lv, rv) {
			continue
		}
		lm, leftMap := lv.(map[string]any)
		rm, rightMap := rv.(map[string]any)
		if leftMap && rightMap {
			collectDiff(path, lm, rm, d)
		} else {
			d.Changed = append(d.Changed, path)
		}
	}
}

func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func writeArtifact(id string, data []byte, name string) (string, string, error) {
	dir := filepath.Join(app.GetBasePath(), "configuration-snapshots", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", "", err
	}
	safeName := filepath.Base(name)
	if safeName == "." || safeName == string(filepath.Separator) || safeName == "" {
		safeName = "artifact.json"
	}
	path := filepath.Join(dir, safeName)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", "", err
	}
	digest := sha256.Sum256(data)
	return path, hex.EncodeToString(digest[:]), nil
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
