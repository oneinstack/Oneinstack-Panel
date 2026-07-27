package audit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"oneinstack/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"oneinstack/app"
	panelServer "oneinstack/server"
)

const (
	ChainVersion     = 1
	genesisHash      = "0000000000000000000000000000000000000000000000000000000000000000"
	defaultPageSize  = 20
	maxPageSize      = 100
	maxExportRowsCap = 100000
)

var (
	defaultMu      sync.RWMutex
	defaultManager *Manager
)

type Manager struct {
	db  *gorm.DB
	key []byte
	mu  sync.Mutex
	now func() time.Time
}

type EventInput struct {
	RequestID     string
	EventType     string
	Action        string
	Method        string
	Route         string
	Path          string
	Status        int
	Outcome       string
	Sensitive     bool
	UserID        int64
	Username      string
	AuthMode      string
	RemoteIP      string
	UserAgent     string
	ContentLength int64
	DurationMS    int64
	Message       string
	CreatedAt     time.Time
}

type VerificationResult struct {
	Valid              bool   `json:"valid"`
	CheckedEntries     int64  `json:"checkedEntries"`
	CheckpointSequence uint64 `json:"checkpointSequence"`
	FirstSequence      uint64 `json:"firstSequence"`
	LastSequence       uint64 `json:"lastSequence"`
	InvalidSequence    uint64 `json:"invalidSequence,omitempty"`
	Message            string `json:"message"`
}

type CleanupResult struct {
	DeletedEntries       int64     `json:"deletedEntries"`
	CheckpointSequence   uint64    `json:"checkpointSequence"`
	CheckpointEntryHash  string    `json:"checkpointEntryHash"`
	RetentionCutoff      time.Time `json:"retentionCutoff"`
	IntegrityCheckPassed bool      `json:"integrityCheckPassed"`
}

func NewManager(database *gorm.DB, signingKey []byte) (*Manager, error) {
	if database == nil {
		return nil, errors.New("audit database is not configured")
	}
	if len(signingKey) < 32 {
		return nil, errors.New("audit signing key must contain at least 32 bytes")
	}
	return &Manager{
		db:  database,
		key: append([]byte(nil), signingKey...),
		now: time.Now,
	}, nil
}

func ConfigureDefault(database *gorm.DB, signingKey []byte) (*Manager, error) {
	manager, err := NewManager(database, signingKey)
	if err != nil {
		return nil, err
	}
	defaultMu.Lock()
	defaultManager = manager
	defaultMu.Unlock()
	return manager, nil
}

func Default() *Manager {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultManager
}

func ClearDefault(manager *Manager) {
	defaultMu.Lock()
	if defaultManager == manager {
		defaultManager = nil
	}
	defaultMu.Unlock()
}

func NewRequestID() string {
	return uuid.NewString()
}

// RemoteIP trusts forwarded client IPs only when the socket peer is explicitly
// configured as a trusted proxy.
func RemoteIP(request *http.Request) string {
	return sanitize(panelServer.ClientIP(request, app.ONE_CONFIG.System.TrustedProxies), 64)
}

func (manager *Manager) Append(input EventInput) (*models.AuditEvent, error) {
	if manager == nil || manager.db == nil {
		return nil, errors.New("audit manager is not configured")
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()

	event := manager.normalize(input)
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		event.ID = 0
		err = manager.db.Transaction(func(tx *gorm.DB) error {
			sequence, previousHash, loadErr := loadChainHead(tx)
			if loadErr != nil {
				return loadErr
			}
			if verifyErr := manager.verifyStoredHead(tx, sequence, previousHash); verifyErr != nil {
				return verifyErr
			}
			event.Sequence = sequence + 1
			event.PreviousHash = previousHash
			event.EntryHash, loadErr = manager.signEvent(event)
			if loadErr != nil {
				return loadErr
			}
			if createErr := tx.Create(event).Error; createErr != nil {
				return createErr
			}
			state := models.AuditChainState{
				ID: 1, LastSequence: event.Sequence, LastEntryHash: event.EntryHash,
				UpdatedAt: manager.now().UTC(),
			}
			state.Signature, loadErr = manager.signState(&state)
			if loadErr != nil {
				return loadErr
			}
			return tx.Save(&state).Error
		})
		if err == nil || !isDatabaseBusy(err) {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 15 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("append audit event: %w", err)
	}
	return event, nil
}

func isDatabaseBusy(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") ||
		strings.Contains(message, "sqlite_busy") ||
		strings.Contains(message, "database table is locked")
}

func (manager *Manager) normalize(input EventInput) *models.AuditEvent {
	createdAt := input.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = manager.now().UTC()
	}
	status := input.Status
	if status < 0 || status > 999 {
		status = 0
	}
	outcome := strings.ToLower(strings.TrimSpace(input.Outcome))
	if outcome != "success" && outcome != "failure" {
		if status >= 200 && status < 400 {
			outcome = "success"
		} else {
			outcome = "failure"
		}
	}
	contentLength := input.ContentLength
	if contentLength < 0 {
		contentLength = 0
	}
	duration := input.DurationMS
	if duration < 0 {
		duration = 0
	}
	return &models.AuditEvent{
		RequestID:     sanitize(input.RequestID, 64),
		EventType:     sanitizeDefault(input.EventType, "http", 32),
		Action:        sanitizeDefault(input.Action, "unknown", 160),
		Method:        sanitize(strings.ToUpper(input.Method), 12),
		Route:         sanitize(input.Route, 255),
		Path:          sanitize(input.Path, 1024),
		Status:        status,
		Outcome:       outcome,
		Sensitive:     input.Sensitive,
		UserID:        input.UserID,
		Username:      sanitize(input.Username, 128),
		AuthMode:      sanitize(input.AuthMode, 32),
		RemoteIP:      sanitize(input.RemoteIP, 64),
		UserAgent:     sanitize(input.UserAgent, 512),
		ContentLength: contentLength,
		DurationMS:    duration,
		Message:       sanitize(input.Message, 255),
		CreatedAt:     createdAt,
		ChainVersion:  ChainVersion,
	}
}

