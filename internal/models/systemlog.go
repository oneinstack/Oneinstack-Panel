package models

import (
	"gorm.io/gorm"
	"time"
)

const (
	// 登录日志
	Login_Type = 1
	// 操作日志
	Operate_Type = 2
	// 错误日志
	Error_Type = 3
	// 定时任务日志
	Task_Type = 4
)

type SystemLog struct {
	ID         int64     `json:"id"`
	LogType    int64     `json:"log_type"`  // 日志类型 1:登录日志 2:操作日志 3:错误日志 4:定时任务日志
	LogInfo    int64     `json:"log_info"`  // 日志结果 0:失败 1:成功
	Content    string    `json:"content"`   // 日志内容
	IP         string    `json:"ip"`        // 操作IP
	Agent      string    `json:"agent"`     // 浏览器
	UserName   string    `json:"user_name"` // 用户ID
	CreateTime time.Time `json:"create_time"`
}

func (m *SystemLog) BeforeCreate(tx *gorm.DB) (err error) {
	m.CreateTime = time.Now()
	return
}
