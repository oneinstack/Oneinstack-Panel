package input

type InstallParams struct {
	Key      string `json:"key"`      //安装的服务
	Version  string `json:"version"`  //安装的版本
	Port     string `json:"port"`     //端口
	Username string `json:"username"` //账号
	Pwd      string `json:"pwd"`      //密码
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
