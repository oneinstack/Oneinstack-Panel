package container

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/utils"
)

var (
	ErrRuntimeUnavailable               = errors.New("container runtime unavailable")
	ErrInvalidContainerConfig           = errors.New("invalid container configuration")
	ErrInvalidLogOptions                = errors.New("invalid container log options")
	ErrInvalidRegistryInput             = errors.New("invalid container registry input")
	ErrRegistryProbeFailed              = errors.New("container registry probe failed")
	ErrRegistryNotAuthenticated         = errors.New("container registry authentication is not configured")
	ErrImageReferenceMissingNamespace   = errors.New("container image reference missing namespace")
	ErrRegistryPushPermissionDenied     = errors.New("container registry push permission denied")
	ErrImagePullFailed                  = errors.New("container image pull failed")
	ErrImagePushFailed                  = errors.New("container image push failed")
	ErrRegistryCredentialUnavailable    = errors.New("container registry credential unavailable")
	ErrDockerCommandTimeout             = errors.New("docker command timed out")
	ErrContainerLogsUnavailable         = errors.New("container logs unavailable")
	ErrProtectedNetwork                 = errors.New("protected container network")
	ErrContainerStatsTimeout            = errors.New("container stats timed out")
	ErrContainerStatsPermissionDenied   = errors.New("container stats permission denied")
	ErrContainerStatsNotFound           = errors.New("container stats container not found")
	ErrContainerStatsEmpty              = errors.New("container stats returned no data")
	ErrContainerStatsInvalidReference   = errors.New("invalid container stats reference")
	ErrContainerInspectUnavailable      = errors.New("container inspect returned no data")
	ErrContainerActionFailed            = errors.New("container action failed")
	ErrContainerActionObserveTimeout    = errors.New("container action observation timed out")
	ErrContainerNetworkAttachmentFailed = errors.New("container network attachment failed")
	ErrContainerNetworkAlreadyAttached  = errors.New("container network already attached")
	ErrContainerNetworkNotAttached      = errors.New("container network is not attached")
)

const (
	// Docker start 返回成功只代表启动请求已提交，容器主进程仍可能在随后
	// 很短时间内因为入口配置错误退出。保留一个短暂的稳定观察窗口，避免
	// 面板在进程刚拉起时就把中间状态当作最终状态。
	containerActionPollInterval    = 500 * time.Millisecond
	containerActionObserveTimeout  = 30 * time.Second
	containerActionStableRunWindow = 3 * time.Second
	// Docker may briefly return an empty inspect/stats snapshot while a
	// container is being restarted. Do not expose that transient snapshot to
	// callers; give the daemon a short window to publish the real state.
	containerReadRetryAttempts = 20
	containerReadRetryInterval = 250 * time.Millisecond
)

// Service is a deliberately small, fixed-command Docker adapter. It never
// accepts a shell command from an HTTP request; every operation is selected
// from the methods below and arguments are validated before execution.
type Service struct {
	binary string
}

func New() *Service { return &Service{binary: "docker"} }

type RuntimeStatus struct {
	Available      bool         `json:"available"`
	Installed      bool         `json:"installed"`
	Running        bool         `json:"running"`
	DockerVersion  string       `json:"dockerVersion,omitempty"`
	ComposeVersion string       `json:"composeVersion,omitempty"`
	ServerVersion  string       `json:"serverVersion,omitempty"`
	Message        string       `json:"message,omitempty"`
	Capabilities   Capabilities `json:"capabilities"`
}

// FeatureAvailability describes whether a module operation can be offered by
// the current Panel process. It deliberately separates Docker dependencies
// from permission checks, which are enforced by the route middleware.
type FeatureAvailability struct {
	Available            bool   `json:"available"`
	RequiresDockerCLI    bool   `json:"requiresDockerCli"`
	RequiresDockerDaemon bool   `json:"requiresDockerDaemon"`
	DisabledReasonCode   string `json:"disabledReasonCode,omitempty"`
	DisabledReason       string `json:"disabledReason,omitempty"`
}

type Capabilities struct {
	Runtime         FeatureAvailability `json:"runtime"`
	ContainerManage FeatureAvailability `json:"containerManage"`
	ImageManage     FeatureAvailability `json:"imageManage"`
	NetworkManage   FeatureAvailability `json:"networkManage"`
	VolumeManage    FeatureAvailability `json:"volumeManage"`
	ComposeManage   FeatureAvailability `json:"composeManage"`
	RegistryManage  FeatureAvailability `json:"registryManage"`
	RegistryTest    FeatureAvailability `json:"registryTest"`
	DockerConfig    FeatureAvailability `json:"dockerConfig"`
}

type ActionRequest struct {
	Action  string `json:"action" binding:"required"`
	Confirm bool   `json:"confirm"`
	Force   bool   `json:"force"`
}

type ContainerActionState struct {
	Status           string
	Running          bool
	Paused           bool
	ExitCode         int
	Error            string
	NetworkMode      string
	Networks         []string
	SandboxID        string
	NetworkExpected  bool
	NetworkConnected bool
}

type ContainerActionError struct {
	Action string
	Err    error
}

type ImagePushError struct {
	Err error
}

func (e *ImagePushError) Error() string {
	if e == nil || e.Err == nil {
		return ErrImagePushFailed.Error()
	}
	return fmt.Sprintf("Docker 镜像推送失败：%s", dockerDiagnosticSummary(e.Err.Error()))
}

func (e *ImagePushError) Unwrap() error {
	if e == nil {
		return ErrImagePushFailed
	}
	return e.Err
}

func (e *ImagePushError) Is(target error) bool {
	return target == ErrImagePushFailed
}

// ImagePushFailureDetail converts Docker push output into an actionable,
// secret-safe detail for the API error envelope. The raw Docker output is
// retained in the error for server-side audit diagnostics, but is not exposed
// by the handler unless it passes the bounded redaction path below.
func ImagePushFailureDetail(err error) string {
	if err == nil {
		return "Docker 未返回可用的推送错误，请结合请求时间查看面板日志。"
	}
	var pushErr *ImagePushError
	cause := err
	if errors.As(err, &pushErr) && pushErr != nil && pushErr.Err != nil {
		cause = pushErr.Err
	}
	lower := strings.ToLower(cause.Error())
	switch {
	case errors.Is(cause, ErrRegistryCredentialUnavailable):
		return "已保存的镜像仓库凭据无法读取，请重新保存 Registry 凭据后重试。"
	case errors.Is(cause, ErrRegistryNotAuthenticated):
		return "当前 Registry 未配置认证，只能拉取和测试连通性，不能推送镜像；请先启用认证并保存用户名和 PAT。"
	case errors.Is(cause, ErrImageReferenceMissingNamespace):
		return "镜像引用缺少命名空间；Docker Hub 请使用 namespace/imagename（可附 :tag），避免被解析为 docker.io/library/镜像名。"
	case errors.Is(cause, ErrRegistryPushPermissionDenied):
		return "Docker Hub 的 library 命名空间通常不允许普通账号推送，请改用账号或组织命名空间，并确认目标仓库的 push 权限。"
	case strings.Contains(lower, "invalid reference format"), strings.Contains(lower, "tag is missing"):
		return "镜像引用格式无效，请检查仓库地址、命名空间、镜像名称和标签。"
	case strings.Contains(lower, "no such image"), strings.Contains(lower, "unable to find image"), strings.Contains(lower, "reference does not exist"), strings.Contains(lower, "tag does not exist"):
		return "本地不存在要推送的镜像，请先确认镜像名称和标签。"
	case strings.Contains(lower, "manifest unknown"), strings.Contains(lower, "name unknown"), strings.Contains(lower, "repository does not exist"), strings.Contains(lower, "not found"):
		return "目标镜像仓库或仓库内镜像不存在，请检查 Registry 地址、命名空间和镜像名称。"
	case strings.Contains(lower, "requested access to the resource is denied"), strings.Contains(lower, "insufficient_scope"), strings.Contains(lower, "access denied"), strings.Contains(lower, "forbidden"), strings.Contains(lower, "permission denied"), strings.Contains(lower, "denied"):
		return "镜像仓库认证可能已通过，但当前账号没有目标仓库的 push 权限，请确认命名空间、目标仓库是否已创建以及账号权限。"
	case strings.Contains(lower, "authentication required"), strings.Contains(lower, "invalid username or password"), strings.Contains(lower, "unauthorized"), strings.Contains(lower, "http 401"), strings.Contains(lower, "401 unauthorized"):
		return "镜像仓库身份认证失败，请检查 Registry 用户名和 PAT 是否正确、是否已过期。"
	case strings.Contains(lower, "no such host"), strings.Contains(lower, "temporary failure in name resolution"), strings.Contains(lower, "server misbehaving"), strings.Contains(lower, "connection refused"), strings.Contains(lower, "network is unreachable"), strings.Contains(lower, "no route to host"), strings.Contains(lower, "dial tcp"), strings.Contains(lower, "lookup "):
		return "无法连接目标镜像仓库，请检查仓库地址、面板服务器网络、DNS 和防火墙配置。"
	case strings.Contains(lower, "x509"), strings.Contains(lower, "certificate"), strings.Contains(lower, "tls"), strings.Contains(lower, "server gave http response to https client"):
		return "目标镜像仓库的 TLS 证书校验失败，请检查证书有效期、域名匹配和信任链。"
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "timed out"), strings.Contains(lower, "deadline exceeded"):
		return "Docker 镜像推送未在限定时间内完成，请检查仓库服务和网络连通性后重试。"
	}
	detail := dockerDiagnosticSummary(cause.Error())
	if detail == "" {
		return "Docker 未返回可分类的推送错误，请结合请求时间查看面板日志。"
	}
	return "Docker 返回错误：" + detail
}

func (e *ContainerActionError) Error() string {
	if e == nil || e.Err == nil {
		return ErrContainerActionFailed.Error()
	}
	return fmt.Sprintf("Docker %s 操作失败: %s", e.Action, dockerDiagnosticSummary(e.Err.Error()))
}

func (e *ContainerActionError) Unwrap() error {
	if e == nil {
		return ErrContainerActionFailed
	}
	return e.Err
}

func (e *ContainerActionError) Is(target error) bool {
	return target == ErrContainerActionFailed
}

func ContainerActionFailureDetail(err error) string {
	if err == nil {
		return ""
	}
	var actionErr *ContainerActionError
	if errors.As(err, &actionErr) && actionErr != nil && actionErr.Err != nil {
		return dockerDiagnosticSummary(actionErr.Err.Error())
	}
	return dockerDiagnosticSummary(err.Error())
}

type ImagePullRequest struct {
	Reference string `json:"reference" binding:"required"`
}

type ResourceRequest struct {
	Name        string
	Driver      string
	Options     map[string]string
	Labels      map[string]string
	OptionsText string
	LabelsText  string
	NFS         bool
}

type NetworkCreateRequest struct {
	Name             string
	Driver           string
	IPv4             bool
	IPv4Subnet       string
	IPv4Gateway      string
	IPv4IPRange      string
	IPv4AuxAddresses map[string]string
	IPv6             bool
	IPv6Subnet       string
	IPv6Gateway      string
	IPv6IPRange      string
	IPv6AuxAddresses map[string]string
	Options          map[string]string
	Labels           map[string]string
	OptionsText      string
	LabelsText       string
}

type ContainerCreateRequest struct {
	Name          string
	Image         string
	Ports         []PortMapping
	Networks      []string
	IPv4          string
	IPv6          string
	Mounts        []Mount
	Command       []string
	Entrypoint    []string
	AutoRemove    bool
	Privileged    bool
	TTY           bool
	OpenStdin     bool
	Restart       string
	CPUWeight     int
	CPULimit      float64
	MemoryLimitMB int64
	Labels        map[string]string
	Environment   map[string]string
}

type PortMapping struct {
	HostPort, ContainerPort int
	Protocol                string
}
type Mount struct {
	Type           string
	Source, Target string
	ReadOnly       bool
}

// ContainerValidationError is one actionable container-create validation
// failure. Field uses the JSON request path so callers can focus the matching
// form control without exposing submitted secret values.
type ContainerValidationError struct {
	Field   string
	Message string
}

// ContainerValidationErrors preserves the invalid-container sentinel while
// carrying every independent validation failure found in one request.
type ContainerValidationErrors struct {
	Items []ContainerValidationError
}

func (e *ContainerValidationErrors) Error() string {
	if e == nil || len(e.Items) == 0 {
		return ErrInvalidContainerConfig.Error()
	}
	details := make([]string, 0, len(e.Items))
	for _, item := range e.Items {
		field := strings.TrimSpace(item.Field)
		message := strings.TrimSpace(item.Message)
		if message == "" {
			continue
		}
		if field == "" {
			details = append(details, message)
			continue
		}
		details = append(details, field+"："+message)
	}
	if len(details) == 0 {
		return ErrInvalidContainerConfig.Error()
	}
	return ErrInvalidContainerConfig.Error() + ": " + strings.Join(details, "；")
}

func (e *ContainerValidationErrors) Unwrap() error {
	return ErrInvalidContainerConfig
}

const (
	MountTypeBind   = "bind"
	MountTypeVolume = "volume"
)

type ContainerStats struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	CPUPercent    string `json:"cpuPercent,omitempty"`
	MemoryUsage   string `json:"memoryUsage,omitempty"`
	MemoryPercent string `json:"memoryPercent,omitempty"`
	NetworkIO     string `json:"networkIO,omitempty"`
	BlockIO       string `json:"blockIO,omitempty"`
	PIDs          string `json:"pids,omitempty"`
}

type LogOptions struct {
	Tail       int
	Since      string
	Until      string
	Timestamps bool
}

type RegistryInput struct {
	Name        string
	Address     string
	Protocol    string
	AuthEnabled bool
	Username    string
	Password    string
}

