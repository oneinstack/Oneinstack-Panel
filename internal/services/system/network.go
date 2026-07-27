package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"oneinstack/app"
	"oneinstack/internal/services/safe"
	panelServer "oneinstack/server"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

var (
	ErrNetworkConfigInvalid = errors.New("panel network configuration is invalid")
	networkConfigMu         sync.Mutex
)

type preparedPanelRule struct {
	id      int64
	created bool
}

type PanelNetworkSettings struct {
	BindAddress          string                  `json:"bindAddress"`
	HTTPPort             string                  `json:"httpPort"`
	HTTPAccessURL        string                  `json:"httpAccessUrl"`
	HTTPSEnabled         bool                    `json:"httpsEnabled"`
	HTTPSPort            string                  `json:"httpsPort"`
	HTTPSAccessURL       string                  `json:"httpsAccessUrl"`
	HTTPSCertificateFile string                  `json:"httpsCertificateFile"`
	HTTPSPrivateKeyFile  string                  `json:"httpsPrivateKeyFile"`
	TrustedProxies       []string                `json:"trustedProxies"`
	Certificate          *PanelCertificateStatus `json:"certificate,omitempty"`
	RestartRequired      bool                    `json:"restartRequired"`
}

type PanelCertificateStatus struct {
	Valid       bool      `json:"valid"`
	Error       string    `json:"error,omitempty"`
	NotBefore   time.Time `json:"notBefore,omitempty"`
	NotAfter    time.Time `json:"notAfter,omitempty"`
	DNSNames    []string  `json:"dnsNames"`
	IPAddresses []string  `json:"ipAddresses"`
}

type UpdatePanelNetworkRequest struct {
	BindAddress          string   `json:"bindAddress"`
	HTTPPort             string   `json:"httpPort"`
	HTTPSEnabled         bool     `json:"httpsEnabled"`
	HTTPSPort            string   `json:"httpsPort"`
	HTTPSCertificateFile string   `json:"httpsCertificateFile"`
	HTTPSPrivateKeyFile  string   `json:"httpsPrivateKeyFile"`
	TrustedProxies       []string `json:"trustedProxies"`
}

func GetPanelNetworkSettings() (*PanelNetworkSettings, error) {
	stored, err := loadStoredPanelConfig()
	if err != nil {
		return nil, err
	}
	effective := effectivePanelConfig()
	return describePanelConfig(stored, !reflect.DeepEqual(stored, effective)), nil
}

func UpdatePanelNetwork(request UpdatePanelNetworkRequest) (*PanelNetworkSettings, error) {
	networkConfigMu.Lock()
	defer networkConfigMu.Unlock()

	next := normalizePanelConfig(panelServer.PanelConfig{
		BindAddress:     request.BindAddress,
		HTTPPort:        request.HTTPPort,
		HTTPSEnabled:    request.HTTPSEnabled,
		HTTPSPort:       request.HTTPSPort,
		CertificateFile: request.HTTPSCertificateFile,
		PrivateKeyFile:  request.HTTPSPrivateKeyFile,
		TrustedProxies:  request.TrustedProxies,
	})
	if err := panelServer.ValidatePanelConfig(next); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNetworkConfigInvalid, err)
	}

	current := effectivePanelConfig()
	candidateListeners, err := probeChangedListeners(current, next)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNetworkConfigInvalid, err)
	}
	defer func() {
		for _, listener := range candidateListeners {
			_ = listener.Close()
		}
	}()

	var preparedRules []preparedPanelRule
	firewallService := safe.NewDefaultService()
	ports := []string{next.HTTPPort}
	if next.HTTPSEnabled {
		ports = append(ports, next.HTTPSPort)
	}
	for _, portValue := range ports {
		port, _ := strconv.Atoi(portValue)
		id, created, err := firewallService.PreparePanelPort(context.Background(), port)
		if err != nil {
			rollbackPreparedRules(firewallService, preparedRules)
			return nil, fmt.Errorf("预置面板端口 %d 的防火墙规则: %w", port, err)
		}
		preparedRules = append(preparedRules, preparedPanelRule{id: id, created: created})
	}

	if err := persistPanelConfig(next); err != nil {
		rollbackPreparedRules(firewallService, preparedRules)
		return nil, err
	}
	return describePanelConfig(next, !reflect.DeepEqual(next, current)), nil
}

func rollbackPreparedRules(firewallService *safe.Service, rules []preparedPanelRule) {
	for index := len(rules) - 1; index >= 0; index-- {
		if rules[index].created {
			_ = firewallService.RollbackPreparedPanelPort(context.Background(), rules[index].id)
		}
	}
}

