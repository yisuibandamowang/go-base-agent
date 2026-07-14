package middleware

import (
	"strings"

	framework "go-base-agent/internal/framework/context"

	"github.com/gin-gonic/gin"
)

const keyTenantContext = "tenantContext"

// Tenant 将请求中的租户信息注入到 gin.Context 和 request context。
func Tenant() gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := firstNonEmpty(
			c.GetHeader("X-Tenant-Id"),
			c.Query("tenantId"),
			c.Query("tenant_id"),
			c.Query("tenant"),
			c.GetHeader("X-Tenant"),
		)
		domain := firstNonEmpty(
			c.GetHeader("X-Tenant-Domain"),
			c.Query("tenantDomain"),
			c.Query("tenant_domain"),
		)
		if tenantID == "" && domain == "" {
			c.Next()
			return
		}

		tenant := &framework.TenantContext{
			TenantID: tenantID,
			Domain:   domain,
		}
		c.Set(keyTenantContext, tenant)
		c.Request = c.Request.WithContext(
			framework.WithTenant(c.Request.Context(), tenant),
		)
		c.Next()
	}
}

// GetTenant 从 gin.Context 获取当前租户信息。
func GetTenant(c *gin.Context) *framework.TenantContext {
	v, _ := c.Get(keyTenantContext)
	if v == nil {
		return nil
	}
	return v.(*framework.TenantContext)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
