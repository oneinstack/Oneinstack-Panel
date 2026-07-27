package models

import "time"

type IptablesRule struct {
	ID         int64     `json:"id"`
	Direction  string    `json:"direction"` // "in" 或 "out"
	Protocol   string    `json:"protocol"`  // "tcp"、"udp" 或 "icmp"
	Strategy   string    `json:"strategy"`  // "allow" 或 "deny"
	IPs        string    `json:"ips"`       // 逗号分隔的 IPv4/CIDR
	Ports      string    `json:"ports"`     // 逗号分隔的端口/端口范围
	State      int       `json:"state"`     // 1 表示启用
	Remark     string    `json:"remark"`
	Backend    string    `json:"backend" gorm:"size:16;index"` // ufw/firewalld/iptables
	Token      string    `json:"-" gorm:"size:64;index"`       // 系统规则的稳定标识
	Protected  bool      `json:"protected" gorm:"index"`       // 系统保护规则不可编辑或删除
	CreateTime time.Time `json:"create_time" gorm:"column:create_time;autoCreateTime"`
	UpdateTime time.Time `json:"update_time" gorm:"column:update_time;autoUpdateTime"`
}
