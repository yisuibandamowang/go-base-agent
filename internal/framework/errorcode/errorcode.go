package errorcode

type ErrorCode struct {
	Code    string
	Message string
}

var (
	CliErr                   = ErrorCode{"A000001", "用户端错误"}
	UserRegisterErr          = ErrorCode{"A000100", "用户注册错误"}
	UserNameVerifyErr        = ErrorCode{"A000110", "用户名校验失败"}
	UserNameExistErr         = ErrorCode{"A000111", "用户名已存在"}
	UserNameSensitiveErr     = ErrorCode{"A000112", "用户名包含敏感词"}
	UserNameSpecialCharErr   = ErrorCode{"A000113", "用户名包含特殊字符"}
	PasswordVerifyErr        = ErrorCode{"A000120", "密码校验失败"}
	PasswordShortErr         = ErrorCode{"A000121", "密码长度不够"}
	PhoneVerifyErr           = ErrorCode{"A000151", "手机格式校验失败"}
	IdempotentTokenNullErr   = ErrorCode{"A000200", "幂等Token为空"}
	IdempotentTokenUsedErr   = ErrorCode{"A000201", "幂等Token已被使用或失效"}
	SearchAmountExceedsLimit = ErrorCode{"A000300", "查询数据量超过最大限制"}
	ServiceErr               = ErrorCode{"B000001", "系统执行出错"}
	ServiceTimeoutErr        = ErrorCode{"B000100", "系统执行超时"}
	RemoteErr                = ErrorCode{"C000001", "调用第三方服务出错"}
)
