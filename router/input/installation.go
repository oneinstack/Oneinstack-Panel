package input

import (
	"encoding/json"
	"fmt"
	"strconv"
)

type InstallParams struct {
	Key      string `json:"key"`      //安装的服务
	Version  string `json:"version"`  //安装的版本
	Port     string `json:"port"`     //端口
	Username string `json:"username"` //账号
	Pwd      string `json:"pwd"`      //密码
	// Parameters contains component-specific values declared by the signed
	// package manifest. It is intentionally not persisted with task metadata.
	Parameters map[string]string `json:"parameters,omitempty"`
	// ExplicitParameters is an internal marker used to distinguish caller
	// supplied values from manifest defaults. It is never accepted from or
	// serialized into the HTTP contract.
	ExplicitParameters map[string]bool `json:"-"`
}

// UnmarshalJSON keeps the legacy flat install form compatible with component
// packages: fields such as BACKUP_ENDPOINT are collected instead of being
// silently discarded by encoding/json. The executor still applies a manifest
// allowlist before exporting them to the action environment.
func (p *InstallParams) UnmarshalJSON(data []byte) error {
	// Keep the public request type string-based for the script environment, but
	// accept JSON scalar values in the generic parameter map. Catalog metadata
	// can declare a parameter as boolean or integer, and older clients may send
	// those values using their native JSON types.
	var decoded struct {
		Key        string                     `json:"key"`
		Version    string                     `json:"version"`
		Port       string                     `json:"port"`
		Username   string                     `json:"username"`
		Pwd        string                     `json:"pwd"`
		Parameters map[string]json.RawMessage `json:"parameters,omitempty"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	values := make(map[string]json.RawMessage)
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	parameters := make(map[string]string, len(decoded.Parameters)+len(values))
	for key, raw := range decoded.Parameters {
		value, err := decodeInstallParameterValue(raw)
		if err != nil {
			return fmt.Errorf("install parameter %s must be a string, boolean, or number", key)
		}
		parameters[key] = value
	}
	known := map[string]bool{"key": true, "version": true, "port": true, "username": true, "pwd": true, "parameters": true}
	for key, raw := range values {
		if known[key] {
			continue
		}
		value, err := decodeInstallParameterValue(raw)
		if err != nil {
			return fmt.Errorf("install parameter %s must be a string, boolean, or number", key)
		}
		parameters[key] = value
	}
	*p = InstallParams{
		Key:        decoded.Key,
		Version:    decoded.Version,
		Port:       decoded.Port,
		Username:   decoded.Username,
		Pwd:        decoded.Pwd,
		Parameters: parameters,
	}
	return nil
}

func decodeInstallParameterValue(raw json.RawMessage) (string, error) {
	var stringValue string
	if err := json.Unmarshal(raw, &stringValue); err == nil {
		return stringValue, nil
	}
	var boolValue bool
	if err := json.Unmarshal(raw, &boolValue); err == nil {
		return strconv.FormatBool(boolValue), nil
	}
	var numberValue json.Number
	if err := json.Unmarshal(raw, &numberValue); err == nil {
		return numberValue.String(), nil
	}
	return "", fmt.Errorf("unsupported JSON scalar")
}

type RemoveParams struct {
	Name                string `json:"name" binding:"required"`    //安装的服务
	Version             string `json:"version" binding:"required"` //安装的版本
	DataPolicy          string `json:"dataPolicy,omitempty"`
	ConfirmDataDeletion bool   `json:"confirmDataDeletion,omitempty"`
	// Parameters contains only values declared by the signed component
	// manifest, for example PRESERVE_DATA during uninstall.
	Parameters map[string]string `json:"parameters,omitempty"`
}

//
//type InstallationParams struct {
//	NginxOption      string   `json:"nginx_option,omitempty"`       // Nginx 版本选项 [1-3]
//	Apache           bool     `json:"apache,omitempty"`             // 是否安装 Apache
//	ApacheModeOption string   `json:"apache_mode_option,omitempty"` // Apache 模式选项 [1-2]
//	ApacheMPMOption  string   `json:"apache_mpm_option,omitempty"`  // Apache MPM 选项 [1-3]
//	PHPOption        string   `json:"php_option,omitempty"`         // PHP 版本选项 [1-10]
//	MultiPHPVersion  string   `json:"mphp_ver,omitempty"`           // 多版本 PHP, 格式如 "74" 对应 PHP 7.4
//	PHPCacheOption   string   `json:"phpcache_option,omitempty"`    // PHP 缓存选项 [1-4]
//	PHPExtensions    []string `json:"php_extensions,omitempty"`
//	TomcatOption     string   `json:"tomcat_option,omitempty"`   // Tomcat 版本选项 [1-4]
//	JDKOption        string   `json:"jdk_option,omitempty"`      // JDK 版本选项 [1-4]
//	DBOption         string   `json:"db_option,omitempty"`       // 数据库版本选项 [1-14]
//	DBInstallMethod  string   `json:"dbinstallmethod,omitempty"` // 数据库安装方法 [1-2]
//	DBRootPWD        string   `json:"dbrootpwd,omitempty"`
//	PureFTPD         bool     `json:"pureftpd,omitempty"`
//	Redis            bool     `json:"redis,omitempty"`
//	Memcached        bool     `json:"memcached,omitempty"`
//	PHPMyAdmin       bool     `json:"phpmyadmin,omitempty"`
//	Python           bool     `json:"python,omitempty"`
//	SSHPort          string   `json:"ssh_port,omitempty"`
//	Iptables         bool     `json:"iptables,omitempty"`
//	Reboot           bool     `json:"reboot,omitempty"`
//}
//
//// BuildCmdArgs 构建命令行参数列表
//func (params *InstallationParams) BuildCmdArgs() []string {
//	var cmdArgs []string
//
//	// 使用正确格式添加命令行参数
//	if params.NginxOption != "" {
//		cmdArgs = append(cmdArgs, "--nginx_option", fmt.Sprintf("%s", params.NginxOption))
//	}
//	if params.Apache {
//		cmdArgs = append(cmdArgs, "--apache")
//	}
//	if params.ApacheModeOption != "" {
//		cmdArgs = append(cmdArgs, "--apache_mode_option", fmt.Sprintf("%s", params.ApacheModeOption))
//	}
//	if params.ApacheMPMOption != "" {
//		cmdArgs = append(cmdArgs, "--apache_mpm_option", fmt.Sprintf("%s", params.ApacheMPMOption))
//	}
//	if params.PHPOption != "" {
//		cmdArgs = append(cmdArgs, "--php_option", fmt.Sprintf("%s", params.PHPOption))
//	}
//	if params.MultiPHPVersion != "" {
//		cmdArgs = append(cmdArgs, "--mphp_ver", params.MultiPHPVersion)
//	}
//	if params.PHPCacheOption != "" {
//		cmdArgs = append(cmdArgs, "--phpcache_option", fmt.Sprintf("%s", params.PHPCacheOption))
//	}
//	for _, ext := range params.PHPExtensions {
//		cmdArgs = append(cmdArgs, "--php_extensions", ext)
//	}
//	if params.TomcatOption != "" {
//		cmdArgs = append(cmdArgs, "--tomcat_option", fmt.Sprintf("%s", params.TomcatOption))
//	}
//	if params.JDKOption != "" {
//		cmdArgs = append(cmdArgs, "--jdk_option", fmt.Sprintf("%s", params.JDKOption))
//	}
//	if params.DBOption != "" {
//		cmdArgs = append(cmdArgs, "--db_option", fmt.Sprintf("%s", params.DBOption))
//	}
//	if params.DBInstallMethod != "" {
//		cmdArgs = append(cmdArgs, "--dbinstallmethod", fmt.Sprintf("%s", params.DBInstallMethod))
//	}
//	if params.DBRootPWD != "" {
//		cmdArgs = append(cmdArgs, "--dbrootpwd", params.DBRootPWD)
//	}
//	if params.PureFTPD {
//		cmdArgs = append(cmdArgs, "--pureftpd")
//	}
//	if params.Redis {
//		cmdArgs = append(cmdArgs, "--redis")
//	}
//	if params.Memcached {
//		cmdArgs = append(cmdArgs, "--memcached")
//	}
//	if params.PHPMyAdmin {
//		cmdArgs = append(cmdArgs, "--phpmyadmin")
//	}
//	if params.Python {
//		cmdArgs = append(cmdArgs, "--python")
//	}
//	if params.SSHPort != "" {
//		cmdArgs = append(cmdArgs, "--ssh_port", params.SSHPort)
//	}
//	if params.Iptables {
//		cmdArgs = append(cmdArgs, "--iptables")
//	}
//	if params.Reboot {
//		cmdArgs = append(cmdArgs, "--reboot")
//	}
//
//	return cmdArgs
//}
