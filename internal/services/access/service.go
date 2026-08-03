package access

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"oneinstack/internal/crypto"
	"oneinstack/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	PermissionDashboardRead  = "dashboard.read"
	PermissionRuntimeLogRead = "runtime_log.read"
	PermissionFileRead       = "file.read"
	PermissionFileWrite      = "file.write" // deprecated: use fine-grained file.* permissions
	PermissionFileCreate     = "file.create"
	PermissionFileEdit       = "file.edit"
	PermissionFileMove       = "file.move"
	PermissionFileDelete     = "file.delete"
	PermissionFileModify     = "file.modify"
	PermissionFileArchive    = "file.archive"
	PermissionFileShare      = "file.share"

	PermissionFileScopeRoot     = "file.scope.root"
	PermissionFileScopeWebsites = "file.scope.websites"
	PermissionFileScopeBackups  = "file.scope.backups"

	PermissionWebsiteRead      = "website.read"
	PermissionWebsiteWrite     = "website.write"
	PermissionWebsiteApproval  = "website.approval.request"
	PermissionDatabaseRead     = "database.read"
	PermissionDatabaseWrite    = "database.write"
	PermissionDatabaseApproval = "database.approval.request"
	PermissionSoftwareRead     = "software.read"
	PermissionSoftwareWrite    = "software.write"
	PermissionServiceRead      = "software.service.read"
	PermissionServiceWrite     = "software.service.write"
	PermissionSecurityRead     = "security.read"
	PermissionSecurityWrite    = "security.write"
	PermissionCronRead         = "cron.read"
	PermissionCronWrite        = "cron.write"
	PermissionMonitoringRead   = "monitoring.read"
	PermissionMonitoringWrite  = "monitoring.write"
	PermissionSystemRead       = "system.settings.read"
	PermissionSystemWrite      = "system.settings.write"
	PermissionTerminalAccess   = "terminal.access"
	PermissionAuditRead        = "audit.read"
	PermissionAuditExport      = "audit.export"
	PermissionAuditVerify      = "audit.verify"
	PermissionApprovalRead     = "approval.read"
	PermissionApprovalRequest  = "approval.request"
	PermissionApprovalReview   = "approval.review"
	PermissionApprovalExecute  = "approval.execute"
	PermissionTaskReadSelf     = "task.read.self"
	PermissionTaskReadAll      = "task.read.all"
	PermissionTaskCancelSelf   = "task.cancel.self"
	PermissionBastionRead      = "bastion.read"
	PermissionBastionWrite     = "bastion.write"
)

const (
	RoleObserver          = "observer"
	RoleWebsiteAdmin      = "website_admin"
	RoleDatabaseAdmin     = "database_admin"
	RoleSystemOperator    = "system_operator"
	RoleSecurityAuditor   = "security_auditor"
	RoleOperationApprover = "operation_approver"
)

type RoleSummary struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions,omitempty"`
}

type UserAccess struct {
	UserID        int64
	Username      string
	IsSuperAdmin  bool
	Roles         []RoleSummary
	Permissions   []string
	PermissionSet map[string]struct{}
	CanApprove    bool
}

type UserListItem struct {
	ID                 int64         `json:"id"`
	Username           string        `json:"username"`
	IsAdmin            bool          `json:"isAdmin"`
	IsSuperAdmin       bool          `json:"isSuperAdmin"`
	MustChangePassword bool          `json:"mustChangePassword"`
	CreatedAt          time.Time     `json:"createdAt"`
	Roles              []RoleSummary `json:"roles"`
}

type UserListResult struct {
	Items    []UserListItem `json:"items"`
	Total    int64          `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"pageSize"`
}

type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

