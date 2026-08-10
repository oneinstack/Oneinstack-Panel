package container

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"oneinstack/app"
	auditservice "oneinstack/internal/services/audit"
	securityservice "oneinstack/internal/services/security"

	"github.com/creack/pty"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const (
	containerTerminalTicketTTL       = 30 * time.Second
	containerTerminalReadLimit       = 64 << 10
	containerTerminalMaxOutputBytes  = 32 << 20
	containerTerminalPingInterval    = 20 * time.Second
	containerTerminalSessionCheck    = 15 * time.Second
	containerTerminalWriteTimeout    = 5 * time.Second
	containerTerminalProcessStopWait = 2 * time.Second
)

var (
	ErrContainerTerminalDisabled = errors.New("container terminal is disabled")
	ErrContainerNotFound         = errors.New("container not found")
	ErrContainerNotRunning       = errors.New("container is not running")
	ErrContainerShellUnavailable = errors.New("container shell is unavailable")
	ErrContainerTerminalCapacity = errors.New("container terminal session capacity reached")
	ErrContainerTerminalAudit    = errors.New("container terminal audit is unavailable")
	ErrInvalidContainerTicket    = errors.New("invalid container terminal ticket")
	ErrExpiredContainerTicket    = errors.New("expired container terminal ticket")

	DefaultTerminalTickets  = NewContainerTerminalTicketStore(containerTerminalTicketTTL)
	DefaultTerminalSessions = NewContainerTerminalSessionManager()
)

type ContainerTerminalPolicy struct {
	Enabled       bool
	MaxDuration   time.Duration
	IdleTimeout   time.Duration
	MaxConcurrent int
	MaxPerUser    int
}

type ContainerTerminalRisk struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ContainerTerminalStatus struct {
	Enabled                      bool                    `json:"enabled"`
	Available                    bool                    `json:"available"`
	ContainerID                  string                  `json:"containerId,omitempty"`
	ContainerName                string                  `json:"containerName,omitempty"`
	Running                      bool                    `json:"running"`
	Shell                        string                  `json:"shell,omitempty"`
	Risks                        []ContainerTerminalRisk `json:"risks"`
	RequiresHighRiskConfirmation bool                    `json:"requiresHighRiskConfirmation"`
	MaxSessionMinutes            int                     `json:"maxSessionMinutes"`
	IdleMinutes                  int                     `json:"idleMinutes"`
	MaxOutputMB                  int                     `json:"maxOutputMB"`
	MaxConcurrent                int                     `json:"maxConcurrent"`
	MaxPerUser                   int                     `json:"maxPerUser"`
	ActiveSessions               int                     `json:"activeSessions"`
	Message                      string                  `json:"message,omitempty"`
}

type ContainerTerminalTarget struct {
	Reference string
	ID        string
	Name      string
	Shell     string
	Risks     []ContainerTerminalRisk
}

func CurrentContainerTerminalPolicy() ContainerTerminalPolicy {
	system := app.ONE_CONFIG.System
	return ContainerTerminalPolicy{
		Enabled:       system.ContainerTermEnabled,
		MaxDuration:   time.Duration(system.ContainerTermSessionMins) * time.Minute,
		IdleTimeout:   time.Duration(system.ContainerTermIdleMins) * time.Minute,
		MaxConcurrent: system.ContainerTermMaxConcurrent,
		MaxPerUser:    system.ContainerTermMaxPerUser,
	}
}

func (s *Service) ContainerTerminalStatus(ctx context.Context, reference string) (ContainerTerminalStatus, error) {
	policy := CurrentContainerTerminalPolicy()
	status := ContainerTerminalStatus{
		Enabled:           policy.Enabled,
		Risks:             []ContainerTerminalRisk{},
		MaxSessionMinutes: int(policy.MaxDuration / time.Minute),
		IdleMinutes:       int(policy.IdleTimeout / time.Minute),
		MaxOutputMB:       containerTerminalMaxOutputBytes >> 20,
		MaxConcurrent:     policy.MaxConcurrent,
		MaxPerUser:        policy.MaxPerUser,
		ActiveSessions:    DefaultTerminalSessions.ActiveCount(),
	}
	if !policy.Enabled {
		status.Message = "容器终端未启用"
		return status, nil
	}
	target, err := s.PrepareContainerTerminal(ctx, reference)
	if err != nil {
		status.Message = containerTerminalStatusMessage(err)
		status.ContainerID = target.ID
		status.ContainerName = target.Name
		status.Risks = target.Risks
		status.RequiresHighRiskConfirmation = len(target.Risks) > 0
		return status, err
	}
	status.Available = true
	status.Running = true
	status.ContainerID = target.ID
	status.ContainerName = target.Name
	status.Shell = target.Shell
	status.Risks = target.Risks
	status.RequiresHighRiskConfirmation = len(target.Risks) > 0
	return status, nil
}

