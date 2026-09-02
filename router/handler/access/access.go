package access

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/i18n"
	accessservice "oneinstack/internal/services/access"
	"oneinstack/router/input"
	"oneinstack/router/middleware"
	"oneinstack/utils"

	"github.com/gin-gonic/gin"
)

func Me(c *gin.Context) {
	access, ok := middleware.UserAccess(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "登录状态无效"))
		return
	}
	core.HandleSuccess(c, buildMeResponse(c.GetString("locale"), access))
}

func Matrix(c *gin.Context) {
	access, ok := middleware.UserAccess(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "登录状态无效"))
		return
	}
	core.HandleSuccess(c, middleware.BuildAuthorizationMatrix(access))
}

func ListRoles(c *gin.Context) {
	roles, err := accessservice.NewService(app.DB()).ListRoles()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取角色清单失败"))
		return
	}
	localizeRoles(c.GetString("locale"), roles)
	core.HandleSuccess(c, roles)
}

func GetRole(c *gin.Context) {
	role, err := accessservice.NewService(app.DB()).GetRole(c.Param("key"))
	if err != nil {
		handleRBACError(c, err, "读取角色详情")
		return
	}
	localizeRole(c.GetString("locale"), &role.RoleSummary)
	core.HandleSuccess(c, role)
}

func CreateRole(c *gin.Context) {
	var request input.AccessRoleRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		handleRBACError(c, err, "创建角色")
		return
	}
	role, err := accessservice.NewService(app.DB()).CreateRole(roleInput(request))
	if err != nil {
		handleRBACError(c, err, "创建角色")
		return
	}
	core.HandleSuccess(c, role)
}

func UpdateRole(c *gin.Context) {
	var request input.AccessRoleRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		handleRBACError(c, err, "更新角色")
		return
	}
	role, err := accessservice.NewService(app.DB()).UpdateRole(c.Param("key"), roleInput(request))
	if err != nil {
		handleRBACError(c, err, "更新角色")
		return
	}
	core.HandleSuccess(c, role)
}

func DeleteRole(c *gin.Context) {
	if err := accessservice.NewService(app.DB()).DeleteRole(c.Param("key")); err != nil {
		handleRBACError(c, err, "删除角色")
		return
	}
	core.HandleSuccess(c, nil)
}

func ListPermissions(c *gin.Context) {
	permissions, err := accessservice.NewService(app.DB()).ListPermissions()
	if err != nil {
		handleRBACError(c, err, "读取权限清单")
		return
	}
	core.HandleSuccess(c, permissions)
}

func ListMenus(c *gin.Context) {
	menus, err := accessservice.NewService(app.DB()).ListMenus()
	if err != nil {
		handleRBACError(c, err, "读取菜单清单")
		return
	}
	core.HandleSuccess(c, menus)
}

func CreateMenu(c *gin.Context) {
	var request input.AccessMenuRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		handleRBACError(c, err, "创建菜单")
		return
	}
	menu, err := accessservice.NewService(app.DB()).CreateMenu(menuInput(request))
	if err != nil {
		handleRBACError(c, err, "创建菜单")
		return
	}
	core.HandleSuccess(c, menu)
}

func UpdateMenu(c *gin.Context) {
	var request input.AccessMenuRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		handleRBACError(c, err, "更新菜单")
		return
	}
	menu, err := accessservice.NewService(app.DB()).UpdateMenu(c.Param("key"), menuInput(request))
	if err != nil {
		handleRBACError(c, err, "更新菜单")
		return
	}
	core.HandleSuccess(c, menu)
}

func DeleteMenu(c *gin.Context) {
	if err := accessservice.NewService(app.DB()).DeleteMenu(c.Param("key")); err != nil {
		handleRBACError(c, err, "删除菜单")
		return
	}
	core.HandleSuccess(c, nil)
}

func ListUsers(c *gin.Context) {
	result, err := accessservice.NewService(app.DB()).ListUsers(
		positiveQueryInt(c, "page", 1),
		positiveQueryInt(c, "pageSize", 20),
		c.Query("keyword"),
	)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取用户列表失败"))
		return
	}
	for index := range result.Items {
		localizeRoles(c.GetString("locale"), result.Items[index].Roles)
	}
	core.HandleSuccess(c, result)
}

