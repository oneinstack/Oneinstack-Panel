package software

import (
	"encoding/json"
	"errors"
	"log"
	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services"
	"oneinstack/pkg"
	"oneinstack/router/input"
	"oneinstack/router/output"
	"os/exec"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

var (
	installJobs = make(map[string]*InstallJob)
	jobMutex    sync.RWMutex
)

type InstallJob struct {
	SoftwareName string
	Version      string
	LogPath      string
	Status       string // pending | running | completed | failed
	CreatedAt    time.Time
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
	cmd := exec.Command("sh", "-c", "ps -ef | grep -w mysqld | grep -v grep >/dev/null")
	err := cmd.Run()
	if err != nil {
		return false
	}
	return true
}

func checkNginx(sf *models.Software) bool {
	cmd := exec.Command("sh", "-c", "ps -ef | grep -w nginx | grep -v grep >/dev/null")
	err := cmd.Run()
	if err != nil {
		return false
	}
	return true
}

func checkPhpMyAdmin(sf *models.Software) bool {
	cmd := exec.Command("sh", "-c", "ps -ef | grep -w phpmyadmin | grep -v grep >/dev/null")
	err := cmd.Run()
	if err != nil {
		return false
	}
	return true
}

func checkRedis(sf *models.Software) bool {
	cmd := exec.Command("sh", "-c", "ps -ef | grep -w redis-server | grep -v grep >/dev/null")
	err := cmd.Run()
	if err != nil {
		return false
	}
	return true
}

func List(param *input.SoftwareParam) (*services.PaginatedResult[output.Software], error) {
	tx := app.DB().Select(
		"MAX(id) as id," +
			"`key`," +
			"GROUP_CONCAT(DISTINCT version ORDER BY version DESC) as versions," +
			"MAX(name) as name," +
			"MAX(icon) as icon," +
			"MAX(type) as type," +
			"MAX(status) as status," +
			"MAX(resource) as resource," +
			"MAX(is_update) as is_update," +
			"MAX(installed) as installed," +
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
		tx = tx.Where("installed = ?", isi)
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
			Id:       item.Id,
			Name:     item.Name,
			Key:      item.Key,
			Icon:     item.Icon,
			Type:     item.Type,
			Status:   item.Status,
			Resource: item.Resource,
			Log:      item.Log,
			Versions: strings.Split(item.Versions, ","),
		})
		var params []*output.SoftParam
		if item.Key == "db" {
			continue
		}
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
		if err := syncSoftware(); err != nil {
			log.Printf("软件同步失败: %v", err)
		}
	}
}

func syncSoftware() error {

	tx := app.DB().Begin()
	defer tx.Rollback()
	result, err := pkg.SyncSoftware()
	if err != nil {
		return err
	}
	for _, remoteSoft := range result {
		var localSoft models.Softwaren
		if err := tx.Preload("Versions").Where("name = ?", remoteSoft.Name).First(&localSoft).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// 新软件
				if err := createSoftwareWithVersions(tx, remoteSoft); err != nil {
					return err
				}
			} else {
				return err
			}
			continue
		}

		// 检查版本更新
		hasUpdate := false
		for _, remoteVer := range remoteSoft.Versions {
			exists := false
			for _, localVer := range localSoft.Versions {
				if localVer.Version == remoteVer.Version {
					exists = true
					break
				}
			}

			if !exists {
				hasUpdate = true
				// 添加新版本
				newVer := remoteVer
				newVer.SoftwareID = localSoft.ID
				// 递归创建关联配置
				if err := createVersionWithConfigs(tx, &newVer); err != nil {
					return err
				}
			}
		}

		// 更新has_update状态
		if hasUpdate != localSoft.HasUpdate {
			if err := tx.Model(&localSoft).Update("has_update", hasUpdate).Error; err != nil {
				return err
			}
		}
	}

	return tx.Commit().Error
}

func createSoftwareWithVersions(tx *gorm.DB, soft *models.Softwaren) error {
	// 创建主记录
	if err := tx.Create(soft).Error; err != nil {
		return err
	}

	// 递归创建关联数据
	for i := range soft.Versions {
		version := &soft.Versions[i]
		version.SoftwareID = soft.ID

		// 创建版本
		if err := tx.Create(version).Error; err != nil {
			return err
		}

		// 处理InstallConfig
		installConfig := version.InstallConfig
		installConfig.VersionID = version.ID
		if err := tx.Create(&installConfig).Error; err != nil {
			return err
		}

		// 创建ConfigParams
		for j := range installConfig.ConfigParams {
			param := &installConfig.ConfigParams[j]
			param.InstallConfigID = installConfig.ID
			if err := tx.Create(param).Error; err != nil {
				return err
			}
		}

		// 创建ServiceConfig
		serviceConfig := installConfig.ServiceConfig
		serviceConfig.InstallConfigID = installConfig.ID
		if err := tx.Create(&serviceConfig).Error; err != nil {
			return err
		}

		// 创建ConfigTemplates
		for j := range installConfig.ConfigTemplates {
			template := &installConfig.ConfigTemplates[j]
			template.InstallConfigID = installConfig.ID
			if err := tx.Create(template).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func createVersionWithConfigs(tx *gorm.DB, version *models.Version) error {
	// 创建版本
	if err := tx.Create(version).Error; err != nil {
		return err
	}

	// 处理InstallConfig
	installConfig := version.InstallConfig
	installConfig.VersionID = version.ID
	if err := tx.Create(&installConfig).Error; err != nil {
		return err
	}

	// 创建ConfigParams
	for j := range installConfig.ConfigParams {
		param := &installConfig.ConfigParams[j]
		param.InstallConfigID = installConfig.ID
		if err := tx.Create(param).Error; err != nil {
			return err
		}
	}

	// 创建ServiceConfig
	serviceConfig := installConfig.ServiceConfig
	serviceConfig.InstallConfigID = installConfig.ID
	if err := tx.Create(&serviceConfig).Error; err != nil {
		return err
	}

	// 创建ConfigTemplates
	for j := range installConfig.ConfigTemplates {
		template := &installConfig.ConfigTemplates[j]
		template.InstallConfigID = installConfig.ID
		if err := tx.Create(template).Error; err != nil {
			return err
		}
	}

	return nil
}
