package panelupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func (m *Manager) Status() (Status, error) {
	content, err := os.ReadFile(m.statusPath())
	if errors.Is(err, os.ErrNotExist) {
		status := Status{
			State: StateIdle, CurrentVersion: m.config.CurrentVersion,
			UpdatedAt: m.now().UTC(),
		}
		return m.withRecoveryState(status)
	}
	if err != nil {
		return Status{}, err
	}
	var status Status
	if err := json.Unmarshal(content, &status); err != nil {
		return Status{}, fmt.Errorf("decode update status: %w", err)
	}
	return m.withRecoveryState(status)
}

func (m *Manager) NeedsRecovery() (bool, error) {
	if _, err := os.Stat(m.journalPath()); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	lock, err := m.acquireLock()
	if errors.Is(err, ErrUpdateBusy) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	releaseLock(lock)
	return true, nil
}

func (m *Manager) withRecoveryState(status Status) (Status, error) {
	needed, err := m.NeedsRecovery()
	if err != nil {
		return Status{}, fmt.Errorf("inspect interrupted update: %w", err)
	}
	if needed {
		status.State = StateRecoveryNeeded
		status.Message = "检测到中断的更新事务，请执行 one update rollback --yes"
		status.UpdatedAt = m.now().UTC()
	}
	return status, nil
}

func (m *Manager) writeStatus(status Status) error {
	status.UpdatedAt = m.now().UTC()
	content, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	path := m.statusPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".status-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func (m *Manager) transition(status *Status, state, message string) error {
	status.State = state
	status.Message = message
	return m.writeStatus(*status)
}

func timePointer(value time.Time) *time.Time {
	return &value
}
