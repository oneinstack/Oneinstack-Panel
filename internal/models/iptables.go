package models

import "time"

type IptablesRule struct {
	ID         int64     `json:"id"`
	Direction  string    `json:"direction"` // 规则方向 "in" 或 "out"
	Protocol   string    `json:"protocol"`  // 协议类型 "tcp", "udp", "icmp"
	Strategy   string    `json:"strategy"`  // 策略类型 "allow", "deny"
	IPs        string    `json:"ips"`       // 支持多个 IP 地址或子网
	Ports      string    `json:"ports"`     // 支持多个端口
	State      int       `json:"state"`     // 状态
	Remark     string    `json:"remark"`    // 备注
	CreateTime time.Time `json:"create_time"`
}
