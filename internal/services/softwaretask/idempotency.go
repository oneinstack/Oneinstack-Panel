package softwaretask

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"oneinstack/internal/models"
)

func firstIdempotencyKey(keys []string) string {
	if len(keys) > 0 {
		return keys[0]
	}
	return ""
}

func (m *Manager) findIdempotentTask(request InstallRequest, actor int64) (*models.SoftwareTask, error) {
	if request.IdempotencyKey == "" {
		return nil, nil
	}
	if raw, err := hex.DecodeString(request.IdempotencyKey); err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("invalid task idempotency digest")
	}
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("oneinstack-retest:"+request.IdempotencyKey)).String()
	var task models.SoftwareTask
	err := m.db.First(&task, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if actor <= 0 || task.RequestedBy != actor || task.SoftwareKey != request.Key || task.RequestedVersion != request.Version ||
		request.ResolvedPackage == nil || task.PackageSHA256 != request.ResolvedPackage.PackageSHA256 {
		return nil, fmt.Errorf("idempotent task identity mismatch")
	}
	return &task, nil
}
