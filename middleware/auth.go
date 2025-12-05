package middleware

import (
	"strings"

	"dinq_message/utils"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var jwtSecret []byte

// InitAuth 初始化认证中间件
func InitAuth(secret string) {
	jwtSecret = []byte(secret)
}

// Claims JWT 声明
type Claims struct {
	UserID uuid.UUID `json:"user_id"`
	jwt.RegisteredClaims
}

// AuthMiddleware HTTP API 认证中间件
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			utils.Unauthorized(c, "missing authorization header")
			c.Abort()
			return
		}

		// Bearer token
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			utils.Unauthorized(c, "invalid authorization header")
			c.Abort()
			return
		}

		tokenString := parts[1]
		userID, err := ValidateToken(tokenString)
		if err != nil {
			utils.Unauthorized(c, "invalid token")
			c.Abort()
			return
		}

		// 将 userID 存入上下文
		c.Set("user_id", userID)
		c.Next()
	}
}

// ValidateToken 验证 JWT Token
func ValidateToken(tokenString string) (uuid.UUID, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	})

	if err != nil {
		return uuid.Nil, err
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims.UserID, nil
	}

	return uuid.Nil, jwt.ErrSignatureInvalid
}

// GetUserID 从上下文获取用户 ID
func GetUserID(c *gin.Context) (uuid.UUID, bool) {
	userID, exists := c.Get("user_id")
	if !exists {
		return uuid.Nil, false
	}
	return userID.(uuid.UUID), true
}
