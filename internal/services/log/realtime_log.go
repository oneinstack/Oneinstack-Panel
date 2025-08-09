package log

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// LogMessage 日志消息结构
type LogMessage struct {
	Timestamp   time.Time `json:"timestamp"`
	Level       string    `json:"level"`
	Message     string    `json:"message"`
	Progress    int       `json:"progress,omitempty"`     // 安装进度 0-100
	Step        string    `json:"step,omitempty"`         // 当前步骤
	TotalSteps  int       `json:"total_steps,omitempty"`  // 总步骤数
	CurrentStep int       `json:"current_step,omitempty"` // 当前步骤数
}

// InstallLogManager 安装日志管理器
type InstallLogManager struct {
	mu         sync.RWMutex
	logStreams map[string]*LogStream // key: taskId, value: LogStream
	upgrader   websocket.Upgrader
}

// LogStream 日志流
type LogStream struct {
	TaskID      string
	LogFile     string
	SoftName    string
	Clients     map[string]*websocket.Conn
	ClientsMu   sync.RWMutex
	LastOffset  int64
	IsCompleted bool
	Progress    int
	ctx         context.Context
	cancel      context.CancelFunc
}

var logManager *InstallLogManager
var once sync.Once

// GetLogManager 获取日志管理器单例
func GetLogManager() *InstallLogManager {
	once.Do(func() {
		logManager = &InstallLogManager{
			logStreams: make(map[string]*LogStream),
			upgrader: websocket.Upgrader{
				CheckOrigin: func(r *http.Request) bool {
					return true // 生产环境需要检查Origin
				},
				ReadBufferSize:  1024,
				WriteBufferSize: 1024,
			},
		}
	})
	return logManager
}

// CreateLogStream 创建日志流
func (lm *InstallLogManager) CreateLogStream(taskID, logFile, softName string) *LogStream {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())

	stream := &LogStream{
		TaskID:      taskID,
		LogFile:     logFile,
		SoftName:    softName,
		Clients:     make(map[string]*websocket.Conn),
		LastOffset:  0,
		IsCompleted: false,
		Progress:    0,
		ctx:         ctx,
		cancel:      cancel,
	}

	lm.logStreams[taskID] = stream

	// 启动日志监控
	go stream.startLogMonitoring()

	return stream
}

// GetLogStream 获取日志流
func (lm *InstallLogManager) GetLogStream(taskID string) (*LogStream, bool) {
	lm.mu.RLock()
	defer lm.mu.RUnlock()
	stream, exists := lm.logStreams[taskID]
	return stream, exists
}

// RemoveLogStream 移除日志流
func (lm *InstallLogManager) RemoveLogStream(taskID string) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	if stream, exists := lm.logStreams[taskID]; exists {
		stream.cancel()
		stream.closeAllClients()
		delete(lm.logStreams, taskID)
	}
}

// HandleWebSocketConnection 处理WebSocket连接
func (lm *InstallLogManager) HandleWebSocketConnection(c *gin.Context) {
	taskID := c.Query("task_id")
	if taskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "task_id is required"})
		return
	}

	conn, err := lm.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	stream, exists := lm.GetLogStream(taskID)
	if !exists {
		conn.WriteJSON(map[string]interface{}{
			"error": "Task not found",
		})
		return
	}

	clientID := fmt.Sprintf("%p", conn)
	stream.addClient(clientID, conn)
	defer stream.removeClient(clientID)

	// 发送历史日志
	if err := stream.sendHistoryLogs(conn); err != nil {
		log.Printf("Failed to send history logs: %v", err)
		return
	}

	// 保持连接
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}
	}
}

// addClient 添加客户端
func (ls *LogStream) addClient(clientID string, conn *websocket.Conn) {
	ls.ClientsMu.Lock()
	defer ls.ClientsMu.Unlock()
	ls.Clients[clientID] = conn
}

// removeClient 移除客户端
func (ls *LogStream) removeClient(clientID string) {
	ls.ClientsMu.Lock()
	defer ls.ClientsMu.Unlock()
	delete(ls.Clients, clientID)
}

// closeAllClients 关闭所有客户端连接
func (ls *LogStream) closeAllClients() {
	ls.ClientsMu.Lock()
	defer ls.ClientsMu.Unlock()

	for _, conn := range ls.Clients {
		conn.Close()
	}
	ls.Clients = make(map[string]*websocket.Conn)
}

