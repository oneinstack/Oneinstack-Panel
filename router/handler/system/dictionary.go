package system

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/internal/services/system"
)

// 列出目录内容
func DictionaryList(c *gin.Context) {
	param := c.Query("q")
	list, err := system.DictionaryList(param)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, list)
}

// 创建文件或目录
func AddDictionary(c *gin.Context) {
	input := &models.Dictionary{}
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	err := system.AddDictionary(input)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, "创建成功")

}

func UpdateDictionary(c *gin.Context) {
	input := &models.Dictionary{}
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	err := system.UpdateDictionary(input)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, "更新成功")
}

func DeleteDictionary(c *gin.Context) {
	input := &models.Dictionary{}
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	err := system.DeleteDictionary(input.ID)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, "删除成功")
}
