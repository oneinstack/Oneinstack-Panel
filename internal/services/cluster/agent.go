package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"oneinstack/app"
	"oneinstack/internal/buildinfo"
	"oneinstack/internal/models"
	"oneinstack/internal/services/monitoring"
	softwareService "oneinstack/internal/services/software"
	websiteService "oneinstack/internal/services/website"
)

type AgentConfig struct {
	ControllerURL  string
	Token          string
	Interval       time.Duration
	RequestTimeout time.Duration
}

type Agent struct {
	cfg              AgentConfig
	client           *http.Client
	collector        *monitoring.SystemCollector
	once             sync.Once
	serviceActionsMu sync.Mutex
	serviceActions   []models.ClusterServiceActionCapability
	serviceActionsAt time.Time
}

func NewAgent(cfg AgentConfig) (*Agent, error) {
	cfg.ControllerURL = strings.TrimRight(strings.TrimSpace(cfg.ControllerURL), "/")
	cfg.Token = strings.TrimSpace(cfg.Token)
	if cfg.ControllerURL == "" || cfg.Token == "" {
		return nil, errors.New("cluster agent controller URL and token are required")
	}
	if cfg.Interval < 5*time.Second {
		cfg.Interval = 30 * time.Second
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 10 * time.Second
	}
	return &Agent{cfg: cfg, client: &http.Client{Timeout: cfg.RequestTimeout}, collector: monitoring.NewSystemCollector()}, nil
}

func (a *Agent) Start(ctx context.Context) {
	a.once.Do(func() {
		go a.run(ctx)
	})
}

func (a *Agent) run(ctx context.Context) {
	registered := false
	if err := a.register(ctx); err != nil {
		markAgentError(err)
		fmt.Printf("cluster agent registration failed: %v\n", err)
	} else {
		registered = true
		markAgentRegistered()
		if pending, resumeErr := a.resumeDeferredPanelUpdate(ctx); resumeErr != nil {
			markAgentError(resumeErr)
		} else if pending {
			markAgentTaskPoll()
		}
	}
	ticker := time.NewTicker(a.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !registered {
				if err := a.register(ctx); err != nil {
					markAgentError(err)
					fmt.Printf("cluster agent registration failed: %v\n", err)
				} else {
					registered = true
					markAgentRegistered()
				}
			}
			if err := a.heartbeat(ctx); err != nil {
				markAgentError(err)
				fmt.Printf("cluster agent heartbeat failed: %v\n", err)
			} else {
				markAgentHeartbeat()
			}
			if registered {
				pending, err := a.resumeDeferredPanelUpdate(ctx)
				if err != nil {
					markAgentError(err)
					fmt.Printf("cluster panel update recovery failed: %v\n", err)
					continue
				}
				if pending {
					markAgentTaskPoll()
					continue
				}
			}
			if err := a.drainTasks(ctx); err != nil {
				markAgentError(err)
				fmt.Printf("cluster agent task processing failed: %v\n", err)
			}
		}
	}
}

type apiEnvelope struct {
	Data json.RawMessage `json:"data"`
}

type claimData struct {
	Task *models.ClusterTask `json:"task"`
}

type taskControlData struct {
	TaskControl TaskControl `json:"taskControl"`
}

