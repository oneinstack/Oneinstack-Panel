package middleware

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"mime"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"oneinstack/internal/i18n"
	translationservice "oneinstack/internal/services/translation"

	"github.com/gin-gonic/gin"
)

const (
	maxBufferedTranslationResponse = 512 << 10
	defaultTranslationFields       = 0 // zero means all eligible fields
	defaultTranslationTimeout      = 15 * time.Second
)

// ResponseTranslation adds a best-effort translation fallback for short,
// user-visible JSON string values. It never changes the original response when
// the provider is unavailable or the response is not JSON.
func ResponseTranslation(service *translationservice.Service, maxFields, responseTimeoutSeconds int) gin.HandlerFunc {
	if service == nil {
		return func(c *gin.Context) { c.Next() }
	}
	if maxFields < 0 {
		maxFields = defaultTranslationFields
	}
	responseTimeout := time.Duration(responseTimeoutSeconds) * time.Second
	if responseTimeoutSeconds <= 0 {
		responseTimeout = defaultTranslationTimeout
	}

	return func(c *gin.Context) {
		if RequestLocale(c) != i18n.LocaleEnUS {
			c.Next()
			return
		}

		originalWriter := c.Writer
		bufferedWriter := newTranslationResponseWriter(originalWriter, maxBufferedTranslationResponse)
		c.Writer = bufferedWriter
		c.Next()
		c.Writer = originalWriter

		if bufferedWriter.passthrough || !isJSONResponse(c, bufferedWriter) {
			bufferedWriter.commit(bufferedWriter.body.Bytes())
			return
		}

		translationContext, cancel := context.WithTimeout(c.Request.Context(), responseTimeout)
		defer cancel()
		translatedBody, changed := translateJSONResponse(
			translationContext,
			service,
			RequestLocale(c),
			bufferedWriter.body.Bytes(),
			maxFields,
		)
		if changed {
			originalWriter.Header().Del("Content-Length")
			bufferedWriter.commit(translatedBody)
			return
		}
		bufferedWriter.commit(bufferedWriter.body.Bytes())
	}
}

func isJSONResponse(c *gin.Context, writer *translationResponseWriter) bool {
	if writer.Status() == http.StatusNoContent || writer.Status() == http.StatusNotModified || c.Request.Method == http.MethodHead {
		return false
	}
	if strings.TrimSpace(writer.Header().Get("Content-Encoding")) != "" && writer.Header().Get("Content-Encoding") != "identity" {
		return false
	}
	contentType, _, err := mime.ParseMediaType(writer.Header().Get("Content-Type"))
	if err != nil {
		return false
	}
	return contentType == "application/json" || strings.HasSuffix(contentType, "+json")
}

func translateJSONResponse(ctx context.Context, service *translationservice.Service, locale string, body []byte, maxFields int) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return body, false
	}

	selected := make(map[string]struct{})
	budget := maxFields
	if budget == 0 {
		budget = -1
	}
	collectJSONStrings(payload, "", &budget, selected)
	texts := make([]string, 0, len(selected))
	for text := range selected {
		texts = append(texts, text)
	}
	sort.Strings(texts)
	outcomes := translateTexts(ctx, service, locale, texts)
	translatedPayload, changed := applyJSONTranslations(payload, "", outcomes)
	if !changed {
		return body, false
	}
	translatedBody, err := json.Marshal(translatedPayload)
	if err != nil {
		return body, false
	}
	return translatedBody, true
}

func collectJSONStrings(value any, key string, budget *int, selected map[string]struct{}) {
	if *budget == 0 {
		return
	}
	if isExcludedTranslationField(key) {
		return
	}

	switch current := value.(type) {
	case string:
		if !isEligibleResponseText(current) {
			return
		}
		if _, exists := selected[current]; exists {
			return
		}
		if *budget > 0 {
			*budget = *budget - 1
		}
		selected[current] = struct{}{}
	case map[string]any:
		keys := sortedJSONMapKeys(current)
		for _, childKey := range keys {
			collectJSONStrings(current[childKey], childKey, budget, selected)
			if *budget == 0 {
				return
			}
		}
	case []any:
		for index := range current {
			collectJSONStrings(current[index], "", budget, selected)
			if *budget == 0 {
				return
			}
		}
	}
}

type translationOutcome struct {
	translated string
	err        error
}

func translateTexts(ctx context.Context, service *translationservice.Service, locale string, texts []string) map[string]translationOutcome {
	outcomes := make(map[string]translationOutcome, len(texts))
	if len(texts) == 0 {
		return outcomes
	}
	for text, outcome := range service.TranslateBatch(ctx, "", locale, texts) {
		outcomes[text] = translationOutcome{translated: outcome.Translated, err: outcome.Err}
	}
	return outcomes
}

