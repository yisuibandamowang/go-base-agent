package context

import (
	stdctx "context"
)

type ctxKey int

const (
	keyTraceID ctxKey = iota
	keyUser
	keyTenant
	keyCodeRepoPath
)

func WithTraceID(ctx stdctx.Context, traceID string) stdctx.Context {
	return stdctx.WithValue(ctx, keyTraceID, traceID)
}

func TraceID(ctx stdctx.Context) string {
	v, _ := ctx.Value(keyTraceID).(string)
	return v
}

func WithUser(ctx stdctx.Context, user *LoginUser) stdctx.Context {
	return stdctx.WithValue(ctx, keyUser, user)
}

func User(ctx stdctx.Context) *LoginUser {
	v, _ := ctx.Value(keyUser).(*LoginUser)
	return v
}

func MustUser(ctx stdctx.Context) *LoginUser {
	user := User(ctx)
	if user == nil {
		return nil
	}
	return user
}

func ClearUser(ctx stdctx.Context) stdctx.Context {
	return stdctx.WithValue(ctx, keyUser, nil)
}

func HasUser(ctx stdctx.Context) bool {
	return User(ctx) != nil
}

// WithCodeRepoPath 将代码仓库路径写入上下文。
func WithCodeRepoPath(ctx stdctx.Context, repoPath string) stdctx.Context {
	return stdctx.WithValue(ctx, keyCodeRepoPath, repoPath)
}

// CodeRepoPath 从上下文读取代码仓库路径。
func CodeRepoPath(ctx stdctx.Context) string {
	v, _ := ctx.Value(keyCodeRepoPath).(string)
	return v
}