func builtinPermissions() []models.Permission {
	return []models.Permission{
		{Code: PermissionDashboardRead, Name: "面板概览查看", Module: "dashboard"},
		{Code: PermissionRuntimeLogRead, Name: "运行日志查看", Module: "log"},
		{Code: PermissionFileRead, Name: "文件读取", Module: "file"},
		{Code: PermissionFileWrite, Name: "文件修改（已弃用）", Module: "file"},
		{Code: PermissionFileCreate, Name: "文件创建/上传", Module: "file"},
		{Code: PermissionFileEdit, Name: "文件编辑", Module: "file"},
		{Code: PermissionFileMove, Name: "文件移动/重命名", Module: "file"},
		{Code: PermissionFileDelete, Name: "文件删除", Module: "file"},
		{Code: PermissionFileModify, Name: "文件属性修改", Module: "file"},
		{Code: PermissionFileArchive, Name: "文件归档", Module: "file"},
		{Code: PermissionFileShare, Name: "文件分享", Module: "file"},
		{Code: PermissionFileScopeRoot, Name: "文件完整根目录", Module: "file"},
		{Code: PermissionFileScopeWebsites, Name: "网站目录访问", Module: "file"},
		{Code: PermissionFileScopeBackups, Name: "备份目录访问", Module: "file"},
		{Code: PermissionWebsiteRead, Name: "网站读取", Module: "website"},
		{Code: PermissionWebsiteWrite, Name: "网站修改", Module: "website"},
		{Code: PermissionWebsiteApproval, Name: "网站审批申请", Module: "website"},
		{Code: PermissionDatabaseRead, Name: "数据库读取", Module: "database"},
		{Code: PermissionDatabaseWrite, Name: "数据库修改", Module: "database"},
		{Code: PermissionDatabaseApproval, Name: "数据库审批申请", Module: "database"},
		{Code: PermissionSoftwareRead, Name: "软件读取", Module: "software"},
		{Code: PermissionSoftwareWrite, Name: "软件修改", Module: "software"},
		{Code: PermissionServiceRead, Name: "组件服务读取", Module: "software"},
		{Code: PermissionServiceWrite, Name: "组件服务修改", Module: "software"},
		{Code: PermissionSecurityRead, Name: "安全配置读取", Module: "security"},
		{Code: PermissionSecurityWrite, Name: "安全配置修改", Module: "security"},
		{Code: PermissionCronRead, Name: "计划任务读取", Module: "cron"},
		{Code: PermissionCronWrite, Name: "计划任务修改", Module: "cron"},
		{Code: PermissionMonitoringRead, Name: "监控读取", Module: "monitoring"},
		{Code: PermissionMonitoringWrite, Name: "监控修改", Module: "monitoring"},
		{Code: PermissionSystemRead, Name: "系统访问配置读取", Module: "system"},
		{Code: PermissionSystemWrite, Name: "系统访问配置修改", Module: "system"},
		{Code: PermissionTerminalAccess, Name: "Web 终端访问", Module: "terminal"},
		{Code: PermissionAuditRead, Name: "审计查看", Module: "audit"},
		{Code: PermissionAuditExport, Name: "审计导出", Module: "audit"},
		{Code: PermissionAuditVerify, Name: "审计校验", Module: "audit"},
		{Code: PermissionApprovalRead, Name: "审批查看", Module: "approval"},
		{Code: PermissionApprovalRequest, Name: "审批申请", Module: "approval"},
		{Code: PermissionApprovalReview, Name: "审批审核", Module: "approval"},
		{Code: PermissionApprovalExecute, Name: "审批执行", Module: "approval"},
		{Code: PermissionTaskReadSelf, Name: "本人任务查看", Module: "task"},
		{Code: PermissionTaskReadAll, Name: "全部任务查看", Module: "task"},
		{Code: PermissionTaskCancelSelf, Name: "本人任务取消", Module: "task"},
		{Code: PermissionBastionRead, Name: "堡垒机读取", Module: "bastion"},
		{Code: PermissionBastionWrite, Name: "堡垒机管理", Module: "bastion"},
	}
}

