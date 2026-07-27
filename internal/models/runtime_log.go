package models

import "time"

// RuntimeLogEntry stores operational output from the Panel process. It is
// intentionally separate from the tamper-evident audit trail: runtime logs are
// diagnostic and time-retained, while audit records describe security actions.
type RuntimeLogEntry struct {
	ID         uint64    `gorm:"primaryKey" json:"id"`
	OccurredAt time.Time `gorm:"index;not null" json:"occurredAt"`
	Level      string    `gorm:"size:16;index;not null" json:"level"`
	Source     string    `gorm:"size:64;index;not null" json:"source"`
	Message    string    `gorm:"type:text;not null" json:"message"`
}
