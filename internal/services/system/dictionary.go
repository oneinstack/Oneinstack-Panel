package system

import (
	"oneinstack/app"
	"oneinstack/internal/models"
)

func DictionaryList(q string) ([]*models.Dictionary, error) {
	ls := []*models.Dictionary{}
	tx := app.DB().Where("q = ? ", q).Find(&ls)
	return ls, tx.Error
}

func AddDictionary(param *models.Dictionary) error {
	tx := app.DB().Create(param)
	return tx.Error
}

func UpdateDictionary(param *models.Dictionary) error {
	tx := app.DB().Where("id = ?", param.ID).Updates(param)
	return tx.Error
}

func DeleteDictionary(id int64) error {
	tx := app.DB().Where("id = ?", id).Delete(&models.Dictionary{})
	return tx.Error
}
