package cluster

import (
	"errors"
	"net/url"
	"strings"

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
	if err := app.PersistClusterAgentConfig(ClusterRoleUnconfigured, false, "", "", defaultAgentIntervalSeconds, defaultAgentTimeoutSeconds); err != nil {
		return AgentSettings{}, err
	}
	stopActiveAgent()
	ResetAgentRuntime()
	return GetAgentSettings(), nil
}

func UpdateAgentSettings(input UpdateAgentSettingsInput) (AgentSettings, error) {
	if EffectiveClusterRole(app.ONE_CONFIG.ClusterAgent) != ClusterRoleNode {
		return AgentSettings{}, ErrClusterNodeRoleRequired
	}
	controllerURL := strings.TrimRight(strings.TrimSpace(input.ControllerURL), "/")
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
	return GetAgentSettings(), nil
}