func (s *Service) PrepareContainerTerminal(ctx context.Context, reference string) (ContainerTerminalTarget, error) {
	target := ContainerTerminalTarget{Reference: strings.TrimSpace(reference), Risks: []ContainerTerminalRisk{}}
	if !CurrentContainerTerminalPolicy().Enabled {
		return target, ErrContainerTerminalDisabled
	}
	raw, err := s.inspectContainerRaw(ctx, reference)
	if err != nil {
		return target, err
	}
	target.ID = terminalString(raw, "Id")
	target.Name = strings.TrimPrefix(terminalString(raw, "Name"), "/")
	if target.ID == "" {
		return target, errors.New("容器详情缺少完整 ID")
	}
	state := terminalMap(raw, "State")
	if !terminalBool(state, "Running") {
		return target, ErrContainerNotRunning
	}
	target.Risks = detectContainerTerminalRisks(raw)
	target.Shell, err = s.detectContainerShell(ctx, target.ID)
	if err != nil {
		return target, err
	}
	return target, nil
}

func (s *Service) ValidateContainerTerminalTarget(ctx context.Context, containerID, shell string) (ContainerTerminalTarget, error) {
	target, err := s.PrepareContainerTerminal(ctx, containerID)
	if err != nil {
		return target, err
	}
	if target.ID != containerID || target.Shell != shell {
		return target, ErrInvalidContainerTicket
	}
	return target, nil
}

func (s *Service) inspectContainerRaw(ctx context.Context, reference string) (map[string]any, error) {
	reference, err := validateReference(reference)
	if err != nil {
		return nil, err
	}
	output, err := s.runWithTimeout(ctx, 15*time.Second, "inspect", reference)
	if err != nil {
		lowerMessage := strings.ToLower(err.Error())
		if strings.Contains(lowerMessage, "no such container") || strings.Contains(lowerMessage, "no such object") {
			return nil, fmt.Errorf("%w: %s", ErrContainerNotFound, reference)
		}
		return nil, err
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &items); err != nil || len(items) == 0 {
		return nil, errors.New("容器详情响应无效")
	}
	return items[0], nil
}

func (s *Service) detectContainerShell(ctx context.Context, containerID string) (string, error) {
	for _, shell := range []string{"/bin/bash", "/bin/sh"} {
		_, err := s.runWithTimeout(ctx, 5*time.Second, "exec", containerID, shell, "-c", "exit 0")
		if err == nil {
			return shell, nil
		}
		if errors.Is(err, ErrRuntimeUnavailable) || errors.Is(err, ErrDockerCommandTimeout) {
			return "", err
		}
	}
	return "", ErrContainerShellUnavailable
}

func detectContainerTerminalRisks(raw map[string]any) []ContainerTerminalRisk {
	risks := make([]ContainerTerminalRisk, 0, 4)
	hostConfig := terminalMap(raw, "HostConfig")
	if terminalBool(hostConfig, "Privileged") {
		risks = append(risks, ContainerTerminalRisk{Code: "privileged", Message: "容器以 privileged 模式运行"})
	}
	for _, item := range terminalSlice(raw, "Mounts") {
		mount, ok := item.(map[string]any)
		if !ok {
			continue
		}
		source := strings.TrimRight(strings.TrimSpace(terminalString(mount, "Source")), "/")
		if source == "" && terminalString(mount, "Source") == "/" {
			source = "/"
		}
		if source == "/var/run/docker.sock" || source == "/run/docker.sock" {
			risks = appendTerminalRisk(risks, "docker_socket_mount", "容器挂载了 Docker socket")
		}
		if source == "/" {
			risks = appendTerminalRisk(risks, "host_root_mount", "容器挂载了宿主机根目录")
		}
	}
	for _, key := range []string{"PidMode", "IpcMode", "NetworkMode"} {
		if strings.EqualFold(strings.TrimSpace(terminalString(hostConfig, key)), "host") {
			risks = appendTerminalRisk(risks, "host_namespace", "容器使用了宿主机命名空间")
			break
		}
	}
	return risks
}

