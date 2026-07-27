package software

import (
	"encoding/json"
	"errors"
	"fmt"
	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services"
	"oneinstack/router/input"
	"oneinstack/router/output"
	"oneinstack/utils"
	"strings"
	"time"

	"github.com/imroc/req/v3"
	"gorm.io/gorm"
)

func RunInstall(p *input.InstallParams) (string, error) {
	op, err := NewInstallOP(p)
	if err != nil {
		return "", err
	}
	return op.Install()
}

func Exploration(param *input.SoftwareParam) bool {
	sf := &models.Software{}
	tx := app.DB().Model(&models.Software{}).Where("id = ?", param.Id).First(sf)
	if tx.Error != nil {
		return false
	}
	if strings.Contains(strings.ToLower(sf.Name), "mysql") {
		return checkMySQL(sf)
	}
	if strings.Contains(strings.ToLower(sf.Name), "nginx") {
		return checkNginx(sf)
	}
	if strings.Contains(strings.ToLower(sf.Name), "phpmyadmin") {
		return checkPhpMyAdmin(sf)
	}
	if strings.Contains(strings.ToLower(sf.Name), "redis") {
		return checkRedis(sf)
	}
	return false
}

func checkMySQL(sf *models.Software) bool {
	output, err := utils.GetProcessList("mysqld")
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(output))) > 0
}

func checkNginx(sf *models.Software) bool {
	output, err := utils.GetProcessList("nginx")
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(output))) > 0
}

func checkPhpMyAdmin(sf *models.Software) bool {
	output, err := utils.GetProcessList("phpmyadmin")
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(output))) > 0
}

func checkRedis(sf *models.Software) bool {
	output, err := utils.GetProcessList("redis-server")
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(output))) > 0
}

func List(param *input.SoftwareParam) (*services.PaginatedResult[output.Software], error) {
	tx := app.DB().Select(
		"MAX(id) as id," +
			"`key`," +
			"MAX(describe) as describe," +
			"GROUP_CONCAT(DISTINCT version) as versions," +
			"MAX(name) as name," +
			"MAX(icon) as icon," +
			"MAX(type) as type," +
			"MAX(status) as status," +
			"MAX(resource) as resource," +
			"MAX(is_update) as is_update," +
			"MAX(CASE WHEN installed = 1 THEN install_version ELSE '' END) as install_version," +
			"MAX(CASE WHEN installed = 1 THEN 1 ELSE 0 END) as installed," +
			"MAX(params) as params," +
			"MAX(log) as log," +
			"MAX(tags) as tags").
		Group("`key`")
	if param.Id > 0 {
		tx = tx.Where("id = ?", param.Id)
	}

	if param.Name != "" {
		tx = tx.Where("name LIKE ?", "%"+param.Name+"%")
	}

	if param.Key != "" {
		tx = tx.Where("key LIKE ?", "%"+param.Key+"%")
	}

	if param.Type != "" {
		tx = tx.Where("type = ?", param.Type)
	}

	if param.Status != "" {
		tx = tx.Where("status = ?", param.Status)
	}

	if param.Resource != "" {
		tx = tx.Where("resource = ?", param.Resource)
	}

	if param.IsUpdate != nil {
		isi := 0
		if *param.IsUpdate {
			isi = 1
		}
		tx = tx.Where("is_update = ?", isi)
	}

	if param.Installed != nil {
		isi := 0
		if *param.Installed {
			isi = 1
		}
		tx = tx.Having(
			"MAX(CASE WHEN installed = 1 THEN 1 ELSE 0 END) = ?",
			isi,
		)
	}

	if param.Tags != "" {
		tx = tx.Where("tags LIKE ?", "%"+param.Tags+"%")
	}

	paginated, err := services.Paginate[models.Softwares](tx, &models.Softwares{}, &input.Page{
		Page:     param.Page.Page,
		PageSize: param.Page.PageSize,
	})

	// 转换版本格式
	var groupedResults []output.Software
	for i, item := range paginated.Data {
		groupedResults = append(groupedResults, output.Software{
			Id:             item.Id,
			Describe:       item.Describe,
			Installed:      item.Installed,
			Name:           item.Name,
			Key:            item.Key,
			Icon:           item.Icon,
			Type:           item.Type,
			Status:         item.Status,
			Resource:       item.Resource,
			InstallVersion: item.InstallVersion,
			Log:            item.Log,
			Versions:       strings.Split(item.Versions, ","),
		})
		var params []*output.SoftParam
		_ = json.Unmarshal([]byte(item.Params), &params)
		groupedResults[i].Params = params
	}

	return &services.PaginatedResult[output.Software]{
		Data:     groupedResults,
		Total:    paginated.Total,
		Page:     paginated.Page,
		PageSize: paginated.PageSize,
	}, err
}

