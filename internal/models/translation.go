package models

import "time"

// TranslationCache stores successful provider translations without retaining
// the original response text. CacheKey is a one-way digest of the source and
// translation context, while TranslatedText is retained until expiration.
type TranslationCache struct {
	ID             uint64    `json:"-" gorm:"primaryKey;autoIncrement"`
	CacheKey       string    `json:"-" gorm:"size:64;not null;uniqueIndex"`
	Provider       string    `json:"-" gorm:"size:64;not null"`
	Model          string    `json:"-" gorm:"size:128;not null"`
	Field          string    `json:"-" gorm:"size:128;not null"`
	SourceLanguage string    `json:"-" gorm:"size:16;not null"`
	TargetLanguage string    `json:"-" gorm:"size:16;not null"`
	TranslatedText string    `json:"-" gorm:"type:text;not null"`
	ExpiresAt      time.Time `json:"-" gorm:"not null;index"`
	CreatedAt      time.Time `json:"-" gorm:"not null"`
	UpdatedAt      time.Time `json:"-" gorm:"not null"`
}

func (TranslationCache) TableName() string {
	return "translation_cache"
}
