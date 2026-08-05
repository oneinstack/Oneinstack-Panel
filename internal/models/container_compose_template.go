package models

import "time"

// ContainerComposeTemplate stores a reusable Compose/YAML document. The
// document is returned only by the detail endpoint, not in list responses.
type ContainerComposeTemplate struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:120;not null;uniqueIndex" json:"name"`
	Description string    `gorm:"size:255" json:"description,omitempty"`
	Content     string    `gorm:"type:text;not null" json:"-"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
