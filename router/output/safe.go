package output

// IptablesStatus 结构体表示iptables的状态
type IptablesStatus struct {
	Install            bool   `json:"install"`
	Enabled            bool   `json:"enabled"`
	PingBlocked        bool   `json:"pingBlocked"`
	Backend            string `json:"backend"`
	RuntimeBackend     string `json:"runtimeBackend,omitempty"`
	ManagedBackend     string `json:"managedBackend,omitempty"`
	Persistent         bool   `json:"persistent"`
	CanToggle          bool   `json:"canToggle"`
	RepairRequired     bool   `json:"repairRequired"`
	Warning            string `json:"warning,omitempty"`
	PanelPort          int    `json:"panelPort"`
	PanelPortProtected bool   `json:"panelPortProtected"`
	ManagedPanelRule   bool   `json:"managedPanelRule"`
	ManagedRuleCount   int64  `json:"managedRuleCount"`
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
