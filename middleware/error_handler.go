package middleware

import (
	"dinq_message/utils"
	"log"

	"github.com/gin-gonic/gin"
)

// ErrorHandlerMiddleware 统一错误处理中间件
// 捕获 panic 和未处理的错误，返回统一格式的错误响应
func ErrorHandlerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				// 记录 panic 信息
				log.Printf("[ERROR] Panic recovered: %v", err)

				// 返回统一错误响应
				if !c.Writer.Written() {
					utils.InternalServerError(c, "internal server error")
				}

				// 终止后续处理
				c.Abort()
			}
		}()

		// 继续处理请求
		c.Next()

		// 检查是否有错误（通过 c.Errors）
		if len(c.Errors) > 0 {
			// 获取最后一个错误
			err := c.Errors.Last()
			log.Printf("[ERROR] Request error: %v", err.Err)

			// 如果响应还没有写入，返回错误
			if !c.Writer.Written() {
				utils.InternalServerError(c, err.Error())
			}
		}
	}
}