func appendTerminalRisk(risks []ContainerTerminalRisk, code, message string) []ContainerTerminalRisk {
	for _, risk := range risks {
		if risk.Code == code {
			return risks
		}
	}
	return append(risks, ContainerTerminalRisk{Code: code, Message: message})
}

func terminalMap(value map[string]any, key string) map[string]any {
	result, _ := value[key].(map[string]any)
	return result
}

func terminalSlice(value map[string]any, key string) []any {
	result, _ := value[key].([]any)
	return result
}

func terminalString(value map[string]any, key string) string {
	result, _ := value[key].(string)
	return strings.TrimSpace(result)
}

func terminalBool(value map[string]any, key string) bool {
	result, _ := value[key].(bool)
	return result
}

func containerTerminalStatusMessage(err error) string {
	switch {
	case errors.Is(err, ErrContainerTerminalDisabled):
		return "容器终端未启用"
	case errors.Is(err, ErrContainerNotRunning):
		return "容器未运行，无法打开终端"
	case errors.Is(err, ErrContainerNotFound):
		return "目标容器不存在或已被删除"
	case errors.Is(err, ErrContainerShellUnavailable):
		return "容器中未找到可用的 /bin/bash 或 /bin/sh"
	case errors.Is(err, ErrRuntimeUnavailable):
		return "Docker 运行时不可用"
	default:
		return "容器终端当前不可用"
	}
}

type ContainerTerminalTicketClaims struct {
	UserID             int64
	Username           string
	ClientIP           string
	UserAgent          string
	SourceSessionID    string
	SecurityVersion    uint64
	ContainerReference string
	ContainerID        string
	ContainerName      string
	Shell              string
	HighRiskConfirmed  bool
}

type containerTerminalTicketRecord struct {
	claims    ContainerTerminalTicketClaims
	expiresAt time.Time
}

type ContainerTerminalTicketStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[[sha256.Size]byte]containerTerminalTicketRecord
	now     func() time.Time
}

func NewContainerTerminalTicketStore(ttl time.Duration) *ContainerTerminalTicketStore {
	if ttl <= 0 {
		ttl = containerTerminalTicketTTL
	}
	return &ContainerTerminalTicketStore{ttl: ttl, entries: make(map[[sha256.Size]byte]containerTerminalTicketRecord), now: time.Now}
}

