package models

import "time"

// FileShare stores only a hash of the public token. The original token is
// returned once when the share is created and is never persisted.
type FileShare struct {
	ID            string     `json:"id" gorm:"primaryKey;size:36"`
	TokenHash     string     `json:"-" gorm:"size:64;not null;uniqueIndex"`
	Path          string     `json:"path" gorm:"size:1024;not null"`
	Name          string     `json:"name" gorm:"size:255;not null"`
	SizeBytes     int64      `json:"sizeBytes" gorm:"not null"`
	ModTimeUnixNS int64      `json:"-" gorm:"not null"`
	CreatedBy     int64      `json:"createdBy" gorm:"not null;index"`
	CreatedByName string     `json:"createdByName" gorm:"size:128;not null"`
	ExpiresAt     time.Time  `json:"expiresAt" gorm:"not null;index"`
	RevokedAt     *time.Time `json:"revokedAt,omitempty" gorm:"index"`
	DownloadCount uint64     `json:"downloadCount" gorm:"not null;default:0"`
	CreatedAt     time.Time  `json:"createdAt" gorm:"not null;index"`
	UpdatedAt     time.Time  `json:"updatedAt" gorm:"not null"`
}

func (FileShare) TableName() string {
	return "file_share"
}
