package service

import (
	"context"
	"fmt"
	"time"

	"go-base-agent/internal/biz/user/repo"
	"go-base-agent/internal/framework/config"
	framework "go-base-agent/internal/framework/context"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// AuthService 认证服务。
type AuthService struct {
	repo  *repo.UserRepo
	token string
	ttl   time.Duration
	key   []byte
}

// NewAuthService 创建 AuthService。
func NewAuthService(userRepo *repo.UserRepo, cfg config.AuthConfig) *AuthService {
	return &AuthService{
		repo:  userRepo,
		token: cfg.TokenName,
		ttl:   time.Duration(cfg.TimeoutSeconds) * time.Second,
		key:   []byte(cfg.JWTSecret),
	}
}

type jwtClaims struct {
	UserID   string `json:"userId"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// Login 验证用户名密码并签发 JWT token。
func (s *AuthService) Login(ctx context.Context, username, password string) (string, error) {
	user, err := s.repo.FindByUsername(ctx, username)
	if err != nil {
		return "", fmt.Errorf("用户名或密码错误")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		// 开发阶段：bcrypt 验证失败时，回退到明文比对
		if user.Password != password {
			return "", fmt.Errorf("用户名或密码错误")
		}
	}
	claims := jwtClaims{
		UserID:   user.ID,
		Username: user.Username,
		Role:     user.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(s.ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString(s.key)
	if err != nil {
		return "", fmt.Errorf("签发token失败: %w", err)
	}
	return tokenStr, nil
}

// ParseToken 解析并验证 JWT token，返回用户信息。
func (s *AuthService) ParseToken(tokenStr string) (*framework.LoginUser, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &jwtClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("不支持的签名算法: %v", t.Header["alg"])
		}
		return s.key, nil
	})
	if err != nil {
		return nil, fmt.Errorf("token解析失败: %w", err)
	}
	claims, ok := token.Claims.(*jwtClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("token无效")
	}
	return &framework.LoginUser{
		UserID:   claims.UserID,
		Username: claims.Username,
		Role:     claims.Role,
	}, nil
}

// TokenName 返回 token 在 HTTP header 中的键名。
func (s *AuthService) TokenName() string {
	return s.token
}