func (a *Agent) drainTasks(ctx context.Context) error {
	for i := 0; i < 20; i++ {
		var envelope apiEnvelope
		if err := a.post(ctx, "/cluster/agent/tasks/next", nil, &envelope); err != nil {
			return err
		}
		markAgentTaskPoll()
		var claimed claimData
		if len(envelope.Data) > 0 {
			if err := json.Unmarshal(envelope.Data, &claimed); err != nil {
				return err
			}
		}
		if claimed.Task == nil {
			return nil
		}
		taskCtx, cancelTask := context.WithCancel(ctx)
		watchDone := make(chan struct{})
		if claimed.Task.Cancelable {
			go a.watchTaskCancellation(taskCtx, claimed.Task.ID, cancelTask, watchDone)
		}
		result, taskErr := a.executeTask(taskCtx, claimed.Task)
		close(watchDone)
		cancelTask()
		if errors.Is(taskErr, errPanelUpdateDeferred) {
			return nil
		}
		completion := TaskCompletion{TaskID: claimed.Task.ID, Result: result}
		if errors.Is(taskErr, context.Canceled) && claimed.Task.Cancelable {
			completion.Status = models.ClusterTaskStatusCanceled
			if isServiceActionTask(claimed.Task.Type) {
				completion.Error = "SERVICE_ACTION_CANCELED"
			} else {
				completion.Error = "task canceled"
			}
		} else if taskErr != nil {
			completion.Status = models.ClusterTaskStatusFailed
			if isServiceActionTask(claimed.Task.Type) {
				phase := "execute"
				if claimed.Task.Type == TaskServiceActionPreflight {
					phase = "preflight"
				}
				completion.Error = serviceActionErrorCode(taskErr, phase)
			} else {
				completion.Error = taskErr.Error()
			}
		} else {
			completion.Status = models.ClusterTaskStatusSucceeded
		}
		if err := a.post(ctx, "/cluster/agent/tasks/complete", completion, nil); err != nil {
			return err
		}
		if taskErr == nil && claimed.Task.Type == "panel.restart" {
			schedulePanelRestart()
			return nil
		}
	}
	return nil
}

func (a *Agent) watchTaskCancellation(ctx context.Context, taskID uint64, cancel context.CancelFunc, done <-chan struct{}) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			var envelope apiEnvelope
			if err := a.post(ctx, "/cluster/agent/tasks/control", map[string]uint64{"taskId": taskID}, &envelope); err != nil {
				continue
			}
			var control TaskControl
			if err := json.Unmarshal(envelope.Data, &control); err == nil && control.CancelRequested && control.Cancelable {
				cancel()
				return
			}
		}
	}
}

func (a *Agent) executeTask(ctx context.Context, task *models.ClusterTask) (json.RawMessage, error) {
	switch task.Type {
	case "panel.restart":
		return json.Marshal(map[string]interface{}{"scheduled": true, "service": "one.service"})
	case TaskPanelUpdateCheck:
		return a.executePanelUpdateCheck(ctx)
	case TaskPanelUpdateApply:
		return a.executePanelUpdateApply(ctx, task)
	case TaskNodeDiagnose:
		return a.executeDiagnosis(ctx, task.Payload)
	case TaskServiceActionPreflight, TaskServiceActionExecute:
		return a.executeServiceAction(ctx, task)
	case "website.sync":
		var payload WebsiteSyncPayload
		if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil {
			return nil, err
		}
		updated, err := websiteService.SyncClusterWebsite(ctx, payload.Website, payload.Settings)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]interface{}{"websiteId": updated.ID, "name": updated.Name, "domain": updated.Domain})
	default:
		return a.executeExtendedTask(ctx, task)
	}
}

func (a *Agent) register(ctx context.Context) error {
	hostname, _ := os.Hostname()
	snapshot, systemID, systemVersion, _ := collectHostSnapshot(ctx, a.collector)
	payload := NodeRegistration{Token: a.cfg.Token, Hostname: hostname, SystemID: systemID, SystemVersion: systemVersion, Architecture: runtime.GOARCH, PanelVersion: buildinfo.Version, AgentVersion: buildinfo.Version, Capabilities: PanelUpdateCapabilities(), ServiceActions: a.currentServiceActions(ctx), HeartbeatIntervalSeconds: int(a.cfg.Interval / time.Second), HostSnapshot: snapshot}
	return a.post(ctx, "/cluster/agent/register", payload, nil)
}