type RegistrySummary struct {
	ID            uint       `json:"id"`
	Name          string     `json:"name"`
	Address       string     `json:"address"`
	Protocol      string     `json:"protocol"`
	AuthEnabled   bool       `json:"authEnabled"`
	Username      string     `json:"username,omitempty"`
	Status        string     `json:"status"`
	StatusMessage string     `json:"statusMessage,omitempty"`
	LastCheckedAt *time.Time `json:"lastCheckedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

func (s *Service) Runtime(ctx context.Context) RuntimeStatus {
	status := RuntimeStatus{}
	if _, err := exec.LookPath(s.binary); err != nil {
		status.Message = "Docker 未安装或不在 PATH 中"
		return withCapabilities(status)
	}
	status.Installed = true
	status.Available = true
	if out, err := s.run(ctx, "version", "--format", "{{.Client.Version}}|{{.Server.Version}}"); err == nil {
		parts := strings.SplitN(strings.TrimSpace(out), "|", 2)
		status.DockerVersion = parts[0]
		if len(parts) == 2 {
			status.ServerVersion = parts[1]
		}
	} else {
		status.Available = false
		status.Running = false
		status.Message = cleanError(err)
		return withCapabilities(status)
	}
	status.Running = true
	if out, err := s.run(ctx, "compose", "version", "--short"); err == nil {
		status.ComposeVersion = strings.TrimSpace(out)
	}
	return withCapabilities(status)
}

func (s *Service) Capabilities(ctx context.Context) Capabilities {
	return s.Runtime(ctx).Capabilities
}

func withCapabilities(status RuntimeStatus) RuntimeStatus {
	runtime := FeatureAvailability{
		Available:            status.Available,
		RequiresDockerCLI:    true,
		RequiresDockerDaemon: true,
	}
	if !status.Available {
		if !status.Installed {
			runtime.DisabledReasonCode = "docker_cli_unavailable"
			runtime.DisabledReason = "未找到 Docker CLI（docker），请安装 Docker CLI/Engine，并确认 docker 已加入面板进程的 PATH。"
		} else {
			runtime.DisabledReasonCode = "docker_daemon_unavailable"
			runtime.DisabledReason = "Docker CLI 已安装，但无法连接 Docker 守护进程；请确认 Docker 服务已启动，并检查面板运行用户是否有访问 Docker socket 的权限。"
		}
	}
	compose := runtime
	if status.Available && strings.TrimSpace(status.ComposeVersion) == "" {
		compose.Available = false
		compose.DisabledReasonCode = "docker_compose_unavailable"
		compose.DisabledReason = "Docker Compose 插件不可用，请安装与当前 Docker CLI 兼容的 Compose 插件后重试。"
	}
	status.Capabilities = Capabilities{
		Runtime:         runtime,
		ContainerManage: runtime,
		ImageManage:     runtime,
		NetworkManage:   runtime,
		VolumeManage:    runtime,
		ComposeManage:   compose,
		RegistryManage:  FeatureAvailability{Available: true},
		RegistryTest:    FeatureAvailability{Available: true},
		DockerConfig:    FeatureAvailability{Available: true},
	}
	return status
}

func (s *Service) ListContainers(ctx context.Context) ([]map[string]any, error) {
	return s.linesJSON(ctx, "ps", "-a", "--format", "{{json .}}")
}

func (s *Service) InspectContainer(ctx context.Context, id string) (map[string]any, error) {
	id, err := validateReference(id)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; ; attempt++ {
		out, inspectErr := s.run(ctx, "inspect", id)
		if inspectErr != nil {
			return nil, inspectErr
		}
		var items []map[string]any
		if unmarshalErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &items); unmarshalErr == nil && len(items) > 0 && items[0] != nil {
			result := sanitizeInspect(items[0])
			if len(result) > 0 {
				return result, nil
			}
		}
		lastErr = ErrContainerInspectUnavailable
		if attempt >= containerReadRetryAttempts-1 {
			return nil, lastErr
		}
		if err := waitForContainerReadRetry(ctx); err != nil {
			return nil, err
		}
	}
}

func (s *Service) Stats(ctx context.Context, id string) (ContainerStats, error) {
	id, err := validateReference(id)
	if err != nil {
		return ContainerStats{}, fmt.Errorf("%w: %v", ErrContainerStatsInvalidReference, err)
	}
	for attempt := 0; ; attempt++ {
		items, statsErr := s.linesJSON(ctx, "stats", "--no-stream", "--format", "{{json .}}", id)
		if statsErr != nil {
			return ContainerStats{}, classifyStatsError(statsErr)
		}
		if len(items) > 0 && validContainerStats(items[0]) {
			item := items[0]
			return ContainerStats{ID: stringValue(item, "ID"), Name: stringValue(item, "Name"), CPUPercent: stringValue(item, "CPUPerc"), MemoryUsage: stringValue(item, "MemUsage"), MemoryPercent: stringValue(item, "MemPerc"), NetworkIO: stringValue(item, "NetIO"), BlockIO: stringValue(item, "BlockIO"), PIDs: stringValue(item, "PIDs")}, nil
		}
		if attempt >= containerReadRetryAttempts-1 {
			return ContainerStats{}, ErrContainerStatsEmpty
		}
		if err := waitForContainerReadRetry(ctx); err != nil {
			return ContainerStats{}, err
		}
	}
}

func validContainerStats(item map[string]any) bool {
	if item == nil || strings.TrimSpace(stringValue(item, "ID")) == "" {
		return false
	}
	for _, key := range []string{"CPUPerc", "MemUsage", "MemPerc", "NetIO", "BlockIO", "PIDs"} {
		if strings.TrimSpace(stringValue(item, key)) != "" {
			return true
		}
	}
	return false
}

func waitForContainerReadRetry(ctx context.Context) error {
	timer := time.NewTimer(containerReadRetryInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func classifyStatsError(err error) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "operation not permitted"):
		return fmt.Errorf("%w: %v", ErrContainerStatsPermissionDenied, err)
	case errors.Is(err, ErrDockerCommandTimeout), errors.Is(err, context.DeadlineExceeded),
		strings.Contains(lower, "timed out"), strings.Contains(lower, "timeout"):
		return fmt.Errorf("%w: %v", ErrContainerStatsTimeout, err)
	case strings.Contains(lower, "no such container"), strings.Contains(lower, "no such object"):
		return fmt.Errorf("%w: %v", ErrContainerStatsNotFound, err)
	default:
		return err
	}
}

func (s *Service) CreateContainer(ctx context.Context, request ContainerCreateRequest) (string, error) {
	if err := validateContainerCreateRequest(request); err != nil {
		return "", err
	}
	name, err := validateName(request.Name)
	if err != nil {
		return "", err
	}
	image, err := validateReference(request.Image)
	if err != nil {
		return "", err
	}
	if err := s.ensureImage(ctx, image); err != nil {
		return "", err
	}
	args := []string{"create", "--name", name}
	// 镜像准备由 ensureImage 负责，禁止 create 再隐式拉取，避免失败原因和
	// 创建动作混在一起，也避免并发请求重复触发镜像拉取。
	args = append(args, "--pull", "never")
	if request.AutoRemove {
		args = append(args, "--rm")
	}
	if request.Privileged {
		args = append(args, "--privileged")
	}
	if request.TTY {
		args = append(args, "-t")
	}
	if request.OpenStdin {
		args = append(args, "-i")
	}
	if request.Restart != "" {
		if err := validateRestart(request.Restart); err != nil {
			return "", err
		}
		args = append(args, "--restart", request.Restart)
	}
	if request.CPUWeight != 0 {
		if request.CPUWeight < 10 || request.CPUWeight > 1000 {
			return "", errors.New("CPU权重必须在10到1000之间")
		}
		args = append(args, "--cpu-shares", strconv.Itoa(request.CPUWeight))
	}
	if request.CPULimit < 0 || request.CPULimit > 256 {
		return "", errors.New("CPU限制无效")
	}
	if request.CPULimit > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(request.CPULimit, 'f', -1, 64))
	}
	if request.MemoryLimitMB < 0 || request.MemoryLimitMB > 1024*1024 {
		return "", errors.New("内存限制无效")
	}
	if request.MemoryLimitMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dM", request.MemoryLimitMB))
	}
	for _, port := range request.Ports {
		if port.ContainerPort < 1 || port.ContainerPort > 65535 || port.HostPort < 0 || port.HostPort > 65535 {
			return "", errors.New("端口映射无效")
		}
		protocol := port.Protocol
		if protocol == "" {
			protocol = "tcp"
		}
		if protocol != "tcp" && protocol != "udp" {
			return "", errors.New("端口协议只支持tcp或udp")
		}
		mapping := fmt.Sprintf("%d:%d/%s", port.HostPort, port.ContainerPort, protocol)
		if port.HostPort == 0 {
			mapping = fmt.Sprintf("%d/%s", port.ContainerPort, protocol)
		}
		args = append(args, "-p", mapping)
	}
	for _, network := range request.Networks {
		network, err := validateName(network)
		if err != nil {
			return "", fmt.Errorf("网络名称无效: %w", err)
		}
		args = append(args, "--network", network)
	}
	if request.IPv4 != "" {
		if !validIP(request.IPv4) {
			return "", errors.New("IPv4地址无效")
		}
		args = append(args, "--ip", request.IPv4)
	}
	if request.IPv6 != "" {
		if !validIP(request.IPv6) {
			return "", errors.New("IPv6地址无效")
		}
		args = append(args, "--ip6", request.IPv6)
	}
	for _, mount := range request.Mounts {
		mountType, source, err := validateMountSource(mount)
		if err != nil {
			return "", err
		}
		target, err := validateMountTarget(mount.Target)
		if err != nil {
			return "", err
		}
		mountSpec := fmt.Sprintf("type=%s,src=%s,dst=%s", mountType, source, target)
		if mount.ReadOnly {
			mountSpec += ",readonly"
		}
		args = append(args, "--mount", mountSpec)
	}
	for key, value := range request.Labels {
		key, err := validateLabel(key)
		if err != nil {
			return "", err
		}
		args = append(args, "--label", key+"="+value)
	}
	for key, value := range request.Environment {
		key, err := validateEnvKey(key)
		if err != nil {
			return "", err
		}
		args = append(args, "--env", key+"="+value)
	}
	if len(request.Entrypoint) > 0 {
		args = append(args, "--entrypoint", strings.Join(request.Entrypoint, " "))
	}
	args = append(args, image)
	args = append(args, request.Command...)
	return s.run(ctx, args...)
}

func validateContainerCreateRequest(request ContainerCreateRequest) error {
	validationErrors := &ContainerValidationErrors{}
	addError := func(field, message string) {
		message = strings.TrimSpace(message)
		if message == "" {
			return
		}
		validationErrors.Items = append(validationErrors.Items, ContainerValidationError{Field: field, Message: message})
	}

	if _, err := validateName(request.Name); err != nil {
		addError("name", "容器名称无效："+err.Error())
	}
	if _, err := validateReference(request.Image); err != nil {
		addError("image", "镜像名称无效："+err.Error())
	}
	if err := validateOfficialPostgresEnvironment(request.Image, request.Environment); err != nil {
		addError("environment", err.Error())
	}
	if isOfficialPostgres18OrNewerImage(request.Image) {
		for index, mount := range request.Mounts {
			if isLegacyPostgresDataMountTarget(mount.Target) {
				addError(fmt.Sprintf("mounts[%d].target", index), postgresDataMountRecommendation())
			}
		}
	}
	if request.AutoRemove && request.Restart != "" && request.Restart != "no" {
		addError("autoRemove", "退出后自动删除不能与重启策略同时使用；请关闭自动删除，或将重启策略设置为不重启")
	}
	if request.Restart != "" {
		if err := validateRestart(request.Restart); err != nil {
			addError("restart", "重启策略无效："+err.Error())
		}
	}
	if request.CPUWeight != 0 && (request.CPUWeight < 10 || request.CPUWeight > 1000) {
		addError("cpuWeight", "CPU权重必须在10到1000之间，0表示不设置")
	}
	if request.CPULimit < 0 || request.CPULimit > 256 {
		addError("cpuLimit", "CPU限制必须在0到256之间，0表示不设置")
	}
	if request.MemoryLimitMB < 0 || request.MemoryLimitMB > 1024*1024 {
		addError("memoryLimitMB", "内存限制必须在0到1048576 MB之间，0表示不设置")
	}

	networks := make(map[string]struct{}, len(request.Networks))
	for index, value := range request.Networks {
		network, err := validateName(value)
		if err != nil {
			addError(fmt.Sprintf("networks[%d]", index), "网络名称无效："+err.Error())
			continue
		}
		if _, exists := networks[network]; exists {
			addError(fmt.Sprintf("networks[%d]", index), "网络重复配置")
			continue
		}
		networks[network] = struct{}{}
	}
	if request.IPv4 != "" && !validIPv4(request.IPv4) {
		addError("ipv4", "IPv4地址无效，请填写合法的IPv4地址")
	}
	if request.IPv6 != "" && !validIPv6(request.IPv6) {
		addError("ipv6", "IPv6地址无效，请填写合法的IPv6地址")
	}

	ports := make(map[string]struct{}, len(request.Ports))
	for index, port := range request.Ports {
		if port.ContainerPort < 1 || port.ContainerPort > 65535 {
			addError(fmt.Sprintf("ports[%d].containerPort", index), "容器端口无效，必须在1到65535之间")
		}
		if port.HostPort < 0 || port.HostPort > 65535 {
			addError(fmt.Sprintf("ports[%d].hostPort", index), "主机端口无效，必须在0到65535之间")
		}
		protocol := strings.ToLower(strings.TrimSpace(port.Protocol))
		if protocol == "" {
			protocol = "tcp"
		}
		if protocol != "tcp" && protocol != "udp" {
			addError(fmt.Sprintf("ports[%d].protocol", index), "端口协议无效，只支持 tcp 或 udp")
		}
		if port.HostPort != 0 && (protocol == "tcp" || protocol == "udp") {
			key := fmt.Sprintf("%d/%s", port.HostPort, protocol)
			if _, exists := ports[key]; exists {
				addError(fmt.Sprintf("ports[%d].hostPort", index), "主机端口和协议重复映射")
				continue
			}
			ports[key] = struct{}{}
		}
	}

	targets := make(map[string]struct{}, len(request.Mounts))
	for index, mount := range request.Mounts {
		if _, _, err := validateMountSource(mount); err != nil {
			field := fmt.Sprintf("mounts[%d].source", index)
			if strings.Contains(err.Error(), "挂载类型") {
				field = fmt.Sprintf("mounts[%d].type", index)
			}
			addError(field, err.Error())
		}
		target, err := validateMountTarget(mount.Target)
		if err != nil {
			addError(fmt.Sprintf("mounts[%d].target", index), "挂载目标无效："+err.Error())
			continue
		}
		if _, exists := targets[target]; exists {
			addError(fmt.Sprintf("mounts[%d].target", index), "容器目录重复挂载")
			continue
		}
		targets[target] = struct{}{}
	}
	for key := range request.Labels {
		if _, err := validateLabel(key); err != nil {
			addError("labels", "Label名称无效："+err.Error())
		}
	}
	for key := range request.Environment {
		if _, err := validateEnvKey(key); err != nil {
			addError("environment", "环境变量名称无效："+err.Error())
		}
	}
	for index, value := range request.Entrypoint {
		if strings.ContainsAny(value, "\r\n") {
			addError(fmt.Sprintf("entrypoint[%d]", index), "入口命令不能包含换行符")
		}
	}
	for index, value := range request.Command {
		if strings.ContainsAny(value, "\r\n") {
			addError(fmt.Sprintf("command[%d]", index), "启动参数不能包含换行符")
		}
	}
	if len(validationErrors.Items) > 0 {
		return validationErrors
	}
	return nil
}

// validateOfficialPostgresEnvironment prevents a container created from the
// official postgres image from reaching Docker in a state that is guaranteed
// to exit during first-time database initialization. The image's entrypoint
// owns cluster creation, while the application owns its schema migrations.
// Do not generate a password here: container task requests are persisted and
// Docker inspect can expose environment variables to administrators.
func validateOfficialPostgresEnvironment(image string, environment map[string]string) error {
	reference := strings.TrimSpace(image)
	if at := strings.IndexByte(reference, '@'); at >= 0 {
		reference = reference[:at]
	}
	if slash := strings.LastIndexByte(reference, '/'); slash >= 0 {
		reference = reference[slash+1:]
	}
	if colon := strings.IndexByte(reference, ':'); colon >= 0 {
		reference = reference[:colon]
	}
	if !strings.EqualFold(reference, "postgres") {
		return nil
	}

	password := strings.TrimSpace(environment["POSTGRES_PASSWORD"])
	passwordFile := strings.TrimSpace(environment["POSTGRES_PASSWORD_FILE"])
	authMethod := strings.TrimSpace(environment["POSTGRES_HOST_AUTH_METHOD"])
	if password != "" || passwordFile != "" || strings.EqualFold(authMethod, "trust") {
		return nil
	}
	return errors.New("PostgreSQL 官方镜像缺少初始化认证参数，请在 environment 中设置非空 POSTGRES_PASSWORD 或已挂载的 POSTGRES_PASSWORD_FILE；仅测试环境可使用 POSTGRES_HOST_AUTH_METHOD=trust，生产环境不建议这样配置")
}

var postgresImageMajorPattern = regexp.MustCompile(`^([0-9]+)(?:[.-]|$)`)

const (
	postgresLegacyDataPath      = "/var/lib/postgresql/data"
	postgresRecommendedDataPath = "/var/lib/postgresql"
)

func isOfficialPostgres18OrNewerImage(image string) bool {
	reference := strings.TrimSpace(image)
	if at := strings.IndexByte(reference, '@'); at >= 0 {
		reference = reference[:at]
	}
	if slash := strings.LastIndexByte(reference, '/'); slash >= 0 {
		reference = reference[slash+1:]
	}
	if !strings.HasPrefix(strings.ToLower(reference), "postgres:") {
		return false
	}

	parts := strings.SplitN(reference, ":", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "postgres") {
		return false
	}
	match := postgresImageMajorPattern.FindStringSubmatch(strings.TrimSpace(parts[1]))
	if len(match) != 2 {
		return false
	}
	major, err := strconv.Atoi(match[1])
	return err == nil && major >= 18
}

func isLegacyPostgresDataMountTarget(target string) bool {
	return filepath.Clean(strings.TrimSpace(target)) == postgresLegacyDataPath
}

func postgresDataMountRecommendation() string {
	return "PostgreSQL 18+ 官方镜像的数据卷应只挂载到 " + postgresRecommendedDataPath + "；旧路径 " + postgresLegacyDataPath + " 会被视为未使用挂载"
}

func (s *Service) ContainerAction(ctx context.Context, id, action string, force, confirm bool) error {
	id, err := validateReference(id)
	if err != nil {
		return err
	}
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "resume":
		action = "unpause"
	case "force-stop", "force_stop":
		action = "kill"
	case "delete", "remove":
		action = "rm"
	}
	args := []string{action, id}
	switch action {
	case "start", "stop", "restart", "pause", "unpause":
	case "kill":
		if !force || !confirm {
			return errors.New("强制停止需要 force=true 且 confirm=true")
		}
	case "rm":
		if !confirm {
			return errors.New("删除容器需要 confirm=true")
		}
		if force {
			args = []string{"rm", "-f", id}
		}
	default:
		return fmt.Errorf("不支持的容器操作: %s", action)
	}
	_, err = s.run(ctx, args...)
	if err != nil {
		return &ContainerActionError{Action: action, Err: err}
	}
	return nil
}

// ObserveContainerAction waits for the real Docker state after start/restart.
// The action command itself can finish before the container's main process has
// finished initializing, so observing only the command exit status can leave
// callers displaying a stale running state.
func (s *Service) ObserveContainerAction(ctx context.Context, id string) (ContainerActionState, error) {
	id, err := validateReference(id)
	if err != nil {
		return ContainerActionState{}, err
	}

	observeCtx, cancel := context.WithTimeout(ctx, containerActionObserveTimeout)
	defer cancel()
	ticker := time.NewTicker(containerActionPollInterval)
	defer ticker.Stop()

	var last ContainerActionState
	var runningSince time.Time
	for {
		state, inspectErr := s.inspectContainerState(observeCtx, id)
		if inspectErr == nil {
			last = state
			if state.NetworkExpected && !state.NetworkConnected {
				return state, nil
			}
			if state.Status == "restarting" || state.Status == "exited" || state.Status == "dead" || state.Status == "created" {
				return state, nil
			}
			now := time.Now()
			if state.Running {
				if runningSince.IsZero() {
					runningSince = now
				}
				if now.Sub(runningSince) >= containerActionStableRunWindow {
					return state, nil
				}
			} else {
				runningSince = time.Time{}
				if state.Status == "exited" || state.Status == "dead" {
					return state, nil
				}
			}
		}

		select {
		case <-ctx.Done():
			return ContainerActionState{}, ctx.Err()
		case <-observeCtx.Done():
			if last.Status == "" {
				return ContainerActionState{}, fmt.Errorf("容器启动状态探测失败: %w", observeCtx.Err())
			}
			return ContainerActionState{}, fmt.Errorf("%w: 当前状态=%s，NetworkMode=%s，当前网络=[%s]", ErrContainerActionObserveTimeout, last.Status, last.NetworkMode, strings.Join(last.Networks, ", "))
		case <-ticker.C:
		}
	}
}

func (s *Service) inspectContainerState(ctx context.Context, id string) (ContainerActionState, error) {
	snapshot, err := s.inspectContainerSnapshot(ctx, id)
	if err != nil {
		return ContainerActionState{}, err
	}
	status := strings.ToLower(strings.TrimSpace(snapshot.State.Status))
	if status == "" {
		return ContainerActionState{}, errors.New("容器状态响应为空")
	}
	networkMode := strings.TrimSpace(snapshot.HostConfig.NetworkMode)
	expectedNetwork := expectedContainerNetwork(networkMode)
	networks := sortedNetworkNames(snapshot.NetworkSettings.Networks)
	_, networkConnected := snapshot.NetworkSettings.Networks[expectedNetwork]
	if expectedNetwork == "" {
		networkConnected = true
	}
	return ContainerActionState{
		Status: status, Running: snapshot.State.Running, Paused: snapshot.State.Paused,
		ExitCode: snapshot.State.ExitCode, Error: strings.TrimSpace(snapshot.State.Error),
		NetworkMode: networkMode, Networks: networks, SandboxID: strings.TrimSpace(snapshot.NetworkSettings.SandboxID),
		NetworkExpected: expectedNetwork != "", NetworkConnected: networkConnected,
	}, nil
}

type containerInspectSnapshot struct {
	State struct {
		Status   string `json:"Status"`
		Running  bool   `json:"Running"`
		Paused   bool   `json:"Paused"`
		ExitCode int    `json:"ExitCode"`
		Error    string `json:"Error"`
	} `json:"State"`
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		SandboxID string                     `json:"SandboxID"`
		Networks  map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
}

func (s *Service) inspectContainerSnapshot(ctx context.Context, id string) (containerInspectSnapshot, error) {
	out, err := s.run(ctx, "inspect", "--format", "{{json .}}", id)
	if err != nil {
		return containerInspectSnapshot{}, err
	}
	if strings.TrimSpace(out) == "" {
		return containerInspectSnapshot{}, ErrContainerInspectUnavailable
	}
	var item containerInspectSnapshot
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &item); err != nil {
		return containerInspectSnapshot{}, fmt.Errorf("容器详情响应无效: %w", err)
	}
	if item.NetworkSettings.Networks == nil {
		item.NetworkSettings.Networks = map[string]json.RawMessage{}
	}
	return item, nil
}

func expectedContainerNetwork(networkMode string) string {
	networkMode = strings.TrimSpace(networkMode)
	switch strings.ToLower(networkMode) {
	case "", "default", "bridge", "host", "none":
		return ""
	}
	if strings.HasPrefix(strings.ToLower(networkMode), "container:") {
		return ""
	}
	return networkMode
}

func sortedNetworkNames(networks map[string]json.RawMessage) []string {
	names := make([]string, 0, len(networks))
	for name := range networks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func dockerDiagnosticSummary(message string) string {
	message = redactContainerLogLine(strings.TrimSpace(message))
	message = redactContainerCredentialURL(message)
	message = redactContainerBearerToken(message)
	const maxDiagnosticLength = 4096
	if len(message) > maxDiagnosticLength {
		return message[:maxDiagnosticLength] + "..."
	}
	return message
}

var containerCredentialURLPattern = regexp.MustCompile(`(?i)(https?://)([^/@\s]+):([^/@\s]+)@`)
var containerBearerTokenPattern = regexp.MustCompile(`(?i)(bearer\s+)[^\s,;]+`)

func redactContainerCredentialURL(message string) string {
	return containerCredentialURLPattern.ReplaceAllString(message, "$1[REDACTED]@")
}

func redactContainerBearerToken(message string) string {
	return containerBearerTokenPattern.ReplaceAllString(message, "$1[REDACTED]")
}

func (s *Service) Logs(ctx context.Context, id string, options LogOptions) (string, error) {
	args, err := containerLogArgs(id, options, false)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var output bytes.Buffer
	var runErr error
	for attempt := 0; ; attempt++ {
		output.Reset()
		runErr = s.runContainerLogs(ctx, args, &output)
		if runErr == nil || !retryableContainerLogsError(runErr) || attempt >= containerReadRetryAttempts-1 {
			break
		}
		if err := waitForContainerReadRetry(ctx); err != nil {
			return "", err
		}
	}
	if runErr == nil {
		if strings.TrimSpace(output.String()) == "" {
			if diagnostic, diagnosticErr := s.containerLogDiagnostic(ctx, id, nil); diagnosticErr == nil && diagnostic != "" {
				return diagnostic, nil
			}
		}
		return output.String(), nil
	}
	if errors.Is(runErr, ErrContainerLogsUnavailable) {
		if diagnostic, diagnosticErr := s.containerLogDiagnostic(ctx, id, runErr); diagnosticErr == nil && diagnostic != "" {
			return diagnostic, nil
		}
	}
	return "", runErr
}

func (s *Service) FollowLogs(ctx context.Context, id string, options LogOptions, output io.Writer) error {
	args, err := containerLogArgs(id, options, true)
	if err != nil {
		return err
	}
	if err := s.runContainerLogs(ctx, args, output); err != nil {
		if errors.Is(err, ErrContainerLogsUnavailable) {
			if diagnostic, diagnosticErr := s.containerLogDiagnostic(ctx, id, err); diagnosticErr == nil && diagnostic != "" {
				_, _ = io.WriteString(output, diagnostic)
				return nil
			}
		}
		return err
	}
	return nil
}

func retryableContainerLogsError(err error) bool {
	if err == nil || errors.Is(err, ErrContainerLogsUnavailable) || errors.Is(err, ErrRuntimeUnavailable) || errors.Is(err, ErrDockerCommandTimeout) {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "no such container") ||
		strings.Contains(lower, "no such object") ||
		strings.Contains(lower, "is restarting") ||
		strings.Contains(lower, "container is not running")
}

func (s *Service) containerLogDiagnostic(ctx context.Context, id string, logErr error) (string, error) {
	state, err := s.inspectContainerState(ctx, id)
	if err != nil {
		return "", err
	}
	if state.Error != "" {
		return fmt.Sprintf("容器启动错误：%s\n", redactContainerLogLine(state.Error)), nil
	}
	if state.NetworkExpected && !state.NetworkConnected {
		return fmt.Sprintf("容器网络端点未连接，NetworkSettings.Networks 中没有目标网络。当前状态：%s，NetworkMode：%s，当前网络：[%s]，日志为空是因为进程尚未完成启动。\n", state.Status, state.NetworkMode, strings.Join(state.Networks, ", ")), nil
	}
	if errors.Is(logErr, ErrContainerLogsUnavailable) {
		status := state.Status
		if status == "" {
			status = "未知"
		}
		return fmt.Sprintf("Docker 当前日志驱动不支持读取该容器日志。容器状态：%s，退出码：%d。请检查 Docker 日志驱动配置或主机日志。\n", status, state.ExitCode), nil
	}
	if state.Status == "created" || state.Status == "restarting" || state.Status == "exited" || state.Status == "dead" {
		return fmt.Sprintf("容器在主进程产生标准输出前已结束，当前状态：%s，退出码：%d。日志为空不代表启动成功，请查看启动错误和 Docker 事件。\n", state.Status, state.ExitCode), nil
	}
	return "", nil
}

func containerLogArgs(id string, options LogOptions, follow bool) ([]string, error) {
	id, err := validateReference(id)
	if err != nil {
		return nil, err
	}
	if err := ValidateLogOptions(options); err != nil {
		return nil, err
	}
	args := []string{"logs", "--tail", strconv.Itoa(options.Tail)}
	if options.Timestamps {
		args = append(args, "--timestamps")
	}
	if options.Since != "" {
		args = append(args, "--since", options.Since)
	}
	if options.Until != "" {
		args = append(args, "--until", options.Until)
	}
	if follow {
		args = append(args, "--follow")
	}
	return append(args, id), nil
}

func ValidateLogOptions(options LogOptions) error {
	if options.Tail < 1 || options.Tail > 10000 {
		return fmt.Errorf("%w: tail 必须是 1 到 10000 的整数", ErrInvalidLogOptions)
	}
	var sinceAt time.Time
	if options.Since != "" {
		if duration, err := time.ParseDuration(options.Since); err == nil {
			if duration <= 0 {
				return fmt.Errorf("%w: since 相对时间必须大于 0", ErrInvalidLogOptions)
			}
		} else {
			parsed, parseErr := time.Parse(time.RFC3339, options.Since)
			if parseErr != nil {
				return fmt.Errorf("%w: since 必须是正数时长（如 10m、4h、24h）或带时区的 RFC3339 时间", ErrInvalidLogOptions)
			}
			sinceAt = parsed
		}
	}
	if options.Until != "" {
		untilAt, err := time.Parse(time.RFC3339, options.Until)
		if err != nil {
			return fmt.Errorf("%w: until 必须是带时区的 RFC3339 时间", ErrInvalidLogOptions)
		}
		if !sinceAt.IsZero() && untilAt.Before(sinceAt) {
			return fmt.Errorf("%w: until 不能早于 since", ErrInvalidLogOptions)
		}
	}
	return nil
}

var containerSensitiveLogAssignmentPattern = regexp.MustCompile(`(?i)([[:alnum:]_.-]*(?:password|passwd|passphrase|secret|token|cookie|authorization|api[_-]?key|access[_-]?key|private[_-]?key|client[_-]?secret|credential)[[:alnum:]_.-]*)(["']?[[:space:]]*[:=][[:space:]]*)("[^"\r\n]*"|'[^'\r\n]*'|[^[:space:],;}\]]+)`)

type containerLogRedactingWriter struct {
	mu      sync.Mutex
	target  io.Writer
	pending string
}

func newContainerLogRedactingWriter(target io.Writer) *containerLogRedactingWriter {
	return &containerLogRedactingWriter{target: target}
}

func (writer *containerLogRedactingWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()

	originalLength := len(data)
	writer.pending += string(data)
	for {
		lineEnd := strings.IndexByte(writer.pending, '\n')
		if lineEnd < 0 {
			return originalLength, nil
		}
		if _, err := io.WriteString(writer.target, redactContainerLogLine(writer.pending[:lineEnd+1])); err != nil {
			return 0, err
		}
		writer.pending = writer.pending[lineEnd+1:]
	}
}

func (writer *containerLogRedactingWriter) Flush() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.pending == "" {
		return nil
	}
	_, err := io.WriteString(writer.target, redactContainerLogLine(writer.pending))
	writer.pending = ""
	return err
}

func redactContainerLogLine(line string) string {
	return containerSensitiveLogAssignmentPattern.ReplaceAllStringFunc(line, func(match string) string {
		parts := containerSensitiveLogAssignmentPattern.FindStringSubmatch(match)
		if len(parts) != 4 {
			return "[REDACTED]"
		}
		value := parts[3]
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			return parts[1] + parts[2] + string(value[0]) + "[REDACTED]" + string(value[len(value)-1])
		}
		return parts[1] + parts[2] + "[REDACTED]"
	})
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = buffer.buffer.Write(data)
	}
	return written, nil
}

