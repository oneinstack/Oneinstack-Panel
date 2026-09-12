package website

import (
	"context"
	"errors"
	"strings"

	"oneinstack/internal/models"

	"gorm.io/gorm"
)

// SyncClusterWebsite applies a website snapshot received from another Panel.
// IDs are deliberately not trusted across nodes; websites are matched by
// name/domain and created with a local ID when absent. All rendering and Web
// server validation still goes through the normal website service.
func SyncClusterWebsite(ctx context.Context, snapshot models.Website, settings *WebsiteSettings) (models.Website, error) {
	service, err := defaultService()
	if err != nil {
		return models.Website{}, err
	}
	if strings.TrimSpace(snapshot.Name) == "" && strings.TrimSpace(snapshot.Domain) == "" {
		return models.Website{}, errors.New("website snapshot requires name or domain")
	}

	var existing models.Website
	query := service.DB.Where("name = ? OR domain = ?", strings.TrimSpace(snapshot.Name), strings.TrimSpace(snapshot.Domain))
	findErr := query.First(&existing).Error
	if errors.Is(findErr, gorm.ErrRecordNotFound) {
		copy := snapshot
		copy.ID = 0
		// Root paths belong to the source node. Let the normal create path map
		// the site into this node's managed Web root.
		copy.RootDir = ""
		copy.Dir = ""
		if err := service.Add(ctx, &copy); err != nil {
			return models.Website{}, err
		}
		if settings != nil {
			if _, err := service.UpdateSettings(ctx, copy.ID, *settings); err != nil {
				return models.Website{}, err
			}
		}
		if !snapshot.Enabled {
			if _, err := service.SetEnabled(ctx, copy.ID, false); err != nil {
				return models.Website{}, err
			}
			copy.Enabled = false
		}
		return copy, nil
	}
	if findErr != nil {
		return models.Website{}, findErr
	}

	copy := snapshot
	copy.ID = existing.ID
	copy.RootDir = existing.RootDir
	copy.Dir = existing.Dir
	copy.Engine = existing.Engine
	if err := service.Update(ctx, &copy); err != nil {
		return models.Website{}, err
	}
	if settings != nil {
		if _, err := service.UpdateSettings(ctx, copy.ID, *settings); err != nil {
			return models.Website{}, err
		}
	}
	if snapshot.Enabled != existing.Enabled {
		updated, err := service.SetEnabled(ctx, copy.ID, snapshot.Enabled)
		if err != nil {
			return models.Website{}, err
		}
		copy = *updated
	}
	return copy, nil
}
