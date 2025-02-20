package output

type Software struct {
	Id        int          `json:"id"`
	Name      string       `json:"name"`
	Key       string       `json:"key"`
	Icon      string       `json:"icon"`
	Type      string       `json:"type"`
	Status    int          `json:"status"` //0待安装,1安装中,2安装成功,3安装失败
	Resource  string       `json:"resource"`
	Log       string       `json:"log"`
	Installed bool         `json:"installed"`
	Versions  []string     `json:"versions"`
	Tags      string       `json:"tags"`
	Params    []*SoftParam `json:"params"`
}

type SoftParam struct {
	Key      string `json:"key"`
	Value    string `json:"name"`
	Rule     string `json:"rule"`
	Required string `json:"required"`
	Types    string `json:"type"`
}
