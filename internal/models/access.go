package models

import "time"

type Role struct {
	ID          uint64    `json:"id" gorm:"primaryKey"`
	Code        string    `json:"code" gorm:"size:64;uniqueIndex;not null"`
	Name        string    `json:"name" gorm:"size:64;not null"`
	Description string    `json:"description" gorm:"size:255"`
	Builtin     bool      `json:"builtin" gorm:"not null;default:true"`
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

type UserRole struct {
	UserID     int64     `json:"userId" gorm:"primaryKey"`
	RoleID     uint64    `json:"roleId" gorm:"primaryKey"`
	AssignedAt time.Time `json:"assignedAt"`
}