func (buffer *limitedBuffer) String() string {
	return buffer.buffer.String()
}

func (s *Service) runContainerLogs(ctx context.Context, args []string, output io.Writer) error {
	if _, err := exec.LookPath(s.binary); err != nil {
		return fmt.Errorf("%w: %s executable file not found in PATH", ErrRuntimeUnavailable, s.binary)
	}
	command := exec.CommandContext(ctx, s.binary, args...)
	safeOutput := newContainerLogRedactingWriter(output)
	command.Stdout = safeOutput
	stderr := &limitedBuffer{limit: 8192}
	command.Stderr = io.MultiWriter(safeOutput, stderr)
	runErr := command.Run()
	flushErr := safeOutput.Flush()
	if runErr != nil {
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("%w: docker logs", ErrDockerCommandTimeout)
			}
			return ctx.Err()
		}
		message := redactContainerLogLine(strings.TrimSpace(stderr.String()))
		if message == "" {
			message = runErr.Error()
		}
		lowerMessage := strings.ToLower(message)
		if strings.Contains(lowerMessage, "configured logging driver does not support reading") ||
			(strings.Contains(lowerMessage, "logging driver") && strings.Contains(lowerMessage, "does not support") && strings.Contains(lowerMessage, "read")) {
			return fmt.Errorf("%w: %s", ErrContainerLogsUnavailable, message)
		}
		if strings.Contains(lowerMessage, "cannot connect to the docker daemon") ||
			strings.Contains(lowerMessage, "is the docker daemon running") {
			return fmt.Errorf("%w: %s", ErrRuntimeUnavailable, message)
		}
		return errors.New(message)
	}
	if flushErr != nil {
		return flushErr
	}
	return nil
}

