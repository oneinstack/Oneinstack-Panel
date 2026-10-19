package panelupdate

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
)

func (m *Manager) AutomationUpdatePending(version, sha, channel string) bool {
	pending, exists, err := m.readPendingUpdate()
	return err == nil && exists && pending.ExpectedVersion == version && pending.ExpectedSHA256 == sha && pending.Channel == channel
}

// QueueAutomationUpdate selects the reviewed channel only for this pinned transaction.
// The independent systemd updater revalidates the signed version and digest.
func (m *Manager) QueueAutomationUpdate(ctx context.Context, version, sha, channel string) (CheckResult, error) {
	decoded, err := hex.DecodeString(sha)
	if err != nil || len(decoded) != 32 || (channel != "development" && channel != "stable") || (channel == "development" && !strings.Contains(version, "-retest.")) || (channel == "stable" && strings.Contains(version, "-")) {
		return CheckResult{}, ErrInvalidManifest
	}
	previous := m.config.Channel
	m.config.Channel = channel
	defer func() { m.config.Channel = previous }()
	if needed, err := m.NeedsRecovery(); err != nil {
		return CheckResult{}, err
	} else if needed {
		return CheckResult{}, ErrRecoveryNeeded
	}
	result, manifest, artifact, err := m.checkUpdate(ctx)
	if err != nil {
		return result, err
	}
	if !result.UpdateAvailable {
		return result, ErrNoUpdate
	}
	if manifest.Version != version || artifact.SHA256 != sha || !result.Compatible {
		return result, ErrTargetChanged
	}
	runner := OSCommandRunner{}
	if _, err := runner.Run(ctx, Command{Name: "systemctl", Args: []string{"is-active", "--quiet", "one-update.service"}}); err == nil {
		if m.AutomationUpdatePending(version, sha, channel) {
			return result, nil
		}
		return result, ErrUpdateBusy
	}
	if !m.AutomationUpdatePending(version, sha, channel) {
		if err = m.writePinnedPendingUpdate(pendingUpdate{ExpectedVersion: version, Channel: channel, ExpectedSHA256: sha, CreatedAt: m.now().UTC()}); err != nil {
			return result, err
		}
	}
	if _, err = runner.Run(ctx, Command{Name: "systemctl", Args: []string{"start", "--no-block", "one-update.service"}}); err != nil {
		// Retain the pinned request after an ambiguous start outcome. Never fall
		// back to the ordinary stable assignment on the independent process.
		return result, fmt.Errorf("%w: independent update start outcome unavailable", ErrUpdateStart)
	}
	return result, nil
}
