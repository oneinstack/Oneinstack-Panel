package storage

import (
	"context"
	"errors"
	"net"
	"net/http"
	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/internal/services/storage"
	userservice "oneinstack/internal/services/user"
	"oneinstack/router/input"
	"oneinstack/router/middleware"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const ApprovalActionDatabaseCredentialReveal = "database.credential.reveal"
const ApprovalActionDatabaseConnectionDelete = "database.connection.delete"

const storageOperationTimeout = 10 * time.Second

type RevealCredentialApprovalPayload struct {
	LibraryID int64  `json:"libraryId"`
	Reason    string `json:"reason"`
}

type DeleteConnectionApprovalPayload struct {
	ConnectionID int64  `json:"connectionId"`
	Type         string `json:"type"`
	Addr         string `json:"addr"`
	Port         string `json:"port"`
}

func ADDStorage(c *gin.Context) {
	var req input.AddParam
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "数据库连接请求参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	err := req.Validate()
	if err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "数据库连接参数无效")
		core.HandleError(c, appErr)
		return
	}
	operationContext, cancel := context.WithTimeout(c.Request.Context(), storageOperationTimeout)
	defer cancel()
	err = storage.AddContext(operationContext, &req)
	if err != nil {
		handleStorageOperationError(c, err, core.ErrInternalError, "新增数据库连接失败")
		return
	}
	core.HandleSuccess(c, "成功")
}

func ADDLib(c *gin.Context) {
	var req input.LibParam
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "数据库实例请求参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	if err := req.Validate(); err != nil {
		message := "数据库参数无效"
		if req.ID <= 0 {
			message = "未检测到可用 MySQL 实例，请先安装或修复 MySQL"
		}
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, message))
		return
	}
	credential, err := storage.AddLibs(&req)
	if err != nil {
		handleStorageOperationError(c, err, core.ErrInternalError, "新增数据库实例失败")
		return
	}
	// The caller may receive the newly created credential so it can be saved
	// during provisioning; reading an existing credential remains restricted.
	core.HandleSuccess(c, credential)
}

func GetStorage(c *gin.Context) {
	t := c.Query("type")
	data, err := storage.List(t)
	if err != nil {
		handleStorageOperationError(c, err, core.ErrInternalError, "查询数据库连接列表失败")
		return
	}
	core.HandleSuccess(c, data)
}

func UpdateStorage(c *gin.Context) {
	var req input.AddParam
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "数据库连接请求参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	err := req.Validate()
	if err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "数据库连接参数无效")
		core.HandleError(c, appErr)
		return
	}
	err = storage.Update(&req)
	if err != nil {
		handleStorageOperationError(c, err, core.ErrInternalError, "更新数据库连接失败")
		return
	}
	core.HandleSuccess(c, "成功")
}

func DelStorage(c *gin.Context) {
	var req input.IDParam
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "数据库连接标识格式不正确")
		core.HandleError(c, appErr)
		return
	}
	var connection models.Storage
	if err := app.DB().First(&connection, req.ID).Error; err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "数据库连接不存在"))
		return
	}
	if shouldRequestDatabaseApproval(c) {
		resourceName := strings.TrimSpace(connection.Remark)
		if resourceName == "" {
			resourceName = connection.Addr + ":" + connection.Port
		}
		approval, createErr := createDatabaseApproval(
			c,
			ApprovalActionDatabaseConnectionDelete,
			resourceName,
			strconv.FormatInt(connection.ID, 10),
			DeleteConnectionApprovalPayload{
				ConnectionID: connection.ID,
				Type:         connection.Type,
				Addr:         connection.Addr,
				Port:         connection.Port,
			},
		)
		if createErr != nil {
			core.HandleError(c, core.WrapError(createErr, core.ErrBadRequest, "创建数据库连接删除审批失败"))
			return
		}
		c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, gin.H{
			"mode":       "approval_pending",
			"approvalId": approval.ID,
			"status":     approval.Status,
		}))
		return
	}
	err := storage.Del(&req)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "删除数据库连接失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, nil)
}

