package safe

import (
	"bufio"
	"fmt"
	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services"
	"oneinstack/router/input"
	"oneinstack/router/output"
	"os"
	"os/exec"
	"strings"
)

func GetUfwStatus() (*output.IptablesStatus, error) {
	// 获取 UFW 状态
	cmd := exec.Command("ufw", "status", "verbose")
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return &output.IptablesStatus{Enabled: false, PingBlocked: false}, nil
	}

	// 检查 UFW 是否启用
	enabled := strings.Contains(out.String(), "Status: active")

	// 检查是否有阻止 ICMP 请求的规则
	pingBlocked, err := pingStatus()
	if err != nil {
		return &output.IptablesStatus{Enabled: enabled, PingBlocked: false}, err
	}

	return &output.IptablesStatus{Enabled: enabled, PingBlocked: pingBlocked}, nil
}

func GetUfwRules(param *input.IptablesRuleParam) (*services.PaginatedResult[models.IptablesRule], error) {
	tx := app.DB()
	if param.Q != "" {
		tx = tx.Where("remark LIKE ?", "%"+param.Q+"%")
	}
	if param.Direction != "" {
		tx = tx.Where("direction = ?", param.Direction)
	}
	return services.Paginate[models.IptablesRule](tx, &models.IptablesRule{}, &input.Page{
		Page:     param.Page.Page,
		PageSize: param.Page.PageSize,
	})
}

func AddUfwRule(param *models.IptablesRule) error {
	if param.State == 0 {
		return fmt.Errorf("状态不能为禁用")
	}
	err := addUfwRule(param)
	if err != nil {
		return err
	}
	tx := app.DB().Create(param)
	if tx.Error != nil {
		return tx.Error
	}
	return nil
}

// 删除UFW规则函数
func DeleteUfwRule(id int64) error {
	// 从数据库查询规则
	rule := &models.IptablesRule{}
	tx := app.DB().Where("id = ?", id).First(rule)
	if tx.Error != nil {
		return fmt.Errorf("failed to find rule with ID %d: %v", id, tx.Error)
	}

	// 验证协议类型
	validProtocols := []string{"tcp", "udp", "icmp"}
	if !contains(validProtocols, rule.Protocol) {
		return fmt.Errorf(
			"invalid protocol: %s. Valid options are: tcp, udp, icmp",
			rule.Protocol,
		)
	}

	// 验证策略类型
	validStrategies := []string{"allow", "deny"}
	if !contains(validStrategies, rule.Strategy) {
		return fmt.Errorf(
			"invalid strategy: %s. Valid options are: allow, deny",
			rule.Strategy,
		)
	}

	// 处理 IP 列表（过滤空值并设置默认值）
	ipList := filterEmpty(strings.Split(rule.IPs, ","))
	if len(ipList) == 0 {
		ipList = []string{"any"}
	}

	// 处理端口列表（过滤空值并设置默认值）
	portList := filterEmpty(strings.Split(rule.Ports, ","))
	if len(portList) == 0 {
		portList = []string{"0"}
	}

	// 检查 ICMP 协议的端口合法性
	if rule.Protocol == "icmp" {
		for _, port := range portList {
			if port != "0" {
				return fmt.Errorf(
					"invalid port '%s' for icmp protocol (must be 0)",
					port,
				)
			}
		}
	}

	// 遍历所有 IP 和端口组合，生成删除命令
	for _, ip := range ipList {
		for _, port := range portList {
			var cmdArgs []string
			cmdArgs = append(cmdArgs, "delete", rule.Strategy, rule.Direction) // 使用 Strategy 字段

			// 根据方向设置地址参数
			switch rule.Direction {
			case "in":
				cmdArgs = append(cmdArgs, "from", ip, "to", "any")
			case "out":
				cmdArgs = append(cmdArgs, "to", ip)
			default:
				return fmt.Errorf(
					"invalid direction: %s. Valid options are: in, out",
					rule.Direction,
				)
			}

			// 处理端口参数（非 ICMP 且端口非 0 时添加）
			if rule.Protocol != "icmp" && port != "0" {
				cmdArgs = append(cmdArgs, "port", port)
			}

			// 添加协议参数
			cmdArgs = append(cmdArgs, "proto", rule.Protocol)

			// 执行 UFW 命令
			cmd := exec.Command("ufw", cmdArgs...)
			output, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf(
					"failed to delete ufw rule: %v\nCommand: ufw %s\nOutput: %s",
					err,
					strings.Join(cmdArgs, " "),
					string(output),
				)
			}

			fmt.Printf("UFW rule deleted: ufw %s\n", strings.Join(cmdArgs, " "))
		}
	}
	tx = app.DB().Where("id = ?", id).Delete(&models.IptablesRule{})
	if tx.Error != nil {
		return tx.Error
	}
	return nil
}