func loadChainHead(tx *gorm.DB) (uint64, string, error) {
	var event models.AuditEvent
	err := tx.Select("sequence", "entry_hash").Order("sequence DESC").First(&event).Error
	if err == nil {
		return event.Sequence, event.EntryHash, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, "", fmt.Errorf("read audit chain head: %w", err)
	}
	var checkpoint models.AuditCheckpoint
	err = tx.First(&checkpoint, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, genesisHash, nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("read audit checkpoint: %w", err)
	}
	return checkpoint.ThroughSequence, checkpoint.ThroughEntryHash, nil
}

type eventPayload struct {
	Sequence      uint64 `json:"sequence"`
	RequestID     string `json:"requestId"`
	EventType     string `json:"eventType"`
	Action        string `json:"action"`
	Method        string `json:"method"`
	Route         string `json:"route"`
	Path          string `json:"path"`
	Status        int    `json:"status"`
	Outcome       string `json:"outcome"`
	Sensitive     bool   `json:"sensitive"`
	UserID        int64  `json:"userId"`
	Username      string `json:"username"`
	AuthMode      string `json:"authMode"`
	RemoteIP      string `json:"remoteIp"`
	UserAgent     string `json:"userAgent"`
	ContentLength int64  `json:"contentLength"`
	DurationMS    int64  `json:"durationMs"`
	Message       string `json:"message"`
	CreatedAtUnix int64  `json:"createdAtUnixNano"`
	PreviousHash  string `json:"previousHash"`
	ChainVersion  uint8  `json:"chainVersion"`
}

func (manager *Manager) signEvent(event *models.AuditEvent) (string, error) {
	payload, err := json.Marshal(eventPayload{
		Sequence: event.Sequence, RequestID: event.RequestID, EventType: event.EventType,
		Action: event.Action, Method: event.Method, Route: event.Route, Path: event.Path,
		Status: event.Status, Outcome: event.Outcome, Sensitive: event.Sensitive,
		UserID: event.UserID, Username: event.Username, AuthMode: event.AuthMode,
		RemoteIP: event.RemoteIP, UserAgent: event.UserAgent,
		ContentLength: event.ContentLength, DurationMS: event.DurationMS,
		Message: event.Message, CreatedAtUnix: event.CreatedAt.UTC().UnixNano(),
		PreviousHash: event.PreviousHash, ChainVersion: event.ChainVersion,
	})
	if err != nil {
		return "", fmt.Errorf("encode audit event: %w", err)
	}
	return manager.sign(payload), nil
}

func (manager *Manager) sign(payload []byte) string {
	mac := hmac.New(sha256.New, manager.key)
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func sanitizeDefault(value, fallback string, limit int) string {
	cleaned := sanitize(value, limit)
	if cleaned == "" {
		return fallback
	}
	return cleaned
}

func sanitize(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || limit <= 0 {
		return ""
	}
	var builder strings.Builder
	builder.Grow(min(len(value), limit))
	for _, character := range value {
		if unicode.IsControl(character) {
			builder.WriteRune(' ')
		} else {
			builder.WriteRune(character)
		}
		if builder.Len() >= limit {
			break
		}
	}
	return strings.TrimSpace(builder.String())
}