func applyJSONTranslations(value any, key string, outcomes map[string]translationOutcome) (any, bool) {
	if isExcludedTranslationField(key) {
		return value, false
	}

	switch current := value.(type) {
	case string:
		outcome, ok := outcomes[current]
		if !ok || outcome.err != nil {
			return value, false
		}
		if outcome.translated == "" || outcome.translated == current {
			return value, false
		}
		return outcome.translated, true
	case map[string]any:
		changed := false
		for _, childKey := range sortedJSONMapKeys(current) {
			translatedChild, childChanged := applyJSONTranslations(current[childKey], childKey, outcomes)
			if childChanged {
				current[childKey] = translatedChild
				changed = true
			}
		}
		return current, changed
	case []any:
		changed := false
		for index := range current {
			translatedItem, itemChanged := applyJSONTranslations(current[index], "", outcomes)
			if itemChanged {
				current[index] = translatedItem
				changed = true
			}
		}
		return current, changed
	default:
		return value, false
	}
}

func sortedJSONMapKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func isEligibleResponseText(text string) bool {
	return strings.TrimSpace(text) != "" && containsNonEnglish(text)
}

func containsNonEnglish(text string) bool {
	for _, character := range text {
		if unicode.IsLetter(character) && character > unicode.MaxASCII {
			return true
		}
	}
	return false
}

func isExcludedTranslationField(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "id", "ids", "uuid", "key", "keys", "code", "codes",
		"statuscode", "errorcode", "errcode", "type", "kind", "action", "method",
		"path", "paths", "url", "urls", "uri", "href", "route", "routes",
		"locale", "language", "version", "timestamp", "createdat", "updatedat",
		"token", "tokens", "secret", "secrets", "password", "passwd",
		"authorization", "cookie", "cookies", "headers", "credential", "credentials",
		"query", "queries", "sql", "command", "commands", "args", "argv",
		"env", "environment", "log", "logs", "output", "stdout", "stderr",
		"raw", "payload", "body", "requestbody", "responsebody", "stack", "stacktrace", "content":
		return true
	default:
		return false
	}
}

type translationResponseWriter struct {
	gin.ResponseWriter
	body          bytes.Buffer
	status        int
	headerWritten bool
	passthrough   bool
	maxBodyBytes  int
}

func newTranslationResponseWriter(writer gin.ResponseWriter, maxBodyBytes int) *translationResponseWriter {
	return &translationResponseWriter{
		ResponseWriter: writer,
		status:         writer.Status(),
		maxBodyBytes:   maxBodyBytes,
	}
}

func (w *translationResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *translationResponseWriter) WriteHeader(code int) {
	if w.passthrough {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if code > 0 && !w.headerWritten {
		w.status = code
	}
}

func (w *translationResponseWriter) WriteHeaderNow() {
	if w.passthrough {
		w.ResponseWriter.WriteHeaderNow()
		return
	}
	w.headerWritten = true
}

func (w *translationResponseWriter) Write(data []byte) (int, error) {
	if w.passthrough {
		return w.ResponseWriter.Write(data)
	}
	w.headerWritten = true
	if len(data) > 0 && w.body.Len()+len(data) > w.maxBodyBytes {
		w.enablePassthrough()
		return w.ResponseWriter.Write(data)
	}
	return w.body.Write(data)
}

func (w *translationResponseWriter) WriteString(value string) (int, error) {
	return w.Write([]byte(value))
}

func (w *translationResponseWriter) Status() int {
	return w.status
}

func (w *translationResponseWriter) Size() int {
	if w.passthrough {
		return w.ResponseWriter.Size()
	}
	return w.body.Len()
}

func (w *translationResponseWriter) Written() bool {
	if w.passthrough {
		return w.ResponseWriter.Written()
	}
	return w.headerWritten
}

func (w *translationResponseWriter) Flush() {
	w.enablePassthrough()
	w.ResponseWriter.Flush()
}

func (w *translationResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.enablePassthrough()
	return w.ResponseWriter.Hijack()
}

func (w *translationResponseWriter) CloseNotify() <-chan bool {
	return w.ResponseWriter.CloseNotify()
}

func (w *translationResponseWriter) Pusher() http.Pusher {
	return w.ResponseWriter.Pusher()
}

func (w *translationResponseWriter) enablePassthrough() {
	if w.passthrough {
		return
	}
	w.passthrough = true
	if w.headerWritten || w.body.Len() > 0 {
		w.ResponseWriter.WriteHeader(w.status)
	}
	if w.body.Len() > 0 {
		_, _ = w.ResponseWriter.Write(w.body.Bytes())
		w.body.Reset()
	}
}

func (w *translationResponseWriter) commit(data []byte) {
	if w.passthrough {
		return
	}
	w.passthrough = true
	if len(data) == 0 {
		if w.headerWritten {
			w.ResponseWriter.WriteHeader(w.status)
			w.ResponseWriter.WriteHeaderNow()
		}
		return
	}
	w.ResponseWriter.WriteHeader(w.status)
	_, _ = w.ResponseWriter.Write(data)
	w.body.Reset()
}
