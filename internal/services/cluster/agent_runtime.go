package cluster

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"oneinstack/app"
)

type AgentRuntimeStatus struct {
	Status           string     `json:"status"`
	LastRegisteredAt *time.Time `json:"lastRegisteredAt,omitempty"`
	LastHeartbeatAt  *time.Time `json:"lastHeartbeatAt,omitempty"`
	LastTaskPollAt   *time.Time `json:"lastTaskPollAt,omitempty"`
	LastError        string     `json:"lastError,omitempty"`
	LastErrorAt      *time.Time `json:"lastErrorAt,omitempty"`
}

var agentRuntime = struct {
	sync.RWMutex
	status AgentRuntimeStatus
}{status: AgentRuntimeStatus{Status: ClusterRoleUnconfigured}}

var activeAgent = struct {
	sync.Mutex
	cancel context.CancelFunc
}{}

func stopActiveAgent() {
	activeAgent.Lock()
	if activeAgent.cancel != nil {
		activeAgent.cancel()
		activeAgent.cancel = nil
	}
	activeAgent.Unlock()
}

func activateAgent(cancel context.CancelFunc, expectedFingerprint string) bool {
	activeAgent.Lock()
	defer activeAgent.Unlock()
	cfg := app.ONE_CONFIG.ClusterAgent
	role := EffectiveClusterRole(cfg)
	if cfgFingerprint(role, cfg.Enabled, cfg.ControllerURL, cfg.Token, cfg.IntervalSeconds, cfg.RequestTimeoutSec) != expectedFingerprint || role != ClusterRoleNode || !cfg.Enabled {
		cancel()
		return false
	}
	if activeAgent.cancel != nil {
		activeAgent.cancel()
	}
	activeAgent.cancel = cancel
	return true
}

func agentIsConfiguredAndRunning() bool {
	cfg := app.ONE_CONFIG.ClusterAgent
	return EffectiveClusterRole(cfg) == ClusterRoleNode && cfg.Enabled
}

func ResetAgentRuntime() {
	agentRuntime.Lock()
	agentRuntime.status = AgentRuntimeStatus{Status: ClusterRoleUnconfigured}
	agentRuntime.Unlock()
}

func AgentRuntimeSnapshot(role string, enabled bool) AgentRuntimeStatus {
	agentRuntime.RLock()
	status := agentRuntime.status
	agentRuntime.RUnlock()
	if role == ClusterRoleUnconfigured {
		status.Status = ClusterRoleUnconfigured
	} else if role == ClusterRoleController {
		status.Status = "controller"
	} else if !enabled {
		status.Status = "not_configured"
	} else if strings.TrimSpace(status.Status) == "" || status.Status == ClusterRoleUnconfigured {
		status.Status = "connecting"
	}
	return status
}

func updateAgentRuntime(fn func(*AgentRuntimeStatus)) {
	agentRuntime.Lock()
	fn(&agentRuntime.status)
	agentRuntime.Unlock()
}

func markAgentConnecting() {
	updateAgentRuntime(func(status *AgentRuntimeStatus) { status.Status = "connecting" })
}

func markAgentRegistered() {
	if !agentIsConfiguredAndRunning() {
		return
	}
	now := time.Now()
	updateAgentRuntime(func(status *AgentRuntimeStatus) {
		status.Status = "online"
		status.LastRegisteredAt = &now
		status.LastError = ""
		status.LastErrorAt = nil
	})
}

func markAgentHeartbeat() {
	if !agentIsConfiguredAndRunning() {
		return
	}
	now := time.Now()
	updateAgentRuntime(func(status *AgentRuntimeStatus) {
		status.Status = "online"
		status.LastHeartbeatAt = &now
		status.LastError = ""
		status.LastErrorAt = nil
	})
}

func markAgentTaskPoll() {
	if !agentIsConfiguredAndRunning() {
		return
	}
	now := time.Now()
	updateAgentRuntime(func(status *AgentRuntimeStatus) { status.LastTaskPollAt = &now })
}

func markAgentError(err error) {
	if err == nil || !agentIsConfiguredAndRunning() {
		return
	}
	now := time.Now()
	message := strings.TrimSpace(err.Error())
	if strings.Contains(message, "HTTP ") {
		parts := strings.SplitN(message, "HTTP ", 2)
		fields := strings.Fields(parts[1])
		if len(fields) > 0 {
			message = "controller returned HTTP " + fields[0]
		} else {
			message = "controller returned an error"
		}
	} else {
		message = "unable to connect to the controller; check the address, network, and token"
	}
	updateAgentRuntime(func(status *AgentRuntimeStatus) {
		status.Status = "error"
		status.LastError = message
		status.LastErrorAt = &now
	})
}

func RunAgentSupervisor(ctx context.Context) {
	go func() {
		var fingerprint string
		defer stopActiveAgent()
		reconcile := func() {
			cfg := app.ONE_CONFIG.ClusterAgent
			role := EffectiveClusterRole(cfg)
			current := cfgFingerprint(role, cfg.Enabled, cfg.ControllerURL, cfg.Token, cfg.IntervalSeconds, cfg.RequestTimeoutSec)
			if current == fingerprint {
				return
			}
			stopActiveAgent()
			fingerprint = current
			if role != ClusterRoleNode || !cfg.Enabled {
				return
			}
			agent, err := NewAgent(AgentConfig{ControllerURL: cfg.ControllerURL, Token: cfg.Token, Interval: time.Duration(cfg.IntervalSeconds) * time.Second, RequestTimeout: time.Duration(cfg.RequestTimeoutSec) * time.Second})
			if err != nil {
				markAgentError(err)
				log.Printf("cluster agent disabled: %v", err)
				return
			}
			markAgentConnecting()
			agentCtx, cancel := context.WithCancel(ctx)
			if !activateAgent(cancel, current) {
				return
			}
			agent.Start(agentCtx)
			log.Printf("cluster agent enabled: controller=%s interval=%ds", cfg.ControllerURL, cfg.IntervalSeconds)
		}
		reconcile()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				reconcile()
			}
		}
	}()
}

// RunNodeStatusSupervisor expires stale workers and task leases even when no
// user is viewing the cluster page. Role changes use the live in-memory config.
func RunNodeStatusSupervisor(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		expire := func() {
			if EffectiveClusterRole(app.ONE_CONFIG.ClusterAgent) != ClusterRoleController {
				return
			}
			manager, err := NewManager(app.DB())
			if err == nil {
				_, err = manager.ExpireStaleNodes(time.Now())
			}
			if err == nil {
				err = manager.RecoverStaleTasks(15 * time.Minute)
			}
			if err != nil {
				log.Printf("expire stale cluster nodes or tasks: %v", err)
			}
		}
		expire()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				expire()
			}
		}
	}()
}

func cfgFingerprint(role string, enabled bool, controllerURL, token string, interval, timeout int) string {
	return fmt.Sprintf("%s|%t|%s|%s|%d|%d", role, enabled, controllerURL, token, interval, timeout)
}