func CreateUser(c *gin.Context) {
	var request input.AccessCreateUserRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "用户创建参数格式不正确"))
		return
	}
	request.Username = strings.TrimSpace(request.Username)
	if err := utils.ValidateUsername(request.Username); err != nil {
		core.HandleError(c, err)
		return
	}
	user, err := accessservice.NewService(app.DB()).
		CreateUser(request.Username, request.Password, request.IsAdmin, request.RoleCodes)
	if err != nil {
		if errors.Is(err, accessservice.ErrRoleNotFound) {
			handleRBACError(c, err, "创建用户")
			return
		}
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "创建用户失败"))
		return
	}
	core.HandleSuccess(c, gin.H{"id": user.ID})
}

func DeleteUser(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || userID <= 0 {
		core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "id 必须是正整数", "id"))
		return
	}
	currentUserID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "登录状态无效"))
		return
	}
	confirmed := strings.EqualFold(strings.TrimSpace(c.Query("confirm")), "true")
	if err := accessservice.NewService(app.DB()).DeleteUser(userID, currentUserID, confirmed); err != nil {
		switch {
		case errors.Is(err, accessservice.ErrDeleteUserNotConfirmed):
			core.HandleError(c, core.NewError(core.ErrOperationNotConfirmed, "删除用户需要显式确认"))
		case errors.Is(err, accessservice.ErrUserNotFound):
			core.HandleError(c, core.NewError(core.ErrUserNotFound, "用户不存在或已被删除"))
		case errors.Is(err, accessservice.ErrDeleteCurrentUser):
			core.HandleError(c, core.NewError(core.ErrForbidden, "不能删除当前登录账号"))
		case errors.Is(err, accessservice.ErrDeleteLastSuperAdmin):
			core.HandleError(c, core.NewError(core.ErrConflict, "不能删除最后一个超级管理员"))
		default:
			core.HandleError(c, core.WrapError(err, core.ErrInternalError, "删除用户失败"))
		}
		return
	}
	core.HandleSuccess(c, nil)
}

func AssignRoles(c *gin.Context) {
	var request input.AccessAssignRolesRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "用户角色参数格式不正确"))
		return
	}
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || userID <= 0 {
		core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "id 必须是正整数", "id"))
		return
	}
	currentUserID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "登录状态无效"))
		return
	}
	if err := accessservice.NewService(app.DB()).AssignRoles(userID, currentUserID, request.RoleCodes); err != nil {
		handleRBACError(c, err, "分配角色")
		return
	}
	core.HandleSuccess(c, nil)
}

func ResetUserPassword(c *gin.Context) {
	var request input.AccessResetUserPasswordRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "用户密码重置参数格式不正确"))
		return
	}
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || userID <= 0 {
		core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "id 必须是正整数", "id"))
		return
	}
	if err := accessservice.NewService(app.DB()).ResetUserPassword(userID, request.Password); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "重置密码失败"))
		return
	}
	response := core.SuccessResponseForContext(c, nil)
	response.Message = i18n.Message(
		c.GetString("locale"),
		i18n.MessagePasswordResetRequiresChange,
		"密码已重置，用户下次登录需修改密码",
	)
	c.JSON(http.StatusOK, response)
}

func buildMeResponse(locale string, access *accessservice.UserAccess) gin.H {
	roles := make([]gin.H, 0, len(access.Roles))
	for _, role := range access.Roles {
		roles = append(roles, gin.H{
			"code":        role.Code,
			"key":         role.Key,
			"name":        i18n.LocalizeBusinessText(locale, role.Name),
			"description": i18n.LocalizeBusinessText(locale, role.Description),
			"builtin":     role.Builtin,
		})
	}
	matrix := middleware.BuildAuthorizationMatrix(access)
	return gin.H{
		"id":                  access.UserID,
		"username":            access.Username,
		"isAdmin":             access.IsSuperAdmin,
		"isSuperAdmin":        access.IsSuperAdmin,
		"firstAccessibleMenu": matrix.FirstAccessibleMenu,
		"menuTree":            matrix.MenuTree,
		"roles":               roles,
		"permissions":         access.Permissions,
		"scopes": gin.H{
			"runtimeLog": matrix.Menu["runtimeLog"],
			"audit":      matrix.Menu["audit"],
			"website":    matrix.Scopes["website"],
			"database":   matrix.Scopes["database"],
			"approval":   matrix.Scopes["approval"],
			"file":       matrix.Scopes["file"],
			"bastion":    matrix.Menu["bastion"],
			"terminal":   matrix.Menu["terminal"],
		},
	}
}