func (s *Service) PullImage(ctx context.Context, reference string) error {
	return s.PullImageStream(ctx, reference, nil)
}

// PullImageStream executes docker pull while forwarding each output line to emit.
// The command remains fixed and the caller controls the bounded context lifetime.
func (s *Service) PullImageStream(ctx context.Context, reference string, emit func(string)) error {
	reference, err := validateReference(reference)
	if err != nil {
		return err
	}
	if err = s.runStreaming(ctx, 30*time.Minute, []string{"pull", reference}, emit); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrImagePullFailed, reference, err)
	}
	return nil
}

func (s *Service) ImageAvailable(ctx context.Context, reference string) (bool, error) {
	reference, err := validateReference(reference)
	if err != nil {
		return false, err
	}
	if _, err := s.run(ctx, "image", "inspect", reference); err == nil {
		return true, nil
	} else if isImageNotFoundError(err) {
		return false, nil
	} else {
		return false, err
	}
}

func (s *Service) ensureImage(ctx context.Context, reference string) error {
	if _, err := s.run(ctx, "image", "inspect", reference); err == nil {
		return nil
	} else if !isImageNotFoundError(err) {
		return err
	}

	// Remote pulls must use the long-running streaming path. The generic Docker
	// command runner is intentionally capped at 60 seconds and is unsuitable for
	// large images or slower test-environment registry links.
	return s.PullImageStream(ctx, reference, nil)
}

func isImageNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such image") ||
		strings.Contains(message, "unable to find image") ||
		strings.Contains(message, "no such object")
}

func (s *Service) RegistryImageReference(registryID uint, imageName, reference string) (string, error) {
	if registryID == 0 {
		return validateReference(reference)
	}
	imageName = strings.TrimSpace(imageName)
	if imageName == "" || strings.ContainsAny(imageName, " \t\r\n") || strings.HasPrefix(imageName, "/") {
		return "", errors.New("镜像名称无效")
	}
	db := app.DB()
	if db == nil {
		return "", errors.New("database is not initialized")
	}
	var registry models.ContainerRegistry
	if err := db.First(&registry, registryID).Error; err != nil {
		return "", fmt.Errorf("镜像仓库不存在: %w", err)
	}
	return validateReference(strings.TrimRight(registry.Address, "/") + "/" + imageName)
}

func validatePushReference(reference string) error {
	parts := strings.Split(reference, "/")
	if len(parts) == 1 {
		return fmt.Errorf("%w: 镜像引用会被 Docker 解析为 docker.io/library/...，请使用 namespace/imagename（可附 :tag）", ErrImageReferenceMissingNamespace)
	}

	first := strings.ToLower(strings.TrimSpace(parts[0]))
	if hasImageRegistryHost(first) && !isDockerHubRegistryHost(first) {
		return nil
	}
	pathParts := parts
	if hasImageRegistryHost(first) {
		pathParts = parts[1:]
	}
	if len(pathParts) < 2 {
		return fmt.Errorf("%w: 镜像引用会被 Docker 解析为 docker.io/library/...，请使用 namespace/imagename（可附 :tag）", ErrImageReferenceMissingNamespace)
	}
	if strings.EqualFold(pathParts[0], "library") {
		return fmt.Errorf("%w: Docker Hub 的 library/%s 通常不是普通账号可推送的目标", ErrRegistryPushPermissionDenied, strings.Join(pathParts[1:], "/"))
	}
	return nil
}

func hasImageRegistryHost(value string) bool {
	return strings.Contains(value, ".") || strings.Contains(value, ":") || strings.EqualFold(value, "localhost")
}

func isDockerHubRegistryHost(value string) bool {
	return strings.EqualFold(value, "docker.io") || strings.EqualFold(value, "index.docker.io") || strings.EqualFold(value, "registry-1.docker.io")
}

func (s *Service) TagImage(ctx context.Context, id, reference string, removeOther, confirm bool) error {
	id, err := validateReference(id)
	if err != nil {
		return err
	}
	reference, err = validateReference(reference)
	if err != nil {
		return err
	}
	if removeOther && !confirm {
		return errors.New("移除其他镜像标签需要 confirm=true")
	}
	if _, err := s.run(ctx, "tag", id, reference); err != nil {
		return err
	}
	if !removeOther {
		return nil
	}
	details, err := s.InspectImage(ctx, id)
	if err != nil {
		return err
	}
	tags, _ := details["RepoTags"].([]any)
	for _, raw := range tags {
		tag, ok := raw.(string)
		if !ok || tag == reference {
			continue
		}
		if _, err := s.run(ctx, "rmi", tag); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) PushImage(ctx context.Context, registryID uint, reference string) error {
	reference, err := validateReference(reference)
	if err != nil {
		return err
	}
	if err := validatePushReference(reference); err != nil {
		return &ImagePushError{Err: err}
	}
	args := []string{"push", reference}
	if registryID != 0 {
		db := app.DB()
		if db == nil {
			return errors.New("database is not initialized")
		}
		var registry models.ContainerRegistry
		if err := db.First(&registry, registryID).Error; err != nil {
			return fmt.Errorf("镜像仓库不存在: %w", err)
		}
		if !registry.AuthEnabled {
			return &ImagePushError{Err: fmt.Errorf("%w: 当前 Registry 未配置认证，只能拉取和测试连通性，不能推送镜像", ErrRegistryNotAuthenticated)}
		}
		if strings.TrimSpace(registry.Username) == "" || strings.TrimSpace(registry.PasswordEnc) == "" {
			return &ImagePushError{Err: ErrRegistryCredentialUnavailable}
		}
		password, decryptErr := utils.DecryptCredential(registry.PasswordEnc, utils.CredentialPurposeRegistryPassword)
		if decryptErr != nil {
			return &ImagePushError{Err: fmt.Errorf("%w: %v", ErrRegistryCredentialUnavailable, decryptErr)}
		}
		configDir, configErr := os.MkdirTemp("", "oneinstack-docker-config-")
		if configErr != nil {
			return &ImagePushError{Err: fmt.Errorf("创建临时 Docker 配置失败: %w", configErr)}
		}
		defer os.RemoveAll(configDir)
		if _, loginErr := s.runWithInput(ctx, time.Minute, password+"\n", "--config", configDir, "login", "--username", registry.Username, "--password-stdin", registry.Address); loginErr != nil {
			return &ImagePushError{Err: fmt.Errorf("镜像仓库登录失败: %w", loginErr)}
		}
		args = []string{"--config", configDir, "push", reference}
	}
	if err := s.runStreaming(ctx, 30*time.Minute, args, nil); err != nil {
		return &ImagePushError{Err: err}
	}
	return nil
}

func (s *Service) LoadImage(ctx context.Context, path string) error {
	path, err := validateLocalFile(path)
	if err != nil {
		return err
	}
	_, err = s.run(ctx, "load", "-i", path)
	return err
}

func (s *Service) ExportImage(ctx context.Context, id string) ([]byte, error) {
	id, err := validateReference(id)
	if err != nil {
		return nil, err
	}
	if _, err := exec.LookPath(s.binary); err != nil {
		return nil, fmt.Errorf("%w: %s executable file not found in PATH", ErrRuntimeUnavailable, s.binary)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, s.binary, "save", id)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	const maxExportSize = int64(4 << 30)
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxExportSize+1))
	waitErr := command.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if int64(len(data)) > maxExportSize {
		return nil, errors.New("镜像导出文件超过4GiB限制")
	}
	if waitErr != nil {
		return nil, waitErr
	}
	return data, nil
}

