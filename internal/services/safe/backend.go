package safe

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"oneinstack/internal/models"
)

func (s *Service) detectBackend(ctx context.Context) backendState {
	states := make([]backendState, 0, 3)
	if _, err := s.runner.LookPath("ufw"); err == nil {
		output, statusErr := s.runner.Run(ctx, "ufw", "status")
		states = append(states, backendState{
			Name: BackendUFW, Installed: true,
			Enabled:    statusErr == nil && strings.Contains(string(output), "Status: active"),
			Persistent: true,
			CanToggle:  true,
		})
	}
	if _, err := s.runner.LookPath("firewall-cmd"); err == nil {
		_, statusErr := s.runner.Run(ctx, "firewall-cmd", "--state")
		state := backendState{
			Name: BackendFirewalld, Installed: true,
			Enabled: statusErr == nil, Persistent: true,
		}
		if !state.Enabled {
			if _, err := s.runner.LookPath("firewall-offline-cmd"); err == nil {
				if _, err := s.runner.Run(ctx, "firewall-offline-cmd", "--check-config"); err != nil {
					state.RepairRequired = true
					state.Warning = appendWarning(
						state.Warning,
						"firewalld 配置校验失败，请先运行修复任务",
					)
				}
			}
		}
		if _, err := s.runner.LookPath("systemctl"); err == nil {
			if _, err := s.runner.Run(ctx, "systemctl", "show-environment"); err == nil {
				state.CanToggle = true
			}
		}
		if !state.CanToggle {
			state.Warning = appendWarning(
				state.Warning,
				"当前环境没有可用的 systemd，不能启停主机 firewalld；Docker 测试容器请使用 Linux 主机模式验证",
			)
		}
		states = append(states, state)
	}
	if _, err := s.runner.LookPath("iptables"); err == nil {
		_, persistentErr := s.runner.LookPath("netfilter-persistent")
		state := backendState{
			Name: BackendIPTables, Installed: true, Enabled: true,
			Persistent: persistentErr == nil, CanToggle: false,
		}
		if !state.Persistent {
			state.Warning = "iptables 缺少 netfilter-persistent，已禁止修改以避免重启后规则丢失"
		} else {
			state.Warning = "iptables 支持持久化规则管理，但不支持从面板整体启停"
		}
		states = append(states, state)
	}
	for _, state := range states {
		if state.Enabled && (state.Name == BackendUFW || state.Name == BackendFirewalld) {
			return state
		}
	}
	// firewalld is the Panel-managed default. When multiple supported
	// backends are installed but inactive (Ubuntu commonly ships an inactive
	// UFW binary), prefer firewalld so a freshly installed firewalld can be
	// protected and enabled through the Panel.
	for _, name := range []string{BackendFirewalld, BackendUFW, BackendIPTables} {
		for _, state := range states {
			if state.Name == name {
				return state
			}
		}
	}
	return backendState{Name: BackendNone, Warning: "未检测到受支持的防火墙"}
}

func (s *Service) ruleOperations(rule *models.IptablesRule) ([]commandOperation, error) {
	switch rule.Backend {
	case BackendUFW:
		return ufwRuleOperations(rule), nil
	case BackendFirewalld:
		return firewalldRuleOperations(rule, "firewall-cmd", true), nil
	case BackendIPTables:
		if _, err := s.runner.LookPath("netfilter-persistent"); err != nil {
			return nil, fmt.Errorf("%w: iptables 需要安装 netfilter-persistent 后才能由面板管理", ErrUnsupported)
		}
		return iptablesRuleOperations(rule), nil
	default:
		return nil, fmt.Errorf("%w: 未知防火墙后端 %q", ErrUnsupported, rule.Backend)
	}
}

func ufwRuleOperations(rule *models.IptablesRule) []commandOperation {
	ips, ports := expandedValues(rule)
	result := make([]commandOperation, 0, len(ips)*max(1, len(ports)))
	for _, ip := range ips {
		for _, port := range ports {
			args := []string{rule.Strategy, rule.Direction}
			if rule.Direction == "in" {
				args = append(args, "from", ufwAddress(ip), "to", "any")
			} else {
				args = append(args, "to", ufwAddress(ip))
			}
			if rule.Protocol != "icmp" && rule.Protocol != "all" && port != "" {
				args = append(args, "port", strings.ReplaceAll(port, "-", ":"))
			}
			if rule.Protocol != "all" {
				args = append(args, "proto", rule.Protocol)
			}
			addArgs := append([]string{}, args...)
			if rule.Token != "" {
				addArgs = append(addArgs, "comment", "oneinstack:"+rule.Token)
			}
			deleteArgs := append([]string{"--force", "delete"}, args...)
			result = append(result, commandOperation{
				name: "ufw", args: addArgs,
				undoName: "ufw", undoArgs: deleteArgs,
			})
		}
	}
	return result
}

func firewalldRuleOperations(rule *models.IptablesRule, command string, permanent bool) []commandOperation {
	if rule.Direction == "out" {
		return firewalldDirectOperations(rule, command, permanent)
	}
	ips, ports := expandedValues(rule)
	result := make([]commandOperation, 0, len(ips)*max(1, len(ports)))
	for _, ip := range ips {
		for _, port := range ports {
			richRule := firewalldRichRule(rule, ip, port)
			add := "--add-rich-rule=" + richRule
			remove := "--remove-rich-rule=" + richRule
			addArgs := []string{add}
			removeArgs := []string{remove}
			if permanent {
				addArgs = append([]string{"--permanent"}, addArgs...)
				removeArgs = append([]string{"--permanent"}, removeArgs...)
			}
			result = append(result, commandOperation{
				name: command, args: addArgs,
				undoName: command, undoArgs: removeArgs,
			})
		}
	}
	return result
}

