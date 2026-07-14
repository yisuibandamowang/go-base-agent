package context

import (
	stdctx "context"
)

type ctxKey int

const (
	keyTraceID ctxKey = iota
	keyUser
	keyTenant
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
