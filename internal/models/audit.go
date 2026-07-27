package models

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// AuditEvent is an append-only security audit record. Request bodies and query
// strings are deliberately excluded so credentials and one-time tickets cannot
// be persisted accidentally.
type AuditEvent struct {
	ID            uint64    `gorm:"primaryKey" json:"id"`
	Sequence      uint64    `gorm:"uniqueIndex;not null" json:"sequence"`
	RequestID     string    `gorm:"size:64;index" json:"requestId"`
	EventType     string    `gorm:"size:32;index;not null" json:"eventType"`
	Action        string    `gorm:"size:160;index;not null" json:"action"`
	Method        string    `gorm:"size:12;index" json:"method"`
	Route         string    `gorm:"size:255;index" json:"route"`
	Path          string    `gorm:"size:1024" json:"path"`
	Status        int       `gorm:"index" json:"status"`
	Outcome       string    `gorm:"size:16;index;not null" json:"outcome"`
	Sensitive     bool      `gorm:"index;not null" json:"sensitive"`
	UserID        int64     `gorm:"index" json:"userId"`
	Username      string    `gorm:"size:128;index" json:"username"`
	AuthMode      string    `gorm:"size:32" json:"authMode"`
	RemoteIP      string    `gorm:"size:64;index" json:"remoteIp"`
	UserAgent     string    `gorm:"size:512" json:"userAgent"`
	ContentLength int64     `json:"contentLength"`
	DurationMS    int64     `json:"durationMs"`
	Message       string    `gorm:"size:255" json:"message"`
	CreatedAt     time.Time `gorm:"index;not null" json:"createdAt"`
	PreviousHash  string    `gorm:"size:64;not null" json:"previousHash"`
	EntryHash     string    `gorm:"size:64;uniqueIndex;not null" json:"entryHash"`
	ChainVersion  uint8     `gorm:"not null" json:"chainVersion"`
}

func (event *AuditEvent) BeforeUpdate(*gorm.DB) error {
	return errors.New("audit events are append-only")
}

func (event *AuditEvent) BeforeDelete(*gorm.DB) error {
	return errors.New("audit events can only be removed by the retention service")
}

// AuditCheckpoint authenticates the prefix removed by retention cleanup. The
// first retained event must continue from ThroughEntryHash.
type AuditCheckpoint struct {
	ID               uint64    `gorm:"primaryKey" json:"id"`
	ThroughSequence  uint64    `gorm:"not null" json:"throughSequence"`
	ThroughEntryHash string    `gorm:"size:64;not null" json:"throughEntryHash"`
	Signature        string    `gorm:"size:64;not null" json:"signature"`
	UpdatedAt        time.Time `gorm:"not null" json:"updatedAt"`
}

// AuditChainState authenticates the current chain head, allowing verification
// to detect deletion of the newest records in addition to in-place mutation.
type AuditChainState struct {
	ID            uint64    `gorm:"primaryKey" json:"id"`
	LastSequence  uint64    `gorm:"not null" json:"lastSequence"`
	LastEntryHash string    `gorm:"size:64;not null" json:"lastEntryHash"`
	Signature     string    `gorm:"size:64;not null" json:"signature"`
	UpdatedAt     time.Time `gorm:"not null" json:"updatedAt"`
}