func (a *Agent) heartbeat(ctx context.Context) error {
	snapshot, _, _, err := collectHostSnapshot(ctx, a.collector)
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	payload := NodeHeartbeat{Token: a.cfg.Token, Hostname: hostname, PanelVersion: buildinfo.Version, AgentVersion: buildinfo.Version, Capabilities: PanelUpdateCapabilities(), ServiceActions: a.currentServiceActions(ctx), HeartbeatIntervalSeconds: int(a.cfg.Interval / time.Second), CPUPercent: snapshot.CPUPercent, MemoryPercent: snapshot.MemoryPercent, DiskPercent: snapshot.DiskPercent, NetworkRecvBPS: snapshot.NetworkRecvBPS, NetworkSendBPS: snapshot.NetworkSendBPS, UptimeSeconds: snapshot.UptimeSeconds, CPUTotalCores: snapshot.CPUTotalCores, CPUUsedCores: snapshot.CPUUsedCores, MemoryUsedBytes: snapshot.MemoryUsedBytes, MemoryTotalBytes: snapshot.MemoryTotalBytes, DiskUsedBytes: snapshot.DiskUsedBytes, DiskTotalBytes: snapshot.DiskTotalBytes, IPAddress: snapshot.IPAddress, SubnetMask: snapshot.SubnetMask, Gateway: snapshot.Gateway, MACAddress: snapshot.MACAddress, InterfaceName: snapshot.InterfaceName}
	return a.post(ctx, "/cluster/agent/heartbeat", payload, nil)
}

// currentServiceActions reports only services that are actually installed and
// whose cached/bundled manifest status probe exposes a fixed lifecycle action.
// This is intentionally a capability snapshot, never a transport for a shell
// command or script body.
func (a *Agent) currentServiceActions(ctx context.Context) []models.ClusterServiceActionCapability {
	a.serviceActionsMu.Lock()
	defer a.serviceActionsMu.Unlock()
	if !a.serviceActionsAt.IsZero() && time.Since(a.serviceActionsAt) < 5*time.Minute {
		return append([]models.ClusterServiceActionCapability(nil), a.serviceActions...)
	}
	if database := app.DB(); database == nil {
		return append([]models.ClusterServiceActionCapability(nil), a.serviceActions...)
	} else {
		var rows []models.Software
		if err := database.Where("installed = ?", true).Order("install_time DESC, id DESC").Find(&rows).Error; err != nil {
			return append([]models.ClusterServiceActionCapability(nil), a.serviceActions...)
		}
		seen := make(map[string]struct{})
		items := make([]models.ClusterServiceActionCapability, 0, len(rows))
		installer := softwareService.NewInstaller()
		for _, row := range rows {
			candidate := strings.TrimSpace(row.Component)
			if candidate == "" {
				candidate = strings.TrimSpace(row.Key)
			}
			definition, err := softwareService.ResolveServiceComponent(database, candidate)
			if err != nil || definition.Component == "" {
				continue
			}
			if _, ok := seen[definition.Component]; ok {
				continue
			}
			version := strings.TrimSpace(row.InstallVersion)
			if version == "" {
				version = strings.TrimSpace(row.Version)
			}
			if version == "" {
				continue
			}
			probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			probe, probeErr := installer.InspectServiceLocal(probeCtx, definition.Component, version)
			cancel()
			if probeErr != nil || len(probe.AvailableActions) == 0 {
				continue
			}
			seen[definition.Component] = struct{}{}
			items = append(items, models.ClusterServiceActionCapability{
				Component: definition.Component, DisplayName: definition.DisplayName, ServiceName: probe.ServiceName,
				SoftwareVersion: version, ActiveState: probe.ActiveState, AvailableActions: probe.AvailableActions,
			})
		}
		a.serviceActions = normalizeServiceActionCapabilities(items)
		a.serviceActionsAt = time.Now()
	}
	return append([]models.ClusterServiceActionCapability(nil), a.serviceActions...)
}

func (a *Agent) offline(ctx context.Context) error {
	return a.post(ctx, "/cluster/agent/offline", nil, nil)
}

func schedulePanelRestart() {
	go func() {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		<-timer.C
		command := exec.Command("systemctl", "restart", "one.service")
		if err := command.Start(); err != nil {
			markAgentError(errors.New("panel restart could not be started"))
		} else {
			_ = command.Process.Release()
		}
	}()
}

func (a *Agent) post(ctx context.Context, path string, payload interface{}, output interface{}) error {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
	} else {
		body = []byte(`{}`)
	}
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.ControllerURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("controller returned HTTP %d", resp.StatusCode)
	}
	if output != nil {
		return json.NewDecoder(resp.Body).Decode(output)
	}
	return nil
}