func (store *ContainerTerminalTicketStore) Issue(claims ContainerTerminalTicketClaims) (string, time.Time, error) {
	if store == nil || claims.UserID <= 0 || claims.Username == "" || claims.ClientIP == "" ||
		claims.SourceSessionID == "" || claims.SecurityVersion == 0 || claims.ContainerReference == "" ||
		claims.ContainerID == "" || claims.Shell == "" {
		return "", time.Time{}, ErrInvalidContainerTicket
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	key := sha256.Sum256([]byte(token))
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now()
	store.removeExpiredLocked(now)
	expiresAt := now.Add(store.ttl)
	store.entries[key] = containerTerminalTicketRecord{claims: claims, expiresAt: expiresAt}
	return token, expiresAt, nil
}

func (store *ContainerTerminalTicketStore) Consume(token, clientIP string) (ContainerTerminalTicketClaims, error) {
	if store == nil || token == "" || clientIP == "" {
		return ContainerTerminalTicketClaims{}, ErrInvalidContainerTicket
	}
	key := sha256.Sum256([]byte(token))
	store.mu.Lock()
	defer store.mu.Unlock()
	record, exists := store.entries[key]
	if !exists {
		return ContainerTerminalTicketClaims{}, ErrInvalidContainerTicket
	}
	delete(store.entries, key)
	if !record.expiresAt.After(store.now()) {
		return ContainerTerminalTicketClaims{}, ErrExpiredContainerTicket
	}
	if record.claims.ClientIP != clientIP {
		return ContainerTerminalTicketClaims{}, ErrInvalidContainerTicket
	}
	return record.claims, nil
}

func (store *ContainerTerminalTicketStore) removeExpiredLocked(now time.Time) {
	for key, record := range store.entries {
		if !record.expiresAt.After(now) {
			delete(store.entries, key)
		}
	}
}

type ContainerTerminalSessionClaims struct {
	UserID          int64
	Username        string
	ClientIP        string
	UserAgent       string
	SourceSessionID string
	SecurityVersion uint64
	ContainerID     string
	ContainerName   string
	Shell           string
	RiskConfirmed   bool
}

type ContainerTerminalSessionInfo struct {
	ID          string
	UserID      int64
	StartedAt   time.Time
	LastActive  time.Time
	Commands    int
	InputBytes  int64
	OutputBytes int64
}

type ContainerTerminalSessionManager struct {
	mu       sync.Mutex
	sessions map[string]*ContainerTerminalSession
	now      func() time.Time
}

type ContainerTerminalSession struct {
	manager   *ContainerTerminalSessionManager
	claims    ContainerTerminalSessionClaims
	info      ContainerTerminalSessionInfo
	line      []byte
	closed    bool
	closeOnce sync.Once
}

func NewContainerTerminalSessionManager() *ContainerTerminalSessionManager {
	return &ContainerTerminalSessionManager{sessions: make(map[string]*ContainerTerminalSession), now: time.Now}
}

func (manager *ContainerTerminalSessionManager) ActiveCount() int {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return len(manager.sessions)
}

func (manager *ContainerTerminalSessionManager) Acquire(claims ContainerTerminalSessionClaims, policy ContainerTerminalPolicy) (*ContainerTerminalSession, error) {
	if manager == nil || claims.UserID <= 0 || claims.Username == "" || claims.ContainerID == "" {
		return nil, errors.New("容器终端会话身份不完整")
	}
	identifier, err := randomContainerTerminalSessionID()
	if err != nil {
		return nil, err
	}
	manager.mu.Lock()
	if len(manager.sessions) >= policy.MaxConcurrent {
		manager.mu.Unlock()
		return nil, ErrContainerTerminalCapacity
	}
	userSessions := 0
	for _, active := range manager.sessions {
		if active.info.UserID == claims.UserID {
			userSessions++
		}
	}
	if userSessions >= policy.MaxPerUser {
		manager.mu.Unlock()
		return nil, ErrContainerTerminalCapacity
	}
	now := manager.now().UTC()
	session := &ContainerTerminalSession{
		manager: manager,
		claims:  claims,
		info:    ContainerTerminalSessionInfo{ID: identifier, UserID: claims.UserID, StartedAt: now, LastActive: now},
		line:    make([]byte, 0, 256),
	}
	manager.sessions[identifier] = session
	manager.mu.Unlock()
	if err := session.appendAudit("container.terminal.session.open", "success", fmt.Sprintf(
		"container=%s shell=%s riskConfirmed=%t", shortContainerID(claims.ContainerID), claims.Shell, claims.RiskConfirmed,
	)); err != nil {
		manager.mu.Lock()
		delete(manager.sessions, identifier)
		manager.mu.Unlock()
		return nil, fmt.Errorf("%w: %v", ErrContainerTerminalAudit, err)
	}
	return session, nil
}

func (session *ContainerTerminalSession) Touch() {
	session.manager.mu.Lock()
	if !session.closed {
		session.info.LastActive = session.manager.now().UTC()
	}
	session.manager.mu.Unlock()
}

func (session *ContainerTerminalSession) IdleFor() time.Duration {
	session.manager.mu.Lock()
	defer session.manager.mu.Unlock()
	return session.manager.now().UTC().Sub(session.info.LastActive)
}

func (session *ContainerTerminalSession) RecordOutput(size int) int64 {
	session.manager.mu.Lock()
	defer session.manager.mu.Unlock()
	if !session.closed && size > 0 {
		session.info.OutputBytes += int64(size)
	}
	return session.info.OutputBytes
}

func (session *ContainerTerminalSession) RecordInput(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	type submittedCommand struct{ chars int }
	commands := make([]submittedCommand, 0, 1)
	session.manager.mu.Lock()
	if session.closed {
		session.manager.mu.Unlock()
		return errors.New("容器终端会话已关闭")
	}
	session.info.InputBytes += int64(len(data))
	session.info.LastActive = session.manager.now().UTC()
	for _, value := range data {
		switch value {
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
		case '\r', '\n':
			chars := terminalCommandLength(session.line)
			if chars > 0 {
				commands = append(commands, submittedCommand{chars: chars})
				session.info.Commands++
			}
			session.line = session.line[:0]
		default:
			if value >= 0x20 && len(session.line) < 4096 {
				session.line = append(session.line, value)
			}
		}
	}
	session.manager.mu.Unlock()
	for _, command := range commands {
		message := fmt.Sprintf("container=%s command=redacted chars=%d content=not_stored", shortContainerID(session.claims.ContainerID), command.chars)
		if err := session.appendAudit("container.terminal.command.submit", "success", message); err != nil {
			return fmt.Errorf("%w: %v", ErrContainerTerminalAudit, err)
		}
	}
	return nil
}

func (session *ContainerTerminalSession) Close(reason string) {
	session.closeOnce.Do(func() {
		session.manager.mu.Lock()
		session.closed = true
		delete(session.manager.sessions, session.info.ID)
		info := session.info
		session.manager.mu.Unlock()
		message := fmt.Sprintf("container=%s reason=%s duration=%s commands=%d inputBytes=%d outputBytes=%d",
			shortContainerID(session.claims.ContainerID), sanitizeContainerTerminalToken(reason, 32),
			time.Since(info.StartedAt).Round(time.Second), info.Commands, info.InputBytes, info.OutputBytes)
		_ = session.appendAudit("container.terminal.session.close", "success", message)
	})
}

func (session *ContainerTerminalSession) appendAudit(action, outcome, message string) error {
	manager := auditservice.Default()
	if manager == nil {
		return ErrContainerTerminalAudit
	}
	_, err := manager.Append(auditservice.EventInput{
		RequestID: session.info.ID, EventType: "container_terminal", Action: action,
		Method: "PTY", Route: "/v1/containers/:id/terminal/open", Path: "/v1/containers/" + shortContainerID(session.claims.ContainerID) + "/terminal/open",
		Status: 200, Outcome: outcome, Sensitive: true, UserID: session.claims.UserID, Username: session.claims.Username,
		AuthMode: "ticket", RemoteIP: session.claims.ClientIP, UserAgent: session.claims.UserAgent,
		Message: message, CreatedAt: session.manager.now().UTC(),
	})
	return err
}

func terminalCommandLength(value []byte) int {
	text := strings.TrimSpace(strings.ToValidUTF8(string(value), ""))
	count := 0
	for _, character := range text {
		if !unicode.IsControl(character) {
			count++
		}
	}
	return count
}

func randomContainerTerminalSessionID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func shortContainerID(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func sanitizeContainerTerminalToken(value string, limit int) string {
	value = strings.TrimSpace(value)
	var result strings.Builder
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '_' || character == '-' {
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

func (s *Service) OpenContainerTerminal(c *gin.Context, target ContainerTerminalTarget, claims ContainerTerminalSessionClaims) error {
	policy := CurrentContainerTerminalPolicy()
	if !policy.Enabled {
		return ErrContainerTerminalDisabled
	}
	if _, err := exec.LookPath(s.binary); err != nil {
		return fmt.Errorf("%w: %s executable file not found in PATH", ErrRuntimeUnavailable, s.binary)
	}
	sessionContext, cancel := context.WithTimeout(c.Request.Context(), policy.MaxDuration)
	defer cancel()
	command := exec.CommandContext(sessionContext, s.binary, "exec", "-it", target.ID, target.Shell)
	command.Env = append(os.Environ(), "TERM=xterm-256color")
	terminalSession, err := DefaultTerminalSessions.Acquire(claims, policy)
	if err != nil {
		return err
	}
	closeReason := "client_closed"
	var reasonMu sync.Mutex
	defer func() {
		reasonMu.Lock()
		reason := closeReason
		reasonMu.Unlock()
		terminalSession.Close(reason)
	}()

	upgrader := websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096, CheckOrigin: sameContainerTerminalOrigin}
	connection, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		closeReason = "upgrade_failed"
		return err
	}
	defer connection.Close()
	connection.SetReadLimit(containerTerminalReadLimit)
	var writeMu sync.Mutex

	terminal, err := pty.StartWithAttrs(command, &pty.Winsize{Rows: 30, Cols: 100}, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		closeReason = "process_start_failed"
		writeContainerTerminalNotice(connection, &writeMu, "无法启动容器终端会话\r\n")
		return err
	}
	defer terminal.Close()
	defer stopContainerTerminalProcess(command.Process, command.Wait)

	done := make(chan struct{})
	var doneOnce sync.Once
	finish := func(reason string) {
		doneOnce.Do(func() {
			reasonMu.Lock()
			closeReason = reason
			reasonMu.Unlock()
			close(done)
			cancel()
			_ = connection.Close()
		})
	}

	go copyContainerTerminalOutput(connection, &writeMu, terminal, terminalSession, finish)
	go enforceContainerTerminalLifetime(sessionContext, connection, &writeMu, terminalSession, claims, policy.IdleTimeout, finish)

	for {
		messageType, data, readErr := connection.ReadMessage()
		if readErr != nil {
			if errors.Is(sessionContext.Err(), context.DeadlineExceeded) {
				finish("duration_limit")
			} else {
				finish("client_closed")
			}
			break
		}
		terminalSession.Touch()
		switch messageType {
		case websocket.TextMessage:
			if handleContainerTerminalResize(terminal, data) {
				continue
			}
			decoded, decodeErr := base64.StdEncoding.DecodeString(string(data))
			if decodeErr != nil {
				finish("invalid_input")
				continue
			}
			if err := writeContainerTerminalInput(terminal, terminalSession, decoded); err != nil {
				writeContainerTerminalNotice(connection, &writeMu, "\r\n审计链不可写或终端输入失败，会话已关闭。\r\n")
				finish("input_failed")
			}
		case websocket.BinaryMessage:
			if err := writeContainerTerminalInput(terminal, terminalSession, data); err != nil {
				writeContainerTerminalNotice(connection, &writeMu, "\r\n审计链不可写或终端输入失败，会话已关闭。\r\n")
				finish("input_failed")
			}
		default:
			finish("unsupported_message")
		}
		select {
		case <-done:
			return nil
		default:
		}
	}
	<-done
	return nil
}

func writeContainerTerminalInput(terminal *os.File, session *ContainerTerminalSession, data []byte) error {
	if err := session.RecordInput(data); err != nil {
		return err
	}
	_, err := terminal.Write(data)
	return err
}

func copyContainerTerminalOutput(connection *websocket.Conn, writeMu *sync.Mutex, terminal *os.File, session *ContainerTerminalSession, finish func(string)) {
	buffer := make([]byte, 4096)
	for {
		count, readErr := terminal.Read(buffer)
		if count > 0 {
			if session.RecordOutput(count) > containerTerminalMaxOutputBytes {
				writeContainerTerminalNotice(connection, writeMu, "\r\n终端输出已达到单会话上限，会话已关闭。\r\n")
				finish("output_limit")
				return
			}
			encoded := base64.StdEncoding.EncodeToString(buffer[:count])
			writeMu.Lock()
			_ = connection.SetWriteDeadline(time.Now().Add(containerTerminalWriteTimeout))
			writeErr := connection.WriteMessage(websocket.TextMessage, []byte(encoded))
			writeMu.Unlock()
			if writeErr != nil {
				finish("client_write_failed")
				return
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				finish("process_read_failed")
			} else {
				finish("process_exited")
			}
			return
		}
	}
}

func enforceContainerTerminalLifetime(ctx context.Context, connection *websocket.Conn, writeMu *sync.Mutex, session *ContainerTerminalSession, claims ContainerTerminalSessionClaims, idleTimeout time.Duration, finish func(string)) {
	pingTicker := time.NewTicker(containerTerminalPingInterval)
	defer pingTicker.Stop()
	checkTicker := time.NewTicker(containerTerminalSessionCheck)
	defer checkTicker.Stop()
	idleTicker := time.NewTicker(time.Second)
	defer idleTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			finish("duration_limit")
			return
		case <-pingTicker.C:
			writeMu.Lock()
			err := connection.WriteControl(websocket.PingMessage, []byte("container-terminal"), time.Now().Add(5*time.Second))
			writeMu.Unlock()
			if err != nil {
				finish("client_unreachable")
				return
			}
		case <-idleTicker.C:
			if session.IdleFor() >= idleTimeout {
				writeContainerTerminalNotice(connection, writeMu, "\r\n会话因长时间无输入已关闭。\r\n")
				finish("idle_timeout")
				return
			}
		case <-checkTicker.C:
			if !containerTerminalSourceSessionValid(claims) {
				writeContainerTerminalNotice(connection, writeMu, "\r\n主登录会话已失效，终端已关闭。\r\n")
				finish("source_session_revoked")
				return
			}
		}
	}
}

