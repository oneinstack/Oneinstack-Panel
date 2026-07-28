package storage

import (
	"oneinstack/core"
	"oneinstack/internal/services/storage"
	userservice "oneinstack/internal/services/user"
	"oneinstack/router/input"
	"oneinstack/router/middleware"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func ADDStorage(c *gin.Context) {
	var req input.AddParam
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "请求参数错误")
		core.HandleError(c, appErr)
		return
	}
	err := req.Validate()
	if err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "数据库连接参数无效")
		core.HandleError(c, appErr)
		return
	}
	err = storage.Add(&req)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, "成功")
}

func ADDLib(c *gin.Context) {
	var req input.LibParam
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "请求参数错误")
		core.HandleError(c, appErr)
		return
	}
	if err := req.Validate(); err != nil {
		message := "数据库参数无效"
		if req.ID <= 0 {
			message = "未检测到可用 MySQL 实例，请先安装或修复 MySQL"
		}
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, message))
		return
	}
	credential, err := storage.AddLibs(&req)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, credential)
}

func GetStorage(c *gin.Context) {
	t := c.Query("type")
	data, err := storage.List(t)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, data)
}

func UpdateStorage(c *gin.Context) {
	var req input.AddParam
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "请求参数错误")
		core.HandleError(c, appErr)
		return
	}
	err := req.Validate()
	if err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "数据库连接参数无效")
		core.HandleError(c, appErr)
		return
	}
	err = storage.Update(&req)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, "成功")
}

func DelStorage(c *gin.Context) {
	var req input.IDParam
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "请求参数错误")
		core.HandleError(c, appErr)
		return
	}
	err := storage.Del(&req)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, nil)
}

func TestStorageConnection(c *gin.Context) {
	var req input.AddParam
	if err := c.ShouldBindJSON(&req); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
		return
	}
	if err := req.Validate(); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "数据库连接参数无效"))
		return
	}
	if err := storage.TestConnection(&req); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "数据库连接测试失败"))
		return
	}
	core.HandleSuccess(c, "连接成功")
}

func DeleteLibrary(c *gin.Context) {
	var req input.DeleteLibraryParam
	if err := c.ShouldBindJSON(&req); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
		return
	}
	if err := storage.DeleteLibrary(&req); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "删除数据库失败"))
		return
	}
	core.HandleSuccess(c, nil)
}

func SyncStorage(c *gin.Context) {
	var req input.IDParam
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "请求参数错误")
		core.HandleError(c, appErr)
		return
	}
	err := storage.Sync(&req)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, nil)
}
func GetLib(c *gin.Context) {
	var req input.QueryParam
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "请求参数错误")
		core.HandleError(c, appErr)
		return
	}
	data, err := storage.LibList(&req)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, data)
}

func GetRedisKeys(c *gin.Context) {
	var req input.QueryParam
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "请求参数错误")
		core.HandleError(c, appErr)
		return
	}
	data, err := storage.RedisKeyList(&req)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, data)
}

func Info(c *gin.Context) {
	mysqlInstall, redisInstall := storage.CheckStorage()
	core.HandleSuccess(c, map[string]interface{}{"mysql": mysqlInstall, "redis": redisInstall})
}

func RevealLibraryCredential(c *gin.Context) {
	var req input.LibraryCredentialVerification
	if err := c.ShouldBindJSON(&req); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请输入当前面板密码"))
		return
	}
	if !verifyCurrentPassword(c, req.PanelPassword) {
		core.HandleError(c, core.NewError(core.ErrInvalidPassword, "当前面板密码错误"))
		return
	}
	id, err := parseLibraryID(c)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "数据库标识无效"))
		return
	}
	credential, err := storage.GetLibraryCredential(id)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "读取数据库账号失败"))
		return
	}
	core.HandleSuccess(c, credential)
}

func UpdateLibraryCredential(c *gin.Context) {
	var req input.UpdateLibraryCredentialParam
	if err := c.ShouldBindJSON(&req); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
		return
	}
	if !verifyCurrentPassword(c, req.PanelPassword) {
		core.HandleError(c, core.NewError(core.ErrInvalidPassword, "当前面板密码错误"))
		return
	}
	id, err := parseLibraryID(c)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "数据库标识无效"))
		return
	}
	credential, err := storage.UpdateLibraryCredential(id, req.Password)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "修改数据库账号密码失败"))
		return
	}
	core.HandleSuccess(c, credential)
}

func verifyCurrentPassword(c *gin.Context, password string) bool {
	usernameValue, exists := c.Get(middleware.ContextUsername)
	username, ok := usernameValue.(string)
	if !exists || !ok || username == "" || password == "" {
		return false
	}
	account, verified := userservice.CheckUserPassword(username, password)
	if !verified {
		return false
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	return ok && account.ID == userID
}

func parseLibraryID(c *gin.Context) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
}
