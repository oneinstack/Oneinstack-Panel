package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"oneinstack/app"
	"oneinstack/internal/i18n"
	systemservice "oneinstack/internal/services/system"

	"github.com/spf13/cobra"
	"gorm.io/gorm"
)

const managedInstallerMarker = "# Managed by OneinStack Panel installer"

var defaultPeek bool
var uninstallPurge bool
var uninstallConfirmed bool

var defaultCmd = &cobra.Command{
	Use:   "default",
	Short: "Show panel access information and the one-time bootstrap password",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return printDefaultInformation(defaultPeek)
	},
}

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Uninstall the Panel while preserving data by default",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCLIUninstall()
	},
}

func printCLIMenu() error {
	fmt.Println("========================================")
	fmt.Println(cliLifecycleText("OneinStack Panel CLI", "OneinStack Panel 命令行"))
	fmt.Println("========================================")
	menuItems := [][2]string{
		{"default", "Show access URL and bootstrap credentials"},
		{"resetpwd", "Reset the administrator password"},
		{"entrance", "Show the current panel access entry"},
		{"server start|restart|stop", "Control the Panel server"},
		{"uninstall [--purge --yes]", "Uninstall the Panel; preserve data by default"},
		{"lang [en-US|zh-CN]", "Show or change CLI language"},
		{"version", "Show version information"},
	}
	translations := map[string]string{
		"Show access URL and bootstrap credentials":     "显示访问地址和初始化凭据",
		"Reset the administrator password":              "修改管理员密码",
		"Show the current panel access entry":           "显示当前面板访问入口",
		"Control the Panel server":                      "控制面板服务",
		"Uninstall the Panel; preserve data by default": "卸载面板，默认保留数据",
		"Show or change CLI language":                   "查看或切换 CLI 语言",
		"Show version information":                      "显示版本信息",
	}
	for _, item := range menuItems {
		if activeCLILanguage == i18n.LocaleZhCN {
			item[1] = translations[item[1]]
		}
		fmt.Printf("  %-24s %s\n", item[0], item[1])
	}
	fmt.Println("========================================")
	return nil
}

func runResetPassword(cmd *cobra.Command, args []string) error {
	username, err := cmd.Flags().GetString("user")
	if err != nil {
		return err
	}
	if strings.TrimSpace(username) == "" {
		username, err = app.PrimaryAdminUsername()
		if err != nil {
			return err
		}
	}
	fmt.Println(cliLifecycleText(
		"Password requirements: 8-128 characters, including uppercase, lowercase, number, and special character.",
		"密码格式要求：长度为 8-128 个字符，且必须包含大写字母、小写字母、数字和特殊字符。",
	))

	newPassword, err := cmd.Flags().GetString("password")
	if err != nil {
		return err
	}
	if strings.TrimSpace(newPassword) == "" && strings.TrimSpace(resetPasswordFile) != "" {
		newPassword, err = resolveInitPassword("", resetPasswordFile)
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(newPassword) == "" {
		first, readErr := readHiddenPassword(cliLifecycleText("New password: ", "新密码："))
		if readErr != nil {
			return errors.New(cliLifecycleText(
				"an interactive terminal is required; use --password-file for non-interactive reset",
				"修改密码需要交互式终端；非交互模式请使用 --password-file",
			))
		}
		second, confirmErr := readHiddenPassword(cliLifecycleText("Confirm new password: ", "再次输入新密码："))
		if confirmErr != nil {
			return errors.New(cliLifecycleText(
				"an interactive terminal is required; use --password-file for non-interactive reset",
				"修改密码需要交互式终端；非交互模式请使用 --password-file",
			))
		}
		if string(first) != string(second) {
			return errors.New(cliLifecycleText("passwords do not match", "两次输入的密码不一致"))
		}
		newPassword = string(first)
	}
	if err := app.ResetUserPassword(username, newPassword); err != nil {
		return err
	}
	fmt.Printf("%s\n", cliLifecycleText(
		"Password reset successfully; all existing sessions were revoked.",
		"密码修改成功，所有已有登录会话已撤销。",
	))
	return nil
}

func readHiddenPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	if err := exec.Command("stty", "-echo").Run(); err != nil {
		return "", err
	}
	defer func() { _ = exec.Command("stty", "echo").Run() }()
	value, err := bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Println()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(value, "\r\n"), nil
}

func printDefaultInformation(peek bool) error {
	settings, err := systemservice.GetPanelNetworkSettings()
	if err != nil {
		return err
	}
	credentials, hasBootstrap, err := app.LoadBootstrapCredentials(!peek)
	if err != nil {
		return err
	}
	username := ""
	if hasBootstrap {
		username = credentials.Username
	} else {
		username, err = app.PrimaryAdminUsername()
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New(cliLifecycleText(
					"no administrator user is initialized; run one init --auto first",
					"尚未初始化管理员用户，请先执行 one init --auto",
				))
			}
			return err
		}
	}

	fmt.Println(cliLifecycleText("OneinStack Panel default access information", "OneinStack Panel 默认访问信息"))
	for _, address := range panelAccessURLs(settings) {
		fmt.Printf("%s: %s\n", cliLifecycleText("Panel URL", "面板地址"), address)
	}
	fmt.Printf("%s: %s\n", cliLifecycleText("Username", "用户名"), username)
	if hasBootstrap {
		fmt.Printf("%s: %s\n", cliLifecycleText("Password", "密码"), credentials.Password)
		if !peek {
			fmt.Println(cliLifecycleText(
				"This bootstrap password has been consumed. Use one resetpwd to set a new password.",
				"此初始化密码已被消费，请使用 one resetpwd 设置新密码。",
			))
		}
	} else {
		fmt.Println(cliLifecycleText(
			"The one-time bootstrap password is no longer available. Use one resetpwd to change the password.",
			"一次性初始化密码已不可用，请使用 one resetpwd 修改密码。",
		))
	}
	return nil
}

