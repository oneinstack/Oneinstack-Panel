package system

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/internal/services/system"
	"strconv"
)

// 列出目录内容
func RemarkList(c *gin.Context) {
	param := c.Param("id")
	atoi, err := strconv.Atoi(param)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	r, err := system.GetRemarkByID(int64(atoi))
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, r)
}

// 创建文件或目录
func AddRemark(c *gin.Context) {
	input := &models.Remark{}
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	err := system.AddRemark(input)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, "创建成功")

}

func UpdateRemark(c *gin.Context) {
	input := &models.Remark{}
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	err := system.UpdateRemark(input)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, "更新成功")
}

func DeleteRemark(c *gin.Context) {
	input := &models.Remark{}
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	err := system.DeleteRemark(input.ID)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, "删除成功")
}
