// 前端实时日志获取示例
// 这个文件展示了如何在前端使用新的实时日志系统

class InstallLogViewer {
    constructor(taskId, containerId) {
        this.taskId = taskId;
        this.container = document.getElementById(containerId);
        this.websocket = null;
        this.isConnected = false;
        this.progressBar = null;
        this.logContainer = null;
        this.statusContainer = null;
        
        this.init();
    }

    init() {
        this.createUI();
        this.connectWebSocket();
        this.startStatusPolling();
    }

    createUI() {
        this.container.innerHTML = `
            <div class="install-log-viewer">
                <div class="log-header">
                    <h3>安装日志 - 任务ID: ${this.taskId}</h3>
                    <div class="log-status" id="status-${this.taskId}">
                        <span class="status-text">连接中...</span>
                        <div class="connection-indicator"></div>
                    </div>
                </div>
                
                <div class="progress-section">
                    <div class="progress-bar-container">
                        <div class="progress-bar" id="progress-${this.taskId}">
                            <div class="progress-fill" style="width: 0%"></div>
                        </div>
                        <span class="progress-text">0%</span>
                    </div>
                    <div class="current-step" id="step-${this.taskId}">准备开始...</div>
                </div>
                
                <div class="log-container" id="log-${this.taskId}">
                    <div class="log-content"></div>
                </div>
                
                <div class="log-controls">
                    <button onclick="this.scrollToBottom()" class="btn-scroll">滚动到底部</button>
                    <button onclick="this.clearLog()" class="btn-clear">清空日志</button>
                    <button onclick="this.downloadLog()" class="btn-download">下载日志</button>
                </div>
            </div>
        `;

        this.progressBar = document.querySelector(`#progress-${this.taskId} .progress-fill`);
        this.logContainer = document.querySelector(`#log-${this.taskId} .log-content`);
        this.statusContainer = document.getElementById(`status-${this.taskId}`);
    }

    connectWebSocket() {
        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        const wsUrl = `${protocol}//${window.location.host}/v1/log/ws?task_id=${this.taskId}`;
        
        this.websocket = new WebSocket(wsUrl);
        
        this.websocket.onopen = () => {
            console.log('WebSocket连接已建立');
            this.isConnected = true;
            this.updateConnectionStatus('已连接', 'connected');
        };
        
        this.websocket.onmessage = (event) => {
            try {
                const message = JSON.parse(event.data);
                this.handleLogMessage(message);
            } catch (error) {
                console.error('解析WebSocket消息失败:', error);
            }
        };
        
        this.websocket.onclose = (event) => {
            console.log('WebSocket连接已关闭:', event.code, event.reason);
            this.isConnected = false;
            this.updateConnectionStatus('连接断开', 'disconnected');
            
            // 如果不是正常关闭，尝试重连
            if (event.code !== 1000) {
                setTimeout(() => {
                    console.log('尝试重新连接...');
                    this.connectWebSocket();
                }, 3000);
            }
        };
        
        this.websocket.onerror = (error) => {
            console.error('WebSocket错误:', error);
            this.updateConnectionStatus('连接错误', 'error');
        };
    }

    handleLogMessage(message) {
        // 添加日志行
        this.addLogLine(message);
        
        // 更新进度
        if (message.progress !== undefined) {
            this.updateProgress(message.progress);
        }
        
        // 更新步骤
        if (message.step) {
            this.updateStep(message.step, message.current_step, message.total_steps);
        }
        
        // 检查是否完成
        if (message.level === 'SUCCESS' && message.progress === 100) {
            this.updateConnectionStatus('安装完成', 'completed');
            this.websocket.close(1000); // 正常关闭
        }
    }

    addLogLine(message) {
        const logLine = document.createElement('div');
        logLine.className = `log-line log-${message.level.toLowerCase()}`;
        
        const timestamp = new Date(message.timestamp).toLocaleTimeString();
        
        logLine.innerHTML = `
            <span class="log-timestamp">[${timestamp}]</span>
            <span class="log-level">[${message.level}]</span>
            <span class="log-message">${this.escapeHtml(message.message)}</span>
        `;
        
        this.logContainer.appendChild(logLine);
        
        // 自动滚动到底部
        this.scrollToBottom();
        
        // 限制日志行数，避免内存占用过多
        const maxLines = 1000;
        if (this.logContainer.children.length > maxLines) {
            this.logContainer.removeChild(this.logContainer.firstChild);
        }
    }

    updateProgress(progress) {
        const progressText = document.querySelector(`#progress-${this.taskId}`).nextElementSibling;
        this.progressBar.style.width = `${progress}%`;
        progressText.textContent = `${progress}%`;
        
        // 添加进度颜色
        if (progress === 100) {
            this.progressBar.className = 'progress-fill completed';
        } else if (progress > 50) {
            this.progressBar.className = 'progress-fill in-progress';
        }
    }

    updateStep(step, currentStep, totalSteps) {
        const stepContainer = document.getElementById(`step-${this.taskId}`);
        let stepText = step;
        
        if (currentStep && totalSteps) {
            stepText = `步骤 ${currentStep}/${totalSteps}: ${step}`;
        }
        
        stepContainer.textContent = stepText;
    }

    updateConnectionStatus(status, className) {
        const statusText = this.statusContainer.querySelector('.status-text');
        const indicator = this.statusContainer.querySelector('.connection-indicator');
        
        statusText.textContent = status;
        indicator.className = `connection-indicator ${className}`;
    }

