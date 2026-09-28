package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/internal/i18n"
	"oneinstack/internal/models"
	"oneinstack/internal/services/componentstate"
	softwareService "oneinstack/internal/services/software"
	"oneinstack/internal/services/softwaretask"
	systemservice "oneinstack/internal/services/system"
	softwareHandler "oneinstack/router/handler/software"
	"oneinstack/router/input"

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
	Short: "Uninstall the Panel; purge also removes managed components and owned data",
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
		{"uninstall [--purge --yes]", "Uninstall the Panel; purge removes managed components and owned data"},
		{"lang [en-US|zh-CN]", "Show or change CLI language"},
		{"version", "Show version information"},
	}
	translations := map[string]string{
		"Show access URL and bootstrap credentials":                            "显示访问地址和初始化凭据",
		"Reset the administrator password":                                     "修改管理员密码",
		"Show the current panel access entry":                                  "显示当前面板访问入口",
		"Control the Panel server":                                             "控制面板服务",
		"Uninstall the Panel; purge removes managed components and owned data": "卸载面板；purge 会同时删除受管组件及其所有权范围内的数据",
		"Show or change CLI language":                                          "查看或切换 CLI 语言",
		"Show version information":                                             "显示版本信息",
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
			"uninstall --purge permanently deletes Panel, managed components, and owned data; use --purge --yes",
			"uninstall --purge 会永久删除 Panel、受管组件及其所有权范围内的数据，必须同时使用 --purge --yes",
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

	var purgePlan []managedPurgeComponent
	var purgeFailures []managedPurgeFailure
	if uninstallPurge {
		if err := app.Initialize(); err != nil {
			return fmt.Errorf("%s: %w", cliLifecycleText(
				"initialize Panel state before managed component purge",
				"清理受管组件前初始化 Panel 状态失败",
			), err)
		}
		var err error
		purgePlan, _, err = prepareManagedComponentPurge(context.Background())
		if err != nil {
			return err
		}
		printManagedPurgePlan(purgePlan)
	}

	if err := stopManagedServices(serviceFiles); err != nil {
		return err
	}
	if uninstallPurge {
		// Close the small window between the first read-only preflight and
		// stopping the Panel service. A just-finished install must be included in
		// the purge plan instead of becoming a new orphan.
		var preparationFailures []managedPurgeFailure
		purgePlan, preparationFailures, err = prepareManagedComponentPurge(context.Background())
		if err != nil {
			if restoreErr := restorePanelAfterFailedPurge(); restoreErr != nil {
				return fmt.Errorf("%w; %s: %v", err, cliLifecycleText(
					"restoring Panel service also failed",
					"恢复 Panel 服务也失败",
				), restoreErr)
			}
			return err
		}
		purgeFailures = mergeManagedPurgeFailures(nil, preparationFailures...)
		executionFailures, err := executeManagedComponentPurge(context.Background(), purgePlan)
		if err != nil {
			if restoreErr := restorePanelAfterFailedPurge(); restoreErr != nil {
				return fmt.Errorf("%w; %s: %v", err, cliLifecycleText(
					"restoring Panel service also failed",
					"恢复 Panel 服务也失败",
				), restoreErr)
			}
			return err
		}
		purgeFailures = mergeManagedPurgeFailures(purgeFailures, executionFailures...)
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
		if database := app.DB(); database != nil {
			if sqlDB, dbErr := database.DB(); dbErr == nil {
				_ = sqlDB.Close()
			}
		}
		if err := os.RemoveAll(basePath); err != nil {
			return fmt.Errorf("purge Panel data: %w", err)
		}
		if len(purgeFailures) == 0 {
			fmt.Println(cliLifecycleText(
				"OneinStack Panel, managed components, and owned data were permanently removed.",
				"OneinStack Panel、受管组件及其所有权范围内的数据已永久删除。",
			))
		} else {
			fmt.Println(cliLifecycleText(
				"OneinStack Panel was permanently removed, but some managed component cleanup failed. Their remaining owned paths were retained:",
				"OneinStack Panel 已永久卸载，但部分受管组件清理失败；这些组件剩余的所有权路径已保留：",
			))
			for _, failure := range purgeFailures {
				fmt.Printf("  - %s %s [%s]\n", failure.Component, failure.Version, failure.Code)
			}
		}
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

type managedPurgeComponent struct {
	Key          string
	Component    string
	Version      string
	StateDir     string
	CleanupPaths []string
	Parameters   map[string]string
	AdoptRowID   int
	InstallTime  time.Time
}

type managedPurgeFailure struct {
	Component string
	Version   string
	Code      string
}

func mergeManagedPurgeFailures(existing []managedPurgeFailure, additions ...managedPurgeFailure) []managedPurgeFailure {
	result := append([]managedPurgeFailure(nil), existing...)
	seen := make(map[string]bool, len(result)+len(additions))
	for _, failure := range result {
		seen[failure.Component+"\x00"+failure.Version+"\x00"+failure.Code] = true
	}
	for _, failure := range additions {
		if !validManagedComponentName(failure.Component) {
			failure.Component = "managed-component"
		}
		if !validManagedVersion(failure.Version) {
			failure.Version = "unknown-version"
		}
		if !validManagedPurgeFailureCode(failure.Code) {
			failure.Code = "PURGE_FAILED"
		}
		key := failure.Component + "\x00" + failure.Version + "\x00" + failure.Code
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, failure)
	}
	return result
}

func validManagedPurgeFailureCode(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func filterUniqueManagedPurgeKeys(plans []managedPurgeComponent) ([]managedPurgeComponent, []managedPurgeFailure) {
	owners := make(map[string]map[string]managedPurgeComponent)
	for _, plan := range plans {
		if owners[plan.Key] == nil {
			owners[plan.Key] = make(map[string]managedPurgeComponent)
		}
		owners[plan.Key][plan.Component] = plan
	}
	conflicted := make(map[string]bool)
	failures := make([]managedPurgeFailure, 0)
	for _, components := range owners {
		if len(components) < 2 {
			continue
		}
		for component, plan := range components {
			conflicted[component] = true
			failures = mergeManagedPurgeFailures(failures, managedPurgeFailure{
				Component: component,
				Version:   plan.Version,
				Code:      "COMPONENT_KEY_CONFLICT",
			})
		}
	}
	filtered := make([]managedPurgeComponent, 0, len(plans))
	for _, plan := range plans {
		if conflicted[plan.Component] {
			continue
		}
		filtered = append(filtered, plan)
	}
	return filtered, failures
}

func prepareManagedComponentPurge(ctx context.Context) ([]managedPurgeComponent, []managedPurgeFailure, error) {
	database := app.DB()
	if database == nil {
		return nil, nil, errors.New(cliLifecycleText(
			"Panel database is unavailable; refusing to purge managed components",
			"Panel 数据库不可用，拒绝清理受管组件",
		))
	}
	if err := ensureNoActiveManagedTasks(database); err != nil {
		return nil, nil, err
	}

	var rows []models.Software
	if err := database.Where("installed = ?", true).
		Order("install_time DESC, id DESC").
		Find(&rows).Error; err != nil {
		return nil, nil, fmt.Errorf("list installed managed components: %w", err)
	}
	plans := make([]managedPurgeComponent, 0, len(rows))
	failures := make([]managedPurgeFailure, 0)
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		key := strings.ToLower(strings.TrimSpace(row.Key))
		component := strings.ToLower(strings.TrimSpace(row.Component))
		if component == "" {
			component = key
		}
		if component != "" && seen[component] {
			continue
		}
		if component != "" {
			seen[component] = true
		}
		version := strings.TrimSpace(row.InstallVersion)
		if version == "" {
			version = strings.TrimSpace(row.Version)
		}
		if key == "" || !validManagedComponentName(component) || version == "" {
			failureComponent := component
			if failureComponent == "" {
				failureComponent = key
			}
			if !validManagedComponentName(failureComponent) {
				failureComponent = "unknown-component"
			}
			failures = mergeManagedPurgeFailures(failures, managedPurgeFailure{
				Component: failureComponent,
				Version:   version,
				Code:      "COMPONENT_METADATA_INCOMPLETE",
			})
			continue
		}
		stateRoot := "/var/lib/oneinstack/components"
		runtimeValues := make(map[string]string)
		if strings.TrimSpace(row.RuntimeParamsJSON) != "" {
			if err := json.Unmarshal([]byte(row.RuntimeParamsJSON), &runtimeValues); err != nil {
				failures = mergeManagedPurgeFailures(failures, managedPurgeFailure{
					Component: component, Version: version, Code: "RUNTIME_STATE_INVALID",
				})
				continue
			}
			runtimeValues = canonicalizePurgeOwnership(runtimeValues)
			if value := strings.TrimSpace(runtimeValues["component-state-dir"]); value != "" {
				stateRoot = value
			}
		}
		ownedPaths := []string(nil)
		componentStateValid := true
		ownership, ownershipErr := componentstate.Read(filepath.Join(filepath.Clean(stateRoot), component))
		if ownershipErr == nil {
			if ownership.Component != component || ownership.SoftwareVersion != version {
				failures = mergeManagedPurgeFailures(failures, managedPurgeFailure{
					Component: component, Version: version, Code: "OWNERSHIP_STATE_CONFLICT",
				})
				continue
			}
			for parameterKey, parameterValue := range ownership.Parameters {
				if existing := strings.TrimSpace(runtimeValues[parameterKey]); existing != "" && existing != parameterValue {
					if managedPurgeParameterConflictIsUnsafe(parameterKey, existing, parameterValue) {
						componentStateValid = false
						break
					}
				}
				// The Panel ownership record is written after a successful managed
				// action and is the best available value when a non-path runtime
				// parameter has drifted in the database row.
				runtimeValues[parameterKey] = parameterValue
			}
			if !componentStateValid {
				failures = mergeManagedPurgeFailures(failures, managedPurgeFailure{
					Component: component, Version: version, Code: "OWNERSHIP_STATE_CONFLICT",
				})
				continue
			}
			ownedPaths = ownership.PurgePaths
		} else if !os.IsNotExist(ownershipErr) {
			failures = mergeManagedPurgeFailures(failures, managedPurgeFailure{
				Component: component, Version: version, Code: "OWNERSHIP_STATE_INVALID",
			})
			continue
		}
		supplemental, recordedVersion, supplementalErr := readSupplementalManagedState(
			filepath.Join(filepath.Clean(stateRoot), component),
			component,
		)
		if supplementalErr != nil && !os.IsNotExist(supplementalErr) {
			failures = mergeManagedPurgeFailures(failures, managedPurgeFailure{
				Component: component, Version: version, Code: "SUPPLEMENTAL_STATE_INVALID",
			})
			continue
		}
		if supplementalErr == nil {
			if recordedVersion != "" && recordedVersion != version {
				failures = mergeManagedPurgeFailures(failures, managedPurgeFailure{
					Component: component, Version: version, Code: "COMPONENT_VERSION_CONFLICT",
				})
				continue
			}
			for parameterKey, parameterValue := range supplemental {
				if existing := strings.TrimSpace(runtimeValues[parameterKey]); existing != "" && existing != parameterValue {
					if managedPurgeParameterConflictIsUnsafe(parameterKey, existing, parameterValue) {
						componentStateValid = false
						break
					}
					// Keep the Panel database/ownership value ahead of legacy
					// supplemental scalar state when only a non-path value differs.
					continue
				}
				runtimeValues[parameterKey] = parameterValue
			}
			if !componentStateValid {
				failures = mergeManagedPurgeFailures(failures, managedPurgeFailure{
					Component: component, Version: version, Code: "OWNERSHIP_STATE_CONFLICT",
				})
				continue
			}
		}
		plan, err := buildManagedPurgePlan(key, component, version, stateRoot, runtimeValues, ownedPaths)
		if err != nil {
			failures = mergeManagedPurgeFailures(failures, managedPurgeFailure{
				Component: component, Version: version, Code: "OWNED_PATH_INVALID",
			})
			continue
		}
		plan.InstallTime = row.InstallTime
		plans = append(plans, plan)
	}
	orphanPlans, err := discoverOrphanManagedComponentState(database, plans)
	if err != nil {
		failures = mergeManagedPurgeFailures(failures, managedPurgeFailure{
			Component: "orphan-component-scan", Code: "ORPHAN_STATE_DISCOVERY_FAILED",
		})
	} else {
		plans = append(plans, orphanPlans...)
	}
	plans, duplicateFailures := filterUniqueManagedPurgeKeys(plans)
	failures = mergeManagedPurgeFailures(failures, duplicateFailures...)
	sort.SliceStable(plans, func(i, j int) bool {
		if !plans[i].InstallTime.Equal(plans[j].InstallTime) {
			return plans[i].InstallTime.After(plans[j].InstallTime)
		}
		return plans[i].Component < plans[j].Component
	})
	installer := softwareService.NewInstaller()
	resolvedPlans := make([]managedPurgeComponent, 0, len(plans))
	for _, plan := range plans {
		parameters := clonePurgeParameters(plan.Parameters)
		parameters["data-policy"] = "delete"
		parameters["delete-data-confirm"] = "true"
		params := &input.RemoveParams{
			Name: plan.Component, Version: plan.Version, DataPolicy: "delete", ConfirmDataDeletion: true,
			Parameters: parameters,
		}
		resolvedValues, resolvedPaths, err := installer.ResolveUninstallOwnership(ctx, params)
		if err != nil {
			failures = mergeManagedPurgeFailures(failures, managedPurgeFailure{
				Component: plan.Component, Version: plan.Version, Code: "UNINSTALL_PREFLIGHT_FAILED",
			})
			continue
		}
		for key, value := range resolvedValues {
			if strings.TrimSpace(plan.Parameters[key]) == "" {
				plan.Parameters[key] = value
			}
		}
		stateRoot := filepath.Dir(plan.StateDir)
		if value := strings.TrimSpace(plan.Parameters["component-state-dir"]); value != "" {
			stateRoot = value
		}
		ownedPaths := make([]string, 0, len(plan.CleanupPaths)+len(resolvedPaths))
		for _, value := range plan.CleanupPaths {
			if value != plan.StateDir {
				ownedPaths = append(ownedPaths, value)
			}
		}
		ownedPaths = append(ownedPaths, resolvedPaths...)
		rebuilt, err := buildManagedPurgePlan(
			plan.Key,
			plan.Component,
			plan.Version,
			stateRoot,
			plan.Parameters,
			ownedPaths,
		)
		if err != nil {
			failures = mergeManagedPurgeFailures(failures, managedPurgeFailure{
				Component: plan.Component, Version: plan.Version, Code: "OWNED_PATH_INVALID",
			})
			continue
		}
		rebuilt.AdoptRowID = plan.AdoptRowID
		rebuilt.InstallTime = plan.InstallTime
		resolvedPlans = append(resolvedPlans, rebuilt)
	}
	return resolvedPlans, failures, nil
}

func executeManagedComponentPurge(ctx context.Context, plans []managedPurgeComponent) ([]managedPurgeFailure, error) {
	database := app.DB()
	if err := ensureNoActiveManagedTasks(database); err != nil {
		return nil, err
	}
	if len(plans) == 0 {
		return nil, nil
	}
	plans, adoptionFailures, err := adoptOrphanManagedComponents(database, plans)
	if err != nil {
		return nil, err
	}
	failures := mergeManagedPurgeFailures(nil, adoptionFailures...)
	requestedBy, err := purgeRequestedBy()
	if err != nil {
		return failures, err
	}
	manager, err := softwareHandler.GetTaskManagerForLocalLifecycle()
	if err != nil {
		return failures, fmt.Errorf("initialize managed component task runner: %w", err)
	}
	managerStopped := false
	defer func() {
		if managerStopped {
			return
		}
		stopContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = manager.Stop(stopContext)
	}()
	for _, plan := range plans {
		fmt.Printf("%s\n", cliLifecycleText(
			fmt.Sprintf("Purging managed component %s %s...", plan.Component, plan.Version),
			fmt.Sprintf("正在彻底清理受管组件 %s %s……", plan.Component, plan.Version),
		))
		parameters := clonePurgeParameters(plan.Parameters)
		parameters["data-policy"] = "delete"
		parameters["delete-data-confirm"] = "true"
		task, err := manager.SubmitUninstallWithParameters(
			plan.Component,
			plan.Version,
			parameters,
			requestedBy,
		)
		if err != nil {
			failures = mergeManagedPurgeFailures(failures, managedPurgeFailure{
				Component: plan.Component, Version: plan.Version, Code: "UNINSTALL_SUBMIT_FAILED",
			})
			continue
		}
		if task == nil {
			failures = mergeManagedPurgeFailures(failures, managedPurgeFailure{
				Component: plan.Component, Version: plan.Version, Code: "UNINSTALL_SUBMIT_FAILED",
			})
			continue
		}
		completed, err := waitForPurgeTask(ctx, manager, task.ID)
		if err != nil {
			return nil, err
		}
		if completed.Status != models.SoftwareTaskStatusSucceeded {
			code := strings.TrimSpace(completed.ErrorCode)
			if !validManagedPurgeFailureCode(code) {
				code = "UNINSTALL_FAILED"
			}
			failures = mergeManagedPurgeFailures(failures, managedPurgeFailure{
				Component: plan.Component,
				Version:   plan.Version,
				Code:      code,
			})
			continue
		}
		for _, ownedPath := range plan.CleanupPaths {
			if err := os.RemoveAll(ownedPath); err != nil {
				failures = mergeManagedPurgeFailures(failures, managedPurgeFailure{
					Component: plan.Component,
					Version:   plan.Version,
					Code:      "OWNED_PATH_CLEANUP_FAILED",
				})
				break
			}
		}
	}
	stopContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := manager.Stop(stopContext); err != nil {
		return nil, fmt.Errorf("stop managed component task runner: %w", err)
	}
	managerStopped = true
	return failures, nil
}

func ensureNoActiveManagedTasks(database *gorm.DB) error {
	if database == nil {
		return errors.New(cliLifecycleText(
			"Panel database is unavailable; refusing to purge managed components",
			"Panel 数据库不可用，拒绝清理受管组件",
		))
	}
	var activeTasks int64
	if err := database.Model(&models.SoftwareTask{}).
		Where("status IN ?", models.ActiveSoftwareTaskStatuses()).
		Count(&activeTasks).Error; err != nil {
		return fmt.Errorf("check active component tasks: %w", err)
	}
	if activeTasks > 0 {
		return fmt.Errorf("%s", cliLifecycleText(
			"managed component tasks are still active; wait for them to finish before purge",
			"仍有受管组件任务正在执行，请等待任务完成后再彻底卸载",
		))
	}
	return nil
}

func clonePurgeParameters(values map[string]string) map[string]string {
	result := make(map[string]string, len(values)+2)
	for key, value := range values {
		result[key] = value
	}
	return result
}

func canonicalizePurgeOwnership(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		canonical := componentstate.NormalizeParameterName(key)
		if componentstate.IsStateRootParameter(canonical) {
			canonical = "component-state-dir"
		}
		if canonical != "" && strings.TrimSpace(value) != "" {
			result[canonical] = strings.TrimSpace(value)
		}
	}
	return result
}

