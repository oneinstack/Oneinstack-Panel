package system

import (
	"bytes"
	"context"
	"crypto/rand"
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

const PanelEntryCLISubcommand = "entrance"

type preparedPanelRule struct {
	id      int64
	created bool
}

type managedPanelConfig struct {
	Network           panelServer.PanelConfig
	PanelEntryEnabled bool
	PanelEntryPath    string
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
	PanelEntryEnabled    bool                    `json:"panelEntryEnabled"`
	PanelEntryPath       string                  `json:"panelEntryPath"`
	PanelAccessURL       string                  `json:"panelAccessUrl"`
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
	PanelEntryEnabled    bool     `json:"panelEntryEnabled"`
	PanelEntryPath       string   `json:"panelEntryPath"`
	RotatePanelEntry     bool     `json:"rotatePanelEntry"`
}

func GetPanelNetworkSettings() (*PanelNetworkSettings, error) {
	stored, err := loadStoredManagedPanelConfig()
	if err != nil {
		return nil, err
	}
	effective := effectiveManagedPanelConfig()
	return describePanelConfig(stored, !reflect.DeepEqual(stored, effective)), nil
}

func UpdatePanelNetwork(request UpdatePanelNetworkRequest) (*PanelNetworkSettings, error) {
	networkConfigMu.Lock()
	defer networkConfigMu.Unlock()

	currentStored, err := loadStoredManagedPanelConfig()
	if err != nil {
		return nil, err
	}
	next := normalizeManagedPanelConfig(managedPanelConfig{
		Network: panelServer.PanelConfig{
			BindAddress:     request.BindAddress,
			HTTPPort:        request.HTTPPort,
			HTTPSEnabled:    request.HTTPSEnabled,
			HTTPSPort:       request.HTTPSPort,
			CertificateFile: request.HTTPSCertificateFile,
			PrivateKeyFile:  request.HTTPSPrivateKeyFile,
			TrustedProxies:  request.TrustedProxies,
		},
		PanelEntryEnabled: request.PanelEntryEnabled,
		PanelEntryPath:    request.PanelEntryPath,
	})
	if request.RotatePanelEntry {
		next.PanelEntryEnabled = true
		next.PanelEntryPath = generatePanelEntryPath()
	}
	if next.PanelEntryEnabled && next.PanelEntryPath == "" {
		next.PanelEntryPath = generatePanelEntryPath()
	}
	if !next.PanelEntryEnabled && next.PanelEntryPath == "" {
		next.PanelEntryPath = currentStored.PanelEntryPath
	}
	if err := validateManagedPanelConfig(next); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNetworkConfigInvalid, err)
	}

	current := effectiveManagedPanelConfig()
	candidateListeners, err := probeChangedListeners(current.Network, next.Network)
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
	ports := []string{next.Network.HTTPPort}
	if next.Network.HTTPSEnabled {
		ports = append(ports, next.Network.HTTPSPort)
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

	if err := persistManagedPanelConfig(next); err != nil {
		rollbackPreparedRules(firewallService, preparedRules)
		return nil, err
	}
	applyRuntimePanelConfig(next)
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
	return effectiveManagedPanelConfig().Network
}

func effectiveManagedPanelConfig() managedPanelConfig {
	system := app.ONE_CONFIG.System
	return normalizeManagedPanelConfig(managedPanelConfig{
		Network: panelServer.PanelConfig{
			BindAddress:     system.BindAddress,
			HTTPPort:        system.Port,
			HTTPSEnabled:    system.HTTPSEnabled,
			HTTPSPort:       system.HTTPSPort,
			CertificateFile: system.HTTPSCertificateFile,
			PrivateKeyFile:  system.HTTPSPrivateKeyFile,
			TrustedProxies:  system.TrustedProxies,
		},
		PanelEntryEnabled: system.PanelEntryEnabled,
		PanelEntryPath:    system.PanelEntryPath,
	})
}

func normalizeManagedPanelConfig(config managedPanelConfig) managedPanelConfig {
	config.Network = normalizePanelConfig(config.Network)
	config.PanelEntryPath = normalizePanelEntryPath(config.PanelEntryPath)
	return config
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
	config, err := loadStoredManagedPanelConfig()
	if err != nil {
		return panelServer.PanelConfig{}, err
	}
	return config.Network, nil
}

