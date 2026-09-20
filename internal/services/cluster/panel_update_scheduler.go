package cluster

import (
	"context"
	"errors"
	"log"
	"time"

	"oneinstack/app"
)

const (
	panelUpdateCheckInterval      = 30 * time.Minute
	panelUpdateCheckSweepInterval = time.Minute
)

// RunPanelUpdateCheckSupervisor schedules controller-side update checks. The
// sweep is short so a node that comes online does not wait for the full check
// interval, while the per-node due check keeps task creation at the configured
// interval.
func RunPanelUpdateCheckSupervisor(ctx context.Context) {
	go func() {
		schedule := func() {
			if EffectiveClusterRole(app.ONE_CONFIG.ClusterAgent) != ClusterRoleController {
				return
			}

			manager, err := NewManager(app.DB())
			if err != nil {
				log.Printf("initialize cluster panel update scheduler: %v", err)
				return
			}
			nodes, err := manager.ListNodes()
			if err != nil {
				log.Printf("list cluster nodes for panel update checks: %v", err)
				return
			}
			ids := make([]uint, 0, len(nodes))
			for _, node := range nodes {
				ids = append(ids, node.ID)
			}
			states, err := manager.GetPanelUpdateStates(ids)
			if err != nil {
				log.Printf("read cluster panel update state for scheduler: %v", err)
				return
			}
			statesByNode := make(map[uint]PanelUpdateState, len(states))
			for _, state := range states {
				statesByNode[state.NodeID] = state
			}

			now := time.Now()
			scheduled := 0
			for _, node := range nodes {
				if !nodeHeartbeatFresh(node, now) || !nodeHasCapability(node, CapabilityPanelUpdateCheck) {
					continue
				}
				state, ok := statesByNode[node.ID]
				if ok && (!panelUpdateCheckDue(state, now) || state.ActiveTask != nil) {
					continue
				}
				if _, err := manager.EnqueuePanelUpdateCheck(node.ID); err != nil {
					if errors.Is(err, ErrNodeUnavailable) || errors.Is(err, ErrPanelUpdateCapability) || errors.Is(err, ErrPanelUpdateActive) {
						continue
					}
					log.Printf("schedule panel update check for node %d: %v", node.ID, err)
					continue
				}
				scheduled++
			}
			if scheduled > 0 {
				log.Printf("scheduled %d cluster panel update checks", scheduled)
			}
		}

		schedule()
		ticker := time.NewTicker(panelUpdateCheckSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				schedule()
			}
		}
	}()
}

func panelUpdateCheckDue(state PanelUpdateState, now time.Time) bool {
	if state.LastCheck != nil && now.Before(state.LastCheck.CheckedAt.Add(panelUpdateCheckInterval)) {
		return false
	}
	if state.LastTask != nil && now.Before(state.LastTask.UpdatedAt.Add(panelUpdateCheckInterval)) {
		return false
	}
	return true
}
