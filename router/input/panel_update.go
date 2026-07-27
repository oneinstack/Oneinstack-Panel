package input

type ApplyPanelUpdateRequest struct {
	Confirm string `json:"confirm" binding:"required"`
}
