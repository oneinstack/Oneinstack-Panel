package models

import "time"

type IptablesRule struct {
	ID         int64      `json:"id"`
	RuleType   string     `json:"ruleType" gorm:"size:24;default:port;index"` // port/ip/region/auto_block
	Direction  string     `json:"direction"`                                  // "in" 或 "out"
	Protocol   string     `json:"protocol"`                                   // "tcp"、"udp"、"icmp" 或 "all"
	Strategy   string     `json:"strategy"`                                   // "allow" 或 "deny"
	IPs        string     `json:"ips"`                                        // 逗号分隔的 IPv4/CIDR
	Ports      string     `json:"ports"`                                      // 逗号分隔的端口/端口范围
	State      int        `json:"state"`                                      // 1 表示启用
	Remark     string     `json:"remark"`
	Location   string     `json:"location" gorm:"size:128"`
	ExpiresAt  *time.Time `json:"expiresAt" gorm:"index"`
	Backend    string     `json:"backend" gorm:"size:16;index"` // ufw/firewalld/iptables
	Token      string     `json:"-" gorm:"size:64;index"`       // 系统规则的稳定标识
	Protected  bool       `json:"protected" gorm:"index"`       // 系统保护规则不可编辑或删除
	CreateTime time.Time  `json:"create_time" gorm:"column:create_time;autoCreateTime"`
	UpdateTime time.Time  `json:"update_time" gorm:"column:update_time;autoUpdateTime"`
}

// FirewallPortForward 保存由面板管理的 firewalld 端口转发规则。
type FirewallPortForward struct {
	ID              int64     `json:"id"`
	Protocol        string    `json:"protocol" gorm:"size:8;index"`
	SourcePort      int       `json:"sourcePort"`
	DestinationIP   string    `json:"destinationIp" gorm:"size:64"`
	DestinationPort int       `json:"destinationPort"`
	State           int       `json:"state" gorm:"index"`
	Remark          string    `json:"remark" gorm:"size:200"`
	Backend         string    `json:"backend" gorm:"size:16"`
	CreateTime      time.Time `json:"create_time" gorm:"column:create_time;autoCreateTime"`
	UpdateTime      time.Time `json:"update_time" gorm:"column:update_time;autoUpdateTime"`
}

// FirewallAutoBlockConfig 控制基于 SSH 失败登录日志的恶意 IP 自动封禁。
// 默认关闭，只有管理员显式开启后维护任务才会读取日志并生成 auto_block 规则。
type FirewallAutoBlockConfig struct {
	ID            uint       `json:"id" gorm:"primaryKey"`
	Enabled       bool       `json:"enabled"`
	Threshold     int        `json:"threshold"`
	WindowMinutes int        `json:"windowMinutes"`
	BanMinutes    int        `json:"banMinutes"`
	LastRunAt     *time.Time `json:"lastRunAt"`
	CreateTime    time.Time  `json:"create_time" gorm:"column:create_time;autoCreateTime"`
	UpdateTime    time.Time  `json:"update_time" gorm:"column:update_time;autoUpdateTime"`
}
