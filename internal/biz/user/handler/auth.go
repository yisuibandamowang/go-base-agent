package handler

import (
	"net/http"

	"go-base-agent/internal/biz/user/dto"
	"go-base-agent/internal/biz/user/service"
	"go-base-agent/internal/framework/convention"
	"go-base-agent/internal/framework/middleware"

	"github.com/gin-gonic/gin"
)

// AuthHandler 认证 HTTP 处理层。
type AuthHandler struct {
	svc *service.AuthService
}

// NewAuthHandler 创建 AuthHandler。
func NewAuthHandler(svc *service.AuthService) *AuthHandler {
	return &AuthHandler{svc: svc}
}

// Login POST /api/ragent/auth/login
func (h *AuthHandler) Login(c *gin.Context) {
	var req dto.LoginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	token, err := h.svc.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(&dto.LoginResp{Token: token}))
}

// Logout POST /api/ragent/auth/logout
func (h *AuthHandler) Logout(c *gin.Context) {
	if err := h.svc.Logout(c.Request.Context(), middleware.GetAuthToken(c)); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// CurrentUser GET /api/ragent/auth/current-user
func (h *AuthHandler) CurrentUser(c *gin.Context) {
	user := middleware.GetLoginUser(c)
	if user == nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "未登录"))
		return
	}
	resp := &dto.UserInfoResp{
		ID:       user.UserID,
		Username: user.Username,
		Role:     user.Role,
		Avatar:   user.Avatar,
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// ChangePassword PUT /api/ragent/user/password
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	user := middleware.GetLoginUser(c)
	if user == nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "未登录"))
		return
	}
	var req struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败"))
		return
	}
	if err := h.svc.ChangePassword(c.Request.Context(), user.UserID, req.OldPassword, req.NewPassword); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}
