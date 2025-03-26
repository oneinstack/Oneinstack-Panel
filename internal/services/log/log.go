package log

import (
	"oneinstack/app"
	"oneinstack/internal/models"
)

func CreateLog(logs *models.SystemLog) error {
	tx := app.DB().Create(&logs)
	if tx.Error != nil {
		return tx.Error
	}
	return nil
}

func IsFirstLogin(username string) bool {
	var count int64
	app.DB().Model(&models.SystemLog{}).Where("user_name = ? AND log_type = ? AND log_info = ?", username, models.Login_Type, 1).Count(&count)
	return count == 1
}
