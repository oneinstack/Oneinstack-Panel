package ftp

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"oneinstack/core"
	"oneinstack/internal/services/filemanager"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
)

const maxImagePreviewBytes int64 = 30 << 20

const (
	imagePreviewTicketTTL = 5 * time.Minute
	maxPreviewTickets     = 1000
)

type imagePreviewTicket struct {
	Path      string
	UserID    int64
	SessionID string
	ExpiresAt time.Time
}

var imagePreviewTickets = struct {
	sync.Mutex
	items map[string]imagePreviewTicket
}{
	items: make(map[string]imagePreviewTicket),
}

var supportedImagePreviewTypes = map[string]struct{}{
	"image/jpeg":               {},
	"image/png":                {},
	"image/gif":                {},
	"image/webp":               {},
	"image/bmp":                {},
	"image/x-icon":             {},
	"image/vnd.microsoft.icon": {},
	"image/avif":               {},
}

func CreateImagePreviewTicket(c *gin.Context) {
	var input struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "文件预览票据参数格式不正确")
		return
	}
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()
	relative, err := manager.Relative(input.Path)
	if err != nil {
		handleFileError(c, err, "图片路径无效")
		return
	}
	if relative == "." {
		handleFileError(c, filemanager.ErrRootOperation, "不能预览文件根目录")
		return
	}
	userID, userOK := middleware.AuthenticatedUserID(c)
	sessionID, sessionOK := middleware.AuthenticatedSessionID(c)
	if !userOK || !sessionOK {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "登录会话无效"))
		return
	}
	token, err := newImagePreviewToken()
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "创建图片预览票据失败"))
		return
	}
	now := time.Now().UTC()
	imagePreviewTickets.Lock()
	for key, ticket := range imagePreviewTickets.items {
		if !ticket.ExpiresAt.After(now) {
			delete(imagePreviewTickets.items, key)
		}
	}
	if len(imagePreviewTickets.items) >= maxPreviewTickets {
		for key := range imagePreviewTickets.items {
			delete(imagePreviewTickets.items, key)
			break
		}
	}
	imagePreviewTickets.items[token] = imagePreviewTicket{
		Path:      manager.VirtualPath(relative),
		UserID:    userID,
		SessionID: sessionID,
		ExpiresAt: now.Add(imagePreviewTicketTTL),
	}
	imagePreviewTickets.Unlock()
	core.HandleSuccess(c, gin.H{
		"url":       "/v1/ftp/preview/" + token,
		"expiresAt": now.Add(imagePreviewTicketTTL),
	})
}

// PreviewImage streams a bounded, verified image to the authenticated browser.
// SVG is deliberately excluded because it can contain active content. The
// opaque ticket is bound to the current user session so server paths never
// appear in access logs.
func PreviewImage(c *gin.Context) {
	virtualPath, ok := resolveImagePreviewTicket(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrNotFound, "图片预览链接无效或已过期"))
		return
	}
	startFileOperation(c, "file.preview", virtualPath)
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	file, relative, err := manager.Open(virtualPath)
	if err != nil {
		handleFileError(c, err, "打开预览图片失败")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		handleFileError(c, err, "读取图片信息失败")
		return
	}
	lstat, err := manager.LstatRelative(relative)
	if err != nil {
		handleFileError(c, err, "读取图片信息失败")
		return
	}
	if !info.Mode().IsRegular() || lstat.Mode()&os.ModeSymlink != 0 {
		handleFileError(c, filemanager.ErrUnsupportedType, unsupportedFilePreviewEditMessage)
		return
	}
	if info.Size() <= 0 || info.Size() > maxImagePreviewBytes {
		handleFileError(
			c,
			filemanager.ErrUnsupportedType,
			unsupportedFilePreviewEditMessage,
		)
		return
	}

	header := make([]byte, min(info.Size(), int64(512)))
	if _, err := io.ReadFull(file, header); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		handleFileError(c, err, "读取图片内容失败")
		return
	}
	contentType := http.DetectContentType(header)
	if _, supported := supportedImagePreviewTypes[contentType]; !supported {
		handleFileError(c, filemanager.ErrUnsupportedType, unsupportedFilePreviewEditMessage)
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		handleFileError(c, err, "读取图片内容失败")
		return
	}

	c.Header("Content-Type", contentType)
	c.Header("Content-Length", fmt.Sprintf("%d", info.Size()))
	c.Header("Content-Disposition", "inline; filename*=utf-8''"+url.PathEscape(info.Name()))
	c.Header("Cache-Control", "private, max-age=60")
	c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
	c.Header("X-Content-Type-Options", "nosniff")
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
	finishFileOperation(c, "success", fmt.Sprintf("预览图片 %d 字节", info.Size()))
}

func resolveImagePreviewTicket(c *gin.Context) (string, bool) {
	token := strings.TrimSpace(c.Param("ticket"))
	if len(token) < 32 || len(token) > 128 {
		return "", false
	}
	userID, userOK := middleware.AuthenticatedUserID(c)
	sessionID, sessionOK := middleware.AuthenticatedSessionID(c)
	if !userOK || !sessionOK {
		return "", false
	}
	now := time.Now().UTC()
	imagePreviewTickets.Lock()
	defer imagePreviewTickets.Unlock()
	ticket, exists := imagePreviewTickets.items[token]
	if !exists {
		return "", false
	}
	if !ticket.ExpiresAt.After(now) {
		delete(imagePreviewTickets.items, token)
		return "", false
	}
	if ticket.UserID != userID || ticket.SessionID != sessionID {
		return "", false
	}
	return ticket.Path, true
}

func newImagePreviewToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
