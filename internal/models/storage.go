package models

import (
	"gorm.io/gorm"
	"time"
)

type Storage struct {
	ID         int64     `json:"id"`
	Addr       string    `json:"addr"`
	Port       string    `json:"port"`
	Root       string    `json:"root"`
	Password   string    `json:"password"`
	Remark     string    `json:"remark"`
	Type       string    `json:"type"`
	CreateTime time.Time `json:"create_time"`
	UpdateTime time.Time `json:"update_time"`
}

func (m *Storage) BeforeCreate(tx *gorm.DB) (err error) {
	m.CreateTime = time.Now()
	return
}

func (m *Storage) BeforeUpdate(tx *gorm.DB) (err error) {
	m.UpdateTime = time.Now()
	return
}
