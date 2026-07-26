package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go-base-agent/internal/biz/user/repo"
	"go-base-agent/internal/framework/config"
	framework "go-base-agent/internal/framework/context"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// AuthService 认证服务。
type AuthService struct {
	repo      *repo.UserRepo
	token     string
	ttl       time.Duration
	key       []byte
	blacklist tokenBlacklist
}

type tokenBlacklist interface {
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	Exists(ctx context.Context, key string) (bool, error)
}

// NewAuthService 创建 AuthService。
func NewAuthService(userRepo *repo.UserRepo, cfg config.AuthConfig, blacklists ...tokenBlacklist) *AuthService {
	var blacklist tokenBlacklist
	if len(blacklists) > 0 {
		blacklist = blacklists[0]
	}
	return &AuthService{
		repo:      userRepo,
		token:     cfg.TokenName,
		ttl:       time.Duration(cfg.TimeoutSeconds) * time.Second,
		key:       []byte(cfg.JWTSecret),
		blacklist: blacklist,
	}
}

type jwtClaims struct {
	UserID   string `json:"userId"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Avatar   string `json:"avatar"`
	jwt.RegisteredClaims
}

const defaultAvatarURL = "https://avatars.githubusercontent.com/u/583231?v=4"

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
		Avatar:   resolveAvatar(user.Avatar),
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(s.ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        uuid.NewString(),
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
	return s.ParseTokenWithContext(context.Background(), tokenStr)
}

// ParseTokenWithContext 解析并验证 JWT token，使用请求上下文检查撤销状态。
func (s *AuthService) ParseTokenWithContext(ctx context.Context, tokenStr string) (*framework.LoginUser, error) {
	claims, err := s.parseClaims(tokenStr)
	if err != nil {
		return nil, err
	}
	if err := s.ensureTokenNotRevoked(ctx, claims); err != nil {
		return nil, err
	}
	return &framework.LoginUser{
		UserID:   claims.UserID,
		Username: claims.Username,
		Role:     claims.Role,
		Avatar:   resolveAvatar(claims.Avatar),
	}, nil
}

// Logout revokes the current JWT until its natural expiration.
func (s *AuthService) Logout(ctx context.Context, tokenStr string) error {
	if s == nil || s.blacklist == nil || strings.TrimSpace(tokenStr) == "" {
		return nil
	}
	claims, err := s.parseClaims(tokenStr)
	if err != nil || claims.ID == "" || claims.ExpiresAt == nil {
		return nil
	}
	ttl := time.Until(claims.ExpiresAt.Time)
	if ttl <= 0 {
		return nil
	}
	if err := s.blacklist.Set(ctx, revokedTokenKey(claims.ID), "1", ttl); err != nil {
		return fmt.Errorf("撤销token失败: %w", err)
	}
	return nil
}

func (s *AuthService) parseClaims(tokenStr string) (*jwtClaims, error) {
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
	return claims, nil
}

func (s *AuthService) ensureTokenNotRevoked(ctx context.Context, claims *jwtClaims) error {
	if s == nil || s.blacklist == nil || claims == nil || claims.ID == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	revoked, err := s.blacklist.Exists(ctx, revokedTokenKey(claims.ID))
	if err != nil {
		return fmt.Errorf("校验token撤销状态失败: %w", err)
	}
	if revoked {
		return fmt.Errorf("token已登出")
	}
	return nil
}

func revokedTokenKey(tokenID string) string {
	return "ragent:auth:revoked:" + tokenID
}

// TokenName 返回 token 在 HTTP header 中的键名。
func (s *AuthService) TokenName() string {
	return s.token
}

// ChangePassword verifies old password and updates to new password.
func (s *AuthService) ChangePassword(ctx context.Context, userID, oldPwd, newPwd string) error {
	oldPwd = strings.TrimSpace(oldPwd)
	newPwd = strings.TrimSpace(newPwd)
	if oldPwd == "" {
		return fmt.Errorf("当前密码不能为空")
	}
	if newPwd == "" {
		return fmt.Errorf("新密码不能为空")
	}
	user, err := s.repo.FindByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("用户不存在")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(oldPwd)); err != nil {
		if user.Password != oldPwd {
			return fmt.Errorf("当前密码不正确")
		}
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(newPwd), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("加密密码失败")
	}
	return s.repo.UpdatePassword(ctx, userID, string(hashed))
}

func resolveAvatar(avatar string) string {
	avatar = strings.TrimSpace(avatar)
	if avatar == "" {
		return defaultAvatarURL
	}
	return avatar
}
