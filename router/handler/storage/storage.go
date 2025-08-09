package storage

import (
	"net/http"
	"oneinstack/core"
	"oneinstack/internal/services/storage"
	"oneinstack/router/input"

	"github.com/gin-gonic/gin"
)

func ADDStorage(c *gin.Context) {
	var req input.AddParam
	if err := c.ShouldBindJSON(&req); err != nil {
		core.HandleError(c, http.StatusUnauthorized, core.ErrBadRequest, err)
		return
	}
	err := req.Validate()
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
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
		core.HandleError(c, http.StatusUnauthorized, core.ErrBadRequest, err)
		return
	}
	err := storage.AddLibs(&req)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, "成功")
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
		core.HandleError(c, http.StatusUnauthorized, core.ErrBadRequest, err)
		return
	}
	err := req.Validate()
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
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
		core.HandleError(c, http.StatusUnauthorized, core.ErrBadRequest, err)
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

func SyncStorage(c *gin.Context) {
	var req input.IDParam
	if err := c.ShouldBindJSON(&req); err != nil {
		core.HandleError(c, http.StatusUnauthorized, core.ErrBadRequest, err)
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
		core.HandleError(c, http.StatusUnauthorized, core.ErrBadRequest, err)
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
		core.HandleError(c, http.StatusUnauthorized, core.ErrBadRequest, err)
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
