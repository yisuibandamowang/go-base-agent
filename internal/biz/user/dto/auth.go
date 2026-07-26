package dto

// LoginReq 登录请求。
type LoginReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// LoginResp 登录响应。
type LoginResp struct {
	UserID string `json:"userId"`
	Role   string `json:"role"`
	Token  string `json:"token"`
	Avatar string `json:"avatar"`
}

// UserInfoResp 当前用户信息响应。
type UserInfoResp struct {
	UserID   string `json:"userId"`
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Avatar   string `json:"avatar"`
}
