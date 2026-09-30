package cluster

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"oneinstack/internal/models"
	websiteService "oneinstack/internal/services/website"
)

type WebsiteDispatchInput struct {
	WebsiteID      int64    `json:"websiteId"`
	Strategy       string   `json:"strategy,omitempty"` // fixed, group, tag, least_load
	NodeIDs        []uint   `json:"nodeIds,omitempty"`
	Groups         []string `json:"groups,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	IdempotencyKey string   `json:"idempotencyKey,omitempty"`
	IncludeContent bool     `json:"includeContent,omitempty"`
}

type WebsiteDispatchResult struct {
	NodeIDs []uint               `json:"nodeIds"`
	Tasks   []ClusterTaskSummary `json:"tasks"`
}

type WebsiteSyncPayload struct {
	Website  models.Website                  `json:"website"`
	Settings *websiteService.WebsiteSettings `json:"settings,omitempty"`
}

type WebsiteContentSyncPayload struct {
	Website       models.Website                  `json:"website"`
	Settings      *websiteService.WebsiteSettings `json:"settings,omitempty"`
	ArchiveBase64 string                          `json:"archiveBase64"`
	SHA256        string                          `json:"sha256"`
}

type WebsiteDispatchPreflightError struct {
	NodeName string
	ReasonZH string
	ReasonEN string
}

func (err *WebsiteDispatchPreflightError) Error() string {
	return fmt.Sprintf("节点 %s：%s", err.NodeName, err.ReasonZH)
}

func (err *WebsiteDispatchPreflightError) Localized(locale string) string {
	if strings.EqualFold(locale, "en-US") {
		return fmt.Sprintf("Node %s: %s", err.NodeName, err.ReasonEN)
	}
	return err.Error()
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
	now := time.Now()
	if _, err := m.ExpireStaleNodes(now); err != nil {
		return WebsiteDispatchResult{}, err
	}
	if err := m.db.Where("enabled = ? AND status = ? AND lifecycle_status = ?", true, models.ClusterNodeStatusOnline, models.ClusterNodeLifecycleActive).Find(&nodes).Error; err != nil {
		return WebsiteDispatchResult{}, err
	}
	freshNodes := nodes[:0]
	for i := range nodes {
		if nodeHeartbeatFresh(nodes[i], now) {
			freshNodes = append(freshNodes, nodes[i])
		}
	}
	nodes = freshNodes
	strategy := strings.ToLower(strings.TrimSpace(input.Strategy))
	if strategy == "" {
		strategy = "least_load"
	}
	if strategy == "group" && len(normalizeSelectionValues(input.Groups)) == 0 {
		return WebsiteDispatchResult{}, errors.New("select at least one group")
	}
	if strategy == "tag" && len(normalizeSelectionValues(input.Tags)) == 0 {
		return WebsiteDispatchResult{}, errors.New("select at least one tag")
	}
	selected := selectNodes(nodes, strategy, input.NodeIDs, input.Groups, input.Tags)
	if len(selected) == 0 {
		return WebsiteDispatchResult{}, errors.New("no eligible nodes found")
	}
	settingsDocument, err := websiteService.GetSettings(site.ID)
	if err != nil {
		return WebsiteDispatchResult{}, err
	}
	for _, node := range selected {
		if err := preflightWebsiteDispatchNode(node, site, settingsDocument.Settings); err != nil {
			return WebsiteDispatchResult{}, err
		}
	}
	payload, _ := json.Marshal(WebsiteSyncPayload{Website: site, Settings: &settingsDocument.Settings})
	contentPayload := []byte(nil)
	if input.IncludeContent {
		archiveData, err := packWebsiteContent(site.RootDir)
		if err != nil {
			return WebsiteDispatchResult{}, err
		}
		contentPayload, _ = json.Marshal(WebsiteContentSyncPayload{Website: site, Settings: &settingsDocument.Settings, ArchiveBase64: base64.StdEncoding.EncodeToString(archiveData), SHA256: fmt.Sprintf("%x", sha256.Sum256(archiveData))})
	}
	result := WebsiteDispatchResult{NodeIDs: make([]uint, 0, len(selected)), Tasks: make([]ClusterTaskSummary, 0, len(selected))}
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
		result.Tasks = append(result.Tasks, SummarizeTask(task))
		if input.IncludeContent {
			contentKey := key
			if contentKey != "" {
				contentKey += ":content"
			}
			contentTask, err := m.EnqueueTask(EnqueueTaskInput{NodeID: node.ID, Type: "website.content_sync", Payload: contentPayload, IdempotencyKey: contentKey})
			if err != nil {
				return WebsiteDispatchResult{}, err
			}
			result.Tasks = append(result.Tasks, SummarizeTask(contentTask))
		}
	}
	return result, nil
}

func preflightWebsiteDispatchNode(node models.ClusterNode, site models.Website, settings websiteService.WebsiteSettings) error {
	reject := func(zh, en string) error {
		return &WebsiteDispatchPreflightError{NodeName: node.Name, ReasonZH: zh, ReasonEN: en}
	}
	if node.ServiceActionsReportedAt == nil {
		return reject("尚未上报 Web Server 状态，请升级节点 Agent 或等待心跳后重试", "Web server status has not been reported; upgrade the node Agent or wait for a heartbeat")
	}
	var engines []string
	for _, service := range node.ServiceActions {
		switch strings.ToLower(strings.TrimSpace(service.Component)) {
		case "nginx", "openresty", "tengine", "apache", "caddy":
			if strings.EqualFold(strings.TrimSpace(service.ActiveState), "active") {
				engines = append(engines, strings.ToLower(strings.TrimSpace(service.Component)))
			}
		}
	}
	if len(engines) == 0 {
		return reject("未探测到运行中的受管 Web Server，请检查节点的服务操作状态", "No running managed Web server was detected; check the node's service actions")
	}
	if len(engines) > 1 {
		return reject("探测到多个运行中的 Web Server，无法确定网站下发目标", "Multiple running Web servers were detected; the website target is ambiguous")
	}
	features, err := websiteService.ClusterSettingsIncompatibility(site, settings, engines[0])
	if err != nil {
		return err
	}
	if len(features) == 0 {
		if engines[0] == "apache" {
			// SyncClusterWebsite creates a missing site before applying its
			// transferred settings. That create uses Nginx-only default security
			// headers, so an Apache target is unsafe even when the transferred
			// settings themselves contain no unsupported directives.
			return reject(
				"当前网站下发流程在创建网站时会生成 Apache 无法渲染的默认安全响应头，请先完善 Apache 网站创建能力",
				"The current website sync creates sites with default security headers that Apache cannot render; Apache website creation must be supported before dispatch",
			)
		}
		return nil
	}
	zhNames := map[string]string{"rewrite_rules": "重写规则", "server_directives": "访问控制、限速或安全响应头", "extra_locations": "子目录绑定、重定向、反向代理等 location 规则"}
	enNames := map[string]string{"rewrite_rules": "rewrite rules", "server_directives": "access control, rate limits or security headers", "extra_locations": "directory bindings, redirects or proxy location rules"}
	zh, en := make([]string, 0, len(features)), make([]string, 0, len(features))
	for _, feature := range features {
		zh = append(zh, zhNames[feature])
		en = append(en, enNames[feature])
	}
	return reject(
		fmt.Sprintf("目标 Web Server 为 %s；源网站设置包含无法渲染的 Nginx 指令：%s。请调整源网站设置或选择兼容节点", engines[0], strings.Join(zh, "、")),
		fmt.Sprintf("Target Web server is %s; source website settings contain Nginx directives it cannot render: %s. Adjust the source settings or select a compatible node", engines[0], strings.Join(en, ", ")),
	)
}

const maxWebsiteSyncBytes = 64 << 20

func packWebsiteContent(root string) ([]byte, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || !filepath.IsAbs(root) {
		return nil, errors.New("website root is invalid for content sync")
	}
	var buffer bytes.Buffer
	zipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(zipWriter)
	var total int64
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("website content path escapes root")
		}
		if info.Size() > maxWebsiteSyncBytes || total+info.Size() > maxWebsiteSyncBytes {
			return errors.New("website content exceeds 64 MiB")
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err == nil {
			header.Name = filepath.ToSlash(rel)
			err = tarWriter.WriteHeader(header)
		}
		if err == nil {
			_, err = io.Copy(tarWriter, file)
		}
		_ = file.Close()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if closeErr := tarWriter.Close(); err == nil {
		err = closeErr
	}
	if closeErr := zipWriter.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func selectNodes(nodes []models.ClusterNode, strategy string, fixed []uint, groups, tags []string) []models.ClusterNode {
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
	if strategy == "group" {
		wanted := normalizeSelectionValues(groups)
		out := make([]models.ClusterNode, 0)
		for _, n := range nodes {
			if matchesGroup(n.Group, wanted) {
				out = append(out, n)
			}
		}
		return out
	}
	if strategy == "tag" && len(tags) > 0 {
		wanted := normalizeSelectionValues(tags)
		out := make([]models.ClusterNode, 0)
		for _, n := range nodes {
			if matchesTags(n.Tags, wanted) {
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

func normalizeSelectionValues(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
	}
	return result
}

func matchesGroup(raw string, wanted []string) bool {
	group := strings.ToLower(strings.TrimSpace(raw))
	for _, value := range wanted {
		if group == strings.ToLower(value) {
			return true
		}
	}
	return false
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
