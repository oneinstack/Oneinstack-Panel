package cluster

import (
	"errors"
	"net/url"
	"strings"

	"oneinstack/app"
)

type AgentSettings struct {
	Enabled           bool   `json:"enabled"`
	ControllerURL     string `json:"controllerUrl"`
	TokenConfigured   bool   `json:"tokenConfigured"`
	IntervalSeconds   int    `json:"intervalSeconds"`
	RequestTimeoutSec int    `json:"requestTimeoutSeconds"`
}

type UpdateAgentSettingsInput struct {
	Enabled           bool   `json:"enabled"`
	ControllerURL     string `json:"controllerUrl"`
	Token             string `json:"token"`
	IntervalSeconds   int    `json:"intervalSeconds"`
	RequestTimeoutSec int    `json:"requestTimeoutSeconds"`
}

func GetAgentSettings() AgentSettings {
	cfg := app.ONE_CONFIG.ClusterAgent
	return AgentSettings{Enabled: cfg.Enabled, ControllerURL: cfg.ControllerURL, TokenConfigured: strings.TrimSpace(cfg.Token) != "", IntervalSeconds: cfg.IntervalSeconds, RequestTimeoutSec: cfg.RequestTimeoutSec}
}

func UpdateAgentSettings(input UpdateAgentSettingsInput) (AgentSettings, error) {
	controllerURL := strings.TrimRight(strings.TrimSpace(input.ControllerURL), "/")
	token := strings.TrimSpace(input.Token)
	if token == "" {
		token = strings.TrimSpace(app.ONE_CONFIG.ClusterAgent.Token)
	}
	if input.Enabled {
		if controllerURL == "" {
			return AgentSettings{}, errors.New("controllerUrl is required when node mode is enabled")
		}
		parsed, err := url.Parse(controllerURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return AgentSettings{}, errors.New("controllerUrl must be a valid http or https URL")
		}
		if token == "" {
			return AgentSettings{}, errors.New("token is required when node mode is enabled")
		}
	}
	interval := input.IntervalSeconds
	if interval == 0 {
		interval = app.ONE_CONFIG.ClusterAgent.IntervalSeconds
	}
	if interval < 5 || interval > 3600 {
		return AgentSettings{}, errors.New("intervalSeconds must be between 5 and 3600")
	}
	timeout := input.RequestTimeoutSec
	if timeout == 0 {
		timeout = app.ONE_CONFIG.ClusterAgent.RequestTimeoutSec
	}
	if timeout < 1 || timeout > 120 {
		return AgentSettings{}, errors.New("requestTimeoutSeconds must be between 1 and 120")
	}
	if err := app.PersistClusterAgentConfig(input.Enabled, controllerURL, token, interval, timeout); err != nil {
		return AgentSettings{}, err
	}
	return GetAgentSettings(), nil
}
