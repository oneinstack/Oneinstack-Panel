package operationpreview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"oneinstack/internal/models"
	"oneinstack/utils"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	credentialPurpose = "operation-preview.payload"
	defaultTTL        = 5 * time.Minute
)

var (
	ErrNotFound       = errors.New("operation preview not found")
	ErrExpired        = errors.New("operation preview expired")
	ErrConsumed       = errors.New("operation preview already consumed")
	ErrRequestChanged = errors.New("operation request changed since preview")
)

type Review struct {
	Required  bool   `json:"required"`
	RiskLevel string `json:"riskLevel"`
	Reason    string `json:"reason,omitempty"`
}

type FileChange struct {
	Path          string `json:"path"`
	Action        string `json:"action"`
	ChangeSummary string `json:"changeSummary,omitempty"`
	Diff          string `json:"diff,omitempty"`
}

type Action struct {
	Type           string `json:"type"`
	Name           string `json:"name"`
	DisplayCommand string `json:"displayCommand,omitempty"`
	Service        string `json:"service,omitempty"`
}

type Precheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type Impact struct {
	WriteFiles     bool `json:"writeFiles"`
	ModifyDatabase bool `json:"modifyDatabase"`
	RestartService bool `json:"restartService"`
	ReloadService  bool `json:"reloadService"`
	NetworkRisk    bool `json:"networkRisk"`
}

type Rollback struct {
	Supported     bool     `json:"supported"`
	Summary       string   `json:"summary,omitempty"`
	Unrecoverable []string `json:"unrecoverable,omitempty"`
}

// EffectiveValue is a non-sensitive value that the operation will use after
// backend normalization. Sensitive values deliberately omit Value and expose
// only their source so a preview cannot disclose credentials.
type EffectiveValue struct {
	Key       string `json:"key"`
	Value     string `json:"value,omitempty"`
	Sensitive bool   `json:"sensitive,omitempty"`
	Source    string `json:"source,omitempty"`
}

type Document struct {
	PreviewID       string           `json:"previewId"`
	Operation       string           `json:"operation"`
	Review          Review           `json:"review"`
	EffectiveValues []EffectiveValue `json:"effectiveValues,omitempty"`
	Files           []FileChange     `json:"files"`
	Actions         []Action         `json:"actions"`
	Prechecks       []Precheck       `json:"prechecks"`
	Impact          Impact           `json:"impact"`
	Rollback        Rollback         `json:"rollback"`
	ExpiresAt       time.Time        `json:"expiresAt"`
}

type Service struct {
	db  *gorm.DB
	now func() time.Time
}

func New(database *gorm.DB) *Service {
	return &Service{db: database, now: time.Now}
}

func NormalizePayload(payload json.RawMessage) (json.RawMessage, string, error) {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, "", fmt.Errorf("payload must be valid JSON: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, "", errors.New("payload must be a JSON object")
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(normalized)
	return normalized, hex.EncodeToString(digest[:]), nil
}

func (s *Service) Create(operation string, userID int64, payload json.RawMessage, document Document, resourceVersion string) (*Document, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("operation preview database is not configured")
	}
	operation = strings.TrimSpace(operation)
	if operation == "" || userID <= 0 {
		return nil, errors.New("operation and user are required")
	}
	normalized, digest, err := NormalizePayload(payload)
	if err != nil {
		return nil, err
	}
	if !utils.CredentialCipherReady() {
		return nil, errors.New("operation preview encryption is not configured")
	}
	encrypted, err := utils.EncryptCredential(string(normalized), credentialPurpose)
	if err != nil {
		return nil, fmt.Errorf("encrypt operation preview payload: %w", err)
	}
	id := uuid.NewString()
	now := s.currentTime()
	document.PreviewID = id
	document.Operation = operation
	document.ExpiresAt = now.Add(defaultTTL)
	previewJSON, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	if err := s.db.Create(&models.OperationPreview{
		ID: id, Operation: operation, UserID: userID, RequestHash: digest,
		ResourceVersion: strings.TrimSpace(resourceVersion), EncryptedPayload: encrypted,
		PreviewJSON: string(previewJSON), ExpiresAt: document.ExpiresAt, CreatedAt: now,
	}).Error; err != nil {
		return nil, err
	}
	return &document, nil
}

func (s *Service) Consume(id string, userID int64) (string, json.RawMessage, *Document, string, error) {
	if s == nil || s.db == nil {
		return "", nil, nil, "", errors.New("operation preview database is not configured")
	}
	var record models.OperationPreview
	if err := s.db.Where("id = ? AND user_id = ?", strings.TrimSpace(id), userID).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil, nil, "", ErrNotFound
		}
		return "", nil, nil, "", err
	}
	now := s.currentTime()
	if record.ConsumedAt != nil {
		return "", nil, nil, "", ErrConsumed
	}
	if !record.ExpiresAt.After(now) {
		return "", nil, nil, "", ErrExpired
	}
	var document Document
	if err := json.Unmarshal([]byte(record.PreviewJSON), &document); err != nil {
		return "", nil, nil, "", err
	}
	decrypted, err := utils.DecryptCredential(record.EncryptedPayload, credentialPurpose)
	if err != nil {
		return "", nil, nil, "", fmt.Errorf("decrypt operation preview payload: %w", err)
	}
	result := s.db.Model(&models.OperationPreview{}).
		Where("id = ? AND consumed_at IS NULL", record.ID).
		Updates(map[string]any{"consumed_at": now})
	if result.Error != nil {
		return "", nil, nil, "", result.Error
	}
	if result.RowsAffected != 1 {
		return "", nil, nil, "", ErrConsumed
	}
	return record.Operation, json.RawMessage(decrypted), &document, record.ResourceVersion, nil
}

// Peek returns the non-sensitive operation metadata so callers can authorize
// an execution before consuming the single-use preview.
func (s *Service) Peek(id string, userID int64) (string, string, error) {
	if s == nil || s.db == nil {
		return "", "", errors.New("operation preview database is not configured")
	}
	var record models.OperationPreview
	if err := s.db.Select("id", "operation", "resource_version", "expires_at", "consumed_at").
		Where("id = ? AND user_id = ?", strings.TrimSpace(id), userID).
		First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", ErrNotFound
		}
		return "", "", err
	}
	if record.ConsumedAt != nil {
		return "", "", ErrConsumed
	}
	if !record.ExpiresAt.After(s.currentTime()) {
		return "", "", ErrExpired
	}
	return record.Operation, record.ResourceVersion, nil
}

func (s *Service) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
