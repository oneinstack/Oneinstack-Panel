package cluster

import (
	"context"
	"fmt"
	"log"
	"oneinstack/app"
	"time"
)

// RunAgentSupervisor keeps the node agent aligned with the persisted
// clusterAgent settings. It allows the web settings page to enable, disable,
// or retarget node mode without requiring a manual service restart.
func RunAgentSupervisor(ctx context.Context) {
	go func() {
		var cancel context.CancelFunc
		var fingerprint string
		defer func() {
			if cancel != nil {
				cancel()
			}
		}()
		reconcile := func() {
			cfg := app.ONE_CONFIG.ClusterAgent
			current := cfgFingerprint(cfg.Enabled, cfg.ControllerURL, cfg.Token, cfg.IntervalSeconds, cfg.RequestTimeoutSec)
			if current == fingerprint {
				return
			}
			if cancel != nil {
				cancel()
				cancel = nil
			}
			fingerprint = current
			if !cfg.Enabled {
				log.Printf("cluster agent disabled")
				return
			}
			agent, err := NewAgent(AgentConfig{ControllerURL: cfg.ControllerURL, Token: cfg.Token, Interval: time.Duration(cfg.IntervalSeconds) * time.Second, RequestTimeout: time.Duration(cfg.RequestTimeoutSec) * time.Second})
			if err != nil {
				log.Printf("cluster agent disabled: %v", err)
				return
			}
			var agentCtx context.Context
			agentCtx, cancel = context.WithCancel(ctx)
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

func cfgFingerprint(enabled bool, controllerURL, token string, interval, timeout int) string {
	return fmt.Sprintf("%t|%s|%s|%d|%d", enabled, controllerURL, token, interval, timeout)
}
