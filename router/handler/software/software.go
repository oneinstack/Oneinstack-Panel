package software

import (
	"fmt"
	"net/http"
	"oneinstack/core"
	"oneinstack/internal/services/software"
	"oneinstack/router/input"
	"oneinstack/utils"

	"github.com/gin-gonic/gin"
)

func RunInstallation(c *gin.Context) {
	var req input.InstallParams
	if err := c.ShouldBindJSON(&req); err != nil {
		core.HandleError(c, http.StatusUnauthorized, core.ErrBadRequest, err)
		return
	}
	install, err := software.RunInstall(&req)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	fmt.Println(install)
	core.HandleSuccess(c, gin.H{
		"installName": install,
	})
}

func GetSoftware(c *gin.Context) {
	var req input.SoftwareParam
	if err := c.ShouldBindJSON(&req); err != nil {
		core.HandleError(c, http.StatusUnauthorized, core.ErrBadRequest, err)
		return
	}
	data, err := software.List(&req)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, data)
}

func GetLogContent(c *gin.Context) {
	param := c.Query("fn")
	install, err := utils.GetLogContent(param)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, gin.H{
		"logs": install,
	})
}

func Exploration(c *gin.Context) {
	var req input.SoftwareParam
	if err := c.ShouldBindJSON(&req); err != nil {
		core.HandleError(c, http.StatusUnauthorized, core.ErrBadRequest, err)
		return
	}
	ok := software.Exploration(&req)
	core.HandleSuccess(c, ok)
}
