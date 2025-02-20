package input

type IptablesRuleParam struct {
	Q         string `json:"q"`
	ID        int64  `json:"id"`
	Direction string `json:"direction"`
	Page
}
