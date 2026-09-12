package cluster

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"oneinstack/internal/models"
)

type WebsiteDispatchInput struct {
	WebsiteID      int64    `json:"websiteId"`
	Strategy       string   `json:"strategy,omitempty"` // fixed, tag, least_load
	NodeIDs        []uint   `json:"nodeIds,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	IdempotencyKey string   `json:"idempotencyKey,omitempty"`
}

type WebsiteDispatchResult struct {
	NodeIDs []uint               `json:"nodeIds"`
	Tasks   []models.ClusterTask `json:"tasks"`
}

func (m *Manager) DispatchWebsite(input WebsiteDispatchInput) (WebsiteDispatchResult, error) {
	if input.WebsiteID <= 0 {
		return WebsiteDispatchResult{}, errors.New("website id is required")
	}
	var site models.Website
	if err := m.db.First(&site, input.WebsiteID).Error; err != nil {
		return WebsiteDispatchResult{}, err
	}
	var nodes []models.ClusterNode
	if err := m.db.Where("enabled = ?", true).Find(&nodes).Error; err != nil {
		return WebsiteDispatchResult{}, err
	}
	strategy := strings.ToLower(strings.TrimSpace(input.Strategy))
	if strategy == "" {
		strategy = "least_load"
	}
	selected := selectNodes(nodes, strategy, input.NodeIDs, input.Tags)
	if len(selected) == 0 {
		return WebsiteDispatchResult{}, errors.New("no eligible nodes found")
	}
	payload, _ := json.Marshal(map[string]interface{}{"website": site, "strategy": strategy})
	result := WebsiteDispatchResult{NodeIDs: make([]uint, 0, len(selected)), Tasks: make([]models.ClusterTask, 0, len(selected))}
	for _, node := range selected {
		key := strings.TrimSpace(input.IdempotencyKey)
		if key != "" && len(selected) > 1 {
			key = key + ":" + stringID(node.ID)
		}
		task, err := m.EnqueueTask(EnqueueTaskInput{NodeID: node.ID, Type: "website.sync", Payload: payload, IdempotencyKey: key})
		if err != nil {
			return WebsiteDispatchResult{}, err
		}
		result.NodeIDs = append(result.NodeIDs, node.ID)
		result.Tasks = append(result.Tasks, task)
	}
	return result, nil
}

func selectNodes(nodes []models.ClusterNode, strategy string, fixed []uint, tags []string) []models.ClusterNode {
	if strategy == "fixed" && len(fixed) > 0 {
		set := map[uint]bool{}
		for _, id := range fixed {
			set[id] = true
		}
		out := make([]models.ClusterNode, 0, len(fixed))
		for _, n := range nodes {
			if set[n.ID] {
				out = append(out, n)
			}
		}
		return out
	}
	if strategy == "tag" && len(tags) > 0 {
		out := make([]models.ClusterNode, 0)
		for _, n := range nodes {
			if matchesTags(n.Tags, tags) {
				out = append(out, n)
			}
		}
		return out
	}
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].CPUPercent+nodes[i].MemoryPercent < nodes[j].CPUPercent+nodes[j].MemoryPercent
	})
	if len(nodes) > 0 {
		return nodes[:1]
	}
	return nil
}

func matchesTags(raw string, wanted []string) bool {
	have := map[string]bool{}
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		have[strings.ToLower(strings.TrimSpace(item))] = true
	}
	for _, item := range wanted {
		if !have[strings.ToLower(strings.TrimSpace(item))] {
			return false
		}
	}
	return true
}
func stringID(id uint) string { return strconv.FormatUint(uint64(id), 10) }
