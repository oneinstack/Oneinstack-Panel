package system

import (
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/internal/services/system"
	"oneinstack/router/input"
	"oneinstack/utils"
)

func GetSystemInfo(c *gin.Context) {
	info, err := system.GetSystemInfo()
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, info)
}

func GetSystemMonitor(c *gin.Context) {
	monitor, err := system.GetSystemMonitor()
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, monitor)
}

func GetLibCount(c *gin.Context) {
	count, err := system.GetLibCount()
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, count)
}

func GetWebSiteCount(c *gin.Context) {
	count, err := system.GetWebSiteCount()
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, count)
}

func SystemInfo(c *gin.Context) {
	info, err := system.SystemInfo()
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, info)
}

func UpdateUser(c *gin.Context) {
	user := models.User{}
	if err := c.ShouldBindJSON(&user); err != nil {
		core.HandleError(c, http.StatusBadRequest, err, nil)
		return
	}
	id, exist := c.Get("id")
	if exist != true {
		core.HandleError(c, http.StatusInternalServerError, errors.New("not found"), nil)
		return
	}
	user.ID = id.(int64)
	err := system.UpdateUser(user)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, nil)
}

func ResetPassword(c *gin.Context) {
	user := input.ResetPasswordRequest{}
	id, exist := c.Get("id")
	if exist != true {
		core.HandleError(c, http.StatusInternalServerError, errors.New("not found"), nil)
		return
	}
	if err := c.ShouldBindJSON(&user); err != nil {
		core.HandleError(c, http.StatusBadRequest, err, nil)
		return
	}
	user.Id = id.(int64)
	user.NewPassword, _ = utils.HashPassword(user.Password)
	err := system.ResetPassword(user)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, nil)
}

func UpdatePort(c *gin.Context) {
	param := input.UpdatePortRequest{}
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, http.StatusBadRequest, err, nil)
		return
	}
	err := system.UpdateSystemPort(param.Port)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, nil)
}

func UpdateSystemTitle(c *gin.Context) {
	param := input.UpdateSystemTitleRequest{}
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, http.StatusBadRequest, err, nil)
		return
	}
	err := system.UpdateSystemTitle(param.Title)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, nil)
}

func GetInfo(c *gin.Context) {
	info, err := system.GetInfo()
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, info)
}
