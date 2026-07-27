package models

import (
	"time"

	"gorm.io/gorm"
)

// UserSession is the server-side authority for a JWT. Tokens are never stored;
// the random session identifier in the JWT is sufficient to revoke access.
type UserSession struct {
	ID               string         `json:"id" gorm:"primaryKey;size:64"`
	UserID           int64          `json:"-" gorm:"index;not null"`
	Username         string         `json:"username" gorm:"size:64;not null"`
	RemoteIP         string         `json:"remoteIp" gorm:"size:64"`
	UserAgent        string         `json:"userAgent" gorm:"size:512"`
	SecurityVersion  uint64         `json:"-"`
	CreatedAt        time.Time      `json:"createdAt" gorm:"index"`
	LastSeenAt       time.Time      `json:"lastSeenAt"`
	ExpiresAt        time.Time      `json:"expiresAt" gorm:"index"`
	RevokedAt        *time.Time     `json:"revokedAt,omitempty" gorm:"index"`
	RevocationReason string         `json:"revocationReason,omitempty" gorm:"size:64"`
	DeletedAt        gorm.DeletedAt `json:"-" gorm:"index"`
}

// UserMFA contains only encrypted TOTP secrets and keyed recovery-code
// digests. Recovery codes and plaintext seeds are never persisted.
type UserMFA struct {
	UserID                  int64     `json:"-" gorm:"primaryKey"`
	Enabled                 bool      `json:"enabled"`
	SecretEncrypted         string    `json:"-" gorm:"type:text"`
	PendingSecretEncrypted  string    `json:"-" gorm:"type:text"`
	RecoveryCodeHashesJSON  string    `json:"-" gorm:"type:text"`
	RecoveryCodesGeneration uint64    `json:"-"`
	LastTOTPCounter         int64     `json:"-"`
	CreatedAt               time.Time `json:"-"`
	UpdatedAt               time.Time `json:"-"`
}