func validateUniqueManagedPurgeKeys(plans []managedPurgeComponent) error {
	keyOwners := make(map[string]string)
	for _, plan := range plans {
		if owner := keyOwners[plan.Key]; owner != "" && owner != plan.Component {
			return fmt.Errorf("multiple managed components share software key %s: %s, %s", plan.Key, owner, plan.Component)
		}
		keyOwners[plan.Key] = plan.Component
	}
	return nil
}

func waitForPurgeTask(ctx context.Context, manager *softwaretask.Manager, taskID string) (*models.SoftwareTask, error) {
	updates, unsubscribe := manager.Subscribe(taskID)
	defer unsubscribe()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		task, err := manager.Get(taskID)
		if err != nil {
			return nil, fmt.Errorf("read managed purge task %s: %w", taskID, err)
		}
		if models.IsSoftwareTaskTerminal(task.Status) {
			return task, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-updates:
		case <-ticker.C:
		}
	}
}

func purgeRequestedBy() (int64, error) {
	var user models.User
	database := app.DB()
	result := database.Where("is_admin = ?", true).Order("id ASC").First(&user)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		result = database.Order("id ASC").First(&user)
	}
	if result.Error != nil {
		return 0, fmt.Errorf("resolve purge audit user: %w", result.Error)
	}
	return user.ID, nil
}