func (s *Service) BuildImage(ctx context.Context, name, dockerfile, contextPath, dockerfilePath string, labels map[string]string, labelsText string) error {
	return s.BuildImageStream(ctx, name, dockerfile, contextPath, dockerfilePath, labels, labelsText, nil)
}

func (s *Service) BuildImageStream(ctx context.Context, name, dockerfile, contextPath, dockerfilePath string, labels map[string]string, labelsText string, emit func(string)) error {
	name, err := validateReference(name)
	if err != nil {
		return err
	}
	labels, err = parseKeyValueOptions(labels, labelsText)
	if err != nil {
		return fmt.Errorf("镜像标签无效: %w", err)
	}
	args := []string{"build", "-t", name}
	for key, value := range labels {
		key, err = validateLabel(key)
		if err != nil {
			return err
		}
		if strings.ContainsAny(value, "\r\n") {
			return errors.New("镜像标签值无效")
		}
		args = append(args, "--label", key+"="+value)
	}
	if strings.TrimSpace(dockerfile) != "" {
		directory, err := os.MkdirTemp("", "oneinstack-docker-build-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(directory)
		path := filepath.Join(directory, "Dockerfile")
		if err := os.WriteFile(path, []byte(dockerfile), 0600); err != nil {
			return fmt.Errorf("写入 Dockerfile 失败: %w", err)
		}
		args = append(args, "-f", path, directory)
	} else {
		contextPath, err = validateBuildPath(contextPath)
		if err != nil {
			return err
		}
		contextInfo, err := os.Stat(contextPath)
		if err != nil || !contextInfo.IsDir() {
			return errors.New("构建上下文必须是目录")
		}
		if dockerfilePath == "" {
			dockerfilePath = filepath.Join(contextPath, "Dockerfile")
		} else {
			dockerfilePath, err = validateBuildPath(dockerfilePath)
			if err != nil {
				return err
			}
		}
		relative, err := filepath.Rel(contextPath, dockerfilePath)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("Dockerfile 必须位于构建上下文目录内")
		}
		args = append(args, "-f", dockerfilePath, contextPath)
	}
	return s.runStreaming(ctx, 30*time.Minute, args, emit)
}

func (s *Service) DeleteImage(ctx context.Context, reference string, confirm bool) error {
	if !confirm {
		return errors.New("删除镜像需要 confirm=true")
	}
	reference, err := validateReference(reference)
	if err != nil {
		return err
	}
	_, err = s.run(ctx, "rmi", reference)
	return err
}

func (s *Service) ListImages(ctx context.Context) ([]map[string]any, error) {
	return s.linesJSON(ctx, "images", "--format", "{{json .}}")
}

func (s *Service) InspectImage(ctx context.Context, id string) (map[string]any, error) {
	return s.inspectResource(ctx, "image", id)
}

