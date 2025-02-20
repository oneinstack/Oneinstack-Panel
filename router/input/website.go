package input

type WebsiteQueryParam struct {
	Page
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Domain  string `json:"domain"`
	Dir     string `json:"dir"`
	Remark  string `json:"remark"`
	RootDir string `json:"root_dir"`
	TarUrl  string `json:"tar_url"`
	SendUrl string `json:"send_url"`
	Class   string `json:"class"`
	Type    string `json:"type"`
}
