package output

// IptablesStatus 结构体表示iptables的状态
type IptablesStatus struct {
	Enabled     bool `json:"enabled"`     // iptables 是否开启
	PingBlocked bool `json:"pingBlocked"` // 是否禁ping
}

// IptablesRule 结构体表示单个iptables规则
type IptablesRule struct {
	Chain  string `json:"chain"`  // 规则所属的链
	Target string `json:"target"` // 目标（ACCEPT, DROP等）
	Proto  string `json:"proto"`  // 协议
	Source string `json:"source"` // 源IP
	Dest   string `json:"dest"`   // 目标IP
	Port   string `json:"port"`   // 端口

}
