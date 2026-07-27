package output

type Software struct {
	Id                 int          `json:"id"`
	Name               string       `json:"name"`
	Describe           string       `json:"describe"`
	Key                string       `json:"key"`
	Component          string       `json:"component"`
	Icon               string       `json:"icon"`
	Type               string       `json:"type"`
	Status             int          `json:"status"` //0待安装,1安装中,2安装成功,3安装失败
	Resource           string       `json:"resource"`
	Log                string       `json:"log"`
	Installed          bool         `json:"installed"`
	Versions           []string     `json:"versions"`
	InstallVersion     string       `json:"install_version"`
	RecommendedVersion string       `json:"recommendedVersion,omitempty"`
	Installable        bool         `json:"installable"`
	CatalogManaged     bool         `json:"catalogManaged"`
	IsUpdate           bool         `json:"isUpdate"`
	Tags               string       `json:"tags"`
	Params             []*SoftParam `json:"params"`
}

type SoftParam struct {
	Key      string `json:"key"`
	Value    string `json:"name"`
	Rule     string `json:"rule"`
	Required string `json:"required"`
	Types    string `json:"type"`
}
