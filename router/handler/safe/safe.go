package safe

import (
	"net/http"
	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/internal/services/safe"
	"oneinstack/router/input"

	"github.com/gin-gonic/gin"
)

func GetFirewallInfo(c *gin.Context) {
	info, err := safe.GetUfwStatus()
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, gin.H{"info": info})
}

func GetFirewallRules(c *gin.Context) {
	var param input.IptablesRuleParam
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, http.StatusBadRequest, err, nil)
		return
	}
	rules, err := safe.GetUfwRules(&param)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, rules)
}

func AddFirewallRule(c *gin.Context) {
	var param models.IptablesRule
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, http.StatusBadRequest, err, nil)
		return
	}
	err := safe.AddUfwRule(&param)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, nil)
}

func UpdateFirewallRule(c *gin.Context) {
	var param models.IptablesRule
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, http.StatusBadRequest, err, nil)
		return
	}
	err := safe.UpdateUfwRule(&param)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, nil)
}

func DeleteFirewallRule(c *gin.Context) {
	var param models.IptablesRule
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, http.StatusBadRequest, err, nil)
		return
	}
	err := safe.DeleteUfwRule(param.ID)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, nil)
}

func StopFirewall(c *gin.Context) {
	err := safe.ToggleUfw()
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, true)
}

func BlockPing(c *gin.Context) {
	err := safe.ToggleICMP()
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, true)
}
