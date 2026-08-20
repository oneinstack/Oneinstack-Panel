package approval

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"oneinstack/internal/i18n"
	"oneinstack/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Service struct {
	db *gorm.DB
}

// ErrRequesterCannotReview is returned when an approval requester tries to
// approve or reject their own pending request.
var (
	ErrRequesterCannotReview = errors.New("requester cannot review own request")
	ErrApprovalNotPending    = errors.New("approval request is not pending")
	ErrApprovalExpired       = errors.New("approval request has expired")
)

type CreateInput struct {
	Module          string
	Action          string
	ResourceID      string
	ResourceName    string
	RiskLevel       string
	Reason          string
	Payload         interface{}
	RequestedBy     int64
	RequestedByName string
	ExpiresAt       time.Time
}

type ListOptions struct {
	Page        int
	PageSize    int
	Status      string
	Module      string
	Action      string
	Mine        bool
	Keyword     string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
	UserID      int64
	IncludeAll  bool
	Locale      string
}

type ListResult struct {
	Items    []ApprovalListItem `json:"items"`
	Total    int64              `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"pageSize"`
}

type ApprovalListItem struct {
	ID            string     `json:"id"`
	ResourceName  string     `json:"resourceName"`
	ActionName    string     `json:"actionName"`
	ModuleName    string     `json:"moduleName"`
	RiskLevelName string     `json:"riskLevelName"`
	Status        string     `json:"status"`
	StatusName    string     `json:"statusName"`
	ApplicantName string     `json:"applicantName"`
	AppliedAt     time.Time  `json:"appliedAt"`
	ReviewedAt    *time.Time `json:"reviewedAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	ExpiresAt     time.Time  `json:"expiresAt"`
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

func (service *Service) Create(input CreateInput) (*models.ApprovalRequest, error) {
	if service.db == nil {
		return nil, errors.New("database is not initialized")
	}
	payload, err := json.Marshal(input.Payload)
	if err != nil {
		return nil, err
	}
	return service.create(input, string(payload))
}

// CreateOrReusePending returns an existing unexpired pending request when the
// requester submits exactly the same approval intent again. The payload is
// part of the match so changed high-risk options always require new approval.
func (service *Service) CreateOrReusePending(input CreateInput) (*models.ApprovalRequest, bool, error) {
	if service.db == nil {
		return nil, false, errors.New("database is not initialized")
	}
	payload, err := json.Marshal(input.Payload)
	if err != nil {
		return nil, false, err
	}

	var existing models.ApprovalRequest
	err = service.db.Where(
		"module = ? AND action = ? AND resource_id = ? AND requested_by = ? AND status = ? AND payload_snapshot = ? AND expires_at > ?",
		strings.TrimSpace(input.Module),
		strings.TrimSpace(input.Action),
		strings.TrimSpace(input.ResourceID),
		input.RequestedBy,
		models.ApprovalStatusPending,
		string(payload),
		time.Now().UTC(),
	).Order("created_at DESC").First(&existing).Error
	if err == nil {
		return &existing, true, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}

	request, err := service.create(input, string(payload))
	return request, false, err
}

func (service *Service) create(input CreateInput, payload string) (*models.ApprovalRequest, error) {
	expiresAt := input.ExpiresAt.UTC()
	if expiresAt.IsZero() {
		expiresAt = time.Now().UTC().Add(24 * time.Hour)
	}
	request := &models.ApprovalRequest{
		ID:              "apr_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Module:          strings.TrimSpace(input.Module),
		Action:          strings.TrimSpace(input.Action),
		ResourceID:      strings.TrimSpace(input.ResourceID),
		ResourceName:    strings.TrimSpace(input.ResourceName),
		RiskLevel:       defaultString(strings.TrimSpace(input.RiskLevel), "high"),
		Status:          models.ApprovalStatusPending,
		Reason:          strings.TrimSpace(input.Reason),
		PayloadSnapshot: payload,
		RequestedBy:     input.RequestedBy,
		RequestedByName: strings.TrimSpace(input.RequestedByName),
		ExpiresAt:       expiresAt,
	}
	return request, service.db.Create(request).Error
}

func (service *Service) Get(id string) (*models.ApprovalRequest, error) {
	var request models.ApprovalRequest
	if err := service.db.First(&request, "id = ?", strings.TrimSpace(id)).Error; err != nil {
		return nil, err
	}
	return &request, nil
}

func (service *Service) List(options ListOptions) (*ListResult, error) {
	page, pageSize := normalizePage(options.Page, options.PageSize)
	query := service.db.Model(&models.ApprovalRequest{})
	now := time.Now().UTC()
	if value := strings.TrimSpace(options.Status); value != "" {
		switch value {
		case models.ApprovalStatusPending:
			query = query.Where("status = ? AND expires_at > ?", value, now)
		case models.ApprovalStatusExpired:
			query = query.Where("(status = ? OR (status = ? AND expires_at <= ?))", value, models.ApprovalStatusPending, now)
		default:
			query = query.Where("status = ?", value)
		}
	}
	if value := strings.TrimSpace(options.Module); value != "" {
		if value == "certificate" {
			query = query.Where("(module = ? OR (module = ? AND action LIKE ?))", value, "website", "certificate.%")
		} else {
			query = query.Where("module = ?", value)
		}
	}
	if value := strings.TrimSpace(options.Action); value != "" {
		query = query.Where("action = ?", value)
	}
	if value := strings.TrimSpace(options.Keyword); value != "" {
		pattern := "%" + value + "%"
		conditions := []string{
			"resource_name LIKE ?", "action LIKE ?", "module LIKE ?", "requested_by_name LIKE ?",
			"id LIKE ?", "resource_id LIKE ?", "bound_task_id LIKE ?",
		}
		args := []interface{}{pattern, pattern, pattern, pattern, pattern, pattern, pattern}
		if actions := matchingApprovalActions(options.Locale, value); len(actions) > 0 {
			conditions = append(conditions, "action IN ?")
			args = append(args, actions)
		}
		if modules := matchingApprovalModules(options.Locale, value); len(modules) > 0 {
			conditions = append(conditions, "module IN ?")
			args = append(args, modules)
		}
		query = query.Where(strings.Join(conditions, " OR "), args...)
	}
	if options.CreatedFrom != nil {
		query = query.Where("created_at >= ?", options.CreatedFrom.UTC())
	}
	if options.CreatedTo != nil {
		query = query.Where("created_at <= ?", options.CreatedTo.UTC())
	}
	if !options.IncludeAll || options.Mine {
		query = query.Where("requested_by = ?", options.UserID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var requests []models.ApprovalRequest
	if err := query.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&requests).Error; err != nil {
		return nil, err
	}
	items := make([]ApprovalListItem, 0, len(requests))
	for i := range requests {
		items = append(items, buildApprovalListItem(&requests[i], options.Locale, now))
	}
	return &ListResult{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func buildApprovalListItem(request *models.ApprovalRequest, locale string, now time.Time) ApprovalListItem {
	var reviewedAt *time.Time
	if request.ApprovedAt != nil {
		reviewedAt = request.ApprovedAt
	} else if request.RejectedAt != nil {
		reviewedAt = request.RejectedAt
	}
	return ApprovalListItem{
		ID:            request.ID,
		ResourceName:  LocalizedApprovalResourceName(locale, request),
		ActionName:    LocalizedActionName(locale, request.Action),
		ModuleName:    LocalizedApprovalModuleName(locale, request.Module, request.Action),
		RiskLevelName: LocalizedRiskLevelName(locale, request.RiskLevel),
		Status:        EffectiveStatus(request, now),
		StatusName:    LocalizedStatusName(locale, EffectiveStatus(request, now)),
		ApplicantName: request.RequestedByName,
		AppliedAt:     request.CreatedAt,
		ReviewedAt:    reviewedAt,
		UpdatedAt:     request.UpdatedAt,
		ExpiresAt:     request.ExpiresAt,
	}
}

func EffectiveStatus(request *models.ApprovalRequest, now time.Time) string {
	if request == nil {
		return ""
	}
	if request.Status == models.ApprovalStatusPending &&
		!request.ExpiresAt.IsZero() && !request.ExpiresAt.After(now.UTC()) {
		return models.ApprovalStatusExpired
	}
	return request.Status
}

func ApprovalResourceName(request *models.ApprovalRequest) string {
	return LocalizedApprovalResourceName(i18n.LocaleZhCN, request)
}

func LocalizedApprovalResourceName(locale string, request *models.ApprovalRequest) string {
	if value := strings.TrimSpace(request.ResourceName); value != "" {
		return value
	}
	if strings.TrimSpace(request.ResourceID) == "" {
		return localizedFallbackName(locale, "资源")
	}
	prefix := "资源"
	switch {
	case request.Module == "database":
		prefix = "数据库"
	case strings.HasPrefix(request.Action, "certificate."):
		prefix = "网站"
	}
	if i18n.Canonical(locale) == i18n.LocaleEnUS {
		prefix = map[string]string{"资源": "Resource", "数据库": "Database", "网站": "Website"}[prefix]
	}
	return prefix + " #" + strings.TrimSpace(request.ResourceID)
}

func LocalizedActionName(locale, action string) string {
	return localizedApprovalName(locale, action, approvalActionNames)
}

func LocalizedModuleName(locale, module string) string {
	return localizedApprovalName(locale, module, approvalModuleNames)
}

func LocalizedApprovalModuleName(locale, module, action string) string {
	if strings.HasPrefix(strings.TrimSpace(action), "certificate.") {
		return localizedApprovalName(locale, "certificate", approvalModuleNames)
	}
	return LocalizedModuleName(locale, module)
}

func LocalizedRiskLevelName(locale, riskLevel string) string {
	return localizedApprovalName(locale, riskLevel, approvalRiskNames)
}

func LocalizedStatusName(locale, status string) string {
	return localizedApprovalName(locale, status, approvalStatusNames)
}

var approvalActionNames = map[string]string{
	"website.delete":             "删除网站",
	"website.restore":            "恢复网站",
	"certificate.issue":          "签发证书",
	"certificate.renew":          "续期证书",
	"certificate.disable":        "禁用证书",
	"database.restore":           "恢复数据库",
	"database.credential.reveal": "查看数据库凭据",
	"database.connection.delete": "删除数据库连接",
}

var approvalModuleNames = map[string]string{
	"website": "网站", "database": "数据库", "certificate": "证书",
}

var approvalRiskNames = map[string]string{
	"low": "低风险", "medium": "中风险", "high": "高风险",
}

var approvalStatusNames = map[string]string{
	models.ApprovalStatusPending:   "待审批",
	models.ApprovalStatusApproved:  "已通过",
	models.ApprovalStatusRejected:  "已拒绝",
	models.ApprovalStatusExpired:   "已过期",
	models.ApprovalStatusExecuting: "执行中",
	models.ApprovalStatusCompleted: "已完成",
	models.ApprovalStatusFailed:    "执行失败",
	models.ApprovalStatusCanceled:  "已取消",
}

func matchingApprovalActions(locale, keyword string) []string {
	return matchingApprovalNames(locale, keyword, approvalActionNames)
}

func matchingApprovalModules(locale, keyword string) []string {
	return matchingApprovalNames(locale, keyword, approvalModuleNames)
}

func matchingApprovalNames(locale, keyword string, names map[string]string) []string {
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	if keyword == "" {
		return nil
	}
	matched := make([]string, 0)
	for code, name := range names {
		if strings.Contains(strings.ToLower(name), keyword) || strings.Contains(strings.ToLower(localizedEnglishName(name)), keyword) {
			matched = append(matched, code)
		}
	}
	return matched
}

func localizedApprovalName(locale, value string, names map[string]string) string {
	value = strings.TrimSpace(value)
	name, ok := names[value]
	if !ok {
		return "其他"
	}
	if i18n.Canonical(locale) == i18n.LocaleEnUS {
		if translated := localizedEnglishName(name); translated != "" {
			return translated
		}
	}
	return name
}

func localizedEnglishName(name string) string {
	return map[string]string{
		"删除网站": "Delete website", "恢复网站": "Restore website", "签发证书": "Issue certificate",
		"续期证书": "Renew certificate", "禁用证书": "Disable certificate", "恢复数据库": "Restore database",
		"查看数据库凭据": "View database credentials", "删除数据库连接": "Delete database connection",
		"网站": "Website", "数据库": "Database", "证书": "Certificate",
		"低风险": "Low", "中风险": "Medium", "高风险": "High",
		"待审批": "Pending", "已通过": "Approved", "已拒绝": "Rejected", "已过期": "Expired",
		"执行中": "Executing", "已完成": "Completed", "执行失败": "Failed", "已取消": "Canceled",
		"其他": "Other",
	}[name]
}

func localizedFallbackName(locale, name string) string {
	if i18n.Canonical(locale) == i18n.LocaleEnUS {
		return localizedEnglishName(name)
	}
	return name
}

func (service *Service) Approve(id string, approverID int64, approverName, comment string) (*models.ApprovalRequest, error) {
	request, err := service.Get(id)
	if err != nil {
		return nil, err
	}
	if request.RequestedBy == approverID {
		return nil, ErrRequesterCannotReview
	}
	if request.Status == models.ApprovalStatusExpired {
		return nil, ErrApprovalExpired
	}
	if request.Status != models.ApprovalStatusPending {
		return nil, ErrApprovalNotPending
	}
	if !request.ExpiresAt.IsZero() && time.Now().UTC().After(request.ExpiresAt) {
		if err := service.db.Model(&models.ApprovalRequest{}).Where("id = ?", request.ID).
			Update("status", models.ApprovalStatusExpired).Error; err != nil {
			return nil, err
		}
		return nil, ErrApprovalExpired
	}
	now := time.Now().UTC()
	err = service.db.Model(&models.ApprovalRequest{}).Where("id = ?", request.ID).Updates(map[string]any{
		"status":           models.ApprovalStatusApproved,
		"approved_by":      approverID,
		"approved_by_name": strings.TrimSpace(approverName),
		"approved_at":      now,
		"review_comment":   strings.TrimSpace(comment),
	}).Error
	if err != nil {
		return nil, err
	}
	return service.Get(request.ID)
}

func (service *Service) Reject(id string, approverID int64, approverName, comment string) (*models.ApprovalRequest, error) {
	request, err := service.Get(id)
	if err != nil {
		return nil, err
	}
	if request.RequestedBy == approverID {
		return nil, ErrRequesterCannotReview
	}
	if request.Status == models.ApprovalStatusExpired {
		return nil, ErrApprovalExpired
	}
	if request.Status != models.ApprovalStatusPending {
		return nil, ErrApprovalNotPending
	}
	if !request.ExpiresAt.IsZero() && time.Now().UTC().After(request.ExpiresAt) {
		if err := service.db.Model(&models.ApprovalRequest{}).Where("id = ?", request.ID).
			Update("status", models.ApprovalStatusExpired).Error; err != nil {
			return nil, err
		}
		return nil, ErrApprovalExpired
	}
	now := time.Now().UTC()
	err = service.db.Model(&models.ApprovalRequest{}).Where("id = ?", request.ID).Updates(map[string]any{
		"status":           models.ApprovalStatusRejected,
		"rejected_by":      approverID,
		"rejected_by_name": strings.TrimSpace(approverName),
		"rejected_at":      now,
		"review_comment":   strings.TrimSpace(comment),
	}).Error
	if err != nil {
		return nil, err
	}
	return service.Get(request.ID)
}

func (service *Service) UpdateExecutionResult(
	id, status, boundTaskType, boundTaskID string,
	result interface{},
) error {
	updates := map[string]any{
		"status":          strings.TrimSpace(status),
		"bound_task_type": strings.TrimSpace(boundTaskType),
		"bound_task_id":   strings.TrimSpace(boundTaskID),
	}
	if result != nil {
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		updates["result_payload"] = string(encoded)
	}
	return service.db.Model(&models.ApprovalRequest{}).Where("id = ?", strings.TrimSpace(id)).Updates(updates).Error
}

// UpdateBoundTaskResult updates an approval that is already executing through
// its durable task binding. Task workers use this path when the reviewer HTTP
// request has already returned, so the approval cannot depend on the request
// lifecycle for its final state.
func (service *Service) UpdateBoundTaskResult(
	boundTaskType, boundTaskID, status string,
	result interface{},
) error {
	updates := map[string]any{"status": strings.TrimSpace(status)}
	if result != nil {
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		updates["result_payload"] = string(encoded)
	}
	return service.db.Model(&models.ApprovalRequest{}).
		Where("bound_task_type = ? AND bound_task_id = ? AND status = ?",
			strings.TrimSpace(boundTaskType), strings.TrimSpace(boundTaskID), models.ApprovalStatusExecuting).
		Updates(updates).Error
}

func normalizePage(page, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
