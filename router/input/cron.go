package input

type CronParam struct {
	Page
	Name string `json:"name"`
}

type AddCronParam struct {
	ID           int      `json:"id"`
	CronType     string   `json:"cron_type"`
	Name         string   `json:"name"`
	CronTimes    []string `json:"cron_times"`
	ShellContent string   `json:"shell_content"`
	Status       int      `json:"status"`
}

type CronIDs struct {
	IDs []int `json:"ids"`
}
