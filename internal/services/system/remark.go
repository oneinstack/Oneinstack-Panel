package system

import (
	"errors"
	"gorm.io/gorm"
	"oneinstack/app"
	"oneinstack/internal/models"
)

func GetRemarkByID(id int64) (*models.Remark, error) {
	r := &models.Remark{}
	tx := app.DB().Where("id = ?", id).First(r)
	if tx.Error != nil && !errors.Is(tx.Error, gorm.ErrRecordNotFound) {
		return nil, tx.Error
	}
	return r, nil
}

func AddRemark(param *models.Remark) error {
	tx := app.DB().Create(param)
	return tx.Error
}

func UpdateRemark(param *models.Remark) error {
	tx := app.DB().Where("id = ?", param.ID).Updates(param)
	return tx.Error
}

func DeleteRemark(id int64) error {
	tx := app.DB().Where("id = ?", id).Delete(&models.Remark{})
	return tx.Error
}
