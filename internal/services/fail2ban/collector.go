package fail2ban

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"oneinstack/internal/models"
	safeservice "oneinstack/internal/services/safe"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type detectorEvent struct {
	Jail       string `json:"jail"`
	IP         string `json:"ip"`
	Failures   int    `json:"failures"`
	ObservedAt int64  `json:"observedAt"`
}

func (m *Manager) runCollector(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	m.collect(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.collect(ctx)
		}
	}
}

func (m *Manager) collect(ctx context.Context) {
	status, err := m.service.Status(ctx)
	if err != nil || status == nil || !status.Installed || !status.ServiceActive {
		return
	}
	if err := m.service.SyncBanRecords(ctx); err != nil {
		log.Printf("fail2ban ban expiry synchronization failed: %v", err)
	}
	if err := m.expireBans(ctx); err != nil {
		log.Printf("fail2ban expired ban cleanup failed: %v", err)
	}
	if err := m.ensureMigrationTask(); err != nil {
		log.Printf("fail2ban legacy migration deferred: %v", err)
	}
	if err := m.collectAuditIncidents(ctx); err != nil {
		log.Printf("fail2ban audit incident collection failed: %v", err)
	}
	if err := m.collectDetectorEvents(ctx); err != nil {
		log.Printf("fail2ban detector event collection failed: %v", err)
	}
}

func (m *Manager) expireBans(ctx context.Context) error {
	var expired []models.Fail2banBan
	if err := m.db.Where("expires_at <= ?", time.Now().UTC()).Order("expires_at ASC").Find(&expired).Error; err != nil {
		return err
	}
	for _, ban := range expired {
		var active int64
		if err := m.db.Model(&models.Fail2banTask{}).
			Where("operation = ? AND policy_id = ? AND target_ip = ? AND status IN ?", "unban_ip", ban.PolicyID, ban.IP,
				[]string{models.Fail2banTaskQueued, models.Fail2banTaskRunning}).Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			continue
		}
		if err := m.submitExpiredUnban(ctx, ban); err != nil {
			log.Printf("fail2ban expired ban unban deferred for %s: %v", ban.IP, err)
		}
	}
	return nil
}

func ensureState(db *gorm.DB) (models.Fail2banState, error) {
	seed := models.Fail2banState{ID: 1, MigrationStatus: "pending"}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&seed).Error; err != nil {
		return models.Fail2banState{}, err
	}
	var state models.Fail2banState
	if err := db.First(&state, 1).Error; err != nil {
		return models.Fail2banState{}, err
	}
	return state, nil
}

func (m *Manager) ensureMigrationTask() error {
	state, err := ensureState(m.db)
	if err != nil {
		return err
	}
	if state.MigrationStatus != "pending" {
		return nil
	}
	_, err = m.SubmitMigration()
	return err
}

