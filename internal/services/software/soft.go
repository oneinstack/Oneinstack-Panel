package software

import (
	"encoding/json"
	"fmt"
	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services"
	"oneinstack/router/input"
	"os"

	"gorm.io/gorm"
)

func GetSoftwareList(param *input.SoftwareParam) (*services.PaginatedResult[models.Softwaren], error) {
	tx := app.DB().Preload("Versions").
		Preload("Versions.InstallConfig.ServiceConfig").
		Preload("Versions.InstallConfig.ConfigTemplates").
		Preload("Versions.InstallConfig.ConfigParams").
		Preload("Versions.InstallConfig.PreCmd").
		Preload("Versions.InstallConfig.DownloadURL")

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

	paginated, err := services.Paginate[models.Softwaren](tx, &models.Softwaren{}, &input.Page{
		Page:     param.Page.Page,
		PageSize: param.Page.PageSize,
	})

	return paginated, err
}

func InstallSoftwaren(param *input.InstallSoftwareParam) error {
	softs := &models.Softwaren{}
	tx := app.DB().Preload("Versions", func(db *gorm.DB) *gorm.DB {
		return db.Where("id = ?", param.VersionId).
			Preload("InstallConfig.ServiceConfig").
			Preload("InstallConfig.ConfigTemplates").
			Preload("InstallConfig.ConfigParams").
			Preload("InstallConfig.PreCmd").
			Preload("InstallConfig.DownloadURL")
	}).
		Where("softwarens.id = ?", param.Id).
		Joins("INNER JOIN versions ON versions.software_id = softwarens.id AND versions.id = ?", param.VersionId).
		First(softs)

	if tx.Error != nil {
		return tx.Error
	}
	mapParams := make(map[string]map[string]string)
	for _, p := range param.Params {
		if _, ok := mapParams[p.ConfigFile]; !ok {
			mapParams[p.ConfigFile] = make(map[string]string)
		}
		mapParams[p.ConfigFile][p.Key] = p.Value
	}
	jsonParams, _ := json.Marshal(softs)
	fmt.Println(string(jsonParams))
	// return nil
	_, err := InstallSoftwareAsync(softs, mapParams, "/usr/local/onesoft")
	// _, err := InstallSoftwareAsync(softs, mapParams, "./onesoft")
	return err
}

func GetInstallLog(softwareName, version string) (string, error) {
	jobMutex.RLock()
	defer jobMutex.RUnlock()

	key := fmt.Sprintf("%s-%s", softwareName, version)
	job, exists := installJobs[key]
	if !exists {
		return "", fmt.Errorf("安装记录不存在")
	}

	content, err := os.ReadFile(job.LogPath)
	return string(content), err
}