func validatePurgePath(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("owned path contains control characters")
	}
	cleaned := filepath.Clean(strings.TrimSpace(value))
	if !filepath.IsAbs(cleaned) {
		return "", errors.New("owned path must be absolute")
	}
	switch cleaned {
	case "/", "/usr", "/usr/local", "/etc", "/var", "/var/lib", "/data", "/home", "/root", "/tmp":
		return "", errors.New("owned path is too broad")
	}
	panelBase := filepath.Clean(app.GetBasePath())
	if relative, err := filepath.Rel(cleaned, panelBase); err == nil &&
		(relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))) {
		return "", errors.New("owned path contains the Panel base directory")
	}
	return cleaned, nil
}

func uniquePaths(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func buildManagedPurgePlan(
	key string,
	component string,
	version string,
	stateRoot string,
	runtimeValues map[string]string,
	ownedPaths []string,
) (managedPurgeComponent, error) {
	stateDir, err := validatePurgePath(filepath.Join(stateRoot, component))
	if err != nil {
		return managedPurgeComponent{}, fmt.Errorf("validate %s state directory: %w", component, err)
	}
	parameters := clonePurgeParameters(runtimeValues)
	if strings.TrimSpace(parameters["component-state-dir"]) == "" {
		parameters["component-state-dir"] = filepath.Clean(stateRoot)
	}
	cleanupPaths := make([]string, 0, len(ownedPaths)+6)
	for _, value := range ownedPaths {
		if value == "" {
			continue
		}
		validated, err := validatePurgePath(value)
		if err != nil {
			return managedPurgeComponent{}, fmt.Errorf("validate %s declared owned path: %w", component, err)
		}
		cleanupPaths = append(cleanupPaths, validated)
	}
	for name, value := range parameters {
		if !componentstate.IsLegacyPurgeParameter(name) || strings.TrimSpace(value) == "" {
			continue
		}
		validated, err := validatePurgePath(value)
		if err != nil {
			return managedPurgeComponent{}, fmt.Errorf("validate %s legacy owned path %s: %w", component, name, err)
		}
		cleanupPaths = append(cleanupPaths, validated)
	}
	// Remove runtime data before the state marker. If a filesystem error occurs,
	// the marker remains available for an explicit retry instead of orphaning
	// the remaining path again.
	cleanupPaths = append(cleanupPaths, stateDir)
	return managedPurgeComponent{
		Key:          key,
		Component:    component,
		Version:      version,
		StateDir:     stateDir,
		CleanupPaths: uniquePaths(cleanupPaths),
		Parameters:   parameters,
	}, nil
}

func discoverOrphanManagedComponentState(
	database *gorm.DB,
	plans []managedPurgeComponent,
) ([]managedPurgeComponent, error) {
	tracked := make(map[string]bool, len(plans))
	stateRoots := map[string]bool{"/var/lib/oneinstack/components": true}
	for _, plan := range plans {
		tracked[plan.Component] = true
		stateRoots[filepath.Dir(plan.StateDir)] = true
	}
	markers := []string{
		componentstate.OwnershipFileName, "version", "installed.json", "installed", "ownership",
		"installed-by-oneinstack", "package-installed-by-oneinstack", "managed",
	}
	orphans := make([]managedPurgeComponent, 0)
	for stateRoot := range stateRoots {
		entries, err := os.ReadDir(stateRoot)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect managed component state %s: %w", stateRoot, err)
		}
		for _, entry := range entries {
			component := strings.ToLower(strings.TrimSpace(entry.Name()))
			if !entry.IsDir() || tracked[component] {
				continue
			}
			stateDir := filepath.Join(stateRoot, entry.Name())
			active := false
			for _, marker := range markers {
				if info, statErr := os.Lstat(filepath.Join(stateDir, marker)); statErr == nil && info.Mode().IsRegular() {
					active = true
					break
				}
			}
			if !active {
				continue
			}
			if entry.Name() != component || !validManagedComponentName(component) {
				return nil, fmt.Errorf("invalid managed component state directory: %s", entry.Name())
			}
			version, runtimeValues, ownedPaths, err := readOrphanManagedState(stateDir, component)
			if err != nil {
				return nil, err
			}
			var catalogRow models.Software
			result := database.Where("component = ? AND catalog_managed = ?", component, true).
				Order("catalog_managed DESC, catalog_visible DESC, id DESC").
				First(&catalogRow)
			if result.Error != nil {
				if errors.Is(result.Error, gorm.ErrRecordNotFound) {
					return nil, fmt.Errorf("%s", cliLifecycleText(
						"managed state has no matching component catalog entry: "+component,
						"受管状态在组件目录中没有对应条目："+component,
					))
				}
				return nil, fmt.Errorf("look up orphan component %s: %w", component, result.Error)
			}
			plan, err := buildManagedPurgePlan(
				strings.ToLower(strings.TrimSpace(catalogRow.Key)),
				component,
				version,
				stateRoot,
				runtimeValues,
				ownedPaths,
			)
			if err != nil {
				return nil, err
			}
			plan.AdoptRowID = catalogRow.Id
			if info, statErr := os.Stat(stateDir); statErr == nil {
				plan.InstallTime = info.ModTime()
			}
			orphans = append(orphans, plan)
			tracked[component] = true
		}
	}
	return orphans, nil
}

