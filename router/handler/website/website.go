package website

import (
	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/internal/services/website"
	"oneinstack/router/input"

	"github.com/gin-gonic/gin"
)

func List(c *gin.Context) {
	input := &input.WebsiteQueryParam{}
	if err := c.ShouldBindJSON(&input); err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	list, err := website.List(input)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, list)
}

func Add(c *gin.Context) {
	input := &models.Website{}
	if err := c.ShouldBindJSON(&input); err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	err := website.Add(input)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, "创建成功")

}

func Update(c *gin.Context) {
	input := &models.Website{}
	if err := c.ShouldBindJSON(&input); err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	err := website.Update(input)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, "更新成功")
}

func Delete(c *gin.Context) {
	input := &models.Website{}
	if err := c.ShouldBindJSON(&input); err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	err := website.Delete(input.ID)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, "删除成功")
}

func Info(context *gin.Context) {
	check := website.Check()
	core.HandleSuccess(context, check)
}