func firewalldRichRule(rule *models.IptablesRule, ip, port string) string {
	parts := []string{`rule family="ipv4"`}
	if ip != "0.0.0.0/0" {
		kind := "source"
		if rule.Direction == "out" {
			kind = "destination"
		}
		parts = append(parts, fmt.Sprintf(`%s address="%s"`, kind, ip))
	}
	if rule.Protocol == "all" {
		// A rich rule without a protocol or port clause matches every protocol.
	} else if rule.Protocol == "icmp" {
		parts = append(parts, `protocol value="icmp"`)
	} else if port == "" {
		parts = append(parts, fmt.Sprintf(`protocol value="%s"`, rule.Protocol))
	} else {
		parts = append(parts, fmt.Sprintf(`port port="%s" protocol="%s"`, port, rule.Protocol))
	}
	if rule.Strategy == "allow" {
		parts = append(parts, "accept")
	} else {
		parts = append(parts, "drop")
	}
	return strings.Join(parts, " ")
}

func firewalldDirectOperations(rule *models.IptablesRule, command string, permanent bool) []commandOperation {
	ips, ports := expandedValues(rule)
	result := make([]commandOperation, 0, len(ips)*max(1, len(ports)))
	for _, ip := range ips {
		for _, port := range ports {
			base := []string{"--direct", "--add-rule", "ipv4", "filter", "OUTPUT", "0"}
			if rule.Protocol != "all" {
				base = append(base, "-p", rule.Protocol)
			}
			base = append(base, "-d", ip)
			if rule.Protocol != "icmp" && rule.Protocol != "all" && port != "" {
				base = append(base, "--dport", strings.ReplaceAll(port, "-", ":"))
			}
			if rule.Token != "" {
				base = append(base, "-m", "comment", "--comment", "oneinstack:"+rule.Token)
			}
			target := "ACCEPT"
			if rule.Strategy == "deny" {
				target = "DROP"
			}
			base = append(base, "-j", target)
			remove := append([]string{}, base...)
			remove[1] = "--remove-rule"
			if permanent {
				base = append([]string{"--permanent"}, base...)
				remove = append([]string{"--permanent"}, remove...)
			}
			result = append(result, commandOperation{
				name: command, args: base,
				undoName: command, undoArgs: remove,
			})
		}
	}
	return result
}

func iptablesRuleOperations(rule *models.IptablesRule) []commandOperation {
	ips, ports := expandedValues(rule)
	result := make([]commandOperation, 0, len(ips)*max(1, len(ports)))
	for _, ip := range ips {
		for _, port := range ports {
			chain := "INPUT"
			addressFlag := "-s"
			if rule.Direction == "out" {
				chain = "OUTPUT"
				addressFlag = "-d"
			}
			args := []string{"-A", chain}
			if rule.Protocol != "all" {
				args = append(args, "-p", rule.Protocol)
			}
			args = append(args, addressFlag, ip)
			if rule.Protocol != "icmp" && rule.Protocol != "all" && port != "" {
				args = append(args, "--dport", strings.ReplaceAll(port, "-", ":"))
			}
			if rule.Token != "" {
				args = append(args, "-m", "comment", "--comment", "oneinstack:"+rule.Token)
			}
			target := "ACCEPT"
			if rule.Strategy == "deny" {
				target = "DROP"
			}
			args = append(args, "-j", target)
			deleteArgs := append([]string{}, args...)
			deleteArgs[0] = "-D"
			result = append(result, commandOperation{
				name: "iptables", args: args,
				undoName: "iptables", undoArgs: deleteArgs,
			})
		}
	}
	return result
}

func expandedValues(rule *models.IptablesRule) ([]string, []string) {
	ips := splitValues(rule.IPs)
	if len(ips) == 0 {
		ips = []string{"0.0.0.0/0"}
	}
	ports := splitValues(rule.Ports)
	if len(ports) == 0 {
		ports = []string{""}
	}
	return ips, ports
}

func ufwAddress(value string) string {
	if value == "0.0.0.0/0" {
		return "any"
	}
	return value
}

func reverseOperations(operations []commandOperation) []commandOperation {
	result := make([]commandOperation, 0, len(operations))
	for index := len(operations) - 1; index >= 0; index-- {
		operation := operations[index]
		result = append(result, commandOperation{
			name: operation.undoName, args: append([]string{}, operation.undoArgs...),
			undoName: operation.name, undoArgs: append([]string{}, operation.args...),
		})
	}
	return result
}

func (s *Service) runOperations(ctx context.Context, backend string, operations []commandOperation) error {
	applied := make([]commandOperation, 0, len(operations))
	for _, operation := range operations {
		if _, err := s.runner.Run(ctx, operation.name, operation.args...); err != nil {
			s.rollbackOperations(ctx, applied)
			return err
		}
		applied = append(applied, operation)
	}
	if err := s.persist(ctx, backend); err != nil {
		s.rollbackOperations(ctx, applied)
		_ = s.persist(ctx, backend)
		return err
	}
	return nil
}

func (s *Service) rollbackOperations(ctx context.Context, applied []commandOperation) {
	for index := len(applied) - 1; index >= 0; index-- {
		operation := applied[index]
		_, _ = s.runner.Run(ctx, operation.undoName, operation.undoArgs...)
	}
}

func (s *Service) persist(ctx context.Context, backend string) error {
	switch backend {
	case BackendFirewalld:
		_, err := s.runner.Run(ctx, "firewall-cmd", "--reload")
		return err
	case BackendIPTables:
		_, err := s.runner.Run(ctx, "netfilter-persistent", "save")
		return err
	default:
		return nil
	}
}

func parsePanelPort(raw string) int {
	port, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || port < 1 || port > 65535 {
		return 8089
	}
	return port
}
