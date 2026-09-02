package models

import "time"

type Role struct {
	ID          uint64    `json:"id" gorm:"primaryKey"`
	Code        string    `json:"code" gorm:"size:64;uniqueIndex;not null"`
	Name        string    `json:"name" gorm:"size:64;not null"`
	Description string    `json:"description" gorm:"size:255"`
	Builtin     bool      `json:"builtin" gorm:"not null;default:false"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type Permission struct {
	ID          uint64    `json:"id" gorm:"primaryKey"`
	Code        string    `json:"code" gorm:"size:96;uniqueIndex;not null"`
	Name        string    `json:"name" gorm:"size:96;not null"`
	Description string    `json:"description" gorm:"size:255"`
	Module      string    `json:"module" gorm:"size:64;index"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type RolePermission struct {
	RoleID       uint64    `json:"roleId" gorm:"primaryKey"`
	PermissionID uint64    `json:"permissionId" gorm:"primaryKey"`
	CreatedAt    time.Time `json:"createdAt"`
}

const (
	MenuTypeDirectory = "directory"
	MenuTypePage      = "page"
	MenuTypeButton    = "button"

	MenuTargetRoute  = "route"
	MenuTargetAction = "action"
)

// Menu is a backend-owned navigation and action descriptor. TargetKey is
// always resolved against the registered route/action catalog; it is never
// evaluated as code or rendered as raw HTML.
type Menu struct {
	ID             uint64    `json:"id" gorm:"primaryKey"`
	Key            string    `json:"key" gorm:"size:96;uniqueIndex;not null"`
	ParentID       *uint64   `json:"parentId" gorm:"index"`
	Type           string    `json:"type" gorm:"size:16;index;not null"`
	Name           string    `json:"name" gorm:"size:96;not null"`
	NameEn         string    `json:"nameEn" gorm:"column:name_en;size:96"`
	TargetType     string    `json:"targetType" gorm:"size:16"`
	TargetKey      string    `json:"targetKey" gorm:"size:128"`
	IconKey        string    `json:"iconKey" gorm:"size:64"`
	Sort           int       `json:"sort" gorm:"index;not null;default:0"`
	Enabled        bool      `json:"enabled" gorm:"not null"`
	Builtin        bool      `json:"builtin" gorm:"not null;default:false"`
	SuperAdminOnly bool      `json:"superAdminOnly" gorm:"not null;default:false"`
	FeatureKey     string    `json:"featureKey" gorm:"size:32"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type MenuPermission struct {
	MenuID       uint64    `json:"menuId" gorm:"primaryKey"`
	PermissionID uint64    `json:"permissionId" gorm:"primaryKey"`
	CreatedAt    time.Time `json:"createdAt"`
}

type UserRole struct {
	UserID     int64     `json:"userId" gorm:"primaryKey"`
	RoleID     uint64    `json:"roleId" gorm:"primaryKey"`
	AssignedAt time.Time `json:"assignedAt"`
}
