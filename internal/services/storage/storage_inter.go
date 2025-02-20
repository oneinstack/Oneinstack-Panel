package storage

import (
	"fmt"
	"oneinstack/internal/models"
)

type StorageOPI interface {
	Connet() error
	Sync() error
	CreateLibrary(lb *models.Library) error
}

func NewStorageOP(p *models.Storage, lib string) (StorageOPI, error) {
	switch p.Type {
	case "mysql":
		return NewMysqlOP(p, lib), nil
	case "pg":
	case "sqlserver":
	case "redis":
		return NewRedisOP(p), nil
	case "mongo":
	}
	return nil, fmt.Errorf("未知的存储服务")
}