func TestStorageConnection(c *gin.Context) {
	var req input.AddParam
	if err := c.ShouldBindJSON(&req); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "数据库连接测试参数格式不正确"))
		return
	}
	if err := req.Validate(); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "数据库连接参数无效"))
		return
	}
	operationContext, cancel := context.WithTimeout(c.Request.Context(), storageOperationTimeout)
	defer cancel()
	if err := storage.TestConnectionContext(operationContext, &req); err != nil {
		handleStorageOperationError(c, err, core.ErrBadRequest, "数据库连接测试失败")
		return
	}
	core.HandleSuccess(c, "连接成功")
}

func handleStorageOperationError(c *gin.Context, err error, code core.ErrorCode, message string) {
	if errors.Is(err, storage.ErrConnectionAlreadyExists) {
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrConflict,
			"数据库连接已存在",
			"相同地址、端口和数据库类型的连接已存在，请刷新连接列表后直接使用或编辑现有连接。",
		))
		return
	}
	if storage.IsConnectionError(err) {
		core.HandleError(c, storageConnectionError(err))
		return
	}
	core.HandleError(c, core.WrapError(err, code, message))
}

func storageConnectionError(err error) *core.AppError {
	lower := strings.ToLower(err.Error())
	var networkErr net.Error

	var appErr *core.AppError
	switch {
	case errors.Is(err, context.DeadlineExceeded),
		(errors.As(err, &networkErr) && networkErr.Timeout()),
		strings.Contains(lower, "timed out"),
		strings.Contains(lower, "timeout"):
		appErr = core.NewErrorWithDetail(
			core.ErrTaskTimeout,
			"数据库连接超时",
			"目标数据库在 5 秒内未响应，请检查地址、端口、防火墙和数据库服务状态后重试。",
		)
		appErr.Field = "addr"
	case strings.Contains(lower, "connection refused"):
		appErr = core.NewErrorWithDetail(
			core.ErrServiceUnavailable,
			"数据库连接被拒绝",
			"目标数据库拒绝连接，请确认服务已启动、监听地址和端口配置正确。",
		)
		appErr.Field = "addr"
	case strings.Contains(lower, "no such host"),
		strings.Contains(lower, "server misbehaving"):
		appErr = core.NewErrorWithDetail(
			core.ErrBadRequest,
			"数据库地址解析失败",
			"无法解析目标数据库地址，请检查地址和 DNS 配置后重试。",
		)
		appErr.Field = "addr"
	case strings.Contains(lower, "network is unreachable"),
		strings.Contains(lower, "no route to host"):
		appErr = core.NewErrorWithDetail(
			core.ErrServiceUnavailable,
			"数据库网络不可达",
			"面板服务器当前无法访问目标数据库网络，请检查路由、安全组和防火墙配置。",
		)
		appErr.Field = "addr"
	case strings.Contains(lower, "access denied"),
		strings.Contains(lower, "authentication failed"),
		strings.Contains(lower, "invalid username"),
		strings.Contains(lower, "invalid password"),
		strings.Contains(lower, "wrongpass"),
		strings.Contains(lower, "invalid username-password pair"):
		appErr = core.NewErrorWithDetail(
			core.ErrBadRequest,
			"数据库认证失败",
			"目标数据库已响应，但用户名或密码未通过认证，请核对登录凭据。",
		)
	default:
		appErr = core.NewErrorWithDetail(
			core.ErrServiceUnavailable,
			"数据库连接失败",
			"无法连接到目标数据库，请检查地址、端口、登录凭据和网络访问策略后重试。",
		)
		appErr.Field = "addr"
	}
	return appErr
}

func DeleteLibrary(c *gin.Context) {
	var req input.DeleteLibraryParam
	if err := c.ShouldBindJSON(&req); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "数据库实例删除参数格式不正确"))
		return
	}
	if err := storage.DeleteLibrary(&req); err != nil {
		handleStorageOperationError(c, err, core.ErrBadRequest, "删除数据库失败")
		return
	}
	core.HandleSuccess(c, nil)
}

