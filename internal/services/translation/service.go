package translation

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strconv"
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
	translationModel    = "center-dual-v2"
	translationField    = "运维面板提示"
	defaultCacheTTL     = 24 * time.Hour
	defaultCacheEntries = 4096
	defaultMaxText      = 512
	defaultMaxBatch     = 50
	statusTranslated    = "translated"
)

var ErrTextNotEligible = errors.New("translation text is not eligible")

var (
	protectedValuePattern = regexp.MustCompile("`[^`]+`|(?i:[a-z][a-z0-9+.-]*://[^\\s，。；;）)]+)|[A-Za-z]:\\\\[^\\s，。；;）)]+|[^\\s，。；;（）()]*[/\\\\][^\\s，。；;（）()]*|[A-Za-z0-9_.+-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}|(?:[0-9]{1,3}\\.){3}[0-9]{1,3}|[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}|[A-Za-z0-9_+/=-]{24,}")
	secretAssignment      = regexp.MustCompile(`(?i)(?:\b(?:password|passwd|token|secret|credential|api[_-]?key)|密码|密钥|令牌)\s*[:=]\s*\S+|--(?:password|token|secret)\s+\S+|Bearer\s+\S+`)
	placeholderPattern    = regexp.MustCompile(`\[\[T[0-9]+\]\]`)
)

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
		responseTimeout = 120 * time.Second
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
	segments := make(map[string]struct{})
	for _, text := range texts {
		value := text
		outcomes[text] = Outcome{Translated: text}
		if strings.TrimSpace(value) == "" || !containsNonEnglish(value) {
			continue
		}
		currentSource := source
		if currentSource == "" {
			currentSource = detectSourceLanguage(value)
		}
		if currentSource != "" && currentSource == target {
			continue
		}
		key := cacheKey(currentSource, target, s.provider, s.model, s.field, value)
		if translated, ok := s.cache.get(key); ok && isValidCachedTranslation(translated, value) {
			outcomes[text] = Outcome{Translated: translated}
			continue
		}
		persistentKey := persistentCacheKey(key)
		if translated, ok := s.persistent.get(persistentKey, currentSource, target); ok && isValidCachedTranslation(translated, value) {
			s.cache.set(key, translated)
			outcomes[text] = Outcome{Translated: translated}
			continue
		}
		masked, protected, ok := protectTranslationText(value)
		if !ok {
			outcomes[text] = Outcome{Translated: text, Err: ErrTextNotEligible}
			continue
		}
		parts := splitTranslationText(masked, s.maxText)
		eligible := true
		localSegments := make([]string, 0, len(parts))
		for _, part := range parts {
			core := strings.TrimSpace(part)
			if !containsNonEnglish(core) {
				continue
			}
			if !isEligibleText(core, s.maxText) {
				eligible = false
				break
			}
			localSegments = append(localSegments, core)
		}
		if !eligible {
			outcomes[text] = Outcome{Translated: text, Err: ErrTextNotEligible}
			continue
		}
		if len(localSegments) == 0 {
			continue
		}
		for _, segment := range localSegments {
			segments[segment] = struct{}{}
		}
		pending = append(pending, pendingText{original: text, value: value, masked: masked, protected: protected, parts: parts, source: currentSource, target: target, key: key, persistentKey: persistentKey})
	}
	if len(pending) == 0 {
		return outcomes
	}
	requested := make([]string, 0, len(segments))
	for segment := range segments {
		requested = append(requested, segment)
	}
	sort.Strings(requested)
	segmentOutcomes := s.translateSegments(ctx, sourceLocale, targetLocale, requested)
	for _, item := range pending {
		var assembled strings.Builder
		complete := true
		for _, part := range item.parts {
			core := strings.TrimSpace(part)
			if !containsNonEnglish(core) {
				assembled.WriteString(part)
				continue
			}
			result, found := segmentOutcomes[core]
			if !found || result.Err != nil || !isUsableTranslation(result.Translated, core, item.target) {
				complete = false
				break
			}
			leading := strings.TrimSuffix(part, strings.TrimLeftFunc(part, unicode.IsSpace))
			trailing := strings.TrimPrefix(part, strings.TrimRightFunc(part, unicode.IsSpace))
			assembled.WriteString(leading)
			assembled.WriteString(strings.TrimSpace(result.Translated))
			assembled.WriteString(trailing)
		}
		if !complete || !slices.Equal(placeholderPattern.FindAllString(item.masked, -1), placeholderPattern.FindAllString(assembled.String(), -1)) {
			outcomes[item.original] = Outcome{Translated: item.original, Err: ErrCenterUnavailable}
			continue
		}
		translated := restoreProtectedText(assembled.String(), item.protected)
		if !isValidCachedTranslation(translated, item.value) {
			outcomes[item.original] = Outcome{Translated: item.original, Err: ErrCenterUnavailable}
			continue
		}
		s.cache.set(item.key, translated)
		s.persistent.set(item.persistentKey, item.source, item.target, translated)
		outcomes[item.original] = Outcome{Translated: translated}
	}
	return outcomes
}

