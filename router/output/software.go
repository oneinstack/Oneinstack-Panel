package output

import "encoding/json"

type Software struct {
	Id                      int               `json:"id"`
	Name                    string            `json:"name"`
	Describe                string            `json:"describe"`
	Key                     string            `json:"key"`
	Component               string            `json:"component"`
	Icon                    string            `json:"icon"`
	Type                    string            `json:"type"`
	Status                  int               `json:"status"` //0待安装,1安装中,2安装成功,3安装失败
	Resource                string            `json:"resource"`
	Log                     string            `json:"log"`
	Installed               bool              `json:"installed"`
	Versions                []string          `json:"versions"`
	InstallVersion          string            `json:"install_version"`
	VersionOptions          []VersionOption   `json:"versionOptions"`
	VersionLines            []string          `json:"versionLines"`
	Port                    string            `json:"port,omitempty"`
	InstalledPackageVersion string            `json:"installedPackageVersion,omitempty"`
	RuntimeVersion          string            `json:"runtimeVersion,omitempty"`
	LatestPackageVersion    string            `json:"latestPackageVersion,omitempty"`
	UpdateReason            string            `json:"updateReason,omitempty"`
	RecommendedVersion      string            `json:"recommendedVersion,omitempty"`
	HostInstallation        *HostInstallation `json:"hostInstallation,omitempty"`
	Installable             bool              `json:"installable"`
	CatalogManaged          bool              `json:"catalogManaged"`
	IsUpdate                bool              `json:"isUpdate"`
	Tags                    string            `json:"tags"`
	ManageScopes            []string          `json:"manageScopes,omitempty"`
	ServiceName             string            `json:"serviceName,omitempty"`
	RuntimeGroup            string            `json:"runtimeGroup,omitempty"`
	Params                  []*SoftParam      `json:"params"`
	FailureMessage          string            `json:"failureMessage,omitempty"`
	Runtime                 *SoftwareRuntime  `json:"runtime,omitempty"`
}

// HostInstallation describes the local package repository, independently of
// the Center component archive and of whether a firewall takeover is allowed.
type HostInstallation struct {
	Source             string   `json:"source"`
	SystemID           string   `json:"systemId,omitempty"`
	SystemVersion      string   `json:"systemVersion,omitempty"`
	PackageManager     string   `json:"packageManager,omitempty"`
	AvailableVersions  []string `json:"availableVersions"`
	InstalledVersion   string   `json:"installedVersion,omitempty"`
	RecommendedVersion string   `json:"recommendedVersion,omitempty"`
	ConflictingBackend string   `json:"conflictingBackend,omitempty"`
	BlockedCode        string   `json:"blockedCode,omitempty"`
	RepositoryError    string   `json:"repositoryError,omitempty"`
}

type SoftwareRuntime struct {
	Status      string `json:"status"`
	Port        string `json:"port,omitempty"`
	BindAddress string `json:"bindAddress,omitempty"`
	InstallDir  string `json:"installDir,omitempty"`
	DataDir     string `json:"dataDir,omitempty"`
	LogDir      string `json:"logDir,omitempty"`
	RunUser     string `json:"runUser,omitempty"`
	RunGroup    string `json:"runGroup,omitempty"`
}

type VersionOption struct {
	Version            string `json:"version"`
	Line               string `json:"line,omitempty"`
	Channel            string `json:"channel"`
	Enabled            bool   `json:"enabled"`
	Recommended        bool   `json:"recommended,omitempty"`
	AllowCustomVersion bool   `json:"allowCustomVersion,omitempty"`
	Installable        bool   `json:"installable"`
	ReleaseNotes       string `json:"releaseNotes,omitempty"`
}

type SoftParam struct {
	Key      string `json:"key"`
	Value    string `json:"name"`
	Rule     string `json:"rule"`
	Required string `json:"required"`
	Types    string `json:"type"`
	Default  string `json:"default,omitempty"`
}

// UnmarshalJSON accepts both the historical string form and the signed
// catalog boolean form for required. The response keeps the existing string
// shape so older Panel clients remain compatible.
func (p *SoftParam) UnmarshalJSON(data []byte) error {
	var decoded struct {
		Key      string          `json:"key"`
		Value    string          `json:"name"`
		Rule     string          `json:"rule"`
		Required json.RawMessage `json:"required"`
		Types    string          `json:"type"`
		Default  string          `json:"default"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	required := ""
	if len(decoded.Required) > 0 {
		var booleanValue bool
		if err := json.Unmarshal(decoded.Required, &booleanValue); err == nil {
			required = map[bool]string{true: "true", false: "false"}[booleanValue]
		} else {
			if err := json.Unmarshal(decoded.Required, &required); err != nil {
				return err
			}
		}
	}
	*p = SoftParam{
		Key: decoded.Key, Value: decoded.Value, Rule: decoded.Rule,
		Required: required, Types: decoded.Types, Default: decoded.Default,
	}
	return nil
}