func validManagedComponentName(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') &&
			character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return value[0] != '-' && value[0] != '_' && value[0] != '.'
}

func readOrphanManagedState(stateDir, expectedComponent string) (string, map[string]string, []string, error) {
	version := ""
	parameters := make(map[string]string)
	ownedPaths := []string(nil)
	if ownership, err := componentstate.Read(stateDir); err == nil {
		if ownership.Component != expectedComponent {
			return "", nil, nil, fmt.Errorf("managed state component mismatch: directory=%s state=%s", expectedComponent, ownership.Component)
		}
		version = ownership.SoftwareVersion
		parameters = canonicalizePurgeOwnership(ownership.Parameters)
		ownedPaths = ownership.PurgePaths
	} else if !os.IsNotExist(err) {
		return "", nil, nil, fmt.Errorf("read %s Panel ownership state: %w", expectedComponent, err)
	}

	for _, marker := range []string{"version", "software-version", "requested-version", "runtime-version"} {
		content, err := readRegularManagedStateFile(filepath.Join(stateDir, marker), 4096)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", nil, nil, fmt.Errorf("read %s managed %s: %w", expectedComponent, marker, err)
		}
		candidate := strings.TrimSpace(string(content))
		if candidate == "" {
			continue
		}
		if !validManagedVersion(candidate) {
			return "", nil, nil, fmt.Errorf("managed %s %s is invalid", expectedComponent, marker)
		}
		if version != "" && (marker == "version" || marker == "software-version") && version != candidate {
			return "", nil, nil, fmt.Errorf("managed %s version markers disagree", expectedComponent)
		}
		if version == "" {
			version = candidate
		}
	}

	supplemental, recordedVersion, err := readSupplementalManagedState(stateDir, expectedComponent)
	if err != nil {
		return "", nil, nil, err
	}
	if recordedVersion != "" && version != "" && recordedVersion != version {
		return "", nil, nil, fmt.Errorf("managed %s version and install parameters disagree", expectedComponent)
	}
	if version == "" {
		version = recordedVersion
	}
	for key, value := range supplemental {
		if err := mergeManagedOwnershipParameter(parameters, key, value, expectedComponent); err != nil {
			return "", nil, nil, err
		}
	}

	// This is the sole component-specific compatibility bridge: very old
	// firewalld state predates every version marker and the generic ownership
	// file. New components must use panel-ownership.json instead.
	if version == "" && expectedComponent == "firewalld" && managedFirewalldMarkerExists(stateDir) {
		version = detectLegacyFirewalldRuntimeVersion()
	}
	if !validManagedVersion(version) {
		return "", nil, nil, fmt.Errorf("%s", cliLifecycleText(
			"managed "+expectedComponent+" state has no trustworthy software version; refusing purge",
			"受管组件 "+expectedComponent+" 的状态中缺少可信软件版本，拒绝清理",
		))
	}
	parameters["component-state-dir"] = filepath.Dir(stateDir)
	return version, parameters, ownedPaths, nil
}

