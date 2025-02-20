package models

import (
	"time"

	"gorm.io/gorm"
)

type Library struct {
	ID         int64     `json:"id"`
	PID        int64     `json:"pid"`
	Name       string    `json:"name"`
	User       string    `json:"user"`
	Password   string    `json:"password"`
	Capacity   string    `json:"capacity"`
	PAddr      string    `json:"p_addr"`
	Type       string    `json:"type"`
	CreateTime time.Time `json:"create_time"`
}

func (m *Library) BeforeCreate(tx *gorm.DB) (err error) {
	m.CreateTime = time.Now()
	return
}