// 更新UFW规则函数（需传入新旧规则）
func UpdateUfwRule(new *models.IptablesRule) error {
	oldRule := &models.IptablesRule{}
	tx := app.DB().Where("id = ?", new.ID).First(oldRule)
	if tx.Error != nil {
		return tx.Error
	}
	if err := DeleteUfwRule(oldRule.ID); err != nil {
		return fmt.Errorf("failed to remove old rule: %v", err)
	}
	if new.State == 0 {
		return fmt.Errorf("状态不能为禁用")
	}
	if err := addUfwRule(new); err != nil {
		// 尝试恢复旧规则
		_ = addUfwRule(oldRule)
		return fmt.Errorf("failed to apply new rule: %v (rolled back)", err)
	}
	tx = app.DB().Model(&models.IptablesRule{}).Where("id = ?", new.ID).Updates(new)
	if tx.Error != nil {
		return tx.Error
	}
	return nil
}

func addUfwRule(rule *models.IptablesRule) error {
	// 验证协议类型
	validProtocols := []string{"tcp", "udp", "icmp"}
	if !contains(validProtocols, rule.Protocol) {
		return fmt.Errorf(
			"invalid protocol: %s. Valid options are: tcp, udp, icmp",
			rule.Protocol,
		)
	}

	// 验证策略类型
	validStrategies := []string{"allow", "deny"}
	if !contains(validStrategies, rule.Strategy) {
		return fmt.Errorf(
			"invalid strategy: %s. Valid options are: allow, deny",
			rule.Strategy,
		)
	}

	// 处理 IP 列表（过滤空值并设置默认值）
	ipList := filterEmpty(strings.Split(rule.IPs, ","))
	if len(ipList) == 0 {
		ipList = []string{"any"}
	}

	// 处理端口列表（过滤空值并设置默认值）
	portList := filterEmpty(strings.Split(rule.Ports, ","))
	if len(portList) == 0 {
		portList = []string{"0"}
	}

	// 检查 ICMP 协议的端口合法性
	if rule.Protocol == "icmp" {
		for _, port := range portList {
			if port != "0" {
				return fmt.Errorf("icmp protocol does not support port '%s'", port)
			}
		}
	}

	// 遍历所有 IP 和端口组合，生成规则
	for _, ip := range ipList {
		for _, port := range portList {
			var cmdArgs []string
			cmdArgs = append(cmdArgs, rule.Strategy, rule.Direction)

			// 根据方向设置地址参数
			switch rule.Direction {
			case "in":
				cmdArgs = append(cmdArgs, "from", ip, "to", "any")
			case "out":
				cmdArgs = append(cmdArgs, "to", ip)
			default:
				return fmt.Errorf(
					"invalid direction: %s. Valid options are: in, out",
					rule.Direction,
				)
			}

			// 处理端口（非 ICMP 且端口非 0 时添加端口参数）
			if rule.Protocol != "icmp" && port != "0" {
				cmdArgs = append(cmdArgs, "port", port)
			}

			// 添加协议参数
			cmdArgs = append(cmdArgs, "proto", rule.Protocol)

			// 执行 UFW 命令
			cmd := exec.Command("ufw", cmdArgs...)
			output, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf(
					"failed to add ufw rule: %v\nCommand: ufw %s\nOutput: %s",
					err,
					strings.Join(cmdArgs, " "),
					string(output),
				)
			}

			fmt.Printf("UFW rule added: ufw %s\n", strings.Join(cmdArgs, " "))
		}
	}

	return nil
}

