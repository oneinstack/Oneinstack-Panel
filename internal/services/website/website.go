package website

import (
	"fmt"
	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services"
	"oneinstack/router/input"
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
	app.DB().Where("domain  = ?", param.Domain).First(w)
	if w.ID > 0 {
		return fmt.Errorf("已存在%v", w.Domain)
	}
	param.Name = param.Domain
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