func SyncStorage(c *gin.Context) {
	var req input.IDParam
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "数据库同步参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	err := storage.Sync(&req)
	if err != nil {
		handleStorageOperationError(c, err, core.ErrInternalError, "同步数据库信息失败")
		return
	}
	core.HandleSuccess(c, nil)
}
func GetLib(c *gin.Context) {
	var req input.QueryParam
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "数据库查询参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	data, err := storage.LibList(&req)
	if err != nil {
		handleStorageOperationError(c, err, core.ErrInternalError, "查询数据库实例列表失败")
		return
	}
	core.HandleSuccess(c, data)
}

func GetRedisKeys(c *gin.Context) {
	var req input.QueryParam
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "Redis 查询参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	data, err := storage.RedisKeyList(&req)
	if err != nil {
		handleStorageOperationError(c, err, core.ErrInternalError, "查询 Redis 键列表失败")
		return
	}
	core.HandleSuccess(c, data)
}

func Info(c *gin.Context) {
	mysqlInstall, redisInstall := storage.CheckStorage()
	core.HandleSuccess(c, map[string]interface{}{"mysql": mysqlInstall, "redis": redisInstall})
}

func RevealLibraryCredential(c *gin.Context) {
	var req input.LibraryCredentialVerification
	if err := c.ShouldBindJSON(&req); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请输入当前面板密码"))
		return
	}
	if !verifyCurrentPassword(c, req.PanelPassword) {
		core.HandleError(c, core.NewError(core.ErrInvalidPassword, "当前面板密码错误"))
		return
	}
	id, err := parseLibraryID(c)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "数据库标识无效"))
		return
	}
	credential, err := storage.GetLibraryCredential(id)
	if err != nil {
		code, message := core.ErrInternalError, "读取数据库账号失败"
		switch {
		case errors.Is(err, storage.ErrLibraryNotFound):
			code, message = core.ErrNotFound, "数据库实例不存在"
		case errors.Is(err, storage.ErrLibraryCredentialUnavailable):
			code, message = core.ErrBadRequest, "该数据库没有可读取的托管 MySQL 账号"
		case errors.Is(err, storage.ErrLibraryCredentialCorrupt):
			code, message = core.ErrConfigReadFailed, "数据库账号凭据无法解密，可能已损坏或密钥已变更"
		}
		core.HandleError(c, core.WrapError(err, code, message))
		return
	}
	core.HandleSuccess(c, credential)
}

func UpdateLibraryCredential(c *gin.Context) {
	var req input.UpdateLibraryCredentialParam
	if err := c.ShouldBindJSON(&req); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "数据库凭据更新参数格式不正确"))
		return
	}
	if !verifyCurrentPassword(c, req.PanelPassword) {
		core.HandleError(c, core.NewError(core.ErrInvalidPassword, "当前面板密码错误"))
		return
	}
	id, err := parseLibraryID(c)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "数据库标识无效"))
		return
	}
	credential, err := storage.UpdateLibraryCredential(id, req.Password)
	if err != nil {
		handleStorageOperationError(c, err, core.ErrBadRequest, "修改数据库账号密码失败")
		return
	}
	// Return the newly rotated password to the operator who explicitly set it.
	// Reading an existing credential remains restricted to the reveal endpoint.
	core.HandleSuccess(c, credential)
}

func verifyCurrentPassword(c *gin.Context, password string) bool {
	usernameValue, exists := c.Get(middleware.ContextUsername)
	username, ok := usernameValue.(string)
	if !exists || !ok || username == "" || password == "" {
		return false
	}
	account, verified := userservice.CheckUserPassword(username, password)
	if !verified {
		return false
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	return ok && account.ID == userID
}

func parseLibraryID(c *gin.Context) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
}

func ExecuteRevealCredentialApproval(payload RevealCredentialApprovalPayload) (any, error) {
	return storage.GetLibraryCredential(payload.LibraryID)
}

func ExecuteDeleteConnectionApproval(payload DeleteConnectionApprovalPayload) error {
	return storage.Del(&input.IDParam{ID: payload.ConnectionID})
}
