package website

import (
	"context"
	"fmt"
	"os"
	osuser "os/user"
	"strconv"
	"strings"

	"oneinstack/internal/services/software"
)

type websiteRootOwner struct {
	user   string
	uid    int
	gid    int
	groups []uint32
}

// Use the installed component's configuration probe, including its actual
// runtime identity, rather than Panel's identity or an install-time default.
func (service *Service) websiteRootOwner(ctx context.Context, siteType string) (*websiteRootOwner, error) {
	if siteType == "proxy" || service.ConfigManager == nil || strings.TrimSpace(service.ConfigManager.Server.Component) == "" {
		// Publisher-only services do not manage a host runtime identity.
		return nil, nil
	}
	server := service.ConfigManager.Server
	configuration, err := software.NewInstaller().InspectServiceConfiguration(ctx, server.Component, server.Version)
	if err != nil {
		return nil, fmt.Errorf("读取 Web 运行账号失败，请检查组件运行配置: %w", err)
	}
	username, groupname := configuration.Values["runUser"], configuration.Values["runGroup"]
	if configuration.Runtime != nil {
		if value := configuration.Runtime.RunUser; strings.TrimSpace(value) != "" {
			username = value
		}
		if value := configuration.Runtime.RunGroup; strings.TrimSpace(value) != "" {
			groupname = value
		}
	}
	if strings.TrimSpace(username) == "" || strings.TrimSpace(groupname) == "" {
		return nil, fmt.Errorf("%s 未返回 Web 运行账号或用户组，请检查组件运行配置", server.Name)
	}
	account, err := osuser.Lookup(strings.TrimSpace(username))
	if err != nil {
		return nil, fmt.Errorf("查找 Web 运行账号 %s 失败: %w", username, err)
	}
	group, err := osuser.LookupGroup(strings.TrimSpace(groupname))
	if err != nil {
		return nil, fmt.Errorf("查找 Web 运行用户组 %s 失败: %w", groupname, err)
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("Web 运行账号 UID 无效: %w", err)
	}
	gid, err := strconv.ParseUint(group.Gid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("Web 运行用户组 GID 无效: %w", err)
	}
	groupIDs, err := account.GroupIds()
	if err != nil {
		return nil, fmt.Errorf("读取 Web 运行账号附加组失败: %w", err)
	}
	owner := &websiteRootOwner{user: account.Username, uid: int(uid), gid: int(gid)}
	for _, value := range groupIDs {
		id, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("Web 运行账号附加组 GID 无效: %w", err)
		}
		owner.groups = append(owner.groups, uint32(id))
	}
	return owner, nil
}

func (owner *websiteRootOwner) prepareFile(file *os.File) error {
	if owner == nil {
		return nil
	}
	if err := file.Chown(owner.uid, owner.gid); err != nil {
		return fmt.Errorf("设置网站默认首页属主失败: %w", err)
	}
	// Explicitly apply the final mode so Panel's UMask cannot make the page
	// unreadable to the selected runtime. Only this newly created file changes.
	if err := file.Chmod(0640); err != nil {
		return fmt.Errorf("设置网站默认首页权限失败: %w", err)
	}
	return nil
}
