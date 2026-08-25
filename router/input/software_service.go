package input

type SoftwareServiceAction struct {
	Action string `json:"action" binding:"required"`
	Switch bool   `json:"switch,omitempty"`
}

type SoftwareServiceConfiguration struct {
	Revision string            `json:"revision" binding:"required"`
	Values   map[string]string `json:"values" binding:"required"`
}