func mergeManagedOwnershipParameter(parameters map[string]string, key, value, component string) error {
	normalized := componentstate.NormalizeParameterName(key)
	value = strings.TrimSpace(value)
	if normalized == "" || value == "" || componentstate.IsSensitiveParameter(normalized) {
		return nil
	}
	if componentstate.IsStateRootParameter(normalized) {
		normalized = "component-state-dir"
	}
	if existing := strings.TrimSpace(parameters[normalized]); existing != "" && existing != value {
		if managedPurgeParameterConflictIsUnsafe(normalized, existing, value) {
			return fmt.Errorf("managed %s ownership state disagrees for %s", component, normalized)
		}
		return nil
	}
	parameters[normalized] = value
	return nil
}

// A stale scalar parameter must not block Panel removal, but disagreement on a
// filesystem path can change the purge scope. Keep rejecting those conflicts.
func managedPurgeParameterConflictIsUnsafe(name, left, right string) bool {
	if componentstate.IsStateRootParameter(name) || componentstate.IsLegacyPurgeParameter(name) {
		return true
	}
	return filepath.IsAbs(strings.TrimSpace(left)) || filepath.IsAbs(strings.TrimSpace(right))
}

func readSupplementalManagedState(stateDir, expectedComponent string) (map[string]string, string, error) {
	parameters, err := readManagedScalarStateParameters(stateDir)
	if err != nil {
		return nil, "", fmt.Errorf("read %s scalar ownership state: %w", expectedComponent, err)
	}
	recordedVersion := ""
	if content, err := readRegularManagedStateFile(filepath.Join(stateDir, "installed.json"), 64*1024); err == nil {
		var state map[string]any
		if err := json.Unmarshal(content, &state); err != nil {
			return nil, "", fmt.Errorf("decode %s managed state: %w", expectedComponent, err)
		}
		for key, rawValue := range state {
			value, ok := rawValue.(string)
			if !ok || strings.TrimSpace(value) == "" {
				continue
			}
			normalized := componentstate.NormalizeParameterName(key)
			switch normalized {
			case "component":
				recorded := strings.ToLower(strings.TrimSpace(value))
				if recorded != expectedComponent {
					return nil, "", fmt.Errorf("managed state component mismatch: directory=%s state=%s", expectedComponent, recorded)
				}
			case "software-version":
				recordedVersion = strings.TrimSpace(value)
			default:
				if err := mergeManagedOwnershipParameter(parameters, normalized, value, expectedComponent); err != nil {
					return nil, "", err
				}
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, "", fmt.Errorf("read %s installed state: %w", expectedComponent, err)
	}
	installParameters, err := readManagedInstallParameters(filepath.Join(stateDir, "install-parameters"))
	if err != nil {
		return nil, "", fmt.Errorf("read %s managed install parameters: %w", expectedComponent, err)
	}
	if value := strings.TrimSpace(installParameters["software-version"]); value != "" {
		if recordedVersion != "" && recordedVersion != value {
			return nil, "", fmt.Errorf("managed %s software version states disagree", expectedComponent)
		}
		recordedVersion = value
	}
	delete(installParameters, "software-version")
	for key, value := range installParameters {
		if err := mergeManagedOwnershipParameter(parameters, key, value, expectedComponent); err != nil {
			return nil, "", err
		}
	}
	return parameters, recordedVersion, nil
}

func readManagedScalarStateParameters(stateDir string) (map[string]string, error) {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	ignored := map[string]bool{
		componentstate.OwnershipFileName: true,
		"installed.json":                 true, "install-parameters": true,
		"version": true, "software-version": true, "requested-version": true, "runtime-version": true,
	}
	for _, entry := range entries {
		if entry.IsDir() || ignored[entry.Name()] {
			continue
		}
		name := componentstate.NormalizeParameterName(entry.Name())
		if name == "" || componentstate.IsSensitiveParameter(name) {
			continue
		}
		content, err := readRegularManagedStateFile(filepath.Join(stateDir, entry.Name()), 4096)
		if err != nil {
			if strings.Contains(err.Error(), "bounded regular file") {
				continue
			}
			return nil, err
		}
		value := strings.TrimSpace(string(content))
		if value == "" || strings.ContainsAny(value, "\x00\r\n") {
			continue
		}
		result[name] = value
	}
	return result, nil
}

func managedFirewalldMarkerExists(stateDir string) bool {
	info, err := os.Lstat(filepath.Join(stateDir, "installed-by-oneinstack"))
	return err == nil && info.Mode().IsRegular()
}

func detectLegacyFirewalldRuntimeVersion() string {
	type versionCommand struct {
		name string
		args []string
	}
	commands := []versionCommand{
		{name: "rpm", args: []string{"-q", "--qf", "%{VERSION}", "firewalld"}},
		{name: "dpkg-query", args: []string{"-W", "-f=${Version}", "firewalld"}},
		{name: "firewall-cmd", args: []string{"--version"}},
	}
	for _, candidate := range commands {
		if _, err := exec.LookPath(candidate.name); err != nil {
			continue
		}
		probeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		output, err := exec.CommandContext(probeContext, candidate.name, candidate.args...).Output()
		cancel()
		if err != nil {
			continue
		}
		version := strings.TrimSpace(strings.SplitN(string(output), "\n", 2)[0])
		if candidate.name == "dpkg-query" {
			if separator := strings.IndexByte(version, ':'); separator >= 0 {
				version = version[separator+1:]
			}
			if separator := strings.IndexByte(version, '-'); separator >= 0 {
				version = version[:separator]
			}
		}
		if validManagedVersion(version) {
			return version
		}
	}
	return ""
}

func readRegularManagedStateFile(path string, maximumSize int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maximumSize {
		return nil, errors.New("state file must be a bounded regular file")
	}
	return os.ReadFile(path)
}

func validManagedVersion(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '.' && character != '-' && character != '_' && character != '+' {
			return false
		}
	}
	return true
}

func readManagedInstallParameters(path string) (map[string]string, error) {
	result := make(map[string]string)
	content, err := readRegularManagedStateFile(path, 64*1024)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	// This file is legacy supplemental state. Panel database and ownership
	// records are the primary sources, so one damaged line must not block an
	// otherwise safe uninstall. Parse valid assignments independently and
	// ignore malformed lines; never evaluate the contents as shell code.
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || !validManagedParameterKey(key) || strings.ContainsAny(value, "\x00\r\n") {
			continue
		}
		compactKey := strings.ToUpper(strings.TrimSpace(key))
		if strings.Contains(compactKey, "PASSWORD") || strings.Contains(compactKey, "SECRET") ||
			strings.Contains(compactKey, "TOKEN") || strings.Contains(compactKey, "CREDENTIAL") ||
			strings.HasSuffix(compactKey, "_KEY") || compactKey == "KEY" {
			continue
		}
		canonical := strings.ToLower(strings.ReplaceAll(compactKey, "_", "-"))
		result[canonical] = strings.TrimSpace(value)
	}
	return result, nil
}

