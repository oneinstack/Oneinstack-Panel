package input

type SoftwareServiceAction struct {
	Action       string `json:"action" binding:"required"`
	Switch       bool   `json:"switch,omitempty"`
	Confirmation string `json:"confirmation,omitempty"`
}

type SoftwareServiceConfiguration struct {
	Revision     string            `json:"revision" binding:"required"`
	Values       map[string]string `json:"values" binding:"required"`
	Confirmation string            `json:"confirmation,omitempty"`
}
