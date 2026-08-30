package input

type ContainerCreateRequest struct {
	Name          string                 `json:"name"`
	Image         string                 `json:"image"`
	Ports         []ContainerPortMapping `json:"ports,omitempty"`
	Networks      []string               `json:"networks,omitempty"`
	IPv4          string                 `json:"ipv4,omitempty"`
	IPv6          string                 `json:"ipv6,omitempty"`
	Mounts        []ContainerMount       `json:"mounts,omitempty"`
	Command       []string               `json:"command,omitempty"`
	Entrypoint    []string               `json:"entrypoint,omitempty"`
	AutoRemove    bool                   `json:"autoRemove"`
	Privileged    bool                   `json:"privileged"`
	TTY           bool                   `json:"tty"`
	OpenStdin     bool                   `json:"openStdin"`
	Restart       string                 `json:"restart,omitempty"`
	CPUWeight     int                    `json:"cpuWeight,omitempty"`
	CPULimit      float64                `json:"cpuLimit,omitempty"`
	MemoryLimitMB int64                  `json:"memoryLimitMB,omitempty"`
	Labels        map[string]string      `json:"labels,omitempty"`
	Environment   map[string]string      `json:"environment,omitempty"`
}

type ContainerPortMapping struct {
	HostPort      int    `json:"hostPort"`
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol,omitempty"`
}

type ContainerMount struct {
	Type string `json:"type,omitempty"`
	// Mode accepts the UI mount selection when a client includes it. Older
	// clients may omit both type and mode; the service infers that case from
	// whether source is an absolute path or a volume name.
	Mode     string `json:"mode,omitempty"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly"`
}

type ContainerBatchActionRequest struct {
	IDs     []string `json:"ids" binding:"required,min=1"`
	Action  string   `json:"action" binding:"required"`
	Confirm bool     `json:"confirm"`
	Force   bool     `json:"force"`
}

type ContainerActionRequest struct {
	Action  string `json:"action" binding:"required"`
	Confirm bool   `json:"confirm"`
	Force   bool   `json:"force"`
}

type ContainerNetworkActionRequest struct {
	Action  string `json:"action" binding:"required"`
	Network string `json:"network" binding:"required"`
	Confirm bool   `json:"confirm"`
}

type ContainerTerminalTicketRequest struct {
	Password        string `json:"password" binding:"required"`
	ConfirmHighRisk bool   `json:"confirmHighRisk"`
}

type ContainerImagePullRequest struct {
	Reference  string `json:"reference,omitempty"`
	RegistryID uint   `json:"registryId,omitempty"`
	ImageName  string `json:"imageName,omitempty"`
}

type ContainerImageTagRequest struct {
	Reference   string `json:"reference" binding:"required"`
	RemoveOther bool   `json:"removeOther"`
	Confirm     bool   `json:"confirm"`
}

type ContainerImagePushRequest struct {
	Reference  string `json:"reference,omitempty"`
	RegistryID uint   `json:"registryId,omitempty"`
	ImageName  string `json:"imageName,omitempty"`
}

type ContainerImageBuildRequest struct {
	Name           string            `json:"name" binding:"required"`
	Dockerfile     string            `json:"dockerfile,omitempty"`
	ContextPath    string            `json:"contextPath,omitempty"`
	DockerfilePath string            `json:"dockerfilePath,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	LabelsText     string            `json:"labelsText,omitempty"`
}

type ContainerResourceRequest struct {
	Name        string            `json:"name" binding:"required"`
	Driver      string            `json:"driver,omitempty"`
	Options     map[string]string `json:"options,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	OptionsText string            `json:"optionsText,omitempty"`
	LabelsText  string            `json:"labelsText,omitempty"`
	NFS         bool              `json:"nfs"`
}

type ContainerBatchDeleteRequest struct {
	IDs     []string `json:"ids" binding:"required,min=1"`
	Confirm bool     `json:"confirm"`
}

type ContainerNetworkRequest struct {
	Name             string            `json:"name" binding:"required"`
	Driver           string            `json:"driver,omitempty"`
	IPv4             bool              `json:"ipv4"`
	IPv4Subnet       string            `json:"ipv4Subnet,omitempty"`
	IPv4Gateway      string            `json:"ipv4Gateway,omitempty"`
	IPv4IPRange      string            `json:"ipv4IpRange,omitempty"`
	IPv4AuxAddresses map[string]string `json:"ipv4AuxAddresses,omitempty"`
	IPv6             bool              `json:"ipv6"`
	IPv6Subnet       string            `json:"ipv6Subnet,omitempty"`
	IPv6Gateway      string            `json:"ipv6Gateway,omitempty"`
	IPv6IPRange      string            `json:"ipv6IpRange,omitempty"`
	IPv6AuxAddresses map[string]string `json:"ipv6AuxAddresses,omitempty"`
	Options          map[string]string `json:"options,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
	OptionsText      string            `json:"optionsText,omitempty"`
	LabelsText       string            `json:"labelsText,omitempty"`
}

type ContainerCleanupRequest struct {
	Confirm bool `json:"confirm"`
}

type ContainerRegistryRequest struct {
	Name        string `json:"name"`
	AuthEnabled bool   `json:"authEnabled"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	Address     string `json:"address"`
	Protocol    string `json:"protocol"`
}

type ContainerConfigRequest struct {
	Raw   string         `json:"raw"`
	Basic map[string]any `json:"basic,omitempty"`
}

type ContainerRuntimeActionRequest struct {
	Action  string `json:"action" binding:"required"`
	Confirm bool   `json:"confirm"`
}

type ContainerComposeTemplateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Content     string `json:"content"`
}

type ContainerComposeRequest struct {
	Name               string `json:"name"`
	Content            string `json:"content,omitempty"`
	TemplateID         uint   `json:"templateId,omitempty"`
	PreviewFingerprint string `json:"previewFingerprint,omitempty"`
	Confirm            bool   `json:"confirm"`
}

type ContainerComposePreviewRequest struct {
	Action        string `json:"action"`
	Name          string `json:"name"`
	Content       string `json:"content,omitempty"`
	TemplateID    uint   `json:"templateId,omitempty"`
	RemoveVolumes bool   `json:"removeVolumes"`
}

type ContainerComposeActionRequest struct {
	Action             string `json:"action"`
	PreviewFingerprint string `json:"previewFingerprint,omitempty"`
	Confirm            bool   `json:"confirm"`
	RemoveVolumes      bool   `json:"removeVolumes"`
}