func (s *Service) ListNetworks(ctx context.Context) ([]map[string]any, error) {
	items, err := s.linesJSON(ctx, "network", "ls", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	for index := range items {
		name := stringValue(items[index], "Name")
		if name == "" {
			continue
		}
		if details, inspectErr := s.inspectResource(ctx, "network", name); inspectErr == nil {
			items[index] = networkSummary(items[index], details)
		} else {
			items[index]["health"] = "unhealthy"
			items[index]["healthCode"] = "NETWORK_INSPECT_FAILED"
			items[index]["healthMessage"] = "Docker 网络列表中存在该对象，但网络详情读取失败，无法确认其端点和 IPAM 状态"
			items[index]["connectivityVerified"] = false
		}
		markNetworkDeleteCapability(items[index])
	}
	return items, nil
}

func (s *Service) InspectNetwork(ctx context.Context, id string) (map[string]any, error) {
	details, err := s.inspectResource(ctx, "network", id)
	if err != nil {
		return nil, err
	}
	return addNetworkHealth(details), nil
}

func (s *Service) VerifyContainerNetworks(ctx context.Context, id string, expected []string) error {
	if _, err := validateReference(id); err != nil {
		return err
	}
	snapshot, err := s.inspectContainerSnapshot(ctx, id)
	if err != nil {
		return fmt.Errorf("创建后校验容器网络失败: %w", err)
	}
	missing := make([]string, 0, len(expected))
	for _, network := range expected {
		network, validateErr := validateName(network)
		if validateErr != nil {
			return fmt.Errorf("网络名称无效: %w", validateErr)
		}
		if _, ok := snapshot.NetworkSettings.Networks[network]; !ok {
			missing = append(missing, network)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: Docker 已创建容器，但 NetworkSettings.Networks 未发现目标网络 [%s]，当前网络 [%s]，NetworkMode=%s，SandboxID=%s", ErrContainerNetworkAttachmentFailed, strings.Join(missing, ", "), strings.Join(sortedNetworkNames(snapshot.NetworkSettings.Networks), ", "), strings.TrimSpace(snapshot.HostConfig.NetworkMode), strings.TrimSpace(snapshot.NetworkSettings.SandboxID))
	}
	return nil
}

func (s *Service) ContainerNetworkAction(ctx context.Context, id, network, action string) error {
	if _, err := validateReference(id); err != nil {
		return err
	}
	network, err := validateName(network)
	if err != nil {
		return fmt.Errorf("网络名称无效: %w", err)
	}
	action = strings.ToLower(strings.TrimSpace(action))
	if action != "connect" && action != "disconnect" && action != "reconnect" {
		return fmt.Errorf("不支持的容器网络操作: %s", action)
	}

	attached, err := s.containerNetworkAttached(ctx, id, network)
	if err != nil {
		return fmt.Errorf("读取容器网络状态失败: %w", err)
	}
	if action == "connect" {
		if attached {
			return fmt.Errorf("%w: 容器已连接网络 %s", ErrContainerNetworkAlreadyAttached, network)
		}
		if err := s.runContainerNetworkCommand(ctx, "connect", network, id); err != nil {
			return err
		}
		return s.VerifyContainerNetworks(ctx, id, []string{network})
	}
	if action == "disconnect" {
		if !attached {
			return fmt.Errorf("%w: 容器未连接网络 %s", ErrContainerNetworkNotAttached, network)
		}
		if err := s.runContainerNetworkCommand(ctx, "disconnect", network, id); err != nil {
			return err
		}
		return s.verifyContainerNetwork(ctx, id, network, false)
	}

	if attached {
		if err := s.runContainerNetworkCommand(ctx, "disconnect", network, id); err != nil {
			return err
		}
	}
	if err := s.runContainerNetworkCommand(ctx, "connect", network, id); err != nil {
		return err
	}
	return s.VerifyContainerNetworks(ctx, id, []string{network})
}

func (s *Service) containerNetworkAttached(ctx context.Context, id, network string) (bool, error) {
	snapshot, err := s.inspectContainerSnapshot(ctx, id)
	if err != nil {
		return false, err
	}
	_, ok := snapshot.NetworkSettings.Networks[network]
	return ok, nil
}

func (s *Service) verifyContainerNetwork(ctx context.Context, id, network string, expectedAttached bool) error {
	attached, err := s.containerNetworkAttached(ctx, id, network)
	if err != nil {
		return fmt.Errorf("断开后校验容器网络失败: %w", err)
	}
	if attached != expectedAttached {
		return fmt.Errorf("%w: 网络 %s 当前仍%s在 NetworkSettings.Networks 中", ErrContainerNetworkAttachmentFailed, network, map[bool]string{true: "不在", false: "存在"}[expectedAttached])
	}
	return nil
}

func (s *Service) runContainerNetworkCommand(ctx context.Context, action, network, id string) error {
	args := []string{"network", action}
	if action == "disconnect" {
		args = append(args, "-f")
	}
	args = append(args, network, id)
	if _, err := s.run(ctx, args...); err != nil {
		return fmt.Errorf("Docker 网络%s失败: %w", map[string]string{"connect": "连接", "disconnect": "断开"}[action], err)
	}
	return nil
}

func (s *Service) CreateNetwork(ctx context.Context, request NetworkCreateRequest) error {
	name, err := validateName(request.Name)
	if err != nil {
		return err
	}
	driver := strings.TrimSpace(request.Driver)
	if driver == "" {
		driver = "bridge"
	}
	if _, err := validateName(driver); err != nil {
		return fmt.Errorf("网络驱动无效: %w", err)
	}
	args := []string{"network", "create", "--driver", driver}
	if request.IPv6 {
		args = append(args, "--ipv6")
	}
	for _, item := range []struct {
		flag, value, family string
	}{
		{"--subnet", request.IPv4Subnet, "IPv4"}, {"--gateway", request.IPv4Gateway, "IPv4"}, {"--ip-range", request.IPv4IPRange, "IPv4"},
		{"--subnet", request.IPv6Subnet, "IPv6"}, {"--gateway", request.IPv6Gateway, "IPv6"}, {"--ip-range", request.IPv6IPRange, "IPv6"},
	} {
		if item.value == "" {
			continue
		}
		if strings.Contains(item.value, "/") {
			if err := validateCIDRFamily(item.value, item.family); err != nil {
				return err
			}
		} else if err := validateIPFamily(item.value, item.family); err != nil {
			return err
		}
		args = append(args, item.flag, item.value)
	}
	for label, address := range mergeAuxAddresses(request.IPv4AuxAddresses, request.IPv6AuxAddresses) {
		if _, err := validateName(label); err != nil {
			return fmt.Errorf("辅助地址标签无效: %w", err)
		}
		if net.ParseIP(address) == nil {
			return errors.New("辅助地址 IP 无效")
		}
		args = append(args, "--aux-address", label+"="+address)
	}
	options, err := parseKeyValueOptions(request.Options, request.OptionsText)
	if err != nil {
		return err
	}
	for key, value := range options {
		args = append(args, "--opt", key+"="+value)
	}
	labels, err := parseKeyValueOptions(request.Labels, request.LabelsText)
	if err != nil {
		return err
	}
	for key, value := range labels {
		args = append(args, "--label", key+"="+value)
	}
	args = append(args, name)
	_, err = s.run(ctx, args...)
	return err
}

func (s *Service) DeleteNetwork(ctx context.Context, name string, confirm bool) error {
	if !confirm {
		return errors.New("删除网络需要 confirm=true")
	}
	name, err := validateReference(name)
	if err != nil {
		return err
	}
	switch name {
	case "bridge", "host", "none":
		return ErrProtectedNetwork
	}
	// The list API exposes Docker's short ID, while the built-in network
	// protection is defined by name. Resolve ID references before invoking
	// `network rm`, otherwise bridge/host/none can bypass the guard and only
	// fail later with Docker's generic command error.
	if details, inspectErr := s.inspectResource(ctx, "network", name); inspectErr == nil {
		if isProtectedNetwork(stringValue(details, "Name")) {
			return ErrProtectedNetwork
		}
	}
	_, err = s.run(ctx, "network", "rm", name)
	return err
}

func (s *Service) ListVolumes(ctx context.Context) ([]map[string]any, error) {
	items, err := s.linesJSON(ctx, "volume", "ls", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	for index := range items {
		name := stringValue(items[index], "Name")
		if name == "" {
			continue
		}
		if details, inspectErr := s.inspectResource(ctx, "volume", name); inspectErr == nil {
			for _, key := range []string{"Mountpoint", "Options", "Labels", "Scope"} {
				if value, ok := details[key]; ok {
					items[index][key] = value
				}
			}
		}
	}
	return items, nil
}

func (s *Service) InspectVolume(ctx context.Context, id string) (map[string]any, error) {
	return s.inspectResource(ctx, "volume", id)
}

func (s *Service) CleanupContainers(ctx context.Context, confirm bool) (string, error) {
	if !confirm {
		return "", errors.New("清理容器需要 confirm=true")
	}
	return s.run(ctx, "container", "prune", "--force")
}

func (s *Service) PruneImages(ctx context.Context, confirm bool) (string, error) {
	if !confirm {
		return "", errors.New("清理镜像需要 confirm=true")
	}
	return s.run(ctx, "image", "prune", "--force")
}

func (s *Service) PruneBuildCache(ctx context.Context, confirm bool) (string, error) {
	if !confirm {
		return "", errors.New("清理构建缓存需要 confirm=true")
	}
	return s.run(ctx, "builder", "prune", "--force")
}

func (s *Service) PruneNetworks(ctx context.Context, confirm bool) (string, error) {
	if !confirm {
		return "", errors.New("清理网络需要 confirm=true")
	}
	return s.run(ctx, "network", "prune", "--force")
}

func (s *Service) PruneVolumes(ctx context.Context, confirm bool) (string, error) {
	if !confirm {
		return "", errors.New("清理存储卷需要 confirm=true")
	}
	return s.run(ctx, "volume", "prune", "--force")
}

func (s *Service) CreateVolume(ctx context.Context, request ResourceRequest) error {
	name, err := validateName(request.Name)
	if err != nil {
		return err
	}
	args := []string{"volume", "create"}
	driver := strings.TrimSpace(request.Driver)
	if driver != "" {
		if _, err := validateName(driver); err != nil {
			return fmt.Errorf("存储卷驱动无效: %w", err)
		}
		args = append(args, "--driver", driver)
	}
	options, err := parseKeyValueOptions(request.Options, request.OptionsText)
	if err != nil {
		return err
	}
	if request.NFS {
		if driver != "local" {
			return errors.New("NFS 存储只支持 local 驱动")
		}
		if _, ok := options["type"]; !ok {
			options["type"] = "nfs"
		}
	}
	for key, value := range options {
		args = append(args, "--opt", key+"="+value)
	}
	labels, err := parseKeyValueOptions(request.Labels, request.LabelsText)
	if err != nil {
		return err
	}
	for key, value := range labels {
		args = append(args, "--label", key+"="+value)
	}
	args = append(args, name)
	_, err = s.run(ctx, args...)
	return err
}

func parseKeyValueOptions(values map[string]string, text string) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return nil, errors.New("存储卷参数必须使用 key=value 格式")
		}
		result[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	for key, value := range result {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") || strings.ContainsAny(value, "\r\n") {
			return nil, errors.New("存储卷参数包含无效字符")
		}
	}
	return result, nil
}

func networkSummary(item, details map[string]any) map[string]any {
	for _, key := range []string{"Name", "Id", "Driver", "Scope", "Labels", "Options", "Internal", "EnableIPv6", "Created"} {
		if value, ok := details[key]; ok {
			item[key] = value
		}
	}
	if ipam, ok := details["IPAM"].(map[string]any); ok {
		if configs, ok := ipam["Config"].([]any); ok {
			for _, raw := range configs {
				config, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				subnet, _ := config["Subnet"].(string)
				if strings.Contains(subnet, ":") {
					item["IPv6Subnet"], item["IPv6Gateway"] = subnet, config["Gateway"]
				} else {
					item["Subnet"], item["Gateway"] = subnet, config["Gateway"]
				}
			}
		}
	}
	for key, value := range networkHealthFields(details) {
		item[key] = value
	}
	return item
}

func addNetworkHealth(details map[string]any) map[string]any {
	for key, value := range networkHealthFields(details) {
		details[key] = value
	}
	return details
}

func networkHealthFields(details map[string]any) map[string]any {
	fields := map[string]any{"connectivityVerified": false, "ipamVerified": false}
	if details == nil {
		fields["health"] = "unhealthy"
		fields["healthCode"] = "NETWORK_DETAILS_EMPTY"
		fields["healthMessage"] = "Docker 未返回网络详情"
		return fields
	}
	if strings.TrimSpace(stringValue(details, "Id")) == "" ||
		strings.TrimSpace(stringValue(details, "Name")) == "" ||
		strings.TrimSpace(stringValue(details, "Driver")) == "" {
		fields["health"] = "unhealthy"
		fields["healthCode"] = "NETWORK_METADATA_INCOMPLETE"
		fields["healthMessage"] = "网络对象缺少 ID、名称或驱动信息，不能认为网络可用"
		return fields
	}
	driver := strings.ToLower(strings.TrimSpace(stringValue(details, "Driver")))
	if driver != "host" && driver != "null" && driver != "none" {
		ipam, ipamOK := details["IPAM"].(map[string]any)
		_, configOK := ipam["Config"].([]any)
		if !ipamOK || !configOK {
			fields["health"] = "unhealthy"
			fields["healthCode"] = "NETWORK_IPAM_INCOMPLETE"
			fields["healthMessage"] = "网络对象缺少可解析的 IPAM 配置，不能确认地址分配状态"
			return fields
		}
		fields["ipamVerified"] = true
	}
	containers, ok := details["Containers"].(map[string]any)
	if !ok || len(containers) == 0 {
		fields["health"] = "unknown"
		fields["healthCode"] = "NETWORK_CONNECTIVITY_UNVERIFIED"
		fields["healthMessage"] = "网络对象存在，但当前没有可用于验证连通性的容器端点"
		return fields
	}
	for containerID, raw := range containers {
		endpoint, ok := raw.(map[string]any)
		if !ok || strings.TrimSpace(stringValue(endpoint, "EndpointID")) == "" {
			fields["health"] = "unhealthy"
			fields["healthCode"] = "NETWORK_ENDPOINT_INCOMPLETE"
			fields["healthMessage"] = fmt.Sprintf("容器端点 %s 缺少 EndpointID，网络对象与 Docker 端点状态可能不一致", containerID)
			return fields
		}
	}
	fields["health"] = "healthy"
	fields["healthCode"] = "NETWORK_ENDPOINTS_PRESENT"
	fields["healthMessage"] = "网络对象和已连接容器端点信息完整；实际业务连通性仍需在容器内验证"
	return fields
}

func markNetworkDeleteCapability(item map[string]any) {
	if item == nil {
		return
	}
	if isProtectedNetwork(stringValue(item, "Name")) {
		item["canDelete"] = false
		item["deleteDisabledReason"] = "Docker内置网络不可删除"
		return
	}
	item["canDelete"] = true
}

func isProtectedNetwork(name string) bool {
	switch strings.TrimSpace(name) {
	case "bridge", "host", "none":
		return true
	default:
		return false
	}
}

func validateCIDRFamily(value, family string) error {
	_, network, err := net.ParseCIDR(value)
	if err != nil {
		return fmt.Errorf("%s 子网无效: %w", family, err)
	}
	if (family == "IPv4" && network.IP.To4() == nil) || (family == "IPv6" && network.IP.To4() != nil) {
		return fmt.Errorf("%s 地址族不匹配", family)
	}
	return nil
}

func validateIPFamily(value, family string) error {
	ip := net.ParseIP(value)
	if ip == nil {
		return fmt.Errorf("%s 地址无效", family)
	}
	if (family == "IPv4" && ip.To4() == nil) || (family == "IPv6" && ip.To4() != nil) {
		return fmt.Errorf("%s 地址族不匹配", family)
	}
	return nil
}

func mergeAuxAddresses(first, second map[string]string) map[string]string {
	result := make(map[string]string, len(first)+len(second))
	for key, value := range first {
		result[key] = value
	}
	for key, value := range second {
		result[key] = value
	}
	return result
}

func (s *Service) DeleteVolume(ctx context.Context, name string, confirm bool) error {
	if !confirm {
		return errors.New("删除存储卷需要 confirm=true")
	}
	name, err := validateReference(name)
	if err != nil {
		return err
	}
	_, err = s.run(ctx, "volume", "rm", name)
	return err
}

type ComposeTemplateSummary struct {
	ID          uint      `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type ComposeTemplateDocument struct {
	ComposeTemplateSummary
	Content string `json:"content"`
}

func (s *Service) ListTemplates(ctx context.Context, search string, page, pageSize int) ([]ComposeTemplateSummary, int64, error) {
	db := app.DB()
	if db == nil {
		return nil, 0, errors.New("database is not initialized")
	}
	query := db.Model(&models.ContainerComposeTemplate{})
	if search = strings.TrimSpace(search); search != "" {
		like := "%" + search + "%"
		query = query.Where("name LIKE ? OR description LIKE ?", like, like)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 10
	}
	var records []models.ContainerComposeTemplate
	if err := query.Select("id", "name", "description", "created_at", "updated_at").
		Order("id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&records).Error; err != nil {
		return nil, 0, err
	}
	items := make([]ComposeTemplateSummary, 0, len(records))
	for _, record := range records {
		items = append(items, composeTemplateSummary(record))
	}
	return items, total, nil
}

func (s *Service) GetTemplate(ctx context.Context, id uint) (ComposeTemplateDocument, error) {
	db := app.DB()
	if db == nil {
		return ComposeTemplateDocument{}, errors.New("database is not initialized")
	}
	var record models.ContainerComposeTemplate
	if err := db.First(&record, id).Error; err != nil {
		return ComposeTemplateDocument{}, err
	}
	return ComposeTemplateDocument{ComposeTemplateSummary: composeTemplateSummary(record), Content: record.Content}, nil
}

func (s *Service) CreateTemplate(ctx context.Context, name, description, content string) (ComposeTemplateDocument, error) {
	if err := validateTemplateInput(&name, &description, &content); err != nil {
		return ComposeTemplateDocument{}, err
	}
	db := app.DB()
	if db == nil {
		return ComposeTemplateDocument{}, errors.New("database is not initialized")
	}
	record := models.ContainerComposeTemplate{Name: name, Description: description, Content: content}
	if err := db.Create(&record).Error; err != nil {
		return ComposeTemplateDocument{}, err
	}
	return ComposeTemplateDocument{ComposeTemplateSummary: composeTemplateSummary(record), Content: record.Content}, nil
}

func (s *Service) UpdateTemplate(ctx context.Context, id uint, name, description, content string) (ComposeTemplateDocument, error) {
	if err := validateTemplateInput(&name, &description, &content); err != nil {
		return ComposeTemplateDocument{}, err
	}
	db := app.DB()
	if db == nil {
		return ComposeTemplateDocument{}, errors.New("database is not initialized")
	}
	var record models.ContainerComposeTemplate
	if err := db.First(&record, id).Error; err != nil {
		return ComposeTemplateDocument{}, err
	}
	record.Name, record.Description, record.Content = name, description, content
	if err := db.Save(&record).Error; err != nil {
		return ComposeTemplateDocument{}, err
	}
	return ComposeTemplateDocument{ComposeTemplateSummary: composeTemplateSummary(record), Content: record.Content}, nil
}

func (s *Service) DeleteTemplate(ctx context.Context, id uint, confirm bool) error {
	if !confirm {
		return errors.New("删除编排模板需要 confirm=true")
	}
	db := app.DB()
	if db == nil {
		return errors.New("database is not initialized")
	}
	result := db.Delete(&models.ContainerComposeTemplate{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("编排模板不存在")
	}
	return nil
}

func composeTemplateSummary(record models.ContainerComposeTemplate) ComposeTemplateSummary {
	return ComposeTemplateSummary{ID: record.ID, Name: record.Name, Description: record.Description,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
}

func validateTemplateInput(name, description, content *string) error {
	*name = strings.TrimSpace(*name)
	*description = strings.TrimSpace(*description)
	*content = strings.TrimSpace(*content)
	if *name == "" || len(*name) > 120 {
		return composeError(ErrComposeConfigInvalid, "编排模板名称无效")
	}
	if len(*description) > 255 {
		return composeError(ErrComposeConfigInvalid, "编排模板描述不能超过 255 个字符")
	}
	if *content == "" {
		return composeError(ErrComposeConfigInvalid, "编排模板内容不能为空")
	}
	if len(*content) > 2<<20 {
		return composeError(ErrComposeConfigInvalid, "编排模板内容不能超过 2 MiB")
	}
	if _, err := validateComposeContent(*content, ""); err != nil {
		return err
	}
	return nil
}

func (s *Service) Config(ctx context.Context) (map[string]any, error) {
	runtime := s.Runtime(ctx)
	raw, exists, err := readDockerConfig()
	if err != nil {
		return nil, err
	}
	values, err := parseDockerConfig(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"runtime":    runtime,
		"configPath": dockerConfigPath(),
		"exists":     exists,
		"raw":        raw,
		"basic":      dockerBasicConfig(values),
		"values":     values,
	}, nil
}

func (s *Service) SaveConfig(ctx context.Context, raw string) (map[string]any, error) {
	values, normalized, err := validateDockerConfig(raw)
	if err != nil {
		return nil, err
	}
	return saveDockerConfig(values, normalized)
}

func (s *Service) SaveBasicConfig(ctx context.Context, basic map[string]any) (map[string]any, error) {
	raw, _, err := readDockerConfig()
	if err != nil {
		return nil, err
	}
	values, err := parseDockerConfig(raw)
	if err != nil {
		return nil, err
	}
	if err := applyBasicConfig(values, basic); err != nil {
		return nil, err
	}
	validated, normalized, err := validateDockerConfigMap(values)
	if err != nil {
		return nil, err
	}
	return saveDockerConfig(validated, normalized)
}

func saveDockerConfig(values map[string]any, normalized string) (map[string]any, error) {
	if err := atomicWriteDockerConfig(normalized); err != nil {
		return nil, err
	}
	return map[string]any{
		"configPath": dockerConfigPath(), "raw": normalized,
		"basic": dockerBasicConfig(values), "restartRequired": true,
	}, nil
}

func (s *Service) RuntimeAction(ctx context.Context, action string, confirm bool) error {
	if !confirm {
		return errors.New("Docker服务操作需要 confirm=true")
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "stop", "restart":
		return runDockerServiceAction(ctx, action)
	default:
		return fmt.Errorf("不支持的 Docker 服务操作: %s", action)
	}
}

func dockerConfigPath() string {
	if configured := strings.TrimSpace(os.Getenv("ONEINSTACK_DOCKER_CONFIG_PATH")); configured != "" {
		return filepath.Clean(configured)
	}
	return "/etc/docker/daemon.json"
}

func readDockerConfig() (string, bool, error) {
	content, err := os.ReadFile(dockerConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return "{}\n", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("读取 Docker 配置失败: %w", err)
	}
	if len(content) > 1024*1024 {
		return "", false, errors.New("Docker 配置不能超过 1 MiB")
	}
	return string(content), true, nil
}

func parseDockerConfig(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("Docker 配置 JSON 无效: %w", err)
	}
	if values == nil {
		return nil, errors.New("Docker 配置必须是 JSON 对象")
	}
	return values, nil
}

func validateDockerConfig(raw string) (map[string]any, string, error) {
	values, err := parseDockerConfig(raw)
	if err != nil {
		return nil, "", err
	}
	return validateDockerConfigMap(values)
}

func validateDockerConfigMap(values map[string]any) (map[string]any, string, error) {
	if mirrors, ok := values["registry-mirrors"]; ok {
		if err := validateStringList(mirrors, "registry-mirrors"); err != nil {
			return nil, "", err
		}
	}
	if insecure, ok := values["insecure-registries"]; ok {
		if err := validateStringList(insecure, "insecure-registries"); err != nil {
			return nil, "", err
		}
	}
	for _, key := range []string{"ipv6", "iptables", "live-restore"} {
		if value, ok := values[key]; ok {
			if _, valid := value.(bool); !valid {
				return nil, "", fmt.Errorf("Docker 配置字段 %s 必须是布尔值", key)
			}
		}
	}
	if hosts, ok := values["hosts"]; ok {
		if err := validateStringList(hosts, "hosts"); err != nil {
			return nil, "", err
		}
	}
	normalizedBytes, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return nil, "", fmt.Errorf("序列化 Docker 配置失败: %w", err)
	}
	normalized := string(normalizedBytes) + "\n"
	return values, normalized, nil
}

func applyBasicConfig(values map[string]any, basic map[string]any) error {
	if value, ok := basic["registryMirrors"]; ok {
		values["registry-mirrors"] = normalizeStringList(value)
	}
	if value, ok := basic["insecureRegistries"]; ok {
		values["insecure-registries"] = normalizeStringList(value)
	}
	for field, dockerField := range map[string]string{
		"ipv6": "ipv6", "iptables": "iptables", "liveRestore": "live-restore",
		"logDriver": "log-driver", "logOpts": "log-opts",
	} {
		if value, ok := basic[field]; ok {
			values[dockerField] = value
		}
	}
	if value, ok := basic["socketPath"]; ok {
		socket, valid := value.(string)
		if !valid || strings.TrimSpace(socket) == "" {
			return errors.New("Socket 路径无效")
		}
		values["hosts"] = []any{socket}
	}
	if value, ok := basic["cgroupDriver"]; ok {
		driver, valid := value.(string)
		if !valid || (driver != "cgroups" && driver != "systemd") {
			return errors.New("cgroup driver 只支持 cgroups 或 systemd")
		}
		values["exec-opts"] = []any{"native.cgroupdriver=" + driver}
	}
	return nil
}

func normalizeStringList(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []string:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			result = append(result, item)
		}
		return result
	case string:
		result := make([]any, 0)
		for _, item := range strings.Split(typed, "\n") {
			if item = strings.TrimSpace(item); item != "" {
				result = append(result, item)
			}
		}
		return result
	default:
		return nil
	}
}

func validateStringList(value any, field string) error {
	items, ok := value.([]any)
	if !ok {
		return fmt.Errorf("Docker 配置字段 %s 必须是字符串数组", field)
	}
	for _, item := range items {
		if text, ok := item.(string); !ok || strings.TrimSpace(text) == "" || strings.ContainsAny(text, "\r\n") {
			return fmt.Errorf("Docker 配置字段 %s 包含无效值", field)
		}
	}
	return nil
}

func dockerBasicConfig(values map[string]any) map[string]any {
	result := map[string]any{
		"registryMirrors":    values["registry-mirrors"],
		"insecureRegistries": values["insecure-registries"],
		"ipv6":               values["ipv6"],
		"iptables":           values["iptables"],
		"liveRestore":        values["live-restore"],
		"socketPath":         firstSocket(values["hosts"]),
		"logDriver":          values["log-driver"],
		"logOpts":            values["log-opts"],
		"cgroupDriver":       cgroupDriver(values["exec-opts"]),
	}
	return result
}

func firstSocket(value any) string {
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	for _, item := range items {
		text, _ := item.(string)
		if strings.HasPrefix(text, "unix://") {
			return text
		}
	}
	return ""
}

func cgroupDriver(value any) string {
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	for _, item := range items {
		text, _ := item.(string)
		if strings.HasPrefix(text, "native.cgroupdriver=") {
			return strings.TrimPrefix(text, "native.cgroupdriver=")
		}
	}
	return ""
}

func atomicWriteDockerConfig(content string) error {
	path := dockerConfigPath()
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return fmt.Errorf("创建 Docker 配置目录失败: %w", err)
	}
	if existing, err := os.ReadFile(path); err == nil {
		backup := path + ".bak." + time.Now().Format("20060102150405")
		if err := os.WriteFile(backup, existing, 0600); err != nil {
			return fmt.Errorf("备份 Docker 配置失败: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("读取 Docker 配置失败: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".daemon.json.*")
	if err != nil {
		return fmt.Errorf("创建 Docker 配置临时文件失败: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(content); err != nil {
		temporary.Close()
		return fmt.Errorf("写入 Docker 配置失败: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("同步 Docker 配置失败: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("应用 Docker 配置失败: %w", err)
	}
	return nil
}

func runDockerServiceAction(ctx context.Context, action string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if systemctl, err := exec.LookPath("systemctl"); err == nil {
		command := exec.CommandContext(ctx, systemctl, action, "docker")
		if output, runErr := command.CombinedOutput(); runErr == nil {
			return nil
		} else if len(output) > 0 {
			return errors.New(strings.TrimSpace(string(output)))
		}
	}
	service, err := exec.LookPath("service")
	if err != nil {
		return errors.New("系统未提供 Docker 服务管理器")
	}
	output, err := exec.CommandContext(ctx, service, "docker", action).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return errors.New(message)
	}
	return nil
}

func (s *Service) ListRegistries(ctx context.Context, search string, page, pageSize int) ([]RegistrySummary, int64, error) {
	db := app.DB()
	if db == nil {
		return nil, 0, errors.New("database is not initialized")
	}
	query := db.Model(&models.ContainerRegistry{})
	if search = strings.TrimSpace(search); search != "" {
		like := "%" + search + "%"
		query = query.Where("name LIKE ? OR address LIKE ?", like, like)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 10
	}
	var records []models.ContainerRegistry
	if err := query.Order("id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&records).Error; err != nil {
		return nil, 0, err
	}
	items := make([]RegistrySummary, 0, len(records))
	for _, record := range records {
		items = append(items, registrySummary(record))
	}
	return items, total, nil
}

func (s *Service) CreateRegistry(ctx context.Context, input RegistryInput) (RegistrySummary, error) {
	if err := validateRegistryInput(&input); err != nil {
		return RegistrySummary{}, err
	}
	db := app.DB()
	if db == nil {
		return RegistrySummary{}, errors.New("database is not initialized")
	}
	password, err := encryptRegistryPassword(input.Password, input.AuthEnabled)
	if err != nil {
		return RegistrySummary{}, err
	}
	record := models.ContainerRegistry{Name: input.Name, Address: input.Address, Protocol: input.Protocol,
		AuthEnabled: input.AuthEnabled, Username: input.Username, PasswordEnc: password, Status: models.RegistryStatusUnknown}
	if err := db.Create(&record).Error; err != nil {
		return RegistrySummary{}, err
	}
	return registrySummary(record), nil
}

func (s *Service) UpdateRegistry(ctx context.Context, id uint, input RegistryInput) (RegistrySummary, error) {
	if err := validateRegistryInput(&input); err != nil {
		return RegistrySummary{}, err
	}
	db := app.DB()
	if db == nil {
		return RegistrySummary{}, errors.New("database is not initialized")
	}
	var record models.ContainerRegistry
	if err := db.First(&record, id).Error; err != nil {
		return RegistrySummary{}, err
	}
	var err error
	if input.Password == "" && input.AuthEnabled && record.AuthEnabled {
		input.Password = record.PasswordEnc
	}
	password := input.Password
	if input.Password != record.PasswordEnc {
		password, err = encryptRegistryPassword(input.Password, input.AuthEnabled)
		if err != nil {
			return RegistrySummary{}, err
		}
	}
	record.Name, record.Address, record.Protocol = input.Name, input.Address, input.Protocol
	record.AuthEnabled, record.Username, record.PasswordEnc = input.AuthEnabled, input.Username, password
	if !input.AuthEnabled {
		record.Username, record.PasswordEnc = "", ""
	}
	if err := db.Save(&record).Error; err != nil {
		return RegistrySummary{}, err
	}
	return registrySummary(record), nil
}

func (s *Service) DeleteRegistry(ctx context.Context, id uint, confirm bool) error {
	if !confirm {
		return errors.New("删除仓库需要 confirm=true")
	}
	db := app.DB()
	if db == nil {
		return errors.New("database is not initialized")
	}
	var record models.ContainerRegistry
	if err := db.First(&record, id).Error; err != nil {
		return err
	}
	if record.Name == "Docker Hub" {
		return errors.New("默认 Docker Hub 不允许删除")
	}
	return db.Delete(&record).Error
}

func (s *Service) TestRegistry(ctx context.Context, id uint) (RegistrySummary, error) {
	db := app.DB()
	if db == nil {
		return RegistrySummary{}, errors.New("database is not initialized")
	}
	var record models.ContainerRegistry
	if err := db.First(&record, id).Error; err != nil {
		return RegistrySummary{}, err
	}
	endpoint := registryProbeEndpoint(record)
	client := &http.Client{Timeout: 15 * time.Second}
	password := ""
	if record.AuthEnabled {
		var decryptErr error
		password, decryptErr = utils.DecryptCredential(record.PasswordEnc, utils.CredentialPurposeRegistryPassword)
		if decryptErr != nil {
			return RegistrySummary{}, errors.New("仓库凭据解密失败")
		}
	}
	probeErr := probeRegistry(ctx, client, endpoint, record, password)
	if probeErr != nil {
		result, updateErr := s.updateRegistryStatus(record, models.RegistryStatusFailed, registryProbeStatusMessage(probeErr))
		if updateErr != nil {
			return RegistrySummary{}, updateErr
		}
		return result, fmt.Errorf("%w: %v", ErrRegistryProbeFailed, probeErr)
	}
	statusMessage := ""
	if !record.AuthEnabled {
		statusMessage = "Registry 连通性测试成功，但未配置认证；仅支持拉取和测试，不代表具备推送权限。"
	}
	return s.updateRegistryStatus(record, models.RegistryStatusSuccess, statusMessage)
}

type registryAuthChallenge struct {
	Scheme string
	Params map[string]string
}

type registryTokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
}

var registryAuthParamPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9_-]*)\s*=\s*("(?:\\.|[^"])*"|[^,\s]+)`)

func probeRegistry(ctx context.Context, client *http.Client, endpoint string, record models.ContainerRegistry, password string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("仓库地址无效")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	status := response.StatusCode
	challenge := parseRegistryAuthChallenge(response.Header.Get("WWW-Authenticate"))
	_ = response.Body.Close()

	if status >= 200 && status < 400 {
		return nil
	}
	if status != http.StatusUnauthorized {
		return fmt.Errorf("仓库返回 HTTP %d", status)
	}
	if !record.AuthEnabled {
		if challenge.Scheme != "" {
			return nil
		}
		return errors.New("仓库返回 HTTP 401")
	}
	if strings.TrimSpace(record.Username) == "" || strings.TrimSpace(password) == "" {
		return errors.New("仓库认证失败: HTTP 401")
	}
	if challenge.Scheme == "" {
		return errors.New("仓库认证失败: HTTP 401")
	}

	switch challenge.Scheme {
	case "bearer":
		token, tokenErr := requestRegistryBearerToken(ctx, client, challenge, record.Username, password)
		if tokenErr != nil {
			return tokenErr
		}
		return probeRegistryWithBearer(ctx, client, endpoint, token)
	case "basic":
		return probeRegistryWithBasicAuth(ctx, client, endpoint, record.Username, password)
	default:
		return fmt.Errorf("仓库认证方式不受支持: %s", challenge.Scheme)
	}
}

func parseRegistryAuthChallenge(header string) registryAuthChallenge {
	header = strings.TrimSpace(header)
	if header == "" {
		return registryAuthChallenge{}
	}
	schemeEnd := strings.IndexAny(header, " \t")
	if schemeEnd < 0 {
		return registryAuthChallenge{Scheme: strings.ToLower(header)}
	}
	challenge := registryAuthChallenge{Scheme: strings.ToLower(strings.TrimSpace(header[:schemeEnd])), Params: map[string]string{}}
	for _, match := range registryAuthParamPattern.FindAllStringSubmatch(header[schemeEnd:], -1) {
		value := match[2]
		if strings.HasPrefix(value, `"`) {
			if unquoted, err := strconv.Unquote(value); err == nil {
				value = unquoted
			} else {
				value = strings.Trim(value, `"`)
			}
		}
		challenge.Params[strings.ToLower(match[1])] = value
	}
	return challenge
}

func requestRegistryBearerToken(ctx context.Context, client *http.Client, challenge registryAuthChallenge, username, password string) (string, error) {
	realm := strings.TrimSpace(challenge.Params["realm"])
	if realm == "" {
		return "", errors.New("仓库认证服务地址缺失")
	}
	tokenURL, err := url.Parse(realm)
	if err != nil || tokenURL.Scheme == "" || tokenURL.Host == "" {
		return "", errors.New("仓库认证服务地址无效")
	}
	query := tokenURL.Query()
	if service := strings.TrimSpace(challenge.Params["service"]); service != "" {
		query.Set("service", service)
	}
	if scope := strings.TrimSpace(challenge.Params["scope"]); scope != "" {
		query.Set("scope", scope)
	}
	tokenURL.RawQuery = query.Encode()
	tokenRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL.String(), nil)
	if err != nil {
		return "", errors.New("仓库认证请求无效")
	}
	tokenRequest.SetBasicAuth(username, password)
	tokenResponse, err := client.Do(tokenRequest)
	if err != nil {
		return "", fmt.Errorf("仓库认证服务请求失败: %w", err)
	}
	defer tokenResponse.Body.Close()
	if tokenResponse.StatusCode < 200 || tokenResponse.StatusCode >= 300 {
		return "", fmt.Errorf("仓库认证服务返回 HTTP %d", tokenResponse.StatusCode)
	}
	var payload registryTokenResponse
	if err := json.NewDecoder(io.LimitReader(tokenResponse.Body, 1<<20)).Decode(&payload); err != nil {
		return "", errors.New("仓库认证服务响应无效")
	}
	token := strings.TrimSpace(payload.Token)
	if token == "" {
		token = strings.TrimSpace(payload.AccessToken)
	}
	if token == "" {
		return "", errors.New("仓库认证服务未返回有效令牌")
	}
	return token, nil
}