func panelAccessURLs(settings *systemservice.PanelNetworkSettings) []string {
	if settings == nil {
		return nil
	}
	if settings.PanelEntryEnabled {
		return []string{settings.PanelAccessURL}
	}
	addresses := []string{settings.HTTPAccessURL}
	if settings.HTTPSEnabled && settings.HTTPSAccessURL != "" {
		addresses = append(addresses, settings.HTTPSAccessURL)
	}
	return addresses
}

func cliLifecycleText(english, chinese string) string {
	if activeCLILanguage == i18n.LocaleZhCN {
		return chinese
	}
	return english
}

func runCLIUninstall() error {
	if uninstallPurge && !uninstallConfirmed {
		return errors.New(cliLifecycleText(
			"uninstall --purge permanently deletes Panel data; use --purge --yes",
			"uninstall --purge 会永久删除面板数据，必须同时使用 --purge --yes",
		))
	}
	if os.Geteuid() != 0 {
		return errors.New(cliLifecycleText(
			"uninstall must be run as root",
			"卸载必须使用 root 用户执行",
		))
	}

	basePath, err := safeUninstallBasePath(app.GetBasePath())
	if err != nil {
		return err
	}
	serviceFiles := []string{
		"/etc/systemd/system/one.service",
		"/etc/systemd/system/one-update.service",
		"/etc/systemd/system/one-network-recover.service",
		"/etc/systemd/system/one-panel-restore.service",
	}
	for _, serviceFile := range serviceFiles {
		if err := verifyManagedFile(serviceFile); err != nil {
			return err
		}
	}

	// The installer owns this stable command path even when --install-dir is
	// customized; keep the CLI uninstall path aligned with install.sh.
	linkPath := "/usr/local/bin/one"
	if info, statErr := os.Lstat(linkPath); statErr == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%s", cliLifecycleText(
				"command path is occupied by a regular file; refusing to delete: "+linkPath,
				"命令路径已被普通文件占用，拒绝删除："+linkPath,
			))
		}
		target, readErr := os.Readlink(linkPath)
		if readErr != nil {
			return fmt.Errorf("read command link: %w", readErr)
		}
		if target != filepath.Join("/usr/local/one", "one") && target != filepath.Join(basePath, "one") {
			return fmt.Errorf("%s", cliLifecycleText(
				"command link points to another program; refusing to delete: "+target,
				"命令链接指向其他程序，拒绝删除："+target,
			))
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect command link: %w", statErr)
	}

	if err := stopManagedServices(serviceFiles); err != nil {
		return err
	}
	for _, serviceFile := range serviceFiles {
		if err := os.Remove(serviceFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove service file: %w", err)
		}
	}
	if err := os.Remove(linkPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove command link: %w", err)
	}

	if uninstallPurge {
		if err := os.RemoveAll(basePath); err != nil {
			return fmt.Errorf("purge Panel data: %w", err)
		}
		fmt.Println(cliLifecycleText(
			"OneinStack Panel and its installation data were permanently removed.",
			"OneinStack Panel 及安装目录内的数据已永久删除。",
		))
	} else {
		if err := os.Remove(filepath.Join(basePath, "one")); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove Panel binary: %w", err)
		}
		fmt.Printf("%s\n", cliLifecycleText(
			"OneinStack Panel was uninstalled; configuration and data were preserved at "+basePath,
			"OneinStack Panel 已卸载，配置和数据保留在 "+basePath,
		))
	}
	if _, lookErr := exec.LookPath("systemctl"); lookErr == nil {
		_ = exec.Command("systemctl", "daemon-reload").Run()
	}
	return nil
}

func safeUninstallBasePath(path string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(path))
	switch clean {
	case "", "/", "/usr", "/usr/local", "/opt", "/var", "/data", "/tmp":
		return "", errors.New(cliLifecycleText(
			"refusing to uninstall from an unsafe base path",
			"拒绝从不安全的基础路径执行卸载",
		))
	}
	if !filepath.IsAbs(clean) {
		return "", errors.New(cliLifecycleText(
			"Panel base path must be absolute",
			"面板基础路径必须是绝对路径",
		))
	}
	return clean, nil
}

func verifyManagedFile(path string) error {
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read service file: %w", err)
	}
	if !strings.Contains(string(contents), managedInstallerMarker) {
		return fmt.Errorf("%s", cliLifecycleText(
			"service file is not managed by OneinStack; refusing to delete: "+path,
			"服务文件不属于 OneinStack，拒绝删除："+path,
		))
	}
	return nil
}

func stopManagedServices(serviceFiles []string) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil
	}
	for _, serviceFile := range serviceFiles {
		if _, err := os.Stat(serviceFile); os.IsNotExist(err) {
			continue
		}
		serviceName := filepath.Base(serviceFile)
		if serviceName == "one.service" {
			if err := exec.Command("systemctl", "disable", "--now", serviceName).Run(); err != nil {
				return fmt.Errorf("stop Panel service: %w", err)
			}
			continue
		}
		if err := exec.Command("systemctl", "stop", serviceName).Run(); err != nil {
			return fmt.Errorf("stop Panel service: %w", err)
		}
	}
	return nil
}