func Sync() {
	ticker := time.NewTicker(5 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		type Data struct {
			Softwares []*models.Software `json:"soft"`
		}
		type Response struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    *Data  `json:"data"`
		}
		client := req.C()
		var result Response
		url := app.ONE_CONFIG.System.Remote + "?key=onesync"
		if app.ONE_CONFIG.System.Remote == "" {
			url = "http://localhost:8189/v1/sys/update"
		}
		resps, err := client.R().SetSuccessResult(&result).Post(url)

		if err != nil {
			fmt.Println("同步软件失败:", err.Error())
			continue
		}

		if !resps.IsSuccessState() {
			fmt.Println("同步软件失败")
			continue
		}
		if result.Data != nil && len(result.Data.Softwares) <= 0 {
			continue
		}
		for _, s := range result.Data.Softwares {
			sf := &models.Software{}
			tx := app.DB().Where("key =? and version = ?", s.Key, s.Version).First(sf)
			if tx.Error != nil && !errors.Is(tx.Error, gorm.ErrRecordNotFound) {
				fmt.Println("同步软件失败:", tx.Error.Error())
				continue
			}

			if sf.Id <= 0 {
				osf := &models.Software{}
				tx := app.DB().Where("key =? and installed = 1", s.Key).First(osf)
				if tx.Error != nil && !errors.Is(tx.Error, gorm.ErrRecordNotFound) {
					fmt.Println("同步软件失败状态更新:", tx.Error.Error())
				}
				if osf.Id > 0 {
					osf.IsUpdate = true
					app.DB().Updates(osf)
				}
				sf = &models.Software{
					Name:      s.Name,
					Key:       s.Key,
					Icon:      s.Icon,
					Type:      s.Type,
					Status:    s.Status,
					Resource:  "remote",
					Installed: s.Installed,
					Log:       s.Log,
					Version:   s.Version,
					Tags:      s.Tags,
					Params:    s.Params,
					Script:    s.Script,
				}
				app.DB().Create(sf)
			} else {
				sf.Script = s.Script
				sf.Resource = "remote"
				app.DB().Updates(sf)
			}
		}

	}
}

// remove software
func Remove(param *input.RemoveParams) (bool, error) {
	_, softwareKey, err := componentForRemove(param.Name)
	if err != nil {
		return false, err
	}
	installer := NewInstaller()
	logFile, err := installer.Uninstall(param, false)
	if err != nil {
		app.DB().Model(&models.Software{}).
			Where("`key` = ? AND version = ?", softwareKey, param.Version).
			Updates(map[string]interface{}{"status": models.Soft_Status_Err, "log": logFile})
		return false, err
	}
	tx := app.DB().Model(&models.Software{}).Where("`key` = ? AND version = ?", softwareKey, param.Version).Updates(map[string]interface{}{
		"status":          models.Soft_Status_Default,
		"log":             logFile,
		"installed":       false,
		"install_version": "",
	})
	if tx.Error != nil {
		return false, tx.Error
	}
	return true, nil
}
