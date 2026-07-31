package ssh

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"oneinstack/internal/services/audit"
)

var (
	ErrTerminalCapacity         = errors.New("terminal session capacity reached")
	ErrTerminalAuditUnavailable = errors.New("terminal audit is unavailable")
	DefaultSessions             = NewTerminalSessionManager()
)

type TerminalSessionClaims struct {
	UserID          int64
	Username        string
	ClientIP        string
	UserAgent       string
	SourceSessionID string
	SecurityVersion uint64
}

type TerminalSessionInfo struct {
	ID          string    `json:"id"`
	UserID      int64     `json:"-"`
	Username    string    `json:"username"`
	ClientIP    string    `json:"clientIp"`
	StartedAt   time.Time `json:"startedAt"`
	LastActive  time.Time `json:"lastActive"`
	Commands    int       `json:"commands"`
	InputBytes  int64     `json:"inputBytes"`
	OutputBytes int64     `json:"outputBytes"`
}

type TerminalSessionManager struct {
	mu       sync.Mutex
	sessions map[string]*TerminalSession
	now      func() time.Time
}

type TerminalSession struct {
	manager   *TerminalSessionManager
	info      TerminalSessionInfo
	claims    TerminalSessionClaims
	line      []byte
	escape    uint8
	closed    bool
	closeOnce sync.Once
}

func NewTerminalSessionManager() *TerminalSessionManager {
	return &TerminalSessionManager{
		sessions: make(map[string]*TerminalSession),
		now:      time.Now,
	}
}

func (manager *TerminalSessionManager) Acquire(
	claims TerminalSessionClaims,
	policy TerminalPolicy,
) (*TerminalSession, error) {
	if manager == nil || claims.UserID <= 0 || strings.TrimSpace(claims.Username) == "" {
		return nil, errors.New("terminal session identity is incomplete")
	}
	if policy.MaxConcurrent < 1 || policy.MaxPerUser < 1 {
		return nil, errors.New("terminal concurrency policy is invalid")
	}
	id, err := randomTerminalSessionID()
	if err != nil {
		return nil, err
	}
	manager.mu.Lock()
	if len(manager.sessions) >= policy.MaxConcurrent {
		manager.mu.Unlock()
		return nil, ErrTerminalCapacity
	}
	userSessions := 0
	for _, active := range manager.sessions {
		if active.info.UserID == claims.UserID {
			userSessions++
		}
	}
	if userSessions >= policy.MaxPerUser {
		manager.mu.Unlock()
		return nil, ErrTerminalCapacity
	}
	now := manager.now().UTC()
	session := &TerminalSession{
		manager: manager,
		claims:  claims,
		info: TerminalSessionInfo{
			ID: id, UserID: claims.UserID, Username: claims.Username,
			ClientIP: claims.ClientIP, StartedAt: now, LastActive: now,
		},
		line: make([]byte, 0, 256),
	}
	manager.sessions[id] = session
	manager.mu.Unlock()
	if err := manager.appendAudit(
		session,
		"terminal.session.open",
		"success",
		"低权限终端会话已建立",
	); err != nil {
		manager.mu.Lock()
		delete(manager.sessions, id)
		session.closed = true
		manager.mu.Unlock()
		return nil, fmt.Errorf("%w: %v", ErrTerminalAuditUnavailable, err)
	}
	return session, nil
}

func (manager *TerminalSessionManager) ActiveCount() int {
	if manager == nil {
		return 0
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return len(manager.sessions)
}

func (manager *TerminalSessionManager) List() []TerminalSessionInfo {
	if manager == nil {
		return []TerminalSessionInfo{}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	result := make([]TerminalSessionInfo, 0, len(manager.sessions))
	for _, session := range manager.sessions {
		result = append(result, session.info)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].StartedAt.After(result[right].StartedAt)
	})
	return result
}

func (session *TerminalSession) ID() string {
	if session == nil {
		return ""
	}
	return session.info.ID
}

func (session *TerminalSession) RecordInput(data []byte) error {
	if session == nil || len(data) == 0 {
		return nil
	}
	session.manager.mu.Lock()
	if session.closed {
		session.manager.mu.Unlock()
		return errors.New("terminal session is closed")
	}
	session.info.InputBytes += int64(len(data))
	session.info.LastActive = session.manager.now().UTC()
	commands := session.consumeInputLocked(data)
	session.manager.mu.Unlock()
	for _, command := range commands {
		if err := session.auditCommand(command); err != nil {
			return fmt.Errorf("%w: %v", ErrTerminalAuditUnavailable, err)
		}
	}
	return nil
}

func (session *TerminalSession) Touch() {
	if session == nil {
		return
	}
	session.manager.mu.Lock()
	if !session.closed {
		session.info.LastActive = session.manager.now().UTC()
	}
	session.manager.mu.Unlock()
}

func (session *TerminalSession) RecordOutput(size int) int64 {
	if session == nil || session.manager == nil || size <= 0 {
		return 0
	}
	session.manager.mu.Lock()
	defer session.manager.mu.Unlock()
	if session.closed {
		return session.info.OutputBytes
	}
	session.info.OutputBytes += int64(size)
	return session.info.OutputBytes
}

