package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"oneinstack/internal/buildinfo"
	"oneinstack/internal/models"
	"oneinstack/internal/services/monitoring"
	websiteService "oneinstack/internal/services/website"

	"github.com/shirou/gopsutil/v4/host"
)

type AgentConfig struct {
	ControllerURL  string
	Token          string
	Interval       time.Duration
	RequestTimeout time.Duration
}

type Agent struct {
	cfg       AgentConfig
	client    *http.Client
	collector *monitoring.SystemCollector
	once      sync.Once
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
	if err := a.register(ctx); err != nil {
		fmt.Printf("cluster agent registration failed: %v\n", err)
	}
	ticker := time.NewTicker(a.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.heartbeat(ctx); err != nil {
				fmt.Printf("cluster agent heartbeat failed: %v\n", err)
			}
			if err := a.drainTasks(ctx); err != nil {
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

func (a *Agent) drainTasks(ctx context.Context) error {
	for i := 0; i < 20; i++ {
		var envelope apiEnvelope
		if err := a.post(ctx, "/cluster/agent/tasks/next", nil, &envelope); err != nil {
			return err
		}
		var claimed claimData
		if len(envelope.Data) > 0 {
			if err := json.Unmarshal(envelope.Data, &claimed); err != nil {
				return err
			}
		}
		if claimed.Task == nil {
			return nil
		}
		result, taskErr := a.executeTask(ctx, claimed.Task)
		completion := TaskCompletion{TaskID: claimed.Task.ID, Result: result}
		if taskErr != nil {
			completion.Status = models.ClusterTaskStatusFailed
			completion.Error = taskErr.Error()
		} else {
			completion.Status = models.ClusterTaskStatusSucceeded
		}
		if err := a.post(ctx, "/cluster/agent/tasks/complete", completion, nil); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) executeTask(ctx context.Context, task *models.ClusterTask) (json.RawMessage, error) {
	switch task.Type {
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
	payload := NodeRegistration{Token: a.cfg.Token, Hostname: hostname, Architecture: runtime.GOARCH, PanelVersion: buildinfo.Version, AgentVersion: buildinfo.Version}
	return a.post(ctx, "/cluster/agent/register", payload, nil)
}

func (a *Agent) heartbeat(ctx context.Context) error {
	sample, err := a.collector.Collect(ctx)
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	uptime, _ := host.UptimeWithContext(ctx)
	payload := NodeHeartbeat{Token: a.cfg.Token, Hostname: hostname, PanelVersion: buildinfo.Version, AgentVersion: buildinfo.Version, CPUPercent: sample.CPUPercent, MemoryPercent: sample.MemoryPercent, DiskPercent: sample.DiskPercent, NetworkRecvBPS: sample.NetworkReceiveBPS, NetworkSendBPS: sample.NetworkSendBPS, UptimeSeconds: uptime}
	return a.post(ctx, "/cluster/agent/heartbeat", payload, nil)
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
