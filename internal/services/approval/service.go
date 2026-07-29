package approval

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"oneinstack/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Service struct {
	db *gorm.DB
}

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
	Page       int
	PageSize   int
	Status     string
	Module     string
	Mine       bool
	Keyword    string
	UserID     int64
	IncludeAll bool
}

type ListResult struct {
	Items    []models.ApprovalRequest `json:"items"`
	Total    int64                    `json:"total"`
	Page     int                      `json:"page"`
	PageSize int                      `json:"pageSize"`
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
		PayloadSnapshot: string(payload),
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
	if value := strings.TrimSpace(options.Status); value != "" {
		query = query.Where("status = ?", value)
	}
	if value := strings.TrimSpace(options.Module); value != "" {
		query = query.Where("module = ?", value)
	}
	if value := strings.TrimSpace(options.Keyword); value != "" {
		query = query.Where("resource_name LIKE ? OR action LIKE ? OR requested_by_name LIKE ?", "%"+value+"%", "%"+value+"%", "%"+value+"%")
	}
	if !options.IncludeAll || options.Mine {
		query = query.Where("requested_by = ?", options.UserID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var items []models.ApprovalRequest
	if err := query.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		return nil, err
	}
	return &ListResult{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (service *Service) Approve(id string, approverID int64, approverName, comment string) (*models.ApprovalRequest, error) {
	request, err := service.Get(id)
	if err != nil {
		return nil, err
	}
	if request.RequestedBy == approverID {
		return nil, errors.New("requester cannot approve own request")
	}
	if request.Status != models.ApprovalStatusPending {
		return nil, errors.New("approval request is not pending")
	}
	if !request.ExpiresAt.IsZero() && time.Now().UTC().After(request.ExpiresAt) {
		if err := service.db.Model(&models.ApprovalRequest{}).Where("id = ?", request.ID).
			Update("status", models.ApprovalStatusExpired).Error; err != nil {
			return nil, err
		}
		return nil, errors.New("approval request has expired")
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
		return nil, errors.New("requester cannot reject own request")
	}
	if request.Status != models.ApprovalStatusPending {
		return nil, errors.New("approval request is not pending")
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