func (session *TerminalSession) IdleFor() time.Duration {
	if session == nil || session.manager == nil {
		return 0
	}
	session.manager.mu.Lock()
	defer session.manager.mu.Unlock()
	return session.manager.now().UTC().Sub(session.info.LastActive)
}

func (session *TerminalSession) Close(reason string) {
	if session == nil || session.manager == nil {
		return
	}
	session.closeOnce.Do(func() {
		session.manager.mu.Lock()
		session.closed = true
		delete(session.manager.sessions, session.info.ID)
		info := session.info
		session.manager.mu.Unlock()
		duration := time.Since(info.StartedAt).Round(time.Second)
		message := fmt.Sprintf(
			"reason=%s duration=%s commands=%d inputBytes=%d outputBytes=%d",
			sanitizeAuditToken(reason, 32),
			duration,
			info.Commands,
			info.InputBytes,
			info.OutputBytes,
		)
		_ = session.manager.appendAudit(session, "terminal.session.close", "success", message)
	})
}

func (session *TerminalSession) consumeInputLocked(data []byte) []string {
	var commands []string
	for _, value := range data {
		if session.consumeEscapeByte(value) {
			continue
		}
		switch value {
		case 0x1b:
			session.escape = 1
		case 0x03, 0x15:
			session.line = session.line[:0]
		case 0x08, 0x7f:
			if len(session.line) > 0 {
				_, size := utf8.DecodeLastRune(session.line)
				if size < 1 {
					size = 1
				}
				session.line = session.line[:len(session.line)-size]
			}
		case '\r', '\n', 0x0f:
			if command := normalizeSubmittedCommand(session.line); command != "" {
				commands = append(commands, command)
				session.info.Commands++
			}
			session.line = session.line[:0]
		default:
			if value >= 0x20 && len(session.line) < 4096 {
				session.line = append(session.line, value)
			}
		}
	}
	return commands
}

func (session *TerminalSession) consumeEscapeByte(value byte) bool {
	switch session.escape {
	case 0:
		return false
	case 1:
		if value == '\r' || value == '\n' || value == 0x0f {
			session.escape = 0
			return false
		}
		switch value {
		case '[', 'O':
			session.escape = 2
		case ']':
			session.escape = 3
		default:
			session.escape = 0
		}
		return true
	case 2:
		if value >= 0x40 && value <= 0x7e {
			session.escape = 0
		}
		return true
	case 3:
		if value == 0x07 {
			session.escape = 0
		} else if value == 0x1b {
			session.escape = 4
		}
		return true
	case 4:
		if value == '\\' {
			session.escape = 0
		} else {
			session.escape = 3
		}
		return true
	default:
		session.escape = 0
		return true
	}
}

func (session *TerminalSession) auditCommand(command string) error {
	message := fmt.Sprintf(
		"command=redacted chars=%d session=%s content=not_stored",
		utf8.RuneCountInString(command),
		session.info.ID,
	)
	return session.manager.appendAudit(
		session,
		"terminal.command.submit",
		"success",
		message,
	)
}

func (manager *TerminalSessionManager) appendAudit(
	session *TerminalSession,
	action string,
	outcome string,
	message string,
) error {
	auditManager := audit.Default()
	if auditManager == nil || session == nil {
		return ErrTerminalAuditUnavailable
	}
	_, err := auditManager.Append(audit.EventInput{
		RequestID: session.info.ID,
		EventType: "terminal",
		Action:    action,
		Method:    "PTY",
		Route:     "/v1/ssh/open",
		Path:      "/v1/ssh/open",
		Status:    200,
		Outcome:   outcome,
		Sensitive: true,
		UserID:    session.claims.UserID,
		Username:  session.claims.Username,
		AuthMode:  "ticket",
		RemoteIP:  session.claims.ClientIP,
		UserAgent: session.claims.UserAgent,
		Message:   message,
		CreatedAt: manager.now().UTC(),
	})
	return err
}

func normalizeSubmittedCommand(value []byte) string {
	text := strings.TrimSpace(strings.ToValidUTF8(string(value), ""))
	if text == "" {
		return ""
	}
	var cleaned strings.Builder
	cleaned.Grow(len(text))
	space := false
	for _, character := range text {
		if unicode.IsSpace(character) {
			if !space {
				cleaned.WriteByte(' ')
				space = true
			}
			continue
		}
		if unicode.IsControl(character) {
			continue
		}
		cleaned.WriteRune(character)
		space = false
	}
	return strings.TrimSpace(cleaned.String())
}

func sanitizeAuditToken(value string, limit int) string {
	var result strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' ||
			character == '-' || character == '_' || character == '.' {
			result.WriteRune(character)
		}
		if result.Len() >= limit {
			break
		}
	}
	if result.Len() == 0 {
		return "unknown"
	}
	return result.String()
}

func randomTerminalSessionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
