package audit

import (
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"oneinstack/internal/models"

	"gorm.io/gorm"
)

type checkpointPayload struct {
	ThroughSequence  uint64 `json:"throughSequence"`
	ThroughEntryHash string `json:"throughEntryHash"`
}

type statePayload struct {
	LastSequence  uint64 `json:"lastSequence"`
	LastEntryHash string `json:"lastEntryHash"`
}

func (manager *Manager) Verify() (*VerificationResult, error) {
	if manager == nil || manager.db == nil {
		return nil, errors.New("audit manager is not configured")
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.verifyLocked(manager.db)
}

func (manager *Manager) verifyLocked(database *gorm.DB) (*VerificationResult, error) {
	result := &VerificationResult{Valid: true, Message: "审计链完整"}
	expectedSequence := uint64(1)
	expectedPrevious := genesisHash

	var checkpoint models.AuditCheckpoint
	err := database.First(&checkpoint, 1).Error
	switch {
	case err == nil:
		valid, verifyErr := manager.verifyCheckpoint(&checkpoint)
		if verifyErr != nil {
			return nil, verifyErr
		}
		if !valid {
			result.Valid = false
			result.Message = "审计保留检查点签名无效"
			result.InvalidSequence = checkpoint.ThroughSequence
			return result, nil
		}
		result.CheckpointSequence = checkpoint.ThroughSequence
		expectedSequence = checkpoint.ThroughSequence + 1
		expectedPrevious = checkpoint.ThroughEntryHash
	case errors.Is(err, gorm.ErrRecordNotFound):
	default:
		return nil, fmt.Errorf("read audit checkpoint: %w", err)
	}

	rows, err := database.Model(&models.AuditEvent{}).Order("sequence ASC").Rows()
	if err != nil {
		return nil, fmt.Errorf("scan audit chain: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var event models.AuditEvent
		if err := database.ScanRows(rows, &event); err != nil {
			return nil, fmt.Errorf("decode audit event: %w", err)
		}
		result.CheckedEntries++
		if result.FirstSequence == 0 {
			result.FirstSequence = event.Sequence
		}
		result.LastSequence = event.Sequence
		if event.Sequence != expectedSequence || event.PreviousHash != expectedPrevious ||
			event.ChainVersion != ChainVersion {
			result.Valid = false
			result.InvalidSequence = event.Sequence
			result.Message = "审计链序号、前置摘要或版本不连续"
			return result, nil
		}
		expectedHash, err := manager.signEvent(&event)
		if err != nil {
			return nil, err
		}
		if !hmac.Equal([]byte(expectedHash), []byte(event.EntryHash)) {
			result.Valid = false
			result.InvalidSequence = event.Sequence
			result.Message = "审计记录签名无效"
			return result, nil
		}
		expectedSequence++
		expectedPrevious = event.EntryHash
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit chain: %w", err)
	}
	logicalHeadSequence := expectedSequence - 1
	logicalHeadHash := expectedPrevious
	if logicalHeadSequence == 0 {
		logicalHeadHash = genesisHash
	}
	var state models.AuditChainState
	err = database.First(&state, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if logicalHeadSequence > 0 {
			result.Valid = false
			result.InvalidSequence = logicalHeadSequence
			result.Message = "审计链头状态缺失，可能存在尾部删除"
		}
		return result, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read audit chain state: %w", err)
	}
	validState, err := manager.verifyState(&state)
	if err != nil {
		return nil, err
	}
	if !validState || state.LastSequence != logicalHeadSequence || state.LastEntryHash != logicalHeadHash {
		result.Valid = false
		result.InvalidSequence = logicalHeadSequence
		result.Message = "审计链头状态无效或与当前尾部不一致"
	}
	return result, nil
}

func (manager *Manager) CleanupBefore(cutoff time.Time) (*CleanupResult, error) {
	if manager == nil || manager.db == nil {
		return nil, errors.New("audit manager is not configured")
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()

	verification, err := manager.verifyLocked(manager.db)
	if err != nil {
		return nil, err
	}
	if !verification.Valid {
		return nil, fmt.Errorf("refuse audit cleanup: %s", verification.Message)
	}
	result := &CleanupResult{
		RetentionCutoff:      cutoff.UTC(),
		IntegrityCheckPassed: true,
	}
	var lastExpired models.AuditEvent
	err = manager.db.Where("created_at < ?", cutoff.UTC()).
		Order("sequence DESC").
		First(&lastExpired).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		result.CheckpointSequence = verification.CheckpointSequence
		return result, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find expired audit events: %w", err)
	}

	checkpoint := models.AuditCheckpoint{
		ID:               1,
		ThroughSequence:  lastExpired.Sequence,
		ThroughEntryHash: lastExpired.EntryHash,
		UpdatedAt:        manager.now().UTC(),
	}
	checkpoint.Signature, err = manager.signCheckpoint(&checkpoint)
	if err != nil {
		return nil, err
	}
	err = manager.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&checkpoint).Error; err != nil {
			return fmt.Errorf("save audit checkpoint: %w", err)
		}
		deletion := tx.Exec("DELETE FROM audit_events WHERE sequence <= ?", lastExpired.Sequence)
		if deletion.Error != nil {
			return fmt.Errorf("delete expired audit events: %w", deletion.Error)
		}
		result.DeletedEntries = deletion.RowsAffected
		return nil
	})
	if err != nil {
		return nil, err
	}
	result.CheckpointSequence = checkpoint.ThroughSequence
	result.CheckpointEntryHash = checkpoint.ThroughEntryHash
	return result, nil
}