func builtinRoles() []struct {
	Role        models.Role
	Permissions []string
} {
	return []struct {
		Role        models.Role
		Permissions []string
	}{
		{
			Role: models.Role{
				Code:        RoleObserver,
				Name:        "只读观察员",
				Description: "可查看概览、运行日志和本人任务",
				Builtin:     true,
			},
			Permissions: []string{
				PermissionDashboardRead,
				PermissionRuntimeLogRead,
				PermissionTaskReadSelf,
				PermissionBastionRead,
			},
		},
		{
			Role: models.Role{
				Code:        RoleWebsiteAdmin,
				Name:        "网站管理员",
				Description: "负责网站资源与相关任务",
				Builtin:     true,
			},
			Permissions: []string{
				PermissionDashboardRead,
				PermissionRuntimeLogRead,
				PermissionFileRead,
				PermissionFileCreate,
				PermissionFileEdit,
				PermissionFileMove,
				PermissionFileDelete,
				PermissionFileArchive,
				PermissionFileShare,
				PermissionFileScopeWebsites,
				PermissionFileScopeBackups,
				PermissionSoftwareRead,
				PermissionServiceRead,
				PermissionServiceWrite,
				PermissionWebsiteRead,
				PermissionWebsiteWrite,
				PermissionWebsiteApproval,
				PermissionApprovalRequest,
				PermissionTaskReadSelf,
				PermissionTaskCancelSelf,
			},
		},
		{
			Role: models.Role{
				Code:        RoleDatabaseAdmin,
				Name:        "数据库管理员",
				Description: "负责数据库资源与相关任务",
				Builtin:     true,
			},
			Permissions: []string{
				PermissionDashboardRead,
				PermissionRuntimeLogRead,
				PermissionSoftwareRead,
				PermissionServiceRead,
				PermissionServiceWrite,
				PermissionDatabaseRead,
				PermissionDatabaseWrite,
				PermissionDatabaseApproval,
				PermissionApprovalRequest,
				PermissionTaskReadSelf,
				PermissionTaskCancelSelf,
			},
		},
		{
			Role: models.Role{
				Code:        RoleSystemOperator,
				Name:        "系统运维",
				Description: "负责文件、软件、监控、计划任务与系统访问配置",
				Builtin:     true,
			},
			Permissions: []string{
				PermissionDashboardRead,
				PermissionRuntimeLogRead,
				PermissionFileRead,
				PermissionFileCreate,
				PermissionFileEdit,
				PermissionFileMove,
				PermissionFileDelete,
				PermissionFileModify,
				PermissionFileArchive,
				PermissionFileShare,
				PermissionFileScopeRoot,
				PermissionSoftwareRead,
				PermissionSoftwareWrite,
				PermissionServiceRead,
				PermissionServiceWrite,
				PermissionSecurityRead,
				PermissionSecurityWrite,
				PermissionCronRead,
				PermissionCronWrite,
				PermissionMonitoringRead,
				PermissionMonitoringWrite,
				PermissionSystemRead,
				PermissionSystemWrite,
				PermissionTerminalAccess,
				PermissionTaskReadSelf,
				PermissionTaskReadAll,
				PermissionTaskCancelSelf,
				PermissionBastionRead,
				PermissionBastionWrite,
			},
		},
		{
			Role: models.Role{
				Code:        RoleSecurityAuditor,
				Name:        "安全审计员",
				Description: "可查看登录审计、运行日志和审批记录",
				Builtin:     true,
			},
			Permissions: []string{
				PermissionDashboardRead,
				PermissionRuntimeLogRead,
				PermissionSecurityRead,
				PermissionMonitoringRead,
				PermissionAuditRead,
				PermissionAuditExport,
				PermissionAuditVerify,
				PermissionApprovalRead,
				PermissionBastionRead,
			},
		},
		{
			Role: models.Role{
				Code:        RoleOperationApprover,
				Name:        "操作审批员",
				Description: "负责审核并执行高风险操作",
				Builtin:     true,
			},
			Permissions: []string{
				PermissionApprovalRead,
				PermissionApprovalReview,
				PermissionApprovalExecute,
				PermissionTaskReadAll,
			},
		},
	}
}

func SeedBuiltin(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	service := NewService(db)
	return service.seedBuiltin()
}

