package translation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"oneinstack/app"
	"oneinstack/config"

	"gorm.io/gorm"
)

const (
	translationProvider = "center"
	translationModel    = "hunyuan-translation-lite"
	translationField    = "运维面板提示"
	defaultCacheTTL     = 24 * time.Hour
	defaultCacheEntries = 4096
	defaultMaxText      = 512
	defaultMaxBatch     = 50
	statusTranslated    = "translated"
)

var ErrTextNotEligible = errors.New("translation text is not eligible")

type Outcome struct {
	Translated string
	Err        error
}

type Service struct {
	client     *centerClient
	provider   string
	model      string
	field      string
	maxText    int
	maxBatch   int
	cache      *cache
	persistent *persistentCache
}

func New(cfg config.Translation, database *gorm.DB) (*Service, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if mode := strings.ToLower(strings.TrimSpace(cfg.Mode)); mode != "" && mode != "center" {
		return nil, errors.New("translation mode must be center")
	}
	centerURL := strings.TrimSpace(cfg.CenterURL)
	if centerURL == "" {
		centerURL = strings.TrimSpace(app.ONE_CONFIG.UpdateCenter.CenterURL)
	}
	if centerURL == "" {
		centerURL = strings.TrimSpace(app.ONE_CONFIG.ScriptCenter.URL)
	}
	if centerURL == "" {
		return nil, errors.New("translation Center URL is not configured")
	}
	responseTimeout := time.Duration(cfg.ResponseTimeoutSeconds) * time.Second
	if responseTimeout <= 0 {
		responseTimeout = 15 * time.Second
	}
	maxText := cfg.MaxTextLength
	if maxText <= 0 {
		maxText = defaultMaxText
	}
	cacheTTL := time.Duration(cfg.CacheTTLMinutes) * time.Minute
	if cacheTTL <= 0 {
		cacheTTL = defaultCacheTTL
	}
	cacheEntries := cfg.CacheMaxEntries
	if cacheEntries <= 0 {
		cacheEntries = defaultCacheEntries
	}
	client, err := newCenterClient(context.Background(), centerClientConfig{
		BaseURL: centerURL, InstallDir: app.GetBasePath(), IdentityPath: cfg.IdentityPath,
		ActivationCodeFile: cfg.ActivationCodeFile, Timeout: responseTimeout,
	})
	if err != nil {
		return nil, err
	}
	return &Service{
		client:     client,
		provider:   translationProvider,
		model:      translationModel,
		field:      translationField,
		maxText:    maxText,
		maxBatch:   defaultMaxBatch,
		cache:      newCache(cacheTTL, cacheEntries),
		persistent: newPersistentCache(database, cacheTTL, cacheEntries, translationProvider, translationModel, translationField),
	}, nil
}

func (s *Service) Translate(ctx context.Context, sourceLocale, targetLocale, text string) (string, error) {
	outcomes := s.TranslateBatch(ctx, sourceLocale, targetLocale, []string{text})
	outcome, ok := outcomes[text]
	if !ok {
		return "", ErrCenterUnavailable
	}
	return outcome.Translated, outcome.Err
}

func (s *Service) TranslateBatch(ctx context.Context, sourceLocale, targetLocale string, texts []string) map[string]Outcome {
	outcomes := make(map[string]Outcome, len(texts))
	if s == nil || s.client == nil {
		for _, text := range texts {
			outcomes[text] = Outcome{Translated: text, Err: ErrCenterUnavailable}
		}
		return outcomes
	}
	target := languageCode(targetLocale)
	if target == "" {
		for _, text := range texts {
			outcomes[text] = Outcome{Translated: text, Err: ErrTextNotEligible}
		}
		return outcomes
	}
	source := languageCode(sourceLocale)
	pending := make([]pendingText, 0, len(texts))
	for _, text := range texts {
		value := strings.TrimSpace(text)
		outcomes[text] = Outcome{Translated: text}
		if value == "" || !containsNonEnglish(value) {
			continue
		}
		currentSource := source
		if currentSource == "" {
			currentSource = detectSourceLanguage(value)
		}
		if currentSource != "" && currentSource == target {
			continue
		}
		if !isEligibleText(value, s.maxText) {
			outcomes[text] = Outcome{Translated: text, Err: ErrTextNotEligible}
			continue
		}
		key := cacheKey(currentSource, target, s.provider, s.model, s.field, value)
		if translated, ok := s.cache.get(key); ok && isUsableTranslation(translated, value, target) {
			outcomes[text] = Outcome{Translated: translated}
			continue
		}
		persistentKey := persistentCacheKey(key)
		if translated, ok := s.persistent.get(persistentKey, currentSource, target); ok && isUsableTranslation(translated, value, target) {
			s.cache.set(key, translated)
			outcomes[text] = Outcome{Translated: translated}
			continue
		}
		pending = append(pending, pendingText{original: text, value: value, source: currentSource, target: target, key: key, persistentKey: persistentKey})
	}
	if len(pending) == 0 {
		return outcomes
	}
	if len(pending) > s.maxBatch {
		for _, item := range pending[s.maxBatch:] {
			outcomes[item.original] = Outcome{Translated: item.original, Err: ErrCenterUnavailable}
		}
		pending = pending[:s.maxBatch]
	}
	requestTexts := make([]string, len(pending))
	for index, item := range pending {
		requestTexts[index] = item.value
	}
	results, err := s.client.batch(ctx, sourceLocale, targetLocale, requestTexts)
	if err != nil {
		for _, item := range pending {
			outcomes[item.original] = Outcome{Translated: item.original, Err: err}
		}
		return outcomes
	}
	for _, result := range results {
		item := pending[result.Index]
		if result.Status != statusTranslated || !isUsableTranslation(result.Translated, item.value, item.target) {
			outcomes[item.original] = Outcome{Translated: item.original, Err: ErrCenterUnavailable}
			continue
		}
		translated := strings.TrimSpace(result.Translated)
		s.cache.set(item.key, translated)
		s.persistent.set(item.persistentKey, item.source, item.target, translated)
		outcomes[item.original] = Outcome{Translated: translated}
	}
	return outcomes
}

