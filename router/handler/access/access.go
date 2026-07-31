package access

import (
	"strconv"

	"oneinstack/app"
	"oneinstack/core"
	accessservice "oneinstack/internal/services/access"
	"oneinstack/router/input"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
)

func Me(c *gin.Context) {
	access, ok := middleware.UserAccess(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "登录状态无效"))
		return
	}
	core.HandleSuccess(c, buildMeResponse(access))
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
	core.HandleSuccess(c, roles)
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
	core.HandleSuccess(c, result)
}

func CreateUser(c *gin.Context) {
	var request input.AccessCreateUserRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
		return
	}
	user, err := accessservice.NewService(app.DB()).
		CreateUser(request.Username, request.Password, request.IsAdmin, request.RoleCodes)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "创建用户失败"))
		return
	}
	core.HandleSuccess(c, gin.H{"id": user.ID})
}

func AssignRoles(c *gin.Context) {
	var request input.AccessAssignRolesRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
		return
	}
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || userID <= 0 {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "用户标识无效"))
		return
	}
	if err := accessservice.NewService(app.DB()).AssignRoles(userID, request.RoleCodes); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "分配角色失败"))
		return
	}
	core.HandleSuccess(c, nil)
}

func ResetUserPassword(c *gin.Context) {
	var request input.AccessResetUserPasswordRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
		return
	}
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || userID <= 0 {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "用户标识无效"))
		return
	}
	if err := accessservice.NewService(app.DB()).ResetUserPassword(userID, request.Password); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "重置密码失败"))
		return
	}
	core.HandleSuccess(c, nil)
}

func buildMeResponse(access *accessservice.UserAccess) gin.H {
	roles := make([]gin.H, 0, len(access.Roles))
	for _, role := range access.Roles {
		roles = append(roles, gin.H{
			"code":        role.Code,
			"name":        role.Name,
			"description": role.Description,
		})
	}
	matrix := middleware.BuildAuthorizationMatrix(access)
	return gin.H{
		"id":           access.UserID,
		"username":     access.Username,
		"isAdmin":      access.IsSuperAdmin,
		"isSuperAdmin": access.IsSuperAdmin,
		"roles":        roles,
		"permissions":  access.Permissions,
		"scopes": gin.H{
			"runtimeLog": matrix.Menu["runtimeLog"],
			"audit":      matrix.Menu["audit"],
			"website":    matrix.Scopes["website"],
			"database":   matrix.Scopes["database"],
			"approval":   matrix.Scopes["approval"],
			"file":       matrix.Scopes["file"],
		},
	}
}

func positiveQueryInt(c *gin.Context, name string, fallback int) int {
	value, err := strconv.Atoi(c.DefaultQuery(name, strconv.Itoa(fallback)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
