package models

import (
	"time"

	"gorm.io/gorm"
)

const (
	PasswordChangeReasonInitial    = "initial"
	PasswordChangeReasonAdminReset = "admin_reset"
)

type User struct {
	ID                 int64  `json:"id"`
	Username           string `json:"username"`
	Password           string `json:"-"`
	IsAdmin            bool   `json:"is_admin"`
	FirstJoin          bool   `json:"first_join"`
	MustChangePassword bool   `json:"must_change_password"`
	// PasswordChangeReason is meaningful only while MustChangePassword is true.
	// An empty value means the provenance is unavailable for legacy records.
	PasswordChangeReason string    `json:"-"`
	SecurityVersion      uint64    `json:"-"`
	CreateTime           time.Time `json:"create_time"`
}

func (m *User) BeforeCreate(tx *gorm.DB) (err error) {
	m.CreateTime = time.Now()
	if m.MustChangePassword && m.PasswordChangeReason == "" {
		m.PasswordChangeReason = PasswordChangeReasonInitial
	}
	if !m.MustChangePassword {
		m.PasswordChangeReason = ""
	}
	if m.SecurityVersion == 0 {
		m.SecurityVersion = 1
	}
	return
}

// EffectiveSecurityVersion keeps users created before the session-security
// migration compatible while still making version zero unusable as a bypass.
func (m User) EffectiveSecurityVersion() uint64 {
	if m.SecurityVersion == 0 {
		return 1
	}
	return m.SecurityVersion
}