type pendingText struct {
	original      string
	value         string
	source        string
	target        string
	key           string
	persistentKey string
}

func cacheKey(source, target, provider, model, field, text string) string {
	return source + "\x00" + target + "\x00" + provider + "\x00" + model + "\x00" + field + "\x00" + text
}

func languageCode(locale string) string {
	switch strings.ToLower(strings.TrimSpace(locale)) {
	case "zh", "zh-cn":
		return "zh"
	case "en", "en-us":
		return "en"
	case "fr", "pt", "es", "ja", "tr", "ru", "ar", "ko", "th", "it", "de", "vi", "ms", "id", "yue":
		return strings.ToLower(strings.TrimSpace(locale))
	default:
		return ""
	}
}

func detectSourceLanguage(text string) string {
	if containsScript(text, unicode.Hiragana) || containsScript(text, unicode.Katakana) {
		return "ja"
	}
	if containsScript(text, unicode.Hangul) {
		return "ko"
	}
	if containsScript(text, unicode.Han) {
		return "zh"
	}
	if containsScript(text, unicode.Cyrillic) {
		return "ru"
	}
	if containsScript(text, unicode.Arabic) {
		return "ar"
	}
	if containsScript(text, unicode.Thai) {
		return "th"
	}
	return ""
}

func containsNonEnglish(text string) bool {
	for _, character := range text {
		if unicode.IsLetter(character) && character > unicode.MaxASCII {
			return true
		}
	}
	return false
}

func containsScript(text string, script *unicode.RangeTable) bool {
	for _, character := range text {
		if unicode.Is(script, character) {
			return true
		}
	}
	return false
}

func isEligibleText(text string, maxRunes int) bool {
	if maxRunes <= 0 || utf8.RuneCountInString(text) > maxRunes {
		return false
	}
	for _, character := range text {
		if unicode.IsControl(character) {
			return false
		}
	}
	return !strings.Contains(strings.ToLower(text), "://") && !strings.ContainsAny(text, "/\\")
}

func isUsableTranslation(translated, source, target string) bool {
	translated = strings.TrimSpace(translated)
	if translated == "" || !utf8.ValidString(translated) || translated == strings.TrimSpace(source) {
		return false
	}
	return target != "en" || !containsNonEnglish(translated)
}

type cache struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	entries map[string]cacheEntry
}

type cacheEntry struct {
	value     string
	expiresAt time.Time
	lastUsed  time.Time
}

func newCache(ttl time.Duration, max int) *cache {
	return &cache{ttl: ttl, max: max, entries: make(map[string]cacheEntry)}
}

func (c *cache) get(key string) (string, bool) {
	if c == nil {
		return "", false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return "", false
	}
	if !entry.expiresAt.After(now) {
		delete(c.entries, key)
		return "", false
	}
	entry.lastUsed = now
	c.entries[key] = entry
	return entry.value, true
}

func (c *cache) set(key, value string) {
	if c == nil || value == "" {
		return
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[key]; ok {
		entry.value = value
		entry.expiresAt = now.Add(c.ttl)
		entry.lastUsed = now
		c.entries[key] = entry
		return
	}
	for len(c.entries) >= c.max {
		var oldestKey string
		var oldest time.Time
		for candidateKey, candidate := range c.entries {
			if oldestKey == "" || candidate.lastUsed.Before(oldest) {
				oldestKey, oldest = candidateKey, candidate.lastUsed
			}
		}
		if oldestKey == "" {
			break
		}
		delete(c.entries, oldestKey)
	}
	c.entries[key] = cacheEntry{value: value, expiresAt: now.Add(c.ttl), lastUsed: now}
}

func (c *cache) delete(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}
