package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	appctx "go-base-agent/internal/framework/context"

	"github.com/gin-gonic/gin"
)

func TestTenantInjectsRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(Tenant())
	r.GET("/tenant", func(c *gin.Context) {
		tenant := appctx.Tenant(c.Request.Context())
		if tenant == nil {
			t.Fatal("expected tenant in request context")
		}
		if tenant.TenantID != "tenant-1" {
			t.Fatalf("expected tenant id tenant-1, got %q", tenant.TenantID)
		}
		if tenant.Domain != "membership" {
			t.Fatalf("expected tenant domain membership, got %q", tenant.Domain)
		}
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tenant", nil)
	req.Header.Set("X-Tenant-Id", "tenant-1")
	req.Header.Set("X-Tenant-Domain", "membership")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestTenantIgnoresMissingHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(Tenant())
	r.GET("/tenant", func(c *gin.Context) {
		if tenant := appctx.Tenant(c.Request.Context()); tenant != nil {
			t.Fatalf("expected no tenant, got %+v", tenant)
		}
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tenant", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestTenantContextHelpers(t *testing.T) {
	ctx := context.Background()
	if appctx.HasTenant(ctx) {
		t.Fatal("expected empty context to have no tenant")
	}

	tenant := &appctx.TenantContext{TenantID: "tenant-1", Domain: "payment"}
	ctx = appctx.WithTenant(ctx, tenant)

	if !appctx.HasTenant(ctx) {
		t.Fatal("expected tenant to be present")
	}
	got := appctx.Tenant(ctx)
	if got == nil || got.TenantID != "tenant-1" || got.Domain != "payment" {
		t.Fatalf("unexpected tenant: %+v", got)
	}

	ctx = appctx.ClearTenant(ctx)
	if appctx.HasTenant(ctx) {
		t.Fatal("expected tenant to be cleared")
	}
}
