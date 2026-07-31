package models

import "time"

type FileFavorite struct {
	ID        int64     `json:"id" gorm:"primaryKey;autoIncrement"`
	UserID    int64     `json:"userId" gorm:"not null;uniqueIndex:idx_file_favorite_user_path"`
	Username  string    `json:"username" gorm:"size:128;not null"`
	Path      string    `json:"path" gorm:"size:1024;not null;uniqueIndex:idx_file_favorite_user_path"`
	Name      string    `json:"name" gorm:"size:255;not null"`
	IsDir     bool      `json:"isDir" gorm:"not null"`
	CreatedAt time.Time `json:"createdAt" gorm:"not null;index"`
	UpdatedAt time.Time `json:"updatedAt" gorm:"not null"`
}

func (FileFavorite) TableName() string {
	return "file_favorite"
}
