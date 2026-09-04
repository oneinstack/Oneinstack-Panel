package security

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"oneinstack/internal/models"

	"gorm.io/gorm"
)

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrSessionRevoked  = errors.New("session is revoked")
	ErrSessionExpired  = errors.New("session is expired")
	ErrSessionVersion  = errors.New("session security version is stale")
)

type SessionManager struct {
	db  *gorm.DB
	now func() time.Time
}

type NewSession struct {
	UserID          int64
	Username        string
	RemoteIP        string
	UserAgent       string
	SecurityVersion uint64
	ExpiresAt       time.Time
}

type SessionList struct {
	Items    []models.UserSession `json:"items"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"pageSize"`
}

func NewSessionManager(db *gorm.DB) *SessionManager {
	return &SessionManager{db: db, now: time.Now}
}

func (m *SessionManager) Create(input NewSession) (*models.UserSession, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("session store is unavailable")
	}
	if input.UserID <= 0 || input.Username == "" || input.ExpiresAt.IsZero() {
		return nil, errors.New("session identity and expiry are required")
	}
	id, err := randomSessionID()
	if err != nil {
		return nil, err
	}
	now := m.now().UTC()
	// Keep the registry bounded without making login depend on cleanup success.
	retentionCutoff := now.Add(-7 * 24 * time.Hour)
	_ = m.db.Unscoped().Where(
		"expires_at < ? OR (revoked_at IS NOT NULL AND revoked_at < ?)",
		retentionCutoff, retentionCutoff,
	).Delete(&models.UserSession{}).Error
	version := input.SecurityVersion
	if version == 0 {
		version = 1
	}
	record := &models.UserSession{
		ID: id, UserID: input.UserID, Username: input.Username,
		RemoteIP: input.RemoteIP, UserAgent: input.UserAgent,
		SecurityVersion: version, CreatedAt: now, LastSeenAt: now,
		ExpiresAt: input.ExpiresAt.UTC(),
	}
	if err := m.db.Create(record).Error; err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return record, nil
}

func (m *SessionManager) Validate(id string, userID int64, securityVersion uint64) (*models.UserSession, error) {
	if m == nil || m.db == nil || id == "" || userID <= 0 {
		return nil, ErrSessionNotFound
	}
	var record models.UserSession
	if err := m.db.Where("id = ? AND user_id = ?", id, userID).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	if record.RevokedAt != nil {
		return nil, ErrSessionRevoked
	}
	now := m.now().UTC()
	if !record.ExpiresAt.After(now) {
		return nil, ErrSessionExpired
	}
	if securityVersion == 0 {
		securityVersion = 1
	}
	if record.SecurityVersion != securityVersion {
		return nil, ErrSessionVersion
	}
	// Last-seen writes are deliberately throttled to avoid a database write on
	// every polling request from the dashboard.
	if record.LastSeenAt.Before(now.Add(-5 * time.Minute)) {
		_ = m.db.Model(&models.UserSession{}).
			Where("id = ? AND revoked_at IS NULL", record.ID).
			Update("last_seen_at", now).Error
		record.LastSeenAt = now
	}
	return &record, nil
}

func (m *SessionManager) ListActive(userID int64, page, pageSize int) (*SessionList, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	query := m.db.Model(&models.UserSession{}).Where(
		"user_id = ? AND revoked_at IS NULL AND expires_at > ?", userID, m.now().UTC(),
	)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("count active sessions: %w", err)
	}

	var records []models.UserSession
	if err := query.Order("last_seen_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list active sessions: %w", err)
	}
	return &SessionList{Items: records, Total: total, Page: page, PageSize: pageSize}, nil
}

func (m *SessionManager) Revoke(userID int64, id, reason string) (bool, error) {
	now := m.now().UTC()
	tx := m.db.Model(&models.UserSession{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", id, userID).
		Updates(map[string]interface{}{"revoked_at": now, "revocation_reason": reason})
	return tx.RowsAffected > 0, tx.Error
}

func (m *SessionManager) RevokeOthers(userID int64, currentID, reason string) (int64, error) {
	now := m.now().UTC()
	tx := m.db.Model(&models.UserSession{}).
		Where("user_id = ? AND id <> ? AND revoked_at IS NULL", userID, currentID).
		Updates(map[string]interface{}{"revoked_at": now, "revocation_reason": reason})
	return tx.RowsAffected, tx.Error
}

func (m *SessionManager) RevokeAll(userID int64, reason string) (int64, error) {
	now := m.now().UTC()
	tx := m.db.Model(&models.UserSession{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Updates(map[string]interface{}{"revoked_at": now, "revocation_reason": reason})
	return tx.RowsAffected, tx.Error
}

func randomSessionID() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate session identifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
