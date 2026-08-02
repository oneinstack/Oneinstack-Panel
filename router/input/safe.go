package input

type IptablesRuleParam struct {
	Q         string `json:"q"`
	ID        int64  `json:"id"`
	Direction string `json:"direction"`
	RuleType  string `json:"ruleType"`
	State     *int   `json:"state"`
	Page
}

type FirewallToggleParam struct {
	Enabled bool   `json:"enabled"`
	Confirm string `json:"confirm"`
}

type FirewallPingParam struct {
	Blocked bool `json:"blocked"`
}

type FirewallRuleStateParam struct {
	ID      int64 `json:"id" binding:"required"`
	Enabled bool  `json:"enabled"`
}

type FirewallRuleBatchParam struct {
	IDs    []int64 `json:"ids" binding:"required"`
	Action string  `json:"action" binding:"required"`
}

type FirewallRuleImportParam struct {
	Rules []FirewallRuleImportItem `json:"rules" binding:"required"`
}

type FirewallRuleImportItem struct {
	RuleType  string  `json:"ruleType"`
	Direction string  `json:"direction"`
	Protocol  string  `json:"protocol"`
	Strategy  string  `json:"strategy"`
	IPs       string  `json:"ips"`
	Ports     string  `json:"ports"`
	State     int     `json:"state"`
	Remark    string  `json:"remark"`
	Location  string  `json:"location"`
	ExpiresAt *string `json:"expiresAt"`
}

type FirewallPortForwardParam struct {
	Q     string `json:"q"`
	State *int   `json:"state"`
	Page
}

type FirewallPortForwardStateParam struct {
	ID      int64 `json:"id" binding:"required"`
	Enabled bool  `json:"enabled"`
}

type FirewallAutoBlockParam struct {
	Enabled       bool `json:"enabled"`
	Threshold     int  `json:"threshold"`
	WindowMinutes int  `json:"windowMinutes"`
	BanMinutes    int  `json:"banMinutes"`
}
