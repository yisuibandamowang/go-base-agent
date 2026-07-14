package context

import stdctx "context"

// TenantContext 保存请求级租户信息。
type TenantContext struct {
	TenantID string `json:"tenantId"`
	Domain   string `json:"domain"`
}

// WithTenant 将租户信息写入上下文。
func WithTenant(ctx stdctx.Context, tenant *TenantContext) stdctx.Context {
	return stdctx.WithValue(ctx, keyTenant, tenant)
}

// Tenant 从上下文读取租户信息。
func Tenant(ctx stdctx.Context) *TenantContext {
	v, _ := ctx.Value(keyTenant).(*TenantContext)
	return v
}

// MustTenant 返回当前租户信息，没有则返回 nil。
func MustTenant(ctx stdctx.Context) *TenantContext {
	tenant := Tenant(ctx)
	if tenant == nil {
		return nil
	}
	return tenant
}

// ClearTenant 清除上下文中的租户信息。
func ClearTenant(ctx stdctx.Context) stdctx.Context {
	return stdctx.WithValue(ctx, keyTenant, nil)
}

// HasTenant 判断上下文中是否存在租户信息。
func HasTenant(ctx stdctx.Context) bool {
	return Tenant(ctx) != nil
}
