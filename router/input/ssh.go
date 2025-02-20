package input

type CommandRequest struct {
	Cmd string `json:"cmd" binding:"required"`
}
