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
	Strategy       string   `json:"strategy,omitempty"` // fixed, tag, least_load
	NodeIDs        []uint   `json:"nodeIds,omitempty"`
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

func (m *Manager) DispatchWebsite(input WebsiteDispatchInput) (WebsiteDispatchResult, error) {
	if input.WebsiteID <= 0 {
		return WebsiteDispatchResult{}, errors.New("website id is required")
	}
	var site models.Website
	if err := m.db.First(&site, input.WebsiteID).Error; err != nil {
		return WebsiteDispatchResult{}, err
	}
	var nodes []models.ClusterNode
	if err := m.db.Where("enabled = ? AND status = ? AND last_seen_at >= ?", true, models.ClusterNodeStatusOnline, time.Now().Add(-2*time.Minute)).Find(&nodes).Error; err != nil {
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
	settingsDocument, err := websiteService.GetSettings(site.ID)
	if err != nil {
		return WebsiteDispatchResult{}, err
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