func (manager *Manager) signCheckpoint(checkpoint *models.AuditCheckpoint) (string, error) {
	payload, err := json.Marshal(checkpointPayload{
		ThroughSequence:  checkpoint.ThroughSequence,
		ThroughEntryHash: checkpoint.ThroughEntryHash,
	})
	if err != nil {
		return "", fmt.Errorf("encode audit checkpoint: %w", err)
	}
	return manager.sign(payload), nil
}

func (manager *Manager) verifyCheckpoint(checkpoint *models.AuditCheckpoint) (bool, error) {
	if checkpoint.ThroughSequence == 0 || checkpoint.ThroughEntryHash == "" || checkpoint.Signature == "" {
		return false, nil
	}
	expected, err := manager.signCheckpoint(checkpoint)
	if err != nil {
		return false, err
	}
	return hmac.Equal([]byte(expected), []byte(checkpoint.Signature)), nil
}

func (manager *Manager) signState(state *models.AuditChainState) (string, error) {
	payload, err := json.Marshal(statePayload{
		LastSequence:  state.LastSequence,
		LastEntryHash: state.LastEntryHash,
	})
	if err != nil {
		return "", fmt.Errorf("encode audit chain state: %w", err)
	}
	return manager.sign(payload), nil
}

func (manager *Manager) verifyState(state *models.AuditChainState) (bool, error) {
	if state.LastSequence == 0 || state.LastEntryHash == "" || state.Signature == "" {
		return false, nil
	}
	expected, err := manager.signState(state)
	if err != nil {
		return false, err
	}
	return hmac.Equal([]byte(expected), []byte(state.Signature)), nil
}

func (manager *Manager) verifyStoredHead(database *gorm.DB, sequence uint64, entryHash string) error {
	var state models.AuditChainState
	err := database.First(&state, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if sequence == 0 {
			return nil
		}
		return errors.New("audit chain head state is missing")
	}
	if err != nil {
		return fmt.Errorf("read audit chain state: %w", err)
	}
	valid, err := manager.verifyState(&state)
	if err != nil {
		return err
	}
	if !valid || state.LastSequence != sequence || state.LastEntryHash != entryHash {
		return errors.New("audit chain head state is invalid")
	}
	return nil
}