func effectivePanelConfig() panelServer.PanelConfig {
	system := app.ONE_CONFIG.System
	return normalizePanelConfig(panelServer.PanelConfig{
		BindAddress:     system.BindAddress,
		HTTPPort:        system.Port,
		HTTPSEnabled:    system.HTTPSEnabled,
		HTTPSPort:       system.HTTPSPort,
		CertificateFile: system.HTTPSCertificateFile,
		PrivateKeyFile:  system.HTTPSPrivateKeyFile,
		TrustedProxies:  system.TrustedProxies,
	})
}

func normalizePanelConfig(config panelServer.PanelConfig) panelServer.PanelConfig {
	config.BindAddress = strings.TrimSpace(config.BindAddress)
	if config.BindAddress == "" {
		config.BindAddress = "0.0.0.0"
	}
	config.HTTPPort = strings.TrimSpace(config.HTTPPort)
	config.HTTPSPort = strings.TrimSpace(config.HTTPSPort)
	if config.HTTPSPort == "" {
		config.HTTPSPort = "8443"
	}
	config.CertificateFile = strings.TrimSpace(config.CertificateFile)
	config.PrivateKeyFile = strings.TrimSpace(config.PrivateKeyFile)
	seen := make(map[string]struct{}, len(config.TrustedProxies))
	trustedProxies := make([]string, 0, len(config.TrustedProxies))
	for _, proxy := range config.TrustedProxies {
		proxy = strings.TrimSpace(proxy)
		if _, exists := seen[proxy]; exists {
			continue
		}
		seen[proxy] = struct{}{}
		trustedProxies = append(trustedProxies, proxy)
	}
	config.TrustedProxies = trustedProxies
	return config
}

func loadStoredPanelConfig() (panelServer.PanelConfig, error) {
	configPath := configFilePath()
	v := viper.New()
	v.SetConfigFile(configPath)
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		return panelServer.PanelConfig{}, fmt.Errorf("读取面板配置: %w", err)
	}
	fallback := effectivePanelConfig()
	config := panelServer.PanelConfig{
		BindAddress:     fallback.BindAddress,
		HTTPPort:        fallback.HTTPPort,
		HTTPSEnabled:    fallback.HTTPSEnabled,
		HTTPSPort:       fallback.HTTPSPort,
		CertificateFile: fallback.CertificateFile,
		PrivateKeyFile:  fallback.PrivateKeyFile,
		TrustedProxies:  append([]string(nil), fallback.TrustedProxies...),
	}
	if v.IsSet("system.bindAddress") {
		config.BindAddress = v.GetString("system.bindAddress")
	}
	if v.IsSet("system.port") {
		config.HTTPPort = v.GetString("system.port")
	}
	if v.IsSet("system.httpsEnabled") {
		config.HTTPSEnabled = v.GetBool("system.httpsEnabled")
	}
	if v.IsSet("system.httpsPort") {
		config.HTTPSPort = v.GetString("system.httpsPort")
	}
	if v.IsSet("system.httpsCertificateFile") {
		config.CertificateFile = v.GetString("system.httpsCertificateFile")
	}
	if v.IsSet("system.httpsPrivateKeyFile") {
		config.PrivateKeyFile = v.GetString("system.httpsPrivateKeyFile")
	}
	if v.IsSet("system.trustedProxies") {
		config.TrustedProxies = v.GetStringSlice("system.trustedProxies")
	}
	return normalizePanelConfig(config), nil
}

func probeChangedListeners(current, next panelServer.PanelConfig) ([]net.Listener, error) {
	currentAddresses := map[string]struct{}{}
	currentHTTPAddress, _ := panelServer.NetworkAddress(current.BindAddress, current.HTTPPort)
	currentAddresses[currentHTTPAddress] = struct{}{}
	if current.HTTPSEnabled {
		currentHTTPSAddress, _ := panelServer.NetworkAddress(current.BindAddress, current.HTTPSPort)
		currentAddresses[currentHTTPSAddress] = struct{}{}
	}

	nextAddresses := []string{}
	nextHTTPAddress, _ := panelServer.NetworkAddress(next.BindAddress, next.HTTPPort)
	nextAddresses = append(nextAddresses, nextHTTPAddress)
	if next.HTTPSEnabled {
		nextHTTPSAddress, _ := panelServer.NetworkAddress(next.BindAddress, next.HTTPSPort)
		nextAddresses = append(nextAddresses, nextHTTPSAddress)
	}

	var listeners []net.Listener
	for _, address := range nextAddresses {
		if _, alreadyActive := currentAddresses[address]; alreadyActive {
			continue
		}
		listener, err := panelServer.ProbeAddress(address)
		if err != nil {
			for _, prepared := range listeners {
				_ = prepared.Close()
			}
			return nil, err
		}
		listeners = append(listeners, listener)
	}
	return listeners, nil
}