func probeRegistryWithBearer(ctx context.Context, client *http.Client, endpoint, token string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("仓库地址无效")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 400 {
		return nil
	}
	return fmt.Errorf("仓库认证后返回 HTTP %d", response.StatusCode)
}

func probeRegistryWithBasicAuth(ctx context.Context, client *http.Client, endpoint, username, password string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("仓库地址无效")
	}
	request.SetBasicAuth(username, password)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 400 {
		return nil
	}
	return fmt.Errorf("仓库认证后返回 HTTP %d", response.StatusCode)
}

func registryProbeStatusMessage(err error) string {
	lower := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(lower, "no such host"), strings.Contains(lower, "server misbehaving"):
		return "仓库域名解析失败，请检查仓库地址和面板服务器 DNS 配置"
	case strings.Contains(lower, "connection refused"):
		return "仓库拒绝连接，请检查仓库服务、端口和防火墙配置"
	case strings.Contains(lower, "network is unreachable"), strings.Contains(lower, "no route to host"):
		return "仓库网络不可达，请检查面板服务器的网络和路由配置"
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "timed out"), strings.Contains(lower, "deadline exceeded"):
		return "仓库连接超时，请检查网络连通性和仓库服务状态"
	case strings.Contains(lower, "x509"), strings.Contains(lower, "certificate"):
		return "仓库 TLS 证书校验失败，请检查证书有效期、域名和信任链"
	case strings.Contains(lower, "仓库认证服务地址缺失"), strings.Contains(lower, "仓库认证服务地址无效"), strings.Contains(lower, "仓库认证方式不受支持"):
		return "仓库认证协议配置无效，请确认该地址支持 Docker Registry V2 API"
	case strings.Contains(lower, "仓库认证服务响应无效"), strings.Contains(lower, "仓库认证服务未返回有效令牌"):
		return "仓库认证服务响应无效，请检查 Registry 认证配置"
	case strings.Contains(lower, "http 401"):
		return "仓库身份认证失败，请检查认证配置和凭据"
	case strings.Contains(lower, "http 403"):
		return "仓库拒绝当前访问，请检查账号权限和访问策略"
	case strings.Contains(lower, "仓库返回 http"):
		return "仓库接口返回异常状态，请确认该地址支持 Docker Registry V2 API"
	default:
		return "仓库连接测试失败，请检查仓库地址、网络和服务状态"
	}
}

