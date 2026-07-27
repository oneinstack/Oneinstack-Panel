package input

type CronParam struct {
	Page
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	StartAt string `json:"start_at"`
	EndAt   string `json:"end_at"`
}

type AddCronParam struct {
	ID                 uint              `json:"id"`
	Name               string            `json:"name"`
	Command            string            `json:"command"`
	TaskType           string            `json:"task_type"`
	TemplateID         string            `json:"template_id"`
	TemplateParams     map[string]string `json:"template_params"`
	ConfirmUnsafeShell bool              `json:"confirm_unsafe_shell"`
	Schedule           []string          `json:"schedule"`
	Description        string            `json:"description"`
	Enabled            bool              `json:"enabled"`
	NotifyOnFailure    bool              `json:"notify_on_failure"`
	TimeoutSeconds     int               `json:"timeout_seconds"`
	ConcurrencyPolicy  string            `json:"concurrency_policy"`
}

type CronIDs struct {
	IDs []int `json:"ids"`
}

type RunCronParam struct {
	ID uint `json:"id" binding:"required"`
}
