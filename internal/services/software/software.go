package software

import (
	"encoding/json"
	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services"
	"oneinstack/internal/services/scriptregistry"
	"oneinstack/router/input"
	"oneinstack/router/output"
	"oneinstack/utils"
	"slices"
	"strings"
)

var softwareCategoryOrder = []string{
	"建站",
	"数据库",
	"Web服务器",
	"运行环境",
	"缓存",
	"实用工具",
	"容器",
	"安全",
	"云存储",
	"AI / 大模型",
}

type Category struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Count int    `json:"count"`
}

func ListCategories() ([]Category, error) {
	var rows []struct {
		Key  string
		Tags string
	}
	if err := app.DB().Model(&models.Software{}).
		Where("(catalog_visible = ? OR installed = ?)", true, true).
		Select("`key`, tags").
		Group("`key`, tags").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	counts := make(map[string]int)
	productKeys := make(map[string]struct{})
	productCategories := make(map[string]map[string]struct{})
	for _, row := range rows {
		productKeys[row.Key] = struct{}{}
		if productCategories[row.Key] == nil {
			productCategories[row.Key] = make(map[string]struct{})
		}
		for _, value := range strings.Split(row.Tags, ",") {
			name := strings.TrimSpace(value)
			if name == "" {
				continue
			}
			if _, exists := productCategories[row.Key][name]; exists {
				continue
			}
			productCategories[row.Key][name] = struct{}{}
			counts[name]++
		}
	}
	otherCount := 0
	for key := range productKeys {
		if len(productCategories[key]) == 0 {
			otherCount++
		}
	}

	categories := []Category{{Name: "全部", Value: "", Count: len(productKeys)}}
	for _, name := range softwareCategoryOrder {
		categories = append(categories, Category{Name: name, Value: name, Count: counts[name]})
		delete(counts, name)
	}
	remaining := make([]string, 0, len(counts))
	for name := range counts {
		remaining = append(remaining, name)
	}
	slices.Sort(remaining)
	for _, name := range remaining {
		categories = append(categories, Category{Name: name, Value: name, Count: counts[name]})
	}
	categories = append(categories, Category{Name: "其他", Value: "其他", Count: otherCount})
	return categories, nil
}

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
				"MAX(CASE WHEN installed = 1 THEN installed_package_version ELSE '' END) as installed_package_version," +
				"MAX(CASE WHEN catalog_visible = 1 AND recommended = 1 THEN latest_package_version ELSE '' END) as latest_package_version," +
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
		if param.Tags == "其他" {
			tx = tx.Having("MAX(CASE WHEN TRIM(COALESCE(tags, '')) <> '' THEN 1 ELSE 0 END) = 0")
		} else {
			tx = tx.Having(
				"MAX(CASE WHEN tags LIKE ? THEN 1 ELSE 0 END) = 1",
				"%"+param.Tags+"%",
			)
		}
	}

	paginated, err := services.Paginate[models.Softwares](tx, &models.Softwares{}, &input.Page{
		Page:     param.Page.Page,
		PageSize: param.Page.PageSize,
	})
	if err != nil {
		return nil, err
	}

	failedTaskByKey := make(map[string]models.SoftwareTask)
	latestTaskByKey := make(map[string]models.SoftwareTask)
	var failedTasks []models.SoftwareTask
	if taskErr := app.DB().
		Where("status IN ?", []string{
			models.SoftwareTaskStatusSucceeded,
			models.SoftwareTaskStatusFailed,
			models.SoftwareTaskStatusCanceled,
			models.SoftwareTaskStatusInterrupted,
		}).
		Order("created_at DESC").
		Find(&failedTasks).Error; taskErr == nil {
		for _, task := range failedTasks {
			if _, exists := latestTaskByKey[task.SoftwareKey]; exists {
				continue
			}
			if task.Status == models.SoftwareTaskStatusFailed || task.Status == models.SoftwareTaskStatusInterrupted {
				failedTaskByKey[task.SoftwareKey] = task
			}
			latestTaskByKey[task.SoftwareKey] = task
		}
	}

	// 转换版本格式
	var groupedResults []output.Software
	for i, item := range paginated.Data {
		groupedResults = append(groupedResults, output.Software{
			Id:                      item.Id,
			Describe:                item.Describe,
			Installed:               item.Installed,
			Name:                    item.Name,
			Key:                     item.Key,
			Component:               item.Component,
			Icon:                    item.Icon,
			Type:                    item.Type,
			Status:                  item.Status,
			Resource:                item.Resource,
			InstallVersion:          item.InstallVersion,
			InstalledPackageVersion: item.InstalledPackageVersion,
			LatestPackageVersion:    item.LatestPackageVersion,
			UpdateReason:            softwareUpdateReason(item),
			RecommendedVersion:      item.RecommendedVersion,
			Installable:             item.Installable,
			CatalogManaged:          item.CatalogManaged,
			IsUpdate:                item.IsUpdate,
			Log:                     item.Log,
			Tags:                    item.Tags,
			Versions:                strings.Split(item.Versions, ","),
		})
		var params []*output.SoftParam
		_ = json.Unmarshal([]byte(item.Params), &params)
		groupedResults[i].Params = params
		if failedTask, exists := failedTaskByKey[item.Key]; exists {
			groupedResults[i].FailureMessage = strings.TrimSpace(failedTask.ErrorMessage)
			if groupedResults[i].FailureMessage == "" {
				groupedResults[i].FailureMessage = strings.TrimSpace(failedTask.Message)
			}
		}
	}

	return &services.PaginatedResult[output.Software]{
		Data:     groupedResults,
		Total:    paginated.Total,
		Page:     paginated.Page,
		PageSize: paginated.PageSize,
	}, nil
}

func softwareUpdateReason(item models.Softwares) string {
	if !item.IsUpdate {
		return ""
	}
	softwareChanged := strings.TrimSpace(item.InstallVersion) != "" &&
		strings.TrimSpace(item.RecommendedVersion) != "" &&
		strings.TrimSpace(item.InstallVersion) != strings.TrimSpace(item.RecommendedVersion)
	packageChanged := strings.TrimSpace(item.InstalledPackageVersion) != "" &&
		strings.TrimSpace(item.LatestPackageVersion) != "" &&
		scriptregistry.ComparePackageVersions(
			item.LatestPackageVersion,
			item.InstalledPackageVersion,
		) > 0
	switch {
	case softwareChanged && packageChanged:
		return "both"
	case packageChanged:
		return "component_package"
	default:
		return "software_version"
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
			Where("`key` = ? AND installed = ?", softwareKey, true).
			Updates(map[string]interface{}{"status": models.Soft_Status_Err, "log": logFile})
		return false, err
	}
	tx := app.DB().Model(&models.Software{}).Where("`key` = ? AND installed = ?", softwareKey, true).Updates(map[string]interface{}{
		"status":                    models.Soft_Status_Default,
		"log":                       logFile,
		"installed":                 false,
		"install_version":           "",
		"installed_package_version": "",
		"is_update":                 false,
	})
	if tx.Error != nil {
		return false, tx.Error
	}
	return true, nil
}
