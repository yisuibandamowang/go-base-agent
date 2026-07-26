package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-base-agent/internal/biz/user/model"
	"go-base-agent/internal/biz/user/repo"
	"go-base-agent/internal/biz/user/service"
	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/framework/middleware"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLogoutRevokesCurrentToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	if err := gdb.Create(&model.User{Username: "tester", Password: "pwd", Role: "user"}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	tokenStore := newMemoryTokenStore()
	authSvc := service.NewAuthService(repo.NewUserRepo(gdb), config.AuthConfig{
		TokenName:      "Authorization",
		TimeoutSeconds: 3600,
		JWTSecret:      "test-secret",
	}, tokenStore)
	token, err := authSvc.Login(t.Context(), "tester", "pwd")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	h := NewAuthHandler(authSvc)
	r := gin.New()
	r.Use(middleware.Auth(authSvc))
	r.POST("/api/ragent/auth/logout", h.Logout)
	r.GET("/api/ragent/user/me", h.CurrentUser)

	logoutW := httptest.NewRecorder()
	logoutReq := httptest.NewRequest(http.MethodPost, "/api/ragent/auth/logout", nil)
	logoutReq.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(logoutW, logoutReq)
	if logoutW.Code != http.StatusOK {
		t.Fatalf("expected logout 200, got %d", logoutW.Code)
	}

	meW := httptest.NewRecorder()
	meReq := httptest.NewRequest(http.MethodGet, "/api/ragent/user/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(meW, meReq)

	var resp map[string]interface{}
	if err := json.Unmarshal(meW.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode current user response: %v", err)
	}
	if resp["code"] == "0" {
		t.Fatalf("expected revoked token to be rejected, got %s", meW.Body.String())
	}
}

func TestLoginAndCurrentUserReturnJavaStyleUserFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	if err := gdb.Create(&model.User{Username: "tester", Password: "pwd", Role: "user", Avatar: "https://example.com/avatar.png"}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	authSvc := service.NewAuthService(repo.NewUserRepo(gdb), config.AuthConfig{
		TokenName:      "Authorization",
		TimeoutSeconds: 3600,
		JWTSecret:      "test-secret",
	})
	h := NewAuthHandler(authSvc)
	r := gin.New()
	r.Use(middleware.Auth(authSvc))
	r.POST("/api/ragent/auth/login", h.Login)
	r.GET("/api/ragent/user/me", h.CurrentUser)

	loginW := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/api/ragent/auth/login", strings.NewReader(`{"username":"tester","password":"pwd"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(loginW, loginReq)

	var loginResp map[string]interface{}
	if err := json.Unmarshal(loginW.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	data := loginResp["data"].(map[string]interface{})
	token, _ := data["token"].(string)
	if token == "" {
		t.Fatalf("expected login token, got %s", loginW.Body.String())
	}
	if data["userId"] == "" || data["role"] != "user" || data["avatar"] != "https://example.com/avatar.png" {
		t.Fatalf("expected Java style login fields, got %s", loginW.Body.String())
	}

	meW := httptest.NewRecorder()
	meReq := httptest.NewRequest(http.MethodGet, "/api/ragent/user/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(meW, meReq)

	var meResp map[string]interface{}
	if err := json.Unmarshal(meW.Body.Bytes(), &meResp); err != nil {
		t.Fatalf("decode current user response: %v", err)
	}
	meData := meResp["data"].(map[string]interface{})
	if meData["userId"] != data["userId"] || meData["username"] != "tester" || meData["role"] != "user" || meData["avatar"] != "https://example.com/avatar.png" {
		t.Fatalf("expected Java style current user fields, got %s", meW.Body.String())
	}
}

func TestChangePasswordAcceptsJavaCurrentPasswordField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	if err := gdb.Create(&model.User{Username: "tester", Password: "pwd", Role: "user"}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	authSvc := service.NewAuthService(repo.NewUserRepo(gdb), config.AuthConfig{
		TokenName:      "Authorization",
		TimeoutSeconds: 3600,
		JWTSecret:      "test-secret",
	})
	token, err := authSvc.Login(t.Context(), "tester", "pwd")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	h := NewAuthHandler(authSvc)
	r := gin.New()
	r.Use(middleware.Auth(authSvc))
	r.PUT("/api/ragent/user/password", h.ChangePassword)

	changeW := httptest.NewRecorder()
	changeReq := httptest.NewRequest(http.MethodPut, "/api/ragent/user/password", bytes.NewBufferString(`{"currentPassword":"pwd","newPassword":"new-pwd"}`))
	changeReq.Header.Set("Content-Type", "application/json")
	changeReq.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(changeW, changeReq)

	var resp map[string]interface{}
	if err := json.Unmarshal(changeW.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode change password response: %v", err)
	}
	if resp["code"] != "0" {
		t.Fatalf("expected password change success, got %s", changeW.Body.String())
	}
	if _, err := authSvc.Login(t.Context(), "tester", "new-pwd"); err != nil {
		t.Fatalf("expected new password to work: %v", err)
	}
}

type memoryTokenStore struct {
	values map[string]time.Time
}

func newMemoryTokenStore() *memoryTokenStore {
	return &memoryTokenStore{values: make(map[string]time.Time)}
}

func (s *memoryTokenStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	s.values[key] = time.Now().Add(ttl)
	return nil
}

func (s *memoryTokenStore) Exists(ctx context.Context, key string) (bool, error) {
	expireAt, ok := s.values[key]
	if !ok {
		return false, nil
	}
	return time.Now().Before(expireAt), nil
}
