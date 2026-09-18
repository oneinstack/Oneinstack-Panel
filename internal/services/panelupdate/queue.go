package panelupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const pendingUpdateFile = "pending-update.json"

type pendingUpdate struct {
	ExpectedVersion string    `json:"expectedVersion"`
	CreatedAt       time.Time `json:"createdAt"`
}

// QueueApplicationUpdate validates and pins the Center-assigned target before
// handing the transaction to the independent updater service. The updater
// resolves Center again and refuses to continue if the assignment changed.
func (m *Manager) QueueApplicationUpdate(ctx context.Context, expectedVersion string) (CheckResult, error) {
	if needed, err := m.NeedsRecovery(); err != nil {
		return CheckResult{}, err
	} else if needed {
		return CheckResult{}, ErrRecoveryNeeded
	}

	result, err := m.Check(ctx)
	if err != nil {
		return result, err
	}
	if !result.UpdateAvailable {
		return result, ErrNoUpdate
	}
	if !result.Compatible {
		return result, fmt.Errorf("%w: assigned release is incompatible", ErrIncompatible)
	}
	target := strings.TrimSpace(expectedVersion)
	if target == "" {
		target = strings.TrimSpace(result.LatestVersion)
	}
	if canonicalVersion(target) == "" || target != strings.TrimSpace(result.LatestVersion) {
		return result, fmt.Errorf("%w: assigned release changed from %q to %q", ErrTargetChanged, expectedVersion, result.LatestVersion)
	}

	runner := OSCommandRunner{}
	if _, err := runner.Run(ctx, Command{
		Name: "systemctl", Args: []string{"is-active", "--quiet", "one-update.service"},
	}); err == nil {
		return result, ErrUpdateBusy
	}
	if err := m.writePendingUpdate(target); err != nil {
		if errors.Is(err, os.ErrExist) {
			return result, ErrUpdateBusy
		}
		return result, err
	}
	if _, err := runner.Run(ctx, Command{
		Name: "systemctl", Args: []string{"start", "--no-block", "one-update.service"},
	}); err != nil {
		_ = m.clearPendingUpdate()
		return result, fmt.Errorf("%w: %v", ErrUpdateStart, err)
	}
	return result, nil
}

func (m *Manager) pendingUpdatePath() string {
	return filepath.Join(m.updateRoot(), pendingUpdateFile)
}

func (m *Manager) writePendingUpdate(expectedVersion string) error {
	if err := os.MkdirAll(m.updateRoot(), 0700); err != nil {
		return err
	}
	content, err := json.Marshal(pendingUpdate{
		ExpectedVersion: expectedVersion,
		CreatedAt:       m.now().UTC(),
	})
	if err != nil {
		return err
	}
	content = append(content, '\n')
	file, err := os.OpenFile(m.pendingUpdatePath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(m.pendingUpdatePath())
		}
	}()
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	remove = false
	return nil
}

func (m *Manager) readPendingUpdate() (pendingUpdate, bool, error) {
	content, err := os.ReadFile(m.pendingUpdatePath())
	if errors.Is(err, os.ErrNotExist) {
		return pendingUpdate{}, false, nil
	}
	if err != nil {
		return pendingUpdate{}, false, err
	}
	var request pendingUpdate
	if err := json.Unmarshal(content, &request); err != nil {
		return pendingUpdate{}, true, fmt.Errorf("decode pending update: %w", err)
	}
	request.ExpectedVersion = strings.TrimSpace(request.ExpectedVersion)
	if canonicalVersion(request.ExpectedVersion) == "" {
		return pendingUpdate{}, true, fmt.Errorf("pending update target is invalid")
	}
	return request, true, nil
}

func (m *Manager) clearPendingUpdate() error {
	err := os.Remove(m.pendingUpdatePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
