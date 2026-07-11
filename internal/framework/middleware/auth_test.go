package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestExtractTokenFromConfiguredQueryName(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/rag/v3/chat?Authorization=Bearer%20abc123", nil)

	token := extractToken(c, "Authorization")
	if token != "abc123" {
		t.Fatalf("expected token from configured query name, got %q", token)
	}
}
