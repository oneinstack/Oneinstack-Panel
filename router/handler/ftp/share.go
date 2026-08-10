package ftp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"strconv"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/internal/services/filemanager"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	defaultShareHours = 24
	maxShareHours     = 24 * 7
)

func CreateFileShare(c *gin.Context) {
	var input struct {
		Path        string `json:"path" binding:"required"`
		ExpiryHours int    `json:"expiryHours"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "文件分享参数格式不正确")
		return
	}
	if input.ExpiryHours == 0 {
		input.ExpiryHours = defaultShareHours
	}
	if input.ExpiryHours < 1 || input.ExpiryHours > maxShareHours {
		handleBadRequest(c, errors.New("expiryHours must be between 1 and 168"), "分享有效期必须为 1 到 168 小时")
		return
	}
	startFileOperation(c, "file.share.create", input.Path)
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()
	info, relative, err := manager.Stat(input.Path)
	if err != nil {
		handleFileError(c, err, "读取分享文件失败")
		return
	}
	lstat, err := manager.LstatRelative(relative)
	if err != nil {
		handleFileError(c, err, "读取分享文件失败")
		return
	}
	if !info.Mode().IsRegular() || lstat.Mode()&os.ModeSymlink != 0 {
		handleFileError(c, filemanager.ErrNotRegular, "仅支持分享普通文件")
		return
	}

	token, tokenHash, err := newShareToken()
	if err != nil {
		handleFileError(c, err, "创建分享令牌失败")
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	username, _ := c.Get(middleware.ContextUsername)
	now := time.Now().UTC()
	share := models.FileShare{
		ID:            uuid.NewString(),
		TokenHash:     tokenHash,
		Path:          manager.VirtualPath(relative),
		Name:          info.Name(),
		SizeBytes:     info.Size(),
		ModTimeUnixNS: info.ModTime().UnixNano(),
		CreatedBy:     userID,
		CreatedByName: valueString(username),
		ExpiresAt:     now.Add(time.Duration(input.ExpiryHours) * time.Hour),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := app.DB().Create(&share).Error; err != nil {
		handleFileError(c, err, "保存分享记录失败")
		return
	}
	core.HandleSuccess(c, gin.H{
		"share":       share,
		"downloadUrl": "/v1/public/file-share/download?token=" + url.QueryEscape(token),
	})
	finishFileOperation(c, "success", fmt.Sprintf("创建 %d 小时有效的外链", input.ExpiryHours))
}

func ListFileShares(c *gin.Context) {
	var shares []models.FileShare
	if err := app.DB().
		Order("created_at DESC").
		Limit(100).
		Find(&shares).Error; err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取外链分享失败"))
		return
	}
	core.HandleSuccess(c, gin.H{"items": shares, "total": len(shares), "now": time.Now().UTC()})
}

func RevokeFileShare(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if _, err := uuid.Parse(id); err != nil {
		handleBadRequest(c, err, "分享编号无效")
		return
	}
	startFileOperation(c, "file.share.revoke", "/shares/"+id)
	now := time.Now().UTC()
	result := app.DB().Model(&models.FileShare{}).
		Where("id = ? AND revoked_at IS NULL", id).
		Updates(map[string]any{"revoked_at": now, "updated_at": now})
	if result.Error != nil {
		handleFileError(c, result.Error, "取消分享失败")
		return
	}
	if result.RowsAffected == 0 {
		handleFileError(c, fs.ErrNotExist, "分享不存在或已经取消")
		return
	}
	core.HandleSuccess(c, "已取消外链分享")
	finishFileOperation(c, "success", "取消外链分享")
}

// DownloadSharedFile is intentionally a fixed public path with the token in
// the query string. The HTTP access logger records URL paths but not queries,
// so the bearer token is not persisted in runtime logs.
func DownloadSharedFile(c *gin.Context) {
	token := strings.TrimSpace(c.Query("token"))
	if len(token) < 32 || len(token) > 128 {
		core.HandleError(c, core.NewError(core.ErrNotFound, "分享链接无效或已失效"))
		return
	}
	var share models.FileShare
	now := time.Now().UTC()
	err := app.DB().
		Where("token_hash = ? AND revoked_at IS NULL AND expires_at > ?", shareTokenHash(token), now).
		First(&share).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		core.HandleError(c, core.NewError(core.ErrNotFound, "分享链接无效或已失效"))
		return
	}
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取分享信息失败"))
		return
	}
	startFileOperation(c, "file.share.download", share.Path)
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()
	file, relative, err := manager.Open(share.Path)
	if err != nil {
		handleFileError(c, err, "分享文件不存在")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		handleFileError(c, err, "读取分享文件失败")
		return
	}
	lstat, err := manager.LstatRelative(relative)
	if err != nil ||
		!info.Mode().IsRegular() ||
		lstat.Mode()&os.ModeSymlink != 0 ||
		info.Size() != share.SizeBytes ||
		info.ModTime().UnixNano() != share.ModTimeUnixNS {
		handleFileError(c, fs.ErrNotExist, "分享文件已发生变化，请重新创建分享")
		return
	}
	if err := app.DB().Model(&models.FileShare{}).
		Where("id = ?", share.ID).
		Updates(map[string]any{
			"download_count": gorm.Expr("download_count + 1"),
			"updated_at":     now,
		}).Error; err != nil {
		handleFileError(c, err, "更新分享下载记录失败")
		return
	}
	if contentType := mime.TypeByExtension(pathpkg.Ext(info.Name())); contentType != "" {
		c.Header("Content-Type", contentType)
	}
	c.Header("Content-Length", strconv.FormatInt(info.Size(), 10))
	c.Header("Content-Disposition", "attachment; filename*=utf-8''"+url.PathEscape(info.Name()))
	c.Header("Cache-Control", "private, no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
	finishFileOperation(c, "success", fmt.Sprintf("通过外链下载 %d 字节", info.Size()))
}

func newShareToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return token, shareTokenHash(token), nil
}

func shareTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