func roleInput(request input.AccessRoleRequest) accessservice.RoleInput {
	permissionCodes := request.PermissionCodes
	if permissionCodes == nil {
		permissionCodes = request.Permissions
	}
	return accessservice.RoleInput{
		Key:             request.Key,
		Code:            request.Code,
		Name:            request.Name,
		Description:     request.Description,
		PermissionCodes: permissionCodes,
	}
}

func menuInput(request input.AccessMenuRequest) accessservice.MenuInput {
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	permissionCodes := request.PermissionCodes
	if permissionCodes == nil {
		permissionCodes = request.Permissions
	}
	return accessservice.MenuInput{
		Key:             request.Key,
		ParentKey:       request.ParentKey,
		Type:            request.Type,
		Name:            request.Name,
		NameEn:          request.NameEn,
		TargetType:      request.TargetType,
		TargetKey:       request.TargetKey,
		IconKey:         request.IconKey,
		Sort:            request.Sort,
		Enabled:         enabled,
		SuperAdminOnly:  request.SuperAdminOnly,
		FeatureKey:      request.FeatureKey,
		PermissionCodes: permissionCodes,
	}
}

func handleRBACError(c *gin.Context, err error, operation string) {
	switch {
	case errors.Is(err, accessservice.ErrRoleNotFound), errors.Is(err, accessservice.ErrMenuNotFound),
		errors.Is(err, accessservice.ErrUserNotFound):
		core.HandleError(c, core.NewError(core.ErrNotFound, "角色、菜单或用户不存在"))
	case errors.Is(err, accessservice.ErrRoleKeyInvalid), errors.Is(err, accessservice.ErrRoleNameRequired),
		errors.Is(err, accessservice.ErrRoleDescriptionInvalid),
		errors.Is(err, accessservice.ErrPermissionNotFound), errors.Is(err, accessservice.ErrMenuKeyInvalid),
		errors.Is(err, accessservice.ErrMenuTypeInvalid), errors.Is(err, accessservice.ErrMenuParentInvalid),
		errors.Is(err, accessservice.ErrMenuCycle), errors.Is(err, accessservice.ErrMenuTargetInvalid),
		errors.Is(err, accessservice.ErrMenuPermissionRequired), errors.Is(err, accessservice.ErrMenuFeatureInvalid),
		errors.Is(err, accessservice.ErrMenuIconInvalid), errors.Is(err, accessservice.ErrMenuNameInvalid):
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "角色或菜单参数不合法"))
	case errors.Is(err, accessservice.ErrRoleExists), errors.Is(err, accessservice.ErrMenuExists),
		errors.Is(err, accessservice.ErrRoleInUse), errors.Is(err, accessservice.ErrMenuHasChildren),
		errors.Is(err, accessservice.ErrDeleteLastSuperAdmin):
		core.HandleError(c, core.NewError(core.ErrConflict, "角色或菜单当前状态不允许此操作"))
	case errors.Is(err, accessservice.ErrRoleBuiltin), errors.Is(err, accessservice.ErrMenuBuiltin),
		errors.Is(err, accessservice.ErrCannotDemoteCurrentSuperAdmin):
		core.HandleError(c, core.NewError(core.ErrForbidden, "内置资源或超级管理员身份不可修改"))
	default:
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, operation+"失败"))
	}
}

func localizeRoles(locale string, roles []accessservice.RoleSummary) {
	for index := range roles {
		localizeRole(locale, &roles[index])
	}
}

func localizeRole(locale string, role *accessservice.RoleSummary) {
	if role == nil {
		return
	}
	role.Name = i18n.LocalizeBusinessText(locale, role.Name)
	role.Description = i18n.LocalizeBusinessText(locale, role.Description)
}

func positiveQueryInt(c *gin.Context, name string, fallback int) int {
	value, err := strconv.Atoi(c.DefaultQuery(name, strconv.Itoa(fallback)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