// ToggleUfw 切换 ufw 的启用和禁用状态
func ToggleUfw() error {
	// 获取 ufw 当前的状态
	cmdStatus := exec.Command("ufw", "status")
	output, err := cmdStatus.CombinedOutput()

	if err != nil {
		return fmt.Errorf("failed to check ufw status: %v, output: %s", err, string(output))
	}
	action := "enable"
	// 判断 UFW 当前状态，查找 "Status" 字段
	if strings.Contains(string(output), "Status: active") {
		action = "disable"
	}

	cmd := exec.Command("ufw", action)
	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to %s ufw: %v, output: %s", action, err, string(output))
	}
	return nil
}

func pingStatus() (bool, error) {
	// 打开 UFW 配置文件
	filePath := "/etc/ufw/before.rules"
	file, err := os.OpenFile(filePath, os.O_RDWR, 0644)
	if err != nil {
		return false, fmt.Errorf("打开配置文件失败: %v", err)
	}
	defer file.Close()

	// 读取文件内容
	var lines []string
	var icmpLineIndex int
	var icmpAllowed bool
	scanner := bufio.NewScanner(file)
	for i := 0; scanner.Scan(); i++ {
		line := scanner.Text()
		lines = append(lines, line)
		if strings.Contains(line, "-A ufw-before-input -p icmp --icmp-type echo-request") {
			icmpLineIndex = i
			icmpAllowed = strings.Contains(line, "ACCEPT")
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("读取配置文件失败: %v", err)
	}

	// 如果未找到 ICMP 相关配置，返回错误
	if icmpLineIndex == -1 {
		return false, fmt.Errorf("未找到 ICMP 相关配置")
	}
	return icmpAllowed, nil
}

// 检查并切换 ICMP 配置
func ToggleICMP() error {
	// 打开 UFW 配置文件
	filePath := "/etc/ufw/before.rules"
	file, err := os.OpenFile(filePath, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("打开配置文件失败: %v", err)
	}
	defer file.Close()

	// 读取文件内容
	var lines []string
	var icmpLineIndex int
	var icmpAllowed bool
	scanner := bufio.NewScanner(file)
	for i := 0; scanner.Scan(); i++ {
		line := scanner.Text()
		lines = append(lines, line)
		if strings.Contains(line, "-A ufw-before-input -p icmp --icmp-type echo-request") {
			icmpLineIndex = i
			icmpAllowed = strings.Contains(line, "ACCEPT")
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取配置文件失败: %v", err)
	}

	// 如果未找到 ICMP 相关配置，返回错误
	if icmpLineIndex == -1 {
		return fmt.Errorf("未找到 ICMP 相关配置")
	}

	// 切换 ICMP 配置
	if icmpAllowed {
		lines[icmpLineIndex] = strings.Replace(lines[icmpLineIndex], "ACCEPT", "DROP", 1)
	} else {
		lines[icmpLineIndex] = strings.Replace(lines[icmpLineIndex], "DROP", "ACCEPT", 1)
	}

	// 重新打开文件以写入修改
	file, err = os.OpenFile(filePath, os.O_RDWR|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("重新打开配置文件失败: %v", err)
	}
	defer file.Close()

	// 写入修改后的内容
	for _, line := range lines {
		_, err := file.WriteString(line + "\n")
		if err != nil {
			return fmt.Errorf("写入配置文件失败: %v", err)
		}
	}

	// 重新加载 UFW 配置
	err = reloadUFW()
	if err != nil {
		return fmt.Errorf("重新加载 UFW 配置失败: %v", err)
	}

	return nil
}

// 重新加载 UFW 配置
func reloadUFW() error {
	cmd := exec.Command("ufw", "reload")
	_, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("执行命令失败: %v", err)
	}
	return nil
}

// 辅助函数：过滤空字符串
func filterEmpty(s []string) []string {
	var filtered []string
	for _, str := range s {
		str = strings.TrimSpace(str)
		if str != "" {
			filtered = append(filtered, str)
		}
	}
	return filtered
}

// 辅助函数：检查字符串是否在切片中
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
