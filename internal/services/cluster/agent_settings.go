package cluster

import (
	"context"
	"errors"
	"log"
	"net/url"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/config"
)

const (
	ClusterRoleUnconfigured = "unconfigured"
	ClusterRoleController   = "controller"
	ClusterRoleNode         = "node"

	defaultAgentIntervalSeconds = 30
	defaultAgentTimeoutSeconds  = 10
)

// NormalizeControllerURL cleans up user-provided controller URLs by trimming
// whitespace, trailing slashes, and the common misconfiguration of appending
// "/v1". The agent endpoints live at /cluster/agent/* (without /v1 prefix),
// so including /v1 in the controllerUrl causes 404 errors. Returns the
// normalized URL and whether a /v1 suffix was stripped.
//
// Only strips "/v1" when it appears as the sole path component (e.g.,
// "http://host:8089/v1" -> "http://host:8089"). Does not strip "/v1" from
// longer paths like "/api/v1" to avoid breaking intentional configurations.
func NormalizeControllerURL(raw string) (string, bool) {
	result := strings.TrimRight(strings.TrimSpace(raw), "/")
	stripped := false

	parsed, err := url.Parse(result)
	if err == nil && (parsed.Path == "/v1" || parsed.Path == "v1") {
		parsed.Path = ""
		parsed.RawPath = ""
		result = parsed.String()
		stripped = true
	}

	return result, stripped
}

var (
	ErrClusterRoleInvalid         = errors.New("cluster role must be controller or node")
	ErrClusterRoleAlreadySelected = errors.New("cluster role has already been selected")
	ErrClusterNodeRoleRequired    = errors.New("cluster node role is required")
)

type AgentSettings struct {
	Role              string             `json:"role"`
	Selected          bool               `json:"selected"`
	Enabled           bool               `json:"enabled"`
	ControllerURL     string             `json:"controllerUrl"`
	TokenConfigured   bool               `json:"tokenConfigured"`
	IntervalSeconds   int                `json:"intervalSeconds"`
	RequestTimeoutSec int                `json:"requestTimeoutSeconds"`
	Runtime           AgentRuntimeStatus `json:"runtime"`
	Notices           []string           `json:"notices,omitempty"`
}

type UpdateAgentSettingsInput struct {
	ControllerURL     string `json:"controllerUrl"`
	Token             string `json:"token"`
	IntervalSeconds   int    `json:"intervalSeconds"`
	RequestTimeoutSec int    `json:"requestTimeoutSeconds"`
}

type SelectClusterRoleInput struct {
	Role string `json:"role"`
}

func EffectiveClusterRole(cfg config.ClusterAgent) string {
	role := strings.ToLower(strings.TrimSpace(cfg.Role))
	if role == ClusterRoleController || role == ClusterRoleNode {
		return role
	}
	// Backward compatibility: an old enabled agent configuration was always a node.
	if cfg.Enabled {
		return ClusterRoleNode
	}
	return ClusterRoleUnconfigured
}

func normalizeAgentDefaults(interval, timeout int) (int, int) {
	if interval <= 0 {
		interval = defaultAgentIntervalSeconds
	}
	if timeout <= 0 {
		timeout = defaultAgentTimeoutSeconds
	}
	return interval, timeout
}

func GetAgentSettings() AgentSettings {
	cfg := app.ONE_CONFIG.ClusterAgent
	role := EffectiveClusterRole(cfg)
	interval, timeout := normalizeAgentDefaults(cfg.IntervalSeconds, cfg.RequestTimeoutSec)
	return AgentSettings{
		Role:              role,
		Selected:          role != ClusterRoleUnconfigured,
		Enabled:           role == ClusterRoleNode && cfg.Enabled,
		ControllerURL:     cfg.ControllerURL,
		TokenConfigured:   strings.TrimSpace(cfg.Token) != "",
		IntervalSeconds:   interval,
		RequestTimeoutSec: timeout,
		Runtime:           AgentRuntimeSnapshot(role, cfg.Enabled),
	}
}