func (service *Service) seedBuiltin() error {
	if service.db == nil {
		return errors.New("database is not initialized")
	}
	return service.db.Transaction(func(tx *gorm.DB) error {
		permissionIDByCode := make(map[string]uint64)
		for _, permission := range builtinPermissions() {
			record := permission
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "code"}},
				DoUpdates: clause.AssignmentColumns([]string{"name", "description", "module", "updated_at"}),
			}).Create(&record).Error; err != nil {
				return err
			}
			var stored models.Permission
			if err := tx.Where("code = ?", permission.Code).First(&stored).Error; err != nil {
				return err
			}
			permissionIDByCode[permission.Code] = stored.ID
		}
		for _, item := range builtinRoles() {
			role := item.Role
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "code"}},
				DoUpdates: clause.AssignmentColumns([]string{"name", "description", "builtin", "updated_at"}),
			}).Create(&role).Error; err != nil {
				return err
			}
			var storedRole models.Role
			if err := tx.Where("code = ?", item.Role.Code).First(&storedRole).Error; err != nil {
				return err
			}
			if err := tx.Where("role_id = ?", storedRole.ID).Delete(&models.RolePermission{}).Error; err != nil {
				return err
			}
			for _, code := range item.Permissions {
				link := models.RolePermission{
					RoleID:       storedRole.ID,
					PermissionID: permissionIDByCode[code],
					CreatedAt:    time.Now().UTC(),
				}
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&link).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (service *Service) LoadUserAccess(userID int64) (*UserAccess, error) {
	if service.db == nil {
		return nil, errors.New("database is not initialized")
	}
	var user models.User
	if err := service.db.Select("id", "username", "is_admin").First(&user, userID).Error; err != nil {
		return nil, err
	}
	access := &UserAccess{
		UserID:        user.ID,
		Username:      user.Username,
		IsSuperAdmin:  user.IsAdmin,
		PermissionSet: make(map[string]struct{}),
	}
	if user.IsAdmin {
		for _, permission := range builtinPermissions() {
			access.PermissionSet[permission.Code] = struct{}{}
		}
		access.Permissions = mapKeys(access.PermissionSet)
		access.CanApprove = access.HasPermission(PermissionApprovalReview) && access.HasPermission(PermissionApprovalExecute)
		return access, nil
	}
	type row struct {
		RoleCode        string
		RoleName        string
		RoleDescription string
		PermissionCode  string
	}
	var rows []row
	if err := service.db.Table("user_roles AS ur").
		Select("r.code AS role_code", "r.name AS role_name", "r.description AS role_description", "p.code AS permission_code").
		Joins("JOIN roles r ON r.id = ur.role_id").
		Joins("LEFT JOIN role_permissions rp ON rp.role_id = r.id").
		Joins("LEFT JOIN permissions p ON p.id = rp.permission_id").
		Where("ur.user_id = ?", userID).
		Order("r.code ASC, p.code ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	roleMap := make(map[string]*RoleSummary)
	for _, item := range rows {
		if item.RoleCode == "" {
			continue
		}
		summary, ok := roleMap[item.RoleCode]
		if !ok {
			summary = &RoleSummary{
				Code:        item.RoleCode,
				Name:        item.RoleName,
				Description: item.RoleDescription,
			}
			roleMap[item.RoleCode] = summary
		}
		if item.PermissionCode != "" {
			access.PermissionSet[item.PermissionCode] = struct{}{}
		}
	}
	roleCodes := make([]string, 0, len(roleMap))
	for code := range roleMap {
		roleCodes = append(roleCodes, code)
	}
	sort.Strings(roleCodes)
	for _, code := range roleCodes {
		access.Roles = append(access.Roles, *roleMap[code])
	}
	access.Permissions = mapKeys(access.PermissionSet)
	access.CanApprove = access.HasPermission(PermissionApprovalReview) && access.HasPermission(PermissionApprovalExecute)
	return access, nil
}

var legacyAliases = map[string][]string{
	PermissionFileWrite: {
		PermissionFileCreate,
		PermissionFileEdit,
		PermissionFileMove,
		PermissionFileDelete,
		PermissionFileModify,
		PermissionFileArchive,
		PermissionFileShare,
	},
}

func (access *UserAccess) HasPermission(code string) bool {
	if access == nil {
		return false
	}
	if access.IsSuperAdmin {
		return true
	}
	if _, ok := access.PermissionSet[code]; ok {
		return true
	}
	if aliases, ok := legacyAliases[code]; ok {
		for _, alias := range aliases {
			if _, exists := access.PermissionSet[alias]; exists {
				return true
			}
		}
	}
	return false
}

func (service *Service) ListRoles() ([]RoleSummary, error) {
	if service.db == nil {
		return nil, errors.New("database is not initialized")
	}
	var roles []models.Role
	if err := service.db.Order("id ASC").Find(&roles).Error; err != nil {
		return nil, err
	}
	type row struct {
		RoleCode       string
		PermissionCode string
	}
	var rows []row
	if err := service.db.Table("roles AS r").
		Select("r.code AS role_code", "p.code AS permission_code").
		Joins("LEFT JOIN role_permissions rp ON rp.role_id = r.id").
		Joins("LEFT JOIN permissions p ON p.id = rp.permission_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	permissionsByRole := make(map[string][]string)
	for _, item := range rows {
		if item.RoleCode == "" || item.PermissionCode == "" {
			continue
		}
		permissionsByRole[item.RoleCode] = append(permissionsByRole[item.RoleCode], item.PermissionCode)
	}
	result := make([]RoleSummary, 0, len(roles))
	for _, role := range roles {
		perms := permissionsByRole[role.Code]
		sort.Strings(perms)
		result = append(result, RoleSummary{
			Code:        role.Code,
			Name:        role.Name,
			Description: role.Description,
			Permissions: perms,
		})
	}
	return result, nil
}

func (service *Service) ListUsers(page, pageSize int, keyword string) (*UserListResult, error) {
	if service.db == nil {
		return nil, errors.New("database is not initialized")
	}
	page, pageSize = normalizePage(page, pageSize)
	query := service.db.Model(&models.User{})
	if value := strings.TrimSpace(keyword); value != "" {
		query = query.Where("username LIKE ?", "%"+value+"%")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var users []models.User
	if err := query.Order("id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&users).Error; err != nil {
		return nil, err
	}
	items := make([]UserListItem, 0, len(users))
	for _, user := range users {
		access, err := service.LoadUserAccess(user.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, UserListItem{
			ID:                 user.ID,
			Username:           user.Username,
			IsAdmin:            user.IsAdmin,
			IsSuperAdmin:       user.IsAdmin,
			MustChangePassword: user.MustChangePassword,
			CreatedAt:          user.CreateTime,
			Roles:              access.Roles,
		})
	}
	return &UserListResult{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (service *Service) CreateUser(username, password string, isAdmin bool, roleCodes []string) (*models.User, error) {
	if service.db == nil {
		return nil, errors.New("database is not initialized")
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("username is required")
	}
	hashed, err := crypto.HashPassword(password)
	if err != nil {
		return nil, err
	}
	user := &models.User{
		Username:           username,
		Password:           hashed,
		IsAdmin:            isAdmin,
		FirstJoin:          false,
		MustChangePassword: true,
		SecurityVersion:    1,
	}
	err = service.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(user).Error; err != nil {
			return err
		}
		if isAdmin {
			return nil
		}
		return assignRoles(tx, user.ID, roleCodes)
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (service *Service) AssignRoles(userID int64, roleCodes []string) error {
	if service.db == nil {
		return errors.New("database is not initialized")
	}
	return service.db.Transaction(func(tx *gorm.DB) error {
		var user models.User
		if err := tx.First(&user, userID).Error; err != nil {
			return err
		}
		if user.IsAdmin {
			return nil
		}
		return assignRoles(tx, userID, roleCodes)
	})
}

func assignRoles(tx *gorm.DB, userID int64, roleCodes []string) error {
	normalized := make([]string, 0, len(roleCodes))
	seen := make(map[string]struct{})
	for _, code := range roleCodes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		normalized = append(normalized, code)
	}
	if err := tx.Where("user_id = ?", userID).Delete(&models.UserRole{}).Error; err != nil {
		return err
	}
	if len(normalized) == 0 {
		return nil
	}
	var roles []models.Role
	if err := tx.Where("code IN ?", normalized).Find(&roles).Error; err != nil {
		return err
	}
	if len(roles) != len(normalized) {
		return fmt.Errorf("one or more roles do not exist")
	}
	for _, role := range roles {
		link := models.UserRole{
			UserID:     userID,
			RoleID:     role.ID,
			AssignedAt: time.Now().UTC(),
		}
		if err := tx.Create(&link).Error; err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) ResetUserPassword(userID int64, password string) error {
	if service.db == nil {
		return errors.New("database is not initialized")
	}
	hashed, err := crypto.HashPassword(password)
	if err != nil {
		return err
	}
	return service.db.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]any{
		"password":             hashed,
		"must_change_password": true,
	}).Error
}

func normalizePage(page, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func mapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
