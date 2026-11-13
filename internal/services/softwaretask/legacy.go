package softwaretask

import (
	"context"
	"errors"

	"oneinstack/internal/models"
	"oneinstack/internal/services/legacyoneinstack"

	"gorm.io/gorm"
)

// SubmitLegacyImport uses the same durable queue, cancellation, events and
// component lock as other operations. A changed host produces a new task.
func (m *Manager) SubmitLegacyImport(ctx context.Context, sourceDir string, actor int64) (*models.SoftwareTask, error) {
	report, err := legacyoneinstack.Scan(ctx, sourceDir)
	if err != nil {
		return nil, err
	}
	if len(report.Components) == 0 && len(report.Sites) == 0 {
		return nil, nil
	}
	var previous models.SoftwareTask
	err = m.db.Where("operation = ? AND component = ? AND release_revision = ? AND status = ?", "adopt", legacyoneinstack.TaskComponent, report.Revision, models.SoftwareTaskStatusSucceeded).Order("created_at DESC").First(&previous).Error
	if err == nil {
		return &previous, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return m.submit(InstallRequest{Operation: "adopt", Key: legacyoneinstack.TaskComponent, Version: "existing", Parameters: map[string]string{"source-dir": report.SourceDir, "source-revision": report.Revision}}, actor)
}

func (m *Manager) activeRuntimeGroupOwners(ctx context.Context, group, exclude string) []RuntimeGroupOwner {
	owners := activeRuntimeGroupOwners(ctx, group, exclude)
	row, err := legacyoneinstack.Installed(m.db, exclude)
	if err != nil || row == nil {
		return owners
	}
	filtered := make([]RuntimeGroupOwner, 0, len(owners))
	for _, owner := range owners {
		if owner.ServiceName != row.ServiceName && !legacyoneinstack.IsProxyPeerService(m.db, row.Component, owner.ServiceName) {
			filtered = append(filtered, owner)
		}
	}
	return filtered
}
