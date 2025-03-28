package core

var (
	// common errors
	ErrSuccess             = newError(0, "Success")
	ErrBadRequest          = newError(400, "参数错误,请检查请求参数")
	ErrUnauthorized        = newError(401, "Unauthorized")
	ErrUnauthorizedAP      = newError(401, "账号或者密码不正确")
	ErrNotFound            = newError(404, "Not Found")
	ErrInternalServerError = newError(500, "Internal Server Error")
)