// broadcast 广播消息给所有客户端
func (ls *LogStream) broadcast(message LogMessage) {
	ls.ClientsMu.RLock()
	defer ls.ClientsMu.RUnlock()

	for clientID, conn := range ls.Clients {
		err := conn.WriteJSON(message)
		if err != nil {
			log.Printf("Failed to send message to client %s: %v", clientID, err)
			conn.Close()
			delete(ls.Clients, clientID)
		}
	}
}

// startLogMonitoring 开始日志监控
func (ls *LogStream) startLogMonitoring() {
	ticker := time.NewTicker(1 * time.Second) // 每秒检查一次
	defer ticker.Stop()

	for {
		select {
		case <-ls.ctx.Done():
			return
		case <-ticker.C:
			ls.checkForNewLogs()
			if ls.IsCompleted {
				return
			}
		}
	}
}

// checkForNewLogs 检查新日志
func (ls *LogStream) checkForNewLogs() {
	logPath := filepath.Join("/data/wwwlogs/install", ls.LogFile)

	file, err := os.Open(logPath)
	if err != nil {
		// 文件可能还未创建
		return
	}
	defer file.Close()

	// 获取文件信息
	fileInfo, err := file.Stat()
	if err != nil {
		return
	}

	currentSize := fileInfo.Size()
	if currentSize <= ls.LastOffset {
		// 检查是否完成
		ls.checkIfCompleted()
		return
	}

	// 从上次位置开始读取
	file.Seek(ls.LastOffset, 0)
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			message := ls.parseLogLine(line)
			ls.broadcast(message)
		}
	}

	ls.LastOffset = currentSize
	ls.checkIfCompleted()
}

// parseLogLine 解析日志行
func (ls *LogStream) parseLogLine(line string) LogMessage {
	message := LogMessage{
		Timestamp: time.Now(),
		Message:   line,
		Level:     "INFO",
	}

	// 解析进度信息
	if progress := ls.extractProgress(line); progress >= 0 {
		message.Progress = progress
		ls.Progress = progress
	}

	// 解析步骤信息
	if step := ls.extractStep(line); step != "" {
		message.Step = step
	}

	return message
}

// extractProgress 提取进度信息
func (ls *LogStream) extractProgress(line string) int {
	// 这里可以根据日志格式解析进度
	// 例如：查找 "Progress: 50%" 这样的模式
	// 简化实现，返回-1表示没有进度信息
	return -1
}

// extractStep 提取步骤信息
func (ls *LogStream) extractStep(line string) string {
	// 这里可以根据日志格式解析当前步骤
	// 例如：查找 "Step 3/10: Installing dependencies"
	return ""
}

// checkIfCompleted 检查是否完成
func (ls *LogStream) checkIfCompleted() {
	// 检查结束标志文件
	endFilePath := filepath.Join("/usr/local/one/logs", ls.SoftName+"-end.log")
	if _, err := os.Stat(endFilePath); err == nil {
		ls.IsCompleted = true
		ls.broadcast(LogMessage{
			Timestamp: time.Now(),
			Level:     "SUCCESS",
			Message:   "安装完成",
			Progress:  100,
		})
	}
}

// sendHistoryLogs 发送历史日志
func (ls *LogStream) sendHistoryLogs(conn *websocket.Conn) error {
	logPath := filepath.Join("/data/wwwlogs/install", ls.LogFile)

	file, err := os.Open(logPath)
	if err != nil {
		// 文件可能还未创建，发送欢迎消息
		return conn.WriteJSON(LogMessage{
			Timestamp: time.Now(),
			Level:     "INFO",
			Message:   "开始安装，正在准备...",
			Progress:  0,
		})
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			message := ls.parseLogLine(line)
			if err := conn.WriteJSON(message); err != nil {
				return err
			}
		}
	}

	return nil
}

// GetLogHistory HTTP接口获取历史日志
func (lm *InstallLogManager) GetLogHistory(c *gin.Context) {
	taskID := c.Query("task_id")
	offset := c.DefaultQuery("offset", "0")
	limit := c.DefaultQuery("limit", "1000")

	stream, exists := lm.GetLogStream(taskID)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	// 读取日志文件
	logPath := filepath.Join("/data/wwwlogs/install", stream.LogFile)
	content, err := readLogFile(logPath, offset, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"task_id":   taskID,
		"content":   content,
		"completed": stream.IsCompleted,
		"progress":  stream.Progress,
	})
}

// readLogFile 读取日志文件（支持分页）
func readLogFile(logPath, offset, limit string) ([]string, error) {
	file, err := os.Open(logPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)

	// 简化实现，实际应该支持offset和limit
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	return lines, scanner.Err()
}