func containerTerminalSourceSessionValid(claims ContainerTerminalSessionClaims) bool {
	if claims.SourceSessionID == "" || claims.UserID <= 0 {
		return false
	}
	database := app.DB()
	if database == nil {
		return false
	}
	_, err := securityservice.NewSessionManager(database).Validate(claims.SourceSessionID, claims.UserID, claims.SecurityVersion)
	return err == nil
}

func handleContainerTerminalResize(terminal *os.File, data []byte) bool {
	var size struct {
		Rows uint16 `json:"rows"`
		Cols uint16 `json:"cols"`
	}
	if err := json.Unmarshal(data, &size); err != nil {
		return false
	}
	if size.Rows < 1 || size.Rows > 500 || size.Cols < 1 || size.Cols > 500 {
		return true
	}
	_ = pty.Setsize(terminal, &pty.Winsize{Rows: size.Rows, Cols: size.Cols})
	return true
}

func writeContainerTerminalNotice(connection *websocket.Conn, writeMu *sync.Mutex, message string) {
	encoded := base64.StdEncoding.EncodeToString([]byte(message))
	writeMu.Lock()
	defer writeMu.Unlock()
	_ = connection.SetWriteDeadline(time.Now().Add(containerTerminalWriteTimeout))
	_ = connection.WriteMessage(websocket.TextMessage, []byte(encoded))
}

