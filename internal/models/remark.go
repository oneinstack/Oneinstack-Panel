package models

import (
	"gorm.io/gorm"
	"time"
)

type Remark struct {
	ID         int64     `json:"id"`
	Content    string    `json:"content"`
	CreateTime time.Time `json:"create_time" json:"create_time"`
}

func (m *Remark) BeforeCreate(tx *gorm.DB) (err error) {
	m.CreateTime = time.Now()
	return
}
