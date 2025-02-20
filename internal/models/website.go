package models

import (
	"gorm.io/gorm"
	"time"
)

type Website struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Domain     string    `json:"domain"`
	Dir        string    `json:"dir"`
	Remark     string    `json:"remark"`
	RootDir    string    `json:"root_dir"`
	TarUrl     string    `json:"tar_url"`
	SendUrl    string    `json:"send_url"`
	Class      string    `json:"class"`
	Type       string    `json:"type"`
	CreateTime time.Time `json:"create_time"`
	UpdateTime time.Time `json:"update_time"`
}

func (m *Website) BeforeCreate(tx *gorm.DB) (err error) {
	m.CreateTime = time.Now()
	return
}

func (m *Website) BeforeUpdate(tx *gorm.DB) (err error) {
	m.UpdateTime = time.Now()
	return
}
