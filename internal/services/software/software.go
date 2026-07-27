package software

import (
	"encoding/json"
	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services"
	"oneinstack/router/input"
	"oneinstack/router/output"
	"oneinstack/utils"
	"strings"
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
	tx := app.DB().
		Where("(catalog_visible = ? OR installed = ?)", true, true).
		Select(
			"MAX(id) as id," +
				"`key`," +
				"MAX(component) as component," +
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
				"MAX(CASE WHEN catalog_visible = 1 AND installable = 1 THEN 1 ELSE 0 END) as installable," +
				"MAX(CASE WHEN catalog_visible = 1 AND recommended = 1 THEN version ELSE '' END) as recommended_version," +
				"MAX(CASE WHEN catalog_managed = 1 THEN 1 ELSE 0 END) as catalog_managed," +
				"MAX(params) as params," +
				"MAX(log) as log," +
				"MAX(tags) as tags").
		Group("`key`").
		Order("MIN(catalog_order) ASC, `key` ASC")
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
			Id:                 item.Id,
			Describe:           item.Describe,
			Installed:          item.Installed,
			Name:               item.Name,
			Key:                item.Key,
			Component:          item.Component,
			Icon:               item.Icon,
			Type:               item.Type,
			Status:             item.Status,
			Resource:           item.Resource,
			InstallVersion:     item.InstallVersion,
			RecommendedVersion: item.RecommendedVersion,
			Installable:        item.Installable,
			CatalogManaged:     item.CatalogManaged,
			IsUpdate:           item.IsUpdate,
			Log:                item.Log,
			Tags:               item.Tags,
			Versions:           strings.Split(item.Versions, ","),
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