func validManagedParameterKey(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if index == 0 && character >= '0' && character <= '9' {
			return false
		}
		if (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func adoptOrphanManagedComponents(database *gorm.DB, plans []managedPurgeComponent) ([]managedPurgeComponent, []managedPurgeFailure, error) {
	if err := validateUniqueManagedPurgeKeys(plans); err != nil {
		return nil, nil, err
	}
	ready := make([]managedPurgeComponent, 0, len(plans))
	failures := make([]managedPurgeFailure, 0)
	for _, plan := range plans {
		if plan.AdoptRowID == 0 {
			ready = append(ready, plan)
			continue
		}
		runtimeJSON, err := json.Marshal(plan.Parameters)
		if err == nil {
			err = database.Transaction(func(tx *gorm.DB) error {
				if err := tx.Model(&models.Software{}).
					Where("`key` = ? AND id <> ?", plan.Key, plan.AdoptRowID).
					Updates(map[string]any{
						"installed":       false,
						"install_version": "",
						"runtime_params":  "",
						"is_update":       false,
					}).Error; err != nil {
					return err
				}
				result := tx.Model(&models.Software{}).
					Where("id = ? AND component = ?", plan.AdoptRowID, plan.Component).
					Updates(map[string]any{
						"installed":       true,
						"install_version": plan.Version,
						"runtime_params":  string(runtimeJSON),
						"status":          models.Soft_Status_Suc,
						"is_update":       false,
						"install_time":    time.Now(),
					})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return fmt.Errorf("managed orphan catalog row changed")
				}
				return nil
			})
		}
		if err != nil {
			failures = mergeManagedPurgeFailures(failures, managedPurgeFailure{
				Component: plan.Component, Version: plan.Version, Code: "ORPHAN_ADOPTION_FAILED",
			})
			continue
		}
		ready = append(ready, plan)
	}
	return ready, failures, nil
}

func printManagedPurgePlan(plans []managedPurgeComponent) {
	if len(plans) == 0 {
		fmt.Println(cliLifecycleText(
			"No installed managed components were recorded; purging Panel data only.",
			"未记录已安装的受管组件，将仅清理 Panel 数据。",
		))
		return
	}
	fmt.Println(cliLifecycleText(
		"The following managed components and their owned data will be permanently removed:",
		"以下受管组件及其所有权范围内的数据将被永久删除：",
	))
	for _, plan := range plans {
		fmt.Printf("  - %s %s\n", plan.Component, plan.Version)
		for _, ownedPath := range plan.CleanupPaths {
			fmt.Printf("      %s\n", ownedPath)
		}
	}
}

func restorePanelAfterFailedPurge() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil
	}
	if _, err := os.Stat("/etc/systemd/system/one.service"); os.IsNotExist(err) {
		return nil
	}
	return exec.Command("systemctl", "enable", "--now", "one.service").Run()
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
