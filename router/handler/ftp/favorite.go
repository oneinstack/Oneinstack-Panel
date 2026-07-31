package ftp

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"
)

type favoriteItem struct {
	ID        int64      `json:"id"`
	Path      string     `json:"path"`
	Name      string     `json:"name"`
	IsDir     bool       `json:"isDir"`
	IsMissing bool       `json:"isMissing"`
	Size      *int64     `json:"size,omitempty"`
	ModTime   *time.Time `json:"modTime,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

func CreateFavorite(c *gin.Context) {
	var input struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "请求参数错误")
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	username, _ := c.Get(middleware.ContextUsername)
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	info, relative, err := manager.Stat(input.Path)
	if err != nil {
		handleFileError(c, err, "收藏路径不存在")
		return
	}
	lstat, err := manager.LstatRelative(relative)
	if err != nil {
		handleFileError(c, err, "读取收藏路径失败")
		return
	}
	virtualPath := manager.VirtualPath(relative)
	startFileOperation(c, "file.favorite.create", virtualPath)
	record := models.FileFavorite{
		UserID:   userID,
		Username: valueString(username),
		Path:     virtualPath,
		Name:     info.Name(),
		IsDir:    info.IsDir(),
	}
	if lstat.Mode()&fs.ModeSymlink != 0 {
		record.IsDir = info.IsDir()
	}
	database := app.DB()
	if database == nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "收藏服务未初始化"))
		return
	}
	if err := database.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "path"}},
		DoUpdates: clause.AssignmentColumns([]string{"username", "name", "is_dir", "updated_at"}),
	}).Create(&record).Error; err != nil {
		handleFileError(c, err, "保存收藏失败")
		return
	}
	var favorite models.FileFavorite
	if err := database.Where("user_id = ? AND path = ?", userID, virtualPath).First(&favorite).Error; err != nil {
		handleFileError(c, err, "读取收藏记录失败")
		return
	}
	core.HandleSuccess(c, favorite)
	finishFileOperation(c, "success", "收藏 "+virtualPath)
}

func CancelFavorite(c *gin.Context) {
	var input struct {
		Path string `json:"path"`
		ID   int64  `json:"id"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "请求参数错误")
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	database := app.DB()
	if database == nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "收藏服务未初始化"))
		return
	}

	query := database.Where("user_id = ?", userID)
	trimmedPath := strings.TrimSpace(input.Path)
	var auditPath string
	switch {
	case trimmedPath != "":
		manager, managerOK := managerForRequest(c)
		if !managerOK {
			return
		}
		relative, err := manager.Relative(trimmedPath)
		manager.Close()
		if err != nil {
			handleFileError(c, err, "路径无效")
			return
		}
		virtualPath := manager.VirtualPath(relative)
		auditPath = virtualPath
		query = query.Where("path = ?", virtualPath)
	case input.ID > 0:
		auditPath = fmt.Sprintf("/favorites/%d", input.ID)
		query = query.Where("id = ?", input.ID)
	default:
		core.HandleError(c, core.NewError(core.ErrBadRequest, "请提供要取消的收藏路径或编号"))
		return
	}
	startFileOperation(c, "file.favorite.cancel", auditPath)
	if err := query.Delete(&models.FileFavorite{}).Error; err != nil {
		handleFileError(c, err, "取消收藏失败")
		return
	}
	core.HandleSuccess(c, "已取消收藏")
	finishFileOperation(c, "success", "取消收藏 "+auditPath)
}

func ListFavorites(c *gin.Context) {
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	database := app.DB()
	if database == nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "收藏服务未初始化"))
		return
	}
	var favorites []models.FileFavorite
	if err := database.Where("user_id = ?", userID).Order("created_at DESC, id DESC").Find(&favorites).Error; err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取收藏列表失败"))
		return
	}
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	items := make([]favoriteItem, 0, len(favorites))
	for _, favorite := range favorites {
		item := favoriteItem{
			ID:        favorite.ID,
			Path:      favorite.Path,
			Name:      favorite.Name,
			IsDir:     favorite.IsDir,
			CreatedAt: favorite.CreatedAt,
			UpdatedAt: favorite.UpdatedAt,
		}
		info, _, err := manager.Stat(favorite.Path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				item.IsMissing = true
				items = append(items, item)
				continue
			}
			handleFileError(c, err, "读取收藏文件失败")
			return
		}
		item.Name = info.Name()
		item.IsDir = info.IsDir()
		size := info.Size()
		modTime := info.ModTime().UTC()
		item.Size = &size
		item.ModTime = &modTime
		items = append(items, item)
	}
	core.HandleSuccess(c, gin.H{"items": items, "total": len(items)})
}

func favoriteMapForPaths(userID int64, paths []string) map[string]int64 {
	result := make(map[string]int64, len(paths))
	if userID <= 0 || len(paths) == 0 {
		return result
	}
	database := app.DB()
	if database == nil {
		return result
	}
	seen := make(map[string]struct{}, len(paths))
	filtered := make([]string, 0, len(paths))
	for _, path := range paths {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		filtered = append(filtered, trimmed)
	}
	if len(filtered) == 0 {
		return result
	}
	var favorites []models.FileFavorite
	if err := database.Select("id", "path").Where("user_id = ? AND path IN ?", userID, filtered).Find(&favorites).Error; err != nil {
		return result
	}
	for _, favorite := range favorites {
		result[favorite.Path] = favorite.ID
	}
	return result
}

func favoriteIDForPath(c *gin.Context, path string) int {
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		return 0
	}
	return int(favoriteMapForPaths(userID, []string{path})[path])
}