func loadStoredManagedPanelConfig() (managedPanelConfig, error) {
	configPath := configFilePath()
	v := viper.New()
	v.SetConfigFile(configPath)
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		return managedPanelConfig{}, fmt.Errorf("读取面板配置: %w", err)
	}
	fallback := effectiveManagedPanelConfig()
	config := managedPanelConfig{
		Network: panelServer.PanelConfig{
			BindAddress:     fallback.Network.BindAddress,
			HTTPPort:        fallback.Network.HTTPPort,
			HTTPSEnabled:    fallback.Network.HTTPSEnabled,
			HTTPSPort:       fallback.Network.HTTPSPort,
			CertificateFile: fallback.Network.CertificateFile,
			PrivateKeyFile:  fallback.Network.PrivateKeyFile,
			TrustedProxies:  append([]string(nil), fallback.Network.TrustedProxies...),
		},
		PanelEntryEnabled: fallback.PanelEntryEnabled,
		PanelEntryPath:    fallback.PanelEntryPath,
	}
	if v.IsSet("system.bindAddress") {
		config.Network.BindAddress = v.GetString("system.bindAddress")
	}
	if v.IsSet("system.port") {
		config.Network.HTTPPort = v.GetString("system.port")
	}
	if v.IsSet("system.httpsEnabled") {
		config.Network.HTTPSEnabled = v.GetBool("system.httpsEnabled")
	}
	if v.IsSet("system.httpsPort") {
		config.Network.HTTPSPort = v.GetString("system.httpsPort")
	}
	if v.IsSet("system.httpsCertificateFile") {
		config.Network.CertificateFile = v.GetString("system.httpsCertificateFile")
	}
	if v.IsSet("system.httpsPrivateKeyFile") {
		config.Network.PrivateKeyFile = v.GetString("system.httpsPrivateKeyFile")
	}
	if v.IsSet("system.trustedProxies") {
		config.Network.TrustedProxies = v.GetStringSlice("system.trustedProxies")
	}
	if v.IsSet("system.panelEntryEnabled") {
		config.PanelEntryEnabled = v.GetBool("system.panelEntryEnabled")
	}
	if v.IsSet("system.panelEntryPath") {
		config.PanelEntryPath = v.GetString("system.panelEntryPath")
	}
	return normalizeManagedPanelConfig(config), nil
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
	return persistManagedPanelConfig(managedPanelConfig{Network: config})
}

