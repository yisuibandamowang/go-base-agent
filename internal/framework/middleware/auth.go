package middleware

import (
	"net/http"
	"strings"

	framework "go-base-agent/internal/framework/context"

	"github.com/gin-gonic/gin"
)

const keyLoginUser = "loginUser"

// TokenParser 定义 token 解析接口，由 auth service 实现。
type TokenParser interface {
	ParseToken(tokenStr string) (*framework.LoginUser, error)
	TokenName() string
}

// Auth 创建 JWT 认证中间件。
// 从 Authorization header 提取 Bearer token，解析后注入 LoginUser 到 context。
func Auth(parser TokenParser) gin.HandlerFunc {
	tokenName := parser.TokenName()

	return func(c *gin.Context) {
		token := extractToken(c, tokenName)
		if token == "" {
			c.Next()
			return
		}

		user, err := parser.ParseToken(token)
		if err != nil {
			c.Next()
			return
		}

		c.Set(keyLoginUser, user)
		c.Request = c.Request.WithContext(
			framework.WithUser(c.Request.Context(), user),
		)
		c.Next()
	}
}

// GetLoginUser 从 gin.Context 获取当前登录用户。
func GetLoginUser(c *gin.Context) *framework.LoginUser {
	v, _ := c.Get(keyLoginUser)
	if v == nil {
		return nil
	}
	return v.(*framework.LoginUser)
}

// RequireAuth 创建需要登录的中间件，未登录返回 401。
func RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		user := GetLoginUser(c)
		if user == nil {
			c.AbortWithStatusJSON(http.StatusOK, gin.H{
				"code":    "A000001",
				"message": "请先登录",
			})
			return
		}
		c.Next()
	}
}

func extractToken(c *gin.Context, tokenName string) string {
	token := c.GetHeader(tokenName)
	if token == "" && tokenName != "" {
		token = c.Query(tokenName)
	}
	if token == "" {
		token = c.Query("token")
	}
	if token == "" {
		token = c.Query("access_token")
	}
	if token == "" {
		token = c.Query("satoken")
	}
	if token == "" {
		return ""
	}
	if strings.HasPrefix(token, "Bearer ") {
		return token[7:]
	}
	return token
}
