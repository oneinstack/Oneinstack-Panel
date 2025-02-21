package cron

import (
	"net/http"
	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/internal/services/cron"
	"oneinstack/router/input"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

var cronService = cron.NewCronService()

func GetCronList(c *gin.Context) {
	var param input.CronParam
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, http.StatusBadRequest, err, nil)
		return
	}
	p, err := cron.GetCronList(c, &param)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, p)
}

func GetCronLogList(c *gin.Context) {
	var param input.CronParam
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, http.StatusBadRequest, err, nil)
		return
	}
	p, err := cron.GetCronLogList(c, &param)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, p)
}

func AddCron(c *gin.Context) {
	var param input.AddCronParam
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, http.StatusBadRequest, err, nil)
		return
	}

	job := &models.CronJob{
		Command:     param.Command,
		Schedule:    strings.Join(param.Schedule, ","),
		Description: param.Description,
		Name:        param.Name,
		Enabled:     true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := cronService.AddJob(job); err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, job)
}

func UpdateCron(c *gin.Context) {
	var param input.AddCronParam
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, http.StatusBadRequest, err, nil)
		return
	}
	updateData := &models.CronJob{
		Command:     param.Command,
		Schedule:    strings.Join(param.Schedule, ","),
		Description: param.Description,
		Name:        param.Name,
		Enabled:     true,
		UpdatedAt:   time.Now(),
	}

	if err := cronService.UpdateJob(uint(param.ID), updateData); err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, nil)
}

func DeleteCron(c *gin.Context) {
	var param input.CronIDs
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, http.StatusBadRequest, err, nil)
		return
	}
	for _, id := range param.IDs {
		cronService.DeleteJob(uint(id))
	}
	core.HandleSuccess(c, nil)
}

func DisableCron(c *gin.Context) {
	var param input.CronIDs
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, http.StatusBadRequest, err, nil)
		return
	}
	crons, err := cron.GetCronByIDs(c, param.IDs)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	for _, c := range crons {
		cronService.UpdateJob(c.ID, c)
	}
	core.HandleSuccess(c, nil)
}

func EnableCron(c *gin.Context) {
	var param input.CronIDs
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, http.StatusBadRequest, err, nil)
		return
	}
	crons, err := cron.GetCronByIDs(c, param.IDs)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	for _, c := range crons {
		cronService.UpdateJob(c.ID, c)
	}
	core.HandleSuccess(c, nil)
}