func persistManagedPanelConfig(config managedPanelConfig) error {
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
	setYAMLMappingValue(systemNode, "bindAddress", yamlScalar(config.Network.BindAddress, "!!str"))
	setYAMLMappingValue(systemNode, "port", yamlScalar(config.Network.HTTPPort, "!!str"))
	setYAMLMappingValue(systemNode, "httpsEnabled", yamlScalar(strconv.FormatBool(config.Network.HTTPSEnabled), "!!bool"))
	setYAMLMappingValue(systemNode, "httpsPort", yamlScalar(config.Network.HTTPSPort, "!!str"))
	setYAMLMappingValue(systemNode, "httpsCertificateFile", yamlScalar(config.Network.CertificateFile, "!!str"))
	setYAMLMappingValue(systemNode, "httpsPrivateKeyFile", yamlScalar(config.Network.PrivateKeyFile, "!!str"))
	proxyNode := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, proxy := range config.Network.TrustedProxies {
		proxyNode.Content = append(proxyNode.Content, yamlScalar(proxy, "!!str"))
	}
	setYAMLMappingValue(systemNode, "trustedProxies", proxyNode)
	setYAMLMappingValue(systemNode, "panelEntryEnabled", yamlScalar(strconv.FormatBool(config.PanelEntryEnabled), "!!bool"))
	setYAMLMappingValue(systemNode, "panelEntryPath", yamlScalar(config.PanelEntryPath, "!!str"))

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

func describePanelConfig(config managedPanelConfig, restartRequired bool) *PanelNetworkSettings {
	settings := &PanelNetworkSettings{
		BindAddress:          config.Network.BindAddress,
		HTTPPort:             config.Network.HTTPPort,
		HTTPAccessURL:        accessURL("http", config.Network.BindAddress, config.Network.HTTPPort),
		HTTPSEnabled:         config.Network.HTTPSEnabled,
		HTTPSPort:            config.Network.HTTPSPort,
		HTTPSAccessURL:       accessURL("https", config.Network.BindAddress, config.Network.HTTPSPort),
		HTTPSCertificateFile: config.Network.CertificateFile,
		HTTPSPrivateKeyFile:  config.Network.PrivateKeyFile,
		TrustedProxies:       append([]string(nil), config.Network.TrustedProxies...),
		PanelEntryEnabled:    config.PanelEntryEnabled,
		PanelEntryPath:       config.PanelEntryPath,
		PanelAccessURL:       panelAccessURL(config),
		RestartRequired:      restartRequired,
	}
	if config.Network.CertificateFile != "" || config.Network.PrivateKeyFile != "" {
		status := &PanelCertificateStatus{DNSNames: []string{}, IPAddresses: []string{}}
		info, err := panelServer.ValidateTLSCertificate(config.Network.CertificateFile, config.Network.PrivateKeyFile, time.Now())
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

func validateManagedPanelConfig(config managedPanelConfig) error {
	if err := panelServer.ValidatePanelConfig(config.Network); err != nil {
		return err
	}
	if !config.PanelEntryEnabled {
		return nil
	}
	if config.PanelEntryPath == "" {
		return errors.New("system.panelEntryPath is required when panel entry is enabled")
	}
	slug := strings.TrimPrefix(config.PanelEntryPath, "/")
	switch strings.ToLower(slug) {
	case "v1", "health", "favicon.ico":
		return errors.New("system.panelEntryPath uses a reserved path")
	}
	if len(slug) < 10 || len(slug) > 20 {
		return errors.New("system.panelEntryPath length must be 10-20")
	}
	for _, ch := range slug {
		switch {
		case ch >= '0' && ch <= '9':
		case ch >= 'A' && ch <= 'Z':
		case ch >= 'a' && ch <= 'z':
		default:
			return errors.New("system.panelEntryPath must contain only letters and digits")
		}
	}
	return nil
}

func normalizePanelEntryPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = "/" + strings.Trim(path, "/")
	if path == "/" {
		return ""
	}
	return path
}

func generatePanelEntryPath() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	const length = 12
	buffer := make([]byte, length)
	if _, err := rand.Read(buffer); err != nil {
		return "/p" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	for index := range buffer {
		buffer[index] = alphabet[int(buffer[index])%len(alphabet)]
	}
	return "/" + string(buffer)
}

func panelAccessURL(config managedPanelConfig) string {
	path := config.PanelEntryPath
	if !config.PanelEntryEnabled {
		path = ""
	}
	if config.Network.HTTPSEnabled {
		return accessURL("https", config.Network.BindAddress, config.Network.HTTPSPort) + path
	}
	return accessURL("http", config.Network.BindAddress, config.Network.HTTPPort) + path
}

func applyRuntimePanelConfig(config managedPanelConfig) {
	app.ONE_CONFIG.System.BindAddress = config.Network.BindAddress
	app.ONE_CONFIG.System.Port = config.Network.HTTPPort
	app.ONE_CONFIG.System.HTTPSEnabled = config.Network.HTTPSEnabled
	app.ONE_CONFIG.System.HTTPSPort = config.Network.HTTPSPort
	app.ONE_CONFIG.System.HTTPSCertificateFile = config.Network.CertificateFile
	app.ONE_CONFIG.System.HTTPSPrivateKeyFile = config.Network.PrivateKeyFile
	app.ONE_CONFIG.System.TrustedProxies = append([]string(nil), config.Network.TrustedProxies...)
	app.ONE_CONFIG.System.PanelEntryEnabled = config.PanelEntryEnabled
	app.ONE_CONFIG.System.PanelEntryPath = config.PanelEntryPath
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

func PanelEntryCLICommand() string {
	return "one " + PanelEntryCLISubcommand
}
