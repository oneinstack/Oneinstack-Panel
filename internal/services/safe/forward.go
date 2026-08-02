package safe

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode/utf8"

	"oneinstack/internal/models"
	"oneinstack/internal/services"
	"oneinstack/router/input"
)

func (s *Service) ListPortForwards(param *input.FirewallPortForwardParam) (*services.PaginatedResult[models.FirewallPortForward], error) {
	tx := s.db.Model(&models.FirewallPortForward{})
	if query := strings.TrimSpace(param.Q); query != "" {
		like := "%" + query + "%"
		tx = tx.Where("remark LIKE ? OR destination_ip LIKE ?", like, like)
	}
	if param.State != nil {
		tx = tx.Where("state = ?", *param.State)
	}
	tx = tx.Order("id DESC")
	return services.Paginate[models.FirewallPortForward](tx, &models.FirewallPortForward{}, &input.Page{
		Page: param.Page.Page, PageSize: param.Page.PageSize,
	})
}

func (s *Service) AddPortForward(ctx context.Context, forward *models.FirewallPortForward) error {
	operationMu.Lock()
	defer operationMu.Unlock()
	if err := s.normalizePortForward(forward); err != nil {
		return err
	}
	state := s.detectBackend(ctx)
	if state.Name != BackendFirewalld || !state.Enabled {
		return fmt.Errorf("%w: 端口转发需要已启用的 firewalld", ErrUnsupported)
	}
	forward.ID = 0
	forward.Backend = BackendFirewalld
	var operation commandOperation
	if forward.State == 1 {
		operation = firewalldForwardOperation(forward)
		if err := s.runOperations(ctx, BackendFirewalld, []commandOperation{operation}); err != nil {
			return fmt.Errorf("应用端口转发失败: %w", err)
		}
	}
	if err := s.db.Create(forward).Error; err != nil {
		if forward.State == 1 {
			s.rollbackOperations(ctx, []commandOperation{operation})
			_ = s.persist(ctx, BackendFirewalld)
		}
		return fmt.Errorf("保存端口转发失败，系统规则已回滚: %w", err)
	}
	return nil
}

func (s *Service) UpdatePortForward(ctx context.Context, requested *models.FirewallPortForward) error {
	operationMu.Lock()
	defer operationMu.Unlock()
	if requested.ID < 1 {
		return validationError("端口转发 ID 无效")
	}
	var old models.FirewallPortForward
	if err := s.db.First(&old, requested.ID).Error; err != nil {
		return err
	}
	if err := s.normalizePortForward(requested); err != nil {
		return err
	}
	state := s.detectBackend(ctx)
	if state.Name != BackendFirewalld || !state.Enabled {
		return fmt.Errorf("%w: 端口转发需要已启用的 firewalld", ErrUnsupported)
	}
	requested.Backend = BackendFirewalld
	oldOperation := firewalldForwardOperation(&old)
	newOperation := firewalldForwardOperation(requested)
	if old.State == 1 {
		if err := s.runOperations(ctx, BackendFirewalld, reverseOperations([]commandOperation{oldOperation})); err != nil {
			return fmt.Errorf("删除旧端口转发失败: %w", err)
		}
	}
	if requested.State == 1 {
		if err := s.runOperations(ctx, BackendFirewalld, []commandOperation{newOperation}); err != nil {
			if old.State == 1 {
				_ = s.runOperations(ctx, BackendFirewalld, []commandOperation{oldOperation})
			}
			return fmt.Errorf("应用新端口转发失败，原规则已恢复: %w", err)
		}
	}
	updates := map[string]any{
		"protocol": requested.Protocol, "source_port": requested.SourcePort,
		"destination_ip": requested.DestinationIP, "destination_port": requested.DestinationPort,
		"state": requested.State, "remark": requested.Remark, "backend": requested.Backend,
	}
	if err := s.db.Model(&models.FirewallPortForward{}).Where("id = ?", old.ID).Updates(updates).Error; err != nil {
		if requested.State == 1 {
			_ = s.runOperations(ctx, BackendFirewalld, reverseOperations([]commandOperation{newOperation}))
		}
		if old.State == 1 {
			_ = s.runOperations(ctx, BackendFirewalld, []commandOperation{oldOperation})
		}
		return fmt.Errorf("保存端口转发失败，原规则已恢复: %w", err)
	}
	return nil
}

func (s *Service) DeletePortForward(ctx context.Context, id int64) error {
	operationMu.Lock()
	defer operationMu.Unlock()
	if id < 1 {
		return validationError("端口转发 ID 无效")
	}
	var forward models.FirewallPortForward
	if err := s.db.First(&forward, id).Error; err != nil {
		return err
	}
	operation := firewalldForwardOperation(&forward)
	if forward.State == 1 {
		if err := s.runOperations(ctx, BackendFirewalld, reverseOperations([]commandOperation{operation})); err != nil {
			return fmt.Errorf("删除系统端口转发失败: %w", err)
		}
	}
	if err := s.db.Delete(&models.FirewallPortForward{}, id).Error; err != nil {
		if forward.State == 1 {
			_ = s.runOperations(ctx, BackendFirewalld, []commandOperation{operation})
		}
		return fmt.Errorf("删除端口转发记录失败，系统规则已恢复: %w", err)
	}
	return nil
}

func (s *Service) SetPortForwardState(ctx context.Context, id int64, enabled bool) error {
	var forward models.FirewallPortForward
	if err := s.db.First(&forward, id).Error; err != nil {
		return err
	}
	forward.State = 0
	if enabled {
		forward.State = 1
	}
	return s.UpdatePortForward(ctx, &forward)
}

func (s *Service) normalizePortForward(forward *models.FirewallPortForward) error {
	if forward == nil {
		return validationError("端口转发不能为空")
	}
	forward.Protocol = strings.ToLower(strings.TrimSpace(forward.Protocol))
	if forward.Protocol != "tcp" && forward.Protocol != "udp" {
		return validationError("转发协议必须是 tcp 或 udp")
	}
	if forward.SourcePort < 1 || forward.SourcePort > 65535 ||
		forward.DestinationPort < 1 || forward.DestinationPort > 65535 {
		return validationError("转发端口必须在 1-65535 之间")
	}
	if forward.SourcePort == s.panelPort {
		return validationError("不能转发当前面板管理端口")
	}
	forward.DestinationIP = strings.TrimSpace(forward.DestinationIP)
	ip := net.ParseIP(forward.DestinationIP)
	if ip == nil || ip.To4() == nil || ip.IsUnspecified() {
		return validationError("目标地址必须是有效的 IPv4 地址")
	}
	forward.DestinationIP = ip.To4().String()
	forward.Remark = strings.TrimSpace(forward.Remark)
	if utf8.RuneCountInString(forward.Remark) > 200 {
		return validationError("备注不能超过 200 个字符")
	}
	if forward.State != 0 {
		forward.State = 1
	}
	return nil
}

func firewalldForwardOperation(forward *models.FirewallPortForward) commandOperation {
	spec := "port=" + strconv.Itoa(forward.SourcePort) +
		":proto=" + forward.Protocol +
		":toport=" + strconv.Itoa(forward.DestinationPort) +
		":toaddr=" + forward.DestinationIP
	return commandOperation{
		name: "firewall-cmd", args: []string{"--permanent", "--add-forward-port=" + spec},
		undoName: "firewall-cmd", undoArgs: []string{"--permanent", "--remove-forward-port=" + spec},
	}
}
