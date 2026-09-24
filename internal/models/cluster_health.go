package models

import "time"

// ClusterHealthResource keeps the latest observation for one managed resource.
// ResourceID is a Panel-local identifier, never a filesystem path or secret.
type ClusterHealthResource struct {
	ID                  uint64     `gorm:"primaryKey" json:"id"`
	NodeID              uint       `gorm:"uniqueIndex:idx_cluster_health_resource;not null" json:"nodeId"`
	ResourceType        string     `gorm:"size:24;uniqueIndex:idx_cluster_health_resource;not null" json:"resourceType"`
	ResourceID          string     `gorm:"size:64;uniqueIndex:idx_cluster_health_resource;not null" json:"resourceId"`
	Check               string     `gorm:"size:24;uniqueIndex:idx_cluster_health_resource;not null" json:"check"`
	Name                string     `gorm:"size:160;not null" json:"name"`
	Present             bool       `gorm:"index;not null;default:true" json:"present"`
	Target              string     `gorm:"size:253" json:"target,omitempty"`
	LocalStatus         string     `gorm:"size:16;not null" json:"localStatus"`
	LocalReason         string     `gorm:"size:64" json:"localReason,omitempty"`
	EntryStatus         string     `gorm:"size:16" json:"entryStatus,omitempty"`
	EntryReason         string     `gorm:"size:64" json:"entryReason,omitempty"`
	Status              string     `gorm:"size:16;index;not null" json:"status"`
	Reason              string     `gorm:"size:64" json:"reason,omitempty"`
	ObservedAt          *time.Time `gorm:"index" json:"observedAt,omitempty"`
	EntryCheckedAt      *time.Time `json:"entryCheckedAt,omitempty"`
	NotificationEnabled bool       `gorm:"not null;default:false" json:"notificationEnabled"`
	FailureCount        int        `gorm:"not null;default:0" json:"-"`
	RecoveryCount       int        `gorm:"not null;default:0" json:"-"`
	LastCountedAt       *time.Time `json:"-"`
	IncidentOpen        bool       `gorm:"not null;default:false" json:"incidentOpen"`
	IncidentStartedAt   *time.Time `json:"-"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}