    startStatusPolling() {
        // 每30秒检查一次状态（作为WebSocket的备用方案）
        this.statusInterval = setInterval(async () => {
            if (!this.isConnected) {
                try {
                    const response = await fetch(`/v1/log/status?task_id=${this.taskId}`);
                    const data = await response.json();
                    
                    if (data.success) {
                        this.updateProgress(data.data.progress || 0);
                        
                        if (data.data.completed) {
                            this.updateConnectionStatus('安装完成', 'completed');
                            clearInterval(this.statusInterval);
                        }
                    }
                } catch (error) {
                    console.error('获取状态失败:', error);
                }
            }
        }, 30000);
    }

    scrollToBottom() {
        this.logContainer.scrollTop = this.logContainer.scrollHeight;
    }

    clearLog() {
        if (confirm('确定要清空日志吗？')) {
            this.logContainer.innerHTML = '';
        }
    }

    async downloadLog() {
        try {
            const response = await fetch(`/v1/log/history?task_id=${this.taskId}&limit=10000`);
            const data = await response.json();
            
            if (data.success) {
                const logContent = data.data.content.join('\n');
                const blob = new Blob([logContent], { type: 'text/plain' });
                const url = URL.createObjectURL(blob);
                
                const a = document.createElement('a');
                a.href = url;
                a.download = `install_log_${this.taskId}.txt`;
                document.body.appendChild(a);
                a.click();
                document.body.removeChild(a);
                URL.revokeObjectURL(url);
            }
        } catch (error) {
            console.error('下载日志失败:', error);
            alert('下载日志失败，请稍后重试');
        }
    }

    escapeHtml(text) {
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
    }

    destroy() {
        if (this.websocket) {
            this.websocket.close();
        }
        if (this.statusInterval) {
            clearInterval(this.statusInterval);
        }
    }
}

// 使用示例
class InstallManager {
    constructor() {
        this.activeInstalls = new Map();
    }

    async startInstall(installParams) {
        try {
            // 发起安装请求
            const response = await fetch('/v1/soft/install', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    'Authorization': `Bearer ${this.getAuthToken()}`
                },
                body: JSON.stringify(installParams)
            });

            const result = await response.json();
            
            if (result.success) {
                const taskId = result.data.installName; // 这里应该是任务ID
                
                // 创建日志查看器
                const logViewer = new InstallLogViewer(taskId, 'log-container');
                this.activeInstalls.set(taskId, logViewer);
                
                return taskId;
            } else {
                throw new Error(result.message || '安装请求失败');
            }
        } catch (error) {
            console.error('启动安装失败:', error);
            throw error;
        }
    }

    stopInstall(taskId) {
        const logViewer = this.activeInstalls.get(taskId);
        if (logViewer) {
            logViewer.destroy();
            this.activeInstalls.delete(taskId);
        }
    }

    getAuthToken() {
        // 从localStorage或其他地方获取认证token
        return localStorage.getItem('auth_token') || '';
    }
}

// CSS样式（应该放在单独的CSS文件中）
const styles = `
.install-log-viewer {
    border: 1px solid #ddd;
    border-radius: 8px;
    background: #fff;
    font-family: 'Monaco', 'Menlo', 'Ubuntu Mono', monospace;
}

.log-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 16px;
    background: #f5f5f5;
    border-bottom: 1px solid #ddd;
    border-radius: 8px 8px 0 0;
}

.log-status {
    display: flex;
    align-items: center;
    gap: 8px;
}

.connection-indicator {
    width: 12px;
    height: 12px;
    border-radius: 50%;
    background: #ccc;
}

.connection-indicator.connected {
    background: #4CAF50;
    animation: pulse 2s infinite;
}

.connection-indicator.disconnected {
    background: #f44336;
}

.connection-indicator.error {
    background: #ff9800;
}

.connection-indicator.completed {
    background: #2196F3;
}

@keyframes pulse {
    0% { opacity: 1; }
    50% { opacity: 0.5; }
    100% { opacity: 1; }
}

.progress-section {
    padding: 16px;
    background: #fafafa;
    border-bottom: 1px solid #eee;
}

.progress-bar-container {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-bottom: 8px;
}

.progress-bar {
    flex: 1;
    height: 8px;
    background: #e0e0e0;
    border-radius: 4px;
    overflow: hidden;
}

.progress-fill {
    height: 100%;
    background: #2196F3;
    transition: width 0.3s ease;
}

.progress-fill.in-progress {
    background: #ff9800;
}

.progress-fill.completed {
    background: #4CAF50;
}

.current-step {
    font-size: 14px;
    color: #666;
    font-style: italic;
}

.log-container {
    height: 400px;
    overflow-y: auto;
    padding: 16px;
    background: #1e1e1e;
    color: #fff;
}

.log-line {
    margin-bottom: 4px;
    line-height: 1.4;
}

.log-timestamp {
    color: #888;
    margin-right: 8px;
}

.log-level {
    margin-right: 8px;
    font-weight: bold;
}

.log-line.log-info .log-level {
    color: #2196F3;
}

.log-line.log-warn .log-level {
    color: #ff9800;
}

.log-line.log-error .log-level {
    color: #f44336;
}

.log-line.log-success .log-level {
    color: #4CAF50;
}

.log-controls {
    display: flex;
    gap: 8px;
    padding: 16px;
    background: #f5f5f5;
    border-top: 1px solid #ddd;
    border-radius: 0 0 8px 8px;
}

.log-controls button {
    padding: 8px 16px;
    border: 1px solid #ddd;
    background: #fff;
    border-radius: 4px;
    cursor: pointer;
    transition: background 0.2s;
}

.log-controls button:hover {
    background: #f0f0f0;
}
`;

// 添加样式到页面
const styleSheet = document.createElement('style');
styleSheet.textContent = styles;
document.head.appendChild(styleSheet);

// 导出类供使用
window.InstallLogViewer = InstallLogViewer;
window.InstallManager = InstallManager;
