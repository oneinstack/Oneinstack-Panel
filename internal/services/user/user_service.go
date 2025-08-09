package user

import (
	"oneinstack/app"
	"oneinstack/internal/crypto"
	"oneinstack/internal/models"
	"oneinstack/utils"
)

func HasUser() (bool, error) {
	var count int64 = 0
	tx := app.DB().Model(models.User{}).Count(&count)
	if tx.Error != nil {
		return false, tx.Error
	}
	return count > 0, nil
}

func CreateUser(username, password string, isAdmin bool) error {
	hashed, err := crypto.HashPassword(password)
	if err != nil {
		return err
	}
	user := &models.User{
		Username:  username,
		Password:  hashed,
		IsAdmin:   isAdmin,
		FirstJoin: false,
	}
	tx := app.DB().Create(user)
	if tx.Error != nil {
		return tx.Error
	}
	return nil
}

func CreateAdminUser() (username, password string, err error) {
	username = utils.GenerateRandomString(8, 12)
	password = utils.GenerateRandomString(12, 16)
	err = CreateUser(username, password, true)
	return
}

func GetUserByUsername(username string) (*models.User, error) {
	var user models.User
	tx := app.DB().Where("username = ?", username).First(&user)
	if tx.Error != nil {
		return nil, tx.Error
	}
	return &user, nil
}

func CheckUserPassword(username, password string) (*models.User, bool) {
	u, err := GetUserByUsername(username)
	if err != nil {
		return nil, false
	}
	if crypto.CheckPasswordHash(password, u.Password) {
		return u, true
	}
	return nil, false
}

func ListUsers() ([]*models.User, error) {
	users := []*models.User{}
	tx := app.DB().Find(&users)
	if tx.Error != nil {
		return nil, tx.Error
	}
	return users, nil
}

func ChangePassword(username, newPassword string) error {
	hashed, err := crypto.HashPassword(newPassword)
	if err != nil {
		return err
	}
	tx := app.DB().Model(&models.User{}).Where("username = ?", username).Update("password", hashed)
	return tx.Error
}

func ResetUsername(newUsername string) error {
	tx := app.DB().Model(&models.User{}).Where("id = 1").Update("username", newUsername)
	return tx.Error
}
