package system

import (
	"github.com/gin-gonic/gin"

	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/internal/services/system"
	"strconv"
)

// 列出目录内容
func RemarkList(c *gin.Context) {
	// 如果有参数id，则获取指定id的目录
	if c.Param("id") != "" {
		param := c.Param("id")
		atoi, err := strconv.Atoi(param)
		if err != nil {
			appErr := core.WrapError(err, core.ErrBadRequest, "备忘录 ID 必须是有效的正整数")
			core.HandleError(c, appErr)
			return
		}
		r, err := system.GetRemarkByID(int64(atoi))
		if err != nil {
			appErr := core.WrapError(err, core.ErrNotFound, "备忘录不存在")
			core.HandleError(c, appErr)
			return
		}
		core.HandleSuccess(c, r)
	} else {
		// 如果没有参数id，则获取所有目录
		r, err := system.GetRemarkList()
		if err != nil {
			appErr := core.WrapError(err, core.ErrInternalError, "读取备忘录列表失败")
			core.HandleError(c, appErr)
			return
		}
		core.HandleSuccess(c, r)
	}
}

// 创建文件或目录
func AddRemark(c *gin.Context) {
	input := &models.Remark{}
	if err := c.ShouldBindJSON(&input); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "备忘录参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	err := system.AddRemark(input)
	if err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "创建备忘录失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, "创建成功")

}

func UpdateRemark(c *gin.Context) {
	input := &models.Remark{}
	if err := c.ShouldBindJSON(&input); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "备忘录参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	err := system.UpdateRemark(input)
	if err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "更新备忘录失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, "更新成功")
}

func DeleteRemark(c *gin.Context) {
	input := &models.Remark{}
	if err := c.ShouldBindJSON(&input); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "备忘录参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	err := system.DeleteRemark(input.ID)
	if err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "删除备忘录失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, "删除成功")
}
