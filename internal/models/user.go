package models

import (
	"gorm.io/gorm"
	"time"
)

type User struct {
	ID         int64     `json:"id"`
	Username   string    `json:"username"`
	Password   string    `json:"password"`
	IsAdmin    bool      `json:"is_admin"`
	CreateTime time.Time `json:"create_time"`
}

func (m *User) BeforeCreate(tx *gorm.DB) (err error) {
	m.CreateTime = time.Now()
	return
}
