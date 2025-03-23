package website

import (
	"fmt"
	"log"
	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services"
	"oneinstack/router/input"
	"os"
)

func List(param *input.WebsiteQueryParam) (*services.PaginatedResult[models.Website], error) {
	tx := app.DB()
	if param.Name != "" {
		tx = tx.Where("name like ?", "%"+param.Name+"%")
	}
	if param.Domain != "" {
		tx = tx.Where("domain like ?", "%"+param.Domain+"%")
	}
	if param.Type != "" {
		tx = tx.Where("type = ?", param.Type)
	}
	return services.Paginate[models.Website](tx, &models.Website{}, &input.Page{
		Page:     param.Page.Page,
		PageSize: param.Page.PageSize,
	})
}

func Add(param *models.Website) error {
	w := &models.Website{}
	app.DB().Where("domain = ?", param.Name).First(w)
	if w.ID > 0 {
		return fmt.Errorf("已存在%v", w.Name)
	}
	//报错只是删除文件夹
	defer func(dir string) {
		err := DelectDir(dir)
		if err != nil {
			log.Printf("无法删除目录: %s", err)
		}
	}(app.ONE_CONFIG.System.WebPath + param.Name)
	// 创建网站目录文件
	if param.Type != "proxy" {
		err := CreateDir(app.ONE_CONFIG.System.WebPath + param.Name)
		if err != nil {
			return err
		}
	}
	tx := app.DB().Create(param)
	if tx.Error != nil {
		return tx.Error
	}
	config, err := GenerateNginxConfig(param)
	if err != nil {
		return err
	}
	fmt.Println(config)
	err = ReloadNginxConfig()
	if err != nil {
		return err
	}
	return nil
}

func DelectDir(dir string) error {
	err := os.RemoveAll(dir)
	if err != nil {
		return fmt.Errorf("无法删除目录: %s", err)
	}
	return nil
}

func CreateDir(dir string) error {
	err := CreateDirIfNotExists(dir)
	if err != nil {
		return err
	}
	return nil
}

func CreateDirIfNotExists(dir string) error {
	// 检查目录是否存在
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		// 如果不存在，创建目录
		err := os.MkdirAll(dir, 0755)
		if err != nil {
			return fmt.Errorf("无法创建目录: %s", err)
		}
	}
	return nil
}

func Update(param *models.Website) error {
	w := &models.Website{}
	app.DB().Where("id = ?", param.ID).First(w)
	if w.ID > 0 && w.ID != param.ID {
		return fmt.Errorf("已存在%v", w.Domain)
	}
	param.Name = param.Domain
	tx := app.DB().Where("id = ?", param.ID).Updates(param)
	if tx.Error != nil {
		return tx.Error
	}
	err := DeleteNginxConfig(w.Name)
	if err != nil {
		return err
	}
	config, err := GenerateNginxConfig(param)
	if err != nil {
		return err
	}
	fmt.Println(config)
	err = ReloadNginxConfig()
	if err != nil {
		return err
	}
	return nil
}

func Delete(id int64) error {
	w := &models.Website{}
	tx := app.DB().Where("id  = ?", id).First(w)
	if tx.Error != nil {
		return tx.Error
	}
	err := DeleteNginxConfig(w.Name)
	if err != nil {
		return err
	}
	err = ReloadNginxConfig()
	if err != nil {
		return err
	}
	tx = app.DB().Where("id = ?", id).Delete(&models.Website{})
	return tx.Error
}

func Check() bool {
	// 检查 Nginx 是否已安装
	redis := &models.Software{}
	redisTx := app.DB().Model(&models.Software{}).Where("name = ? AND installed = 1", "Nginx").First(redis)
	if redisTx.Error != nil {
		return false
	}
	if redis.Id > 0 {
		return true
	}
	return false
}
