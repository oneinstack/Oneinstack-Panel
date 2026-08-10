package access

var operationPermissions = map[string]string{
	"website.create":              PermissionWebsiteWrite,
	"website.update":              PermissionWebsiteWrite,
	"website.toggle":              PermissionWebsiteWrite,
	"software.install":            PermissionSoftwareWrite,
	"software.uninstall":          PermissionSoftwareWrite,
	"software.service_action":     PermissionServiceWrite,
	"software.configure":          PermissionSoftwareWrite,
	"firewall.rule_change":        PermissionSecurityWrite,
	"firewall.port_forward":       PermissionSecurityWrite,
	"firewall.toggle":             PermissionSecurityWrite,
	"firewall.ping":               PermissionSecurityWrite,
	"panel.network":               PermissionSystemWrite,
	"container.create":            PermissionContainerWrite,
	"container.start":             PermissionContainerWrite,
	"container.stop":              PermissionContainerWrite,
	"container.restart":           PermissionContainerWrite,
	"container.pause":             PermissionContainerWrite,
	"container.resume":            PermissionContainerWrite,
	"container.delete":            PermissionContainerDelete,
	"container.force_stop":        PermissionContainerForceAction,
	"container.force_delete":      PermissionContainerForceAction,
	"container.terminal":          PermissionContainerTerminal,
	"container.image.pull":        PermissionContainerImageWrite,
	"container.image.import":      PermissionContainerImageWrite,
	"container.image.build":       PermissionContainerImageWrite,
	"container.image.tag":         PermissionContainerImageWrite,
	"container.image.push":        PermissionContainerImageWrite,
	"container.image.delete":      PermissionContainerDelete,
	"container.image.cleanup":     PermissionContainerDangerousCleanup,
	"container.image.build-cache": PermissionContainerDangerousCleanup,
	"container.network.change":    PermissionContainerNetworkWrite,
	"container.volume.change":     PermissionContainerVolumeWrite,
	"container.compose.change":    PermissionContainerComposeWrite,
	"container.registry.change":   PermissionContainerRegistryWrite,
	"container.config.change":     PermissionContainerConfigWrite,
	"container.runtime.install":   PermissionContainerRuntimeInstall,
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
