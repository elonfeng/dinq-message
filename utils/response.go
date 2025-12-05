package utils

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Response 统一响应结构
type Response struct {
	Code    int         `json:"code"`           // 业务状态码（0表示成功）
	Message string      `json:"message"`        // 提示信息
	Data    interface{} `json:"data,omitempty"` // 响应数据
}

// SuccessResponse 成功响应（带数据）
func SuccessResponse(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Response{
		Code:    0,
		Message: "success",
		Data:    data,
	})
}

// SuccessWithMessage 成功响应（自定义消息）
func SuccessWithMessage(c *gin.Context, message string, data interface{}) {
	c.JSON(http.StatusOK, Response{
		Code:    0,
		Message: message,
		Data:    data,
	})
}

// ErrorResponse 错误响应
func ErrorResponse(c *gin.Context, httpStatus int, message string) {
	c.JSON(httpStatus, Response{
		Code:    500,
		Message: message,
		Data:    nil,
	})
}

// 常用错误响应快捷方法

// BadRequest 400 错误请求
func BadRequest(c *gin.Context, message string) {
	ErrorResponse(c, http.StatusBadRequest, message)
}

// Unauthorized 401 未授权
func Unauthorized(c *gin.Context, message string) {
	ErrorResponse(c, http.StatusUnauthorized, message)
}

// Forbidden 403 禁止访问
func Forbidden(c *gin.Context, message string) {
	ErrorResponse(c, http.StatusForbidden, message)
}

// NotFound 404 资源不存在
func NotFound(c *gin.Context, message string) {
	ErrorResponse(c, http.StatusNotFound, message)
}

// Conflict 409 冲突
func Conflict(c *gin.Context, message string) {
	ErrorResponse(c, http.StatusConflict, message)
}

// InternalServerError 500 服务器错误
func InternalServerError(c *gin.Context, message string) {
	ErrorResponse(c, http.StatusInternalServerError, message)
}