func stopContainerTerminalProcess(process *os.Process, wait func() error) {
	if process == nil {
		return
	}
	_ = syscall.Kill(-process.Pid, syscall.SIGTERM)
	waited := make(chan struct{})
	go func() {
		_ = wait()
		close(waited)
	}()
	timer := time.NewTimer(containerTerminalProcessStopWait)
	defer timer.Stop()
	select {
	case <-waited:
	case <-timer.C:
		_ = syscall.Kill(-process.Pid, syscall.SIGKILL)
		<-waited
	}
}

func sameContainerTerminalOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return false
	}
	return parsed.Host == request.Host
}

func ContainerTerminalTransportAllowed(request *http.Request) bool {
	if app.IsDevelopmentEnvironment() {
		return true
	}
	if request == nil {
		return false
	}
	if request.TLS != nil {
		return true
	}
	if !containerTerminalPeerTrusted(request.RemoteAddr, app.ONE_CONFIG.System.TrustedProxies) {
		return false
	}
	forwardedProto := strings.TrimSpace(strings.Split(request.Header.Get("X-Forwarded-Proto"), ",")[0])
	return strings.EqualFold(forwardedProto, "https")
}

func containerTerminalPeerTrusted(remoteAddress string, trustedProxies []string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddress))
	if err != nil {
		host = strings.TrimSpace(remoteAddress)
	}
	peer := net.ParseIP(host)
	if peer == nil {
		return false
	}
	for _, configured := range trustedProxies {
		configured = strings.TrimSpace(configured)
		if address := net.ParseIP(configured); address != nil {
			if address.Equal(peer) {
				return true
			}
			continue
		}
		_, network, parseErr := net.ParseCIDR(configured)
		if parseErr == nil && network.Contains(peer) {
			return true
		}
	}
	return false
}