type pendingText struct {
	original      string
	value         string
	masked        string
	protected     []string
	parts         []string
	source        string
	target        string
	key           string
	persistentKey string
}

func (s *Service) translateSegments(ctx context.Context, sourceLocale, targetLocale string, segments []string) map[string]Outcome {
	results := make(map[string]Outcome, len(segments))
	for _, segment := range segments {
		results[segment] = Outcome{Translated: segment, Err: ErrCenterUnavailable}
	}
	if len(segments) == 0 {
		return results
	}
	var mu sync.Mutex
	jobs := make(chan []string)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for batch := range jobs {
				if ctx.Err() != nil {
					continue
				}
				response, err := s.client.batch(ctx, sourceLocale, targetLocale, batch)
				if err != nil {
					continue
				}
				mu.Lock()
				for _, item := range response {
					if item.Status == statusTranslated && item.Index >= 0 && item.Index < len(batch) {
						results[batch[item.Index]] = Outcome{Translated: item.Translated}
					}
				}
				mu.Unlock()
			}
		}()
	}
	for start := 0; start < len(segments); start += s.maxBatch {
		end := min(start+s.maxBatch, len(segments))
		select {
		case jobs <- segments[start:end]:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return results
		}
	}
	close(jobs)
	workers.Wait()
	return results
}

func protectTranslationText(value string) (string, []string, bool) {
	if secretAssignment.MatchString(value) || placeholderPattern.MatchString(value) {
		return "", nil, false
	}
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' {
			return "", nil, false
		}
	}
	protected := make([]string, 0)
	masked := protectedValuePattern.ReplaceAllStringFunc(value, func(found string) string {
		placeholder := "[[T" + strconv.Itoa(len(protected)) + "]]"
		protected = append(protected, found)
		return placeholder
	})
	if strings.ContainsAny(masked, "/\\") || strings.Contains(masked, "://") {
		return "", nil, false
	}
	return masked, protected, true
}

func restoreProtectedText(masked string, protected []string) string {
	return placeholderPattern.ReplaceAllStringFunc(masked, func(marker string) string {
		index, err := strconv.Atoi(marker[3 : len(marker)-2])
		if err != nil || index < 0 || index >= len(protected) {
			return marker
		}
		return protected[index]
	})
}

func splitTranslationText(value string, maxRunes int) []string {
	if maxRunes <= 0 {
		return nil
	}
	runes := []rune(value)
	parts := make([]string, 0, (len(runes)/maxRunes)+1)
	for offset := 0; offset < len(runes); {
		if runes[offset] == '\n' || runes[offset] == '\r' || runes[offset] == '\t' {
			parts = append(parts, string(runes[offset]))
			offset++
			continue
		}
		lineEnd := offset
		for lineEnd < len(runes) && runes[lineEnd] != '\n' && runes[lineEnd] != '\r' && runes[lineEnd] != '\t' {
			lineEnd++
		}
		for offset < lineEnd {
			cut := min(maxRunes, lineEnd-offset)
			sentenceBoundary := false
			for index := 0; index < cut; index++ {
				if strings.ContainsRune("。！？!?；;", runes[offset+index]) {
					cut = index + 1
					sentenceBoundary = true
					break
				}
			}
			if !sentenceBoundary && cut < lineEnd-offset {
				for index := cut - 1; index >= cut/2; index-- {
					if unicode.IsSpace(runes[offset+index]) || strings.ContainsRune("，,：:.", runes[offset+index]) {
						cut = index + 1
						break
					}
				}
			}
			prefix := string(runes[offset : offset+cut])
			if marker := strings.LastIndex(prefix, "[[T"); marker >= 0 && !strings.Contains(prefix[marker:], "]]") {
				if before := utf8.RuneCountInString(prefix[:marker]); before > 0 {
					cut = before
				} else {
					remaining := string(runes[offset:lineEnd])
					if end := strings.Index(remaining, "]]"); end >= 0 {
						cut = utf8.RuneCountInString(remaining[:end+2])
					}
				}
			}
			parts = append(parts, string(runes[offset:offset+cut]))
			offset += cut
		}
	}
	return parts
}

func isValidCachedTranslation(translated, source string) bool {
	return strings.TrimSpace(translated) != "" && utf8.ValidString(translated) && translated != source
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
	if !slices.Equal(placeholderPattern.FindAllString(source, -1), placeholderPattern.FindAllString(translated, -1)) {
		return false
	}
	if target == "en" && containsScript(translated, unicode.Han) {
		return false
	}
	sourceBody := placeholderPattern.ReplaceAllString(source, "")
	translatedBody := placeholderPattern.ReplaceAllString(translated, "")
	return utf8.RuneCountInString(sourceBody) < 40 || utf8.RuneCountInString(translatedBody)*2 >= utf8.RuneCountInString(sourceBody)
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