func registryProbeEndpoint(record models.ContainerRegistry) string {
	address := strings.TrimRight(record.Address, "/")
	// docker.io is the image-reference namespace. Docker Hub serves the Registry
	// V2 API from registry-1.docker.io; probing docker.io follows the website path
	// instead of checking the registry used by Docker pulls.
	if isDockerHubRegistryHost(address) {
		address = "registry-1.docker.io"
	}
	return record.Protocol + "://" + address + "/v2/"
}

func (s *Service) updateRegistryStatus(record models.ContainerRegistry, status, message string) (RegistrySummary, error) {
	now := time.Now()
	db := app.DB()
	if err := db.Model(&record).Updates(map[string]any{"status": status, "status_message": message, "last_checked_at": now}).Error; err != nil {
		return RegistrySummary{}, err
	}
	record.Status, record.StatusMessage, record.LastCheckedAt = status, message, &now
	return registrySummary(record), nil
}

func registrySummary(record models.ContainerRegistry) RegistrySummary {
	statusMessage := record.StatusMessage
	if !record.AuthEnabled && strings.TrimSpace(statusMessage) == "" {
		statusMessage = "Registry 未配置认证；仅支持拉取和测试，不代表具备推送权限。"
	}
	return RegistrySummary{ID: record.ID, Name: record.Name, Address: record.Address, Protocol: record.Protocol,
		AuthEnabled: record.AuthEnabled, Username: record.Username, Status: record.Status, StatusMessage: statusMessage,
		LastCheckedAt: record.LastCheckedAt, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
}

func encryptRegistryPassword(password string, enabled bool) (string, error) {
	if !enabled {
		return "", nil
	}
	if strings.TrimSpace(password) == "" {
		return "", errors.New("启用认证时密码不能为空")
	}
	return utils.EncryptCredential(password, utils.CredentialPurposeRegistryPassword)
}

func validateRegistryInput(input *RegistryInput) error {
	input.Name = strings.TrimSpace(input.Name)
	input.Address = strings.TrimSpace(input.Address)
	input.Protocol = strings.ToLower(strings.TrimSpace(input.Protocol))
	if input.Name == "" || len(input.Name) > 120 {
		return registryValidationError("仓库名称无效")
	}
	if input.Protocol != "http" && input.Protocol != "https" {
		return registryValidationError("仓库协议只支持 http 或 https")
	}
	if input.Address == "" || strings.ContainsAny(input.Address, " \\\r\n") {
		return registryValidationError("仓库地址无效")
	}
	if strings.Contains(input.Address, "://") {
		addressURL, err := url.Parse(input.Address)
		if err != nil || !strings.EqualFold(addressURL.Scheme, input.Protocol) {
			return registryValidationError("仓库地址协议必须与 protocol 一致")
		}
		if addressURL.Host == "" || addressURL.Path != "" && addressURL.Path != "/" || addressURL.RawQuery != "" || addressURL.Fragment != "" || addressURL.User != nil {
			return registryValidationError("仓库地址无效")
		}
		input.Address = addressURL.Host
	}
	input.Address = strings.TrimRight(input.Address, "/")
	parsed, err := url.Parse(input.Protocol + "://" + input.Address)
	if err != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return registryValidationError("仓库地址无效")
	}
	if input.AuthEnabled {
		input.Username = strings.TrimSpace(input.Username)
		if input.Username == "" || len(input.Username) > 128 {
			return registryValidationError("仓库用户名无效")
		}
	}
	return nil
}

func registryValidationError(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidRegistryInput, message)
}

func (s *Service) inspectResource(ctx context.Context, kind, id string) (map[string]any, error) {
	id, err := validateReference(id)
	if err != nil {
		return nil, err
	}
	out, err := s.run(ctx, kind, "inspect", id)
	if err != nil {
		return nil, err
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &items); err != nil || len(items) == 0 {
		return nil, fmt.Errorf("%s详情响应无效", kind)
	}
	return items[0], nil
}

func (s *Service) run(ctx context.Context, args ...string) (string, error) {
	return s.runWithTimeout(ctx, 60*time.Second, args...)
}

func (s *Service) runWithTimeout(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	return s.runCommand(ctx, timeout, nil, args...)
}

func (s *Service) runWithInput(ctx context.Context, timeout time.Duration, input string, args ...string) (string, error) {
	return s.runCommand(ctx, timeout, strings.NewReader(input), args...)
}

func (s *Service) runCommand(ctx context.Context, timeout time.Duration, input io.Reader, args ...string) (string, error) {
	if _, err := exec.LookPath(s.binary); err != nil {
		return "", fmt.Errorf("%w: %s executable file not found in PATH", ErrRuntimeUnavailable, s.binary)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, s.binary, args...)
	if input != nil {
		command.Stdin = input
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("%w: docker %s", ErrDockerCommandTimeout, strings.Join(args, " "))
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		if strings.Contains(strings.ToLower(message), "cannot connect to the docker daemon") ||
			strings.Contains(strings.ToLower(message), "is the docker daemon running") {
			return "", fmt.Errorf("%w: %s", ErrRuntimeUnavailable, message)
		}
		return "", errors.New(message)
	}
	return stdout.String(), nil
}

type streamLineWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	all  bytes.Buffer
	emit func(string)
}

func (w *streamLineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.buf.Write(p)
	_, _ = w.all.Write(p)
	for {
		data := w.buf.Bytes()
		delimiter := -1
		for index, value := range data {
			if value == '\n' || value == '\r' {
				delimiter = index
				break
			}
		}
		if delimiter < 0 {
			break
		}
		line := string(data[:delimiter])
		consume := delimiter + 1
		if data[delimiter] == '\r' && consume < len(data) && data[consume] == '\n' {
			consume++
		}
		w.buf.Next(consume)
		line = strings.TrimSpace(line)
		if line != "" && w.emit != nil {
			w.emit(line)
		}
	}
	return len(p), nil
}

func (w *streamLineWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return normalizeStreamOutput(w.all.String())
}

func (w *streamLineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	line := strings.TrimSpace(w.buf.String())
	w.buf.Reset()
	if line != "" && w.emit != nil {
		w.emit(line)
	}
}

func normalizeStreamOutput(output string) string {
	return strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(output)
}

func (s *Service) runStreaming(ctx context.Context, timeout time.Duration, args []string, emit func(string)) error {
	if _, err := exec.LookPath(s.binary); err != nil {
		return fmt.Errorf("%w: %s executable file not found in PATH", ErrRuntimeUnavailable, s.binary)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, s.binary, args...)
	var emitMu sync.Mutex
	streamEmit := func(line string) {
		if emit == nil {
			return
		}
		emitMu.Lock()
		defer emitMu.Unlock()
		emit(line)
	}
	var stdout, stderr streamLineWriter
	stdout.emit = streamEmit
	stderr.emit = streamEmit
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		stdout.Flush()
		stderr.Flush()
		if ctx.Err() != nil {
			return fmt.Errorf("%w: docker %s", ErrDockerCommandTimeout, strings.Join(args, " "))
		}
		message := strings.TrimSpace(strings.Join([]string{stdout.String(), stderr.String()}, "\n"))
		if message == "" {
			message = err.Error()
		}
		lowerMessage := strings.ToLower(message)
		if strings.Contains(lowerMessage, "cannot connect to the docker daemon") ||
			strings.Contains(lowerMessage, "is the docker daemon running") {
			return fmt.Errorf("%w: %s", ErrRuntimeUnavailable, message)
		}
		return errors.New(message)
	}
	stdout.Flush()
	stderr.Flush()
	return nil
}

func (s *Service) linesJSON(ctx context.Context, args ...string) ([]map[string]any, error) {
	out, err := s.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0)
	scanner := bufio.NewScanner(strings.NewReader(out))
	// Docker's JSON format includes fields such as labels, mounts, and
	// networks. A single container can therefore exceed Scanner's 64 KiB
	// default token limit and make the whole initial resource list fail.
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func validateName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.ContainsAny(value, " \t\r\n/\\") {
		return "", errors.New("名称无效")
	}
	return value, nil
}

func validateReference(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || strings.ContainsAny(value, "\r\n") || strings.HasPrefix(value, "-") {
		return "", errors.New("Docker资源标识无效")
	}
	return value, nil
}

func validateLocalFile(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("本地文件路径无效")
	}
	info, err := os.Stat(value)
	if err != nil {
		return "", fmt.Errorf("读取本地文件失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("本地文件路径必须指向普通文件")
	}
	return value, nil
}

func validateBuildPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("构建路径无效")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("解析构建路径失败: %w", err)
	}
	root, err := filepath.Abs(strings.TrimSpace(os.Getenv("ONEINSTACK_DOCKER_BUILD_ROOT")))
	if strings.TrimSpace(os.Getenv("ONEINSTACK_DOCKER_BUILD_ROOT")) == "" {
		root, err = filepath.Abs(app.GetBasePath())
	}
	if err != nil {
		return "", fmt.Errorf("解析构建根目录失败: %w", err)
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("构建路径超出授权目录")
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("读取构建路径失败: %w", err)
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return "", errors.New("构建路径必须是目录或普通文件")
	}
	return absolute, nil
}

func validateRestart(value string) error {
	if value == "no" || value == "always" || value == "unless-stopped" || value == "on-failure" || strings.HasPrefix(value, "on-failure:") {
		return nil
	}
	return errors.New("重启策略无效")
}

func validateMountSource(mount Mount) (string, string, error) {
	mountType := strings.ToLower(strings.TrimSpace(mount.Type))
	source := strings.TrimSpace(mount.Source)
	if source == "" || strings.ContainsAny(source, "\r\n") {
		return "", "", errors.New("挂载源不能为空，且不能包含换行符")
	}
	if mountType == "" {
		// The bundled container form historically omitted the selected mount
		// type from its JSON payload. Preserve absolute-path bind mounts while
		// treating a valid non-absolute source name as a named volume.
		mountType = MountTypeBind
		if !filepath.IsAbs(source) {
			if _, err := validateName(source); err == nil {
				mountType = MountTypeVolume
			}
		}
	}

	switch mountType {
	case MountTypeBind:
		if !filepath.IsAbs(source) {
			return "", "", errors.New("type=bind 时挂载源必须是 Docker 主机上的绝对路径")
		}
	case MountTypeVolume:
		if _, err := validateName(source); err != nil {
			return "", "", fmt.Errorf("命名卷名称无效：%w", err)
		}
	default:
		return "", "", fmt.Errorf("挂载类型 %q 无效，只支持 bind 或 volume", mount.Type)
	}

	return mountType, source, nil
}

func validateMountTarget(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("挂载目标必须是绝对路径")
	}
	return value, nil
}

func validateLabel(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("Label名称无效")
	}
	return value, nil
}

func validateEnvKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "=\r\n") {
		return "", errors.New("环境变量名称无效")
	}
	return value, nil
}

func validIPv4(value string) bool {
	ip := net.ParseIP(strings.TrimSpace(value))
	return ip != nil && ip.To4() != nil
}

func validIPv6(value string) bool {
	ip := net.ParseIP(strings.TrimSpace(value))
	return ip != nil && ip.To4() == nil
}

func validIP(value string) bool {
	return net.ParseIP(strings.TrimSpace(value)) != nil
}

func stringValue(item map[string]any, key string) string {
	value, _ := item[key].(string)
	return value
}

func sanitizeInspect(item map[string]any) map[string]any {
	result := make(map[string]any)
	for _, key := range []string{"Id", "Name", "Created", "Path", "Args", "State", "Image", "ResolvConfPath", "HostnamePath", "HostsPath", "LogPath", "Name", "Mounts", "Config", "NetworkSettings"} {
		if value, ok := item[key]; ok {
			result[key] = value
		}
	}
	if config, ok := result["Config"].(map[string]any); ok {
		delete(config, "Env")
		result["Config"] = config
	}
	return result
}

func cleanError(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}