func SelectClusterRole(input SelectClusterRoleInput) (AgentSettings, error) {
	role := strings.ToLower(strings.TrimSpace(input.Role))
	if role != ClusterRoleController && role != ClusterRoleNode {
		return AgentSettings{}, ErrClusterRoleInvalid
	}
	if EffectiveClusterRole(app.ONE_CONFIG.ClusterAgent) != ClusterRoleUnconfigured {
		return AgentSettings{}, ErrClusterRoleAlreadySelected
	}
	if err := app.PersistClusterAgentConfig(role, false, "", "", defaultAgentIntervalSeconds, defaultAgentTimeoutSeconds); err != nil {
		return AgentSettings{}, err
	}
	ResetAgentRuntime()
	return GetAgentSettings(), nil
}

func ResetClusterRole() (AgentSettings, error) {
	previous := app.ONE_CONFIG.ClusterAgent
	if err := app.PersistClusterAgentConfig(ClusterRoleUnconfigured, false, "", "", defaultAgentIntervalSeconds, defaultAgentTimeoutSeconds); err != nil {
		return AgentSettings{}, err
	}
	stopActiveAgent()
	notifyControllerOffline(previous)
	ResetAgentRuntime()
	return GetAgentSettings(), nil
}

func notifyControllerOffline(cfg config.ClusterAgent) {
	if EffectiveClusterRole(cfg) != ClusterRoleNode || !cfg.Enabled || strings.TrimSpace(cfg.ControllerURL) == "" || strings.TrimSpace(cfg.Token) == "" {
		return
	}
	timeout := time.Duration(cfg.RequestTimeoutSec) * time.Second
	if timeout <= 0 || timeout > 3*time.Second {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	agent, err := NewAgent(AgentConfig{ControllerURL: cfg.ControllerURL, Token: cfg.Token, Interval: time.Duration(cfg.IntervalSeconds) * time.Second, RequestTimeout: timeout})
	if err == nil {
		err = agent.offline(ctx)
	}
	if err != nil {
		log.Printf("cluster agent offline notification failed: %v", err)
	}
}

func UpdateAgentSettings(input UpdateAgentSettingsInput) (AgentSettings, error) {
	if EffectiveClusterRole(app.ONE_CONFIG.ClusterAgent) != ClusterRoleNode {
		return AgentSettings{}, ErrClusterNodeRoleRequired
	}
	controllerURL, v1Stripped := NormalizeControllerURL(input.ControllerURL)
	token := strings.TrimSpace(input.Token)
	if token == "" {
		token = strings.TrimSpace(app.ONE_CONFIG.ClusterAgent.Token)
	}
	if controllerURL == "" {
		return AgentSettings{}, errors.New("controllerUrl is required")
	}
	parsed, err := url.Parse(controllerURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return AgentSettings{}, errors.New("controllerUrl must be a valid http or https URL")
	}
	if token == "" {
		return AgentSettings{}, errors.New("token is required")
	}
	interval, timeout := normalizeAgentDefaults(input.IntervalSeconds, input.RequestTimeoutSec)
	if interval < 5 || interval > 3600 {
		return AgentSettings{}, errors.New("intervalSeconds must be between 5 and 3600")
	}
	if timeout < 1 || timeout > 120 {
		return AgentSettings{}, errors.New("requestTimeoutSeconds must be between 1 and 120")
	}
	if err := app.PersistClusterAgentConfig(ClusterRoleNode, true, controllerURL, token, interval, timeout); err != nil {
		return AgentSettings{}, err
	}
	settings := GetAgentSettings()
	if v1Stripped {
		settings.Notices = append(settings.Notices, "controllerUrl 中的 /v1 后缀已自动移除。Agent 端点无需 /v1 前缀。")
	}
	return settings, nil
}
