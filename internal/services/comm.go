package services

import (
	"math"
	"oneinstack/router/input"

	"gorm.io/gorm"
)

// PaginatedResult holds the result of a paginated query.
type PaginatedResult[T any] struct {
	Data       []T `json:"data"`
	Total      int `json:"total"`
	Page       int `json:"page"`
	PageSize   int `json:"page_size"`
	TotalPages int `json:"total_pages"`
}

// Paginate performs a paginated query on the provided model.
func Paginate[T any](db *gorm.DB, model interface{}, pagination *input.Page) (*PaginatedResult[T], error) {
	if pagination.PageSize <= 0 {
		pagination.PageSize = 10 // 默认每页显示10条记录
	}
	if pagination.Page <= 0 {
		pagination.Page = 1 // 默认从第一页开始
	}

	var total int64
	result := db.Model(model).Count(&total)
	if result.Error != nil {
		return nil, result.Error
	}

	var data []T
	offset := (pagination.Page - 1) * pagination.PageSize
	err := db.Offset(offset).Limit(pagination.PageSize).Find(&data).Error
	if err != nil {
		return nil, err
	}

	totalPages := int(math.Ceil(float64(total) / float64(pagination.PageSize)))

	return &PaginatedResult[T]{
		Data:       data,
		Total:      int(total),
		Page:       pagination.Page,
		PageSize:   pagination.PageSize,
		TotalPages: totalPages,
	}, nil
}