func (m *Manager) collectAuditIncidents(ctx context.Context) error {
	var policies []models.Fail2banPolicy
	if err := m.db.Where("enabled = ? AND template = ?", true, "panel-login").Find(&policies).Error; err != nil {
		return err
	}
	var maxSequence uint64
	for i := range policies {
		policy := &policies[i]
		var events []models.AuditEvent
		since := time.Now().UTC().Add(-time.Duration(policy.FindTimeSeconds) * time.Second)
		if err := m.db.Where("action = ? AND outcome = ? AND created_at >= ? AND remote_ip <> ''", "auth.login_failed", "failure", since).
			Order("sequence ASC").Limit(5000).Find(&events).Error; err != nil {
			return err
		}
		grouped := make(map[string][]models.AuditEvent)
		for _, event := range events {
			if event.Sequence > maxSequence {
				maxSequence = event.Sequence
			}
			ip := net.ParseIP(strings.TrimSpace(event.RemoteIP))
			if ip == nil || isProtectedIP(ip, "") {
				continue
			}
			grouped[ip.String()] = append(grouped[ip.String()], event)
		}
		for address, matches := range grouped {
			if len(matches) < policy.MaxRetry {
				continue
			}
			evidence := make([]uint64, 0, min(len(matches), 20))
			for _, event := range matches {
				if len(evidence) < 20 {
					evidence = append(evidence, event.ID)
				}
			}
			first, last := matches[0].CreatedAt.UTC(), matches[len(matches)-1].CreatedAt.UTC()
			if _, err := m.recordIncident(ctx, policy, "panel-login", address, len(matches), first, last, evidence); err != nil {
				return err
			}
		}
	}
	if maxSequence > 0 {
		if err := m.db.Model(&models.Fail2banState{}).Where("id = ?", 1).
			Update("audit_sequence", maxSequence).Error; err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) collectDetectorEvents(ctx context.Context) error {
	state, err := ensureState(m.db)
	if err != nil {
		return err
	}
	file, err := os.Open(eventSpoolPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	if state.EventFileOffset < 0 || state.EventFileOffset > stat.Size() {
		state.EventFileOffset = 0
	}
	if _, err := file.Seek(state.EventFileOffset, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReader(file)
	offset := state.EventFileOffset
	for processed := 0; processed < 100; processed++ {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if len(line) == 0 {
			break
		}
		if errors.Is(readErr, io.EOF) && line[len(line)-1] != '\n' {
			break
		}
		offset += int64(len(line))
		var event detectorEvent
		if err := json.Unmarshal(line, &event); err == nil {
			var policy models.Fail2banPolicy
			if err := m.db.First(&policy, "detector_jail = ? AND enabled = ?", event.Jail, true).Error; err == nil {
				ip := net.ParseIP(event.IP)
				if ip != nil && !isProtectedIP(ip, "") {
					observed := time.Unix(event.ObservedAt, 0).UTC()
					_, _ = m.recordIncident(ctx, &policy, policy.Template, ip.String(), event.Failures, observed, observed, nil)
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return m.db.Model(&models.Fail2banState{}).Where("id = ?", 1).Update("event_file_offset", offset).Error
}

func (m *Manager) recordIncident(ctx context.Context, policy *models.Fail2banPolicy, source, address string, attempts int, first, last time.Time, evidence []uint64) (*models.SecurityIncident, error) {
	bucket := last.Unix() / int64(policy.FindTimeSeconds)
	fingerprint := digest(policy.ID + "|" + address + "|" + source + "|" + strconv.FormatInt(bucket, 10))
	incident := &models.SecurityIncident{
		ID: uuid.NewString(), PolicyID: policy.ID, Source: source, RemoteIP: address,
		Fingerprint: fingerprint, Attempts: attempts, Severity: incidentSeverity(attempts, policy.MaxRetry),
		Status: "open", Evidence: evidence, FirstSeenAt: first, LastSeenAt: last,
	}
	err := m.db.Where("fingerprint = ?", fingerprint).FirstOrCreate(incident).Error
	if err != nil {
		return nil, err
	}
	if incident.Status == "open" && policy.EnforcementMode == "autoBan" && incident.TaskID == "" && m.allowAutoBan() {
		task, submitErr := m.SubmitBan("ban_ip", BanRequest{
			IncidentID: incident.ID, PolicyID: policy.ID, Reason: "规则达到阈值后自动封禁",
		}, 0, "", "system")
		if submitErr == nil {
			incident.TaskID = task.ID
			_ = m.db.Model(&models.SecurityIncident{}).Where("id = ? AND task_id = ''", incident.ID).Update("task_id", task.ID).Error
		}
	}
	return incident, nil
}

func (m *Manager) allowAutoBan() bool {
	m.banMu.Lock()
	defer m.banMu.Unlock()
	now := time.Now()
	if m.banSince.IsZero() || now.Sub(m.banSince) >= time.Minute {
		m.banSince, m.bansInWindow = now, 0
	}
	if m.bansInWindow >= 20 {
		return false
	}
	m.bansInWindow++
	return true
}

func (m *Manager) migrateLegacy(ctx context.Context) error {
	state, err := ensureState(m.db)
	if err != nil {
		return err
	}
	if state.MigrationStatus == "completed" || state.MigrationStatus == "not_required" {
		return nil
	}
	legacy := safeservice.NewDefaultService()
	config, err := legacy.GetAutoBlockConfig()
	if err != nil {
		return m.setMigrationError(err)
	}
	if !config.Enabled {
		return m.db.Model(&models.Fail2banState{}).Where("id = ?", 1).Updates(map[string]any{"migration_status": "not_required", "migration_error": ""}).Error
	}
	var policy models.Fail2banPolicy
	err = m.db.First(&policy, "template = ?", "sshd").Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		request := PolicyChangeRequest{Action: "create", Policy: PolicyInput{
			Template: "sshd", Name: "SSH 登录防护（由旧自动封禁迁移）", Enabled: true,
			EnforcementMode: "autoBan", MaxRetry: config.Threshold,
			FindTimeSeconds: config.WindowMinutes * 60, BanTimeSeconds: config.BanMinutes * 60,
		}}
		policyResult, applyErr := m.service.ApplyPolicyChange(ctx, request, 0)
		if applyErr != nil {
			return m.setMigrationError(applyErr)
		}
		policy = *policyResult
	} else if err != nil {
		return m.setMigrationError(err)
	}
	var rules []models.IptablesRule
	if err := m.db.Where("rule_type = ? AND state = ? AND (expires_at IS NULL OR expires_at > ?)", "auto_block", 1, time.Now()).Find(&rules).Error; err != nil {
		return m.setMigrationError(err)
	}
	for _, rule := range rules {
		request := BanRequest{PolicyID: policy.ID, IP: rule.IPs, Reason: "迁移旧 SSH 自动封禁规则"}
		if err := m.service.Ban(ctx, request, "migration"); err != nil {
			return m.setMigrationError(err)
		}
	}
	deleted := make([]models.IptablesRule, 0, len(rules))
	for _, rule := range rules {
		if err := legacy.Delete(ctx, rule.ID); err != nil {
			for i := range deleted {
				copy := deleted[i]
				copy.ID = 0
				_ = legacy.Add(ctx, &copy)
			}
			return m.setMigrationError(err)
		}
		deleted = append(deleted, rule)
	}
	config.Enabled = false
	now := time.Now().UTC()
	if err := m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.FirewallAutoBlockConfig{}).Where("id = ?", 1).Update("enabled", false).Error; err != nil {
			return err
		}
		return tx.Model(&models.Fail2banState{}).Where("id = ?", 1).Updates(map[string]any{
			"migration_status": "completed", "migration_error": "", "migrated_at": &now,
		}).Error
	}); err != nil {
		for i := range deleted {
			copy := deleted[i]
			copy.ID = 0
			_ = legacy.Add(ctx, &copy)
		}
		return m.setMigrationError(err)
	}
	return nil
}

func (m *Manager) setMigrationError(cause error) error {
	_ = m.db.Model(&models.Fail2banState{}).Where("id = ?", 1).Updates(map[string]any{
		"migration_status": "pending", "migration_error": cause.Error(),
	}).Error
	return cause
}

func incidentSeverity(attempts, threshold int) string {
	if attempts >= threshold*3 {
		return "critical"
	}
	if attempts >= threshold*2 {
		return "high"
	}
	return "medium"
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
