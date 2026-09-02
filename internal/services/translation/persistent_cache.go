package translation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"oneinstack/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type persistentCache struct {
	db       *gorm.DB
	ttl      time.Duration
	max      int
	provider string
	model    string
	field    string

	cleanupMu   sync.Mutex
	lastCleanup time.Time
}

func newPersistentCache(db *gorm.DB, ttl time.Duration, max int, provider, model, field string) *persistentCache {
	if db == nil {
		return nil
	}
	return &persistentCache{
		db:       db,
		ttl:      ttl,
		max:      max,
		provider: provider,
		model:    model,
		field:    field,
	}
}

func persistentCacheKey(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func (c *persistentCache) get(key, sourceLanguage, targetLanguage string) (string, bool) {
	if c == nil || c.db == nil {
		return "", false
	}
	var entry models.TranslationCache
	result := c.db.Where(
		"cache_key = ? AND provider = ? AND model = ? AND field = ? AND source_language = ? AND target_language = ? AND expires_at > ?",
		key, c.provider, c.model, c.field, sourceLanguage, targetLanguage, time.Now().UTC(),
	).First(&entry)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return "", false
	}
	if result.Error != nil {
		return "", false
	}
	return entry.TranslatedText, entry.TranslatedText != ""
}

func (c *persistentCache) set(key, sourceLanguage, targetLanguage, translated string) {
	if c == nil || c.db == nil || translated == "" {
		return
	}
	now := time.Now().UTC()
	entry := models.TranslationCache{
		CacheKey:       key,
		Provider:       c.provider,
		Model:          c.model,
		Field:          c.field,
		SourceLanguage: sourceLanguage,
		TargetLanguage: targetLanguage,
		TranslatedText: translated,
		ExpiresAt:      now.Add(c.ttl),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	result := c.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "cache_key"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"provider", "model", "field", "source_language", "target_language",
			"translated_text", "expires_at", "updated_at",
		}),
	}).Create(&entry)
	if result.Error != nil {
		return
	}
	c.cleanup(now)
}

func (c *persistentCache) delete(key, sourceLanguage, targetLanguage string) {
	if c == nil || c.db == nil {
		return
	}
	c.db.Where(
		"cache_key = ? AND provider = ? AND model = ? AND field = ? AND source_language = ? AND target_language = ?",
		key, c.provider, c.model, c.field, sourceLanguage, targetLanguage,
	).Delete(&models.TranslationCache{})
}

func (c *persistentCache) cleanup(now time.Time) {
	if c == nil || c.db == nil || c.max <= 0 {
		return
	}
	c.cleanupMu.Lock()
	if now.Sub(c.lastCleanup) < time.Minute {
		c.cleanupMu.Unlock()
		return
	}
	c.lastCleanup = now
	c.cleanupMu.Unlock()

	if err := c.db.Where("expires_at <= ?", now).Delete(&models.TranslationCache{}).Error; err != nil {
		return
	}
	var count int64
	if err := c.db.Model(&models.TranslationCache{}).Count(&count).Error; err != nil {
		return
	}
	if count <= int64(c.max) {
		return
	}
	deleteCount := count - int64(c.max)
	_ = c.db.Exec(
		"DELETE FROM translation_cache WHERE id IN (SELECT id FROM translation_cache ORDER BY expires_at ASC, id ASC LIMIT ?)",
		deleteCount,
	)
}
