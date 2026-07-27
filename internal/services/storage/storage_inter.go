package storage

import (
	"fmt"
	"oneinstack/internal/models"
)

type StorageOPI interface {
	Connect() error
	Close() error
	Sync() error
	CreateLibrary(lb *models.Library) error
	UpdateLibraryPassword(lb *models.Library, password string) error
	DeleteLibrary(lb *models.Library) error
}

var newStorageOP = NewStorageOP

func NewStorageOP(p *models.Storage, lib string) (StorageOPI, error) {
	switch p.Type {
	case "mysql":
		return NewMysqlOP(p, lib), nil
	case "redis":
		return NewRedisOP(p), nil
	}
	return nil, fmt.Errorf("未知的存储服务")
}
