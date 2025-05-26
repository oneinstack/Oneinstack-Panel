package software

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services"
	"oneinstack/router/input"
	"oneinstack/router/output"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/imroc/req/v3"
	"gorm.io/gorm"
)

//go:embed scripts/UninstallRedis.sh
var UninstallRedis embed.FS

//go:embed scripts/UninstallMySQL.sh
var UninstallMySQL embed.FS

//go:embed scripts/UninstallNginx.sh
var UninstallNginx embed.FS

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
			"describe," +
			"GROUP_CONCAT(DISTINCT version) as versions," +
			"MAX(name) as name," +
			"MAX(icon) as icon," +
			"MAX(type) as type," +
			"MAX(status) as status," +
			"MAX(resource) as resource," +
			"MAX(is_update) as is_update," +
			"install_version as install_version," +
			"installed as installed," +
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
	// 将软件状态设置为 0 并且设置installed为false
	fmt.Println(param.Name + "|" + param.Version)
	tx := app.DB().Model(&models.Software{}).Where("name = ? AND version = ?", param.Name, param.Version).Updates(map[string]interface{}{
		"status":          models.Soft_Status_Default,
		"log":             "",
		"installed":       false,
		"install_version": "",
	})
	err := RunUnInstallScript(param.Name)
	if err != nil {
		return false, err
	}
	if tx.Error != nil {
		return false, tx.Error
	}
	return true, nil
}

func RunUnInstallScript(soft string) error {
	// 读取脚本内容
	if soft == "Redis" {
		data, err := UninstallRedis.ReadFile("scripts/UninstallRedis.sh")
		if err != nil {
			return err
		}

		// 创建临时文件保存脚本
		tmpDir := os.TempDir()
		scriptPath := filepath.Join(tmpDir, "UninstallRedis.sh")
		if err := os.WriteFile(scriptPath, data, 0755); err != nil {
			return err
		}

		// 执行脚本
		cmd := exec.Command("/bin/bash", scriptPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	} else if soft == "MySQL" {
		data, err := UninstallMySQL.ReadFile("scripts/UninstallMySQL.sh")
		if err != nil {
			return err
		}

		// 创建临时文件保存脚本
		tmpDir := os.TempDir()
		scriptPath := filepath.Join(tmpDir, "UninstallRedis.sh")
		if err := os.WriteFile(scriptPath, data, 0755); err != nil {
			return err
		}

		// 执行脚本
		cmd := exec.Command("/bin/bash", scriptPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	} else if soft == "Nginx" {
		data, err := UninstallNginx.ReadFile("scripts/UninstallNginx.sh")
		if err != nil {
			return err
		}

		// 创建临时文件保存脚本
		tmpDir := os.TempDir()
		scriptPath := filepath.Join(tmpDir, "UninstallNginx.sh")
		if err := os.WriteFile(scriptPath, data, 0755); err != nil {
			return err
		}

		// 执行脚本
		cmd := exec.Command("/bin/bash", scriptPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	return nil
}
