package safe

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/internal/services/safe"
	"oneinstack/router/input"
	"oneinstack/utils"
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
	firewall := utils.CheckFirewall()
	//判断防火墙类型
	if firewall == "firewalld" {
		err := safe.AddFirewalldRule(&param)
		if err != nil {
			core.HandleError(c, http.StatusInternalServerError, err, nil)
			return
		}
		core.HandleSuccess(c, nil)
		return
	} else if firewall == "iptables" {
		err := safe.AddIptablesRule(&param)
		if err != nil {
			core.HandleError(c, http.StatusInternalServerError, err, nil)
			return
		}
		core.HandleSuccess(c, nil)
		return
	} else {
		err := safe.AddUfwRule(&param)
		if err != nil {
			core.HandleError(c, http.StatusInternalServerError, err, nil)
			return
		}
		core.HandleSuccess(c, nil)
	}
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

func InstallFirewall(context *gin.Context) {
	err := safe.InstallUfw()
	if err != nil {
		core.HandleError(context, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(context, true)
}
