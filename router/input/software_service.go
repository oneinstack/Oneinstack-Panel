package input

type SoftwareServiceAction struct {
	Action string `json:"action" binding:"required"`
}

type SoftwareServiceConfiguration struct {
	Revision string            `json:"revision" binding:"required"`
	Values   map[string]string `json:"values" binding:"required"`
}
