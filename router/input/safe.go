package input

type IptablesRuleParam struct {
	Q         string `json:"q"`
	ID        int64  `json:"id"`
	Direction string `json:"direction"`
	Page
}

type FirewallToggleParam struct {
	Enabled bool   `json:"enabled"`
	Confirm string `json:"confirm"`
}

type FirewallPingParam struct {
	Blocked bool `json:"blocked"`
}
