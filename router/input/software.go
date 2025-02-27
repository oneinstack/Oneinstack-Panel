package input

type SoftwareParam struct {
	Id        int    `json:"id"`
	Name      string `json:"name"`
	Key       string `json:"key"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Resource  string `json:"resource"`
	Installed *bool  `json:"installed"`
	IsUpdate  *bool  `json:"isUpdate"`
	Versions  string `json:"versions"`
	Tags      string `json:"tags"`
	Page
}

type InstallSoftwareParam struct {
	Id        int `json:"id"`
	VersionId int `json:"version_id"`
	Params    []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
		Type  string `json:"type"`
	} `json:"params"`
}
