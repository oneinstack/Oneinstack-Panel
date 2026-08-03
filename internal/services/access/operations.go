package access

var operationPermissions = map[string]string{
	"website.create":          PermissionWebsiteWrite,
	"website.update":          PermissionWebsiteWrite,
	"website.toggle":          PermissionWebsiteWrite,
	"software.install":        PermissionSoftwareWrite,
	"software.uninstall":      PermissionSoftwareWrite,
	"software.service_action": PermissionServiceWrite,
	"software.configure":      PermissionSoftwareWrite,
	"firewall.rule_change":    PermissionSecurityWrite,
	"firewall.port_forward":   PermissionSecurityWrite,
	"firewall.toggle":         PermissionSecurityWrite,
}

func OperationPermission(operation string) (string, bool) {
	permission, ok := operationPermissions[operation]
	return permission, ok
}

func OperationPermissions() map[string]string {
	permissions := make(map[string]string, len(operationPermissions))
	for operation, permission := range operationPermissions {
		permissions[operation] = permission
	}
	return permissions
}