func persistPanelConfig(config panelServer.PanelConfig) error {
	configPath := configFilePath()
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("读取面板配置: %w", err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("解析面板配置: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return errors.New("面板配置根节点必须是 YAML 映射")
	}
	root := document.Content[0]
	systemNode := yamlMappingValue(root, "system")
	if systemNode == nil || systemNode.Kind != yaml.MappingNode {
		return errors.New("面板配置缺少 system 映射")
	}
	setYAMLMappingValue(systemNode, "bindAddress", yamlScalar(config.BindAddress, "!!str"))
	setYAMLMappingValue(systemNode, "port", yamlScalar(config.HTTPPort, "!!str"))
	setYAMLMappingValue(systemNode, "httpsEnabled", yamlScalar(strconv.FormatBool(config.HTTPSEnabled), "!!bool"))
	setYAMLMappingValue(systemNode, "httpsPort", yamlScalar(config.HTTPSPort, "!!str"))
	setYAMLMappingValue(systemNode, "httpsCertificateFile", yamlScalar(config.CertificateFile, "!!str"))
	setYAMLMappingValue(systemNode, "httpsPrivateKeyFile", yamlScalar(config.PrivateKeyFile, "!!str"))
	proxyNode := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, proxy := range config.TrustedProxies {
		proxyNode.Content = append(proxyNode.Content, yamlScalar(proxy, "!!str"))
	}
	setYAMLMappingValue(systemNode, "trustedProxies", proxyNode)

	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(4)
	if err := encoder.Encode(&document); err != nil {
		return fmt.Errorf("编码面板配置: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("关闭配置编码器: %w", err)
	}

	directory := filepath.Dir(configPath)
	temporary, err := os.CreateTemp(directory, ".config-network-*.yaml")
	if err != nil {
		return fmt.Errorf("创建配置事务文件: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("保护配置事务文件: %w", err)
	}
	if _, err := temporary.Write(encoded.Bytes()); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("写入配置事务文件: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("同步配置事务文件: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("关闭配置事务文件: %w", err)
	}
	if err := os.Rename(temporaryPath, configPath); err != nil {
		return fmt.Errorf("提交面板访问配置: %w", err)
	}
	return nil
}

func yamlMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if strings.EqualFold(mapping.Content[index].Value, key) {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func setYAMLMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if strings.EqualFold(mapping.Content[index].Value, key) {
			mapping.Content[index].Value = key
			mapping.Content[index+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}

func yamlScalar(value, tag string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
}

func configFilePath() string {
	if app.ONE_VIP != nil && strings.TrimSpace(app.ONE_VIP.ConfigFileUsed()) != "" {
		return app.ONE_VIP.ConfigFileUsed()
	}
	return filepath.Join(app.GetBasePath(), "config.yaml")
}

func describePanelConfig(config panelServer.PanelConfig, restartRequired bool) *PanelNetworkSettings {
	settings := &PanelNetworkSettings{
		BindAddress:          config.BindAddress,
		HTTPPort:             config.HTTPPort,
		HTTPAccessURL:        accessURL("http", config.BindAddress, config.HTTPPort),
		HTTPSEnabled:         config.HTTPSEnabled,
		HTTPSPort:            config.HTTPSPort,
		HTTPSAccessURL:       accessURL("https", config.BindAddress, config.HTTPSPort),
		HTTPSCertificateFile: config.CertificateFile,
		HTTPSPrivateKeyFile:  config.PrivateKeyFile,
		TrustedProxies:       append([]string(nil), config.TrustedProxies...),
		RestartRequired:      restartRequired,
	}
	if config.CertificateFile != "" || config.PrivateKeyFile != "" {
		status := &PanelCertificateStatus{DNSNames: []string{}, IPAddresses: []string{}}
		info, err := panelServer.ValidateTLSCertificate(config.CertificateFile, config.PrivateKeyFile, time.Now())
		if err != nil {
			status.Error = err.Error()
		} else {
			status.Valid = true
			status.NotBefore = info.NotBefore
			status.NotAfter = info.NotAfter
			status.DNSNames = info.DNSNames
			status.IPAddresses = info.IPAddresses
		}
		settings.Certificate = status
	}
	return settings
}

func accessURL(scheme, bindAddress, port string) string {
	host := strings.TrimSpace(bindAddress)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "服务器IP"
	} else if parsed := net.ParseIP(host); parsed != nil && strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("%s://%s:%s", scheme, host, port)
}
