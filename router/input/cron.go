package input

type CronParam struct {
	Page
	Name string `json:"name"`
}

type AddCronParam struct {
	ID          uint     `json:"id"`
	Name        string   `json:"name"`
	Command     string   `json:"command"`
	Schedule    []string `json:"schedule"`
	Description string   `json:"description"`
	Enabled     bool     `json:"enabled"`
}

type CronIDs struct {
	IDs []int `json:"ids"`
}
