package middleware

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"go-base-agent/internal/framework/convention"

	"github.com/gin-gonic/gin"
)

const demoModeRejectMessage = "体验环境仅支持查询操作"

// DemoMode creates a read-only middleware for public demo environments.
func DemoMode(enabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !enabled || strings.EqualFold(c.Request.Method, http.MethodOptions) {
			c.Next()
			return
		}
		if isDemoModeSSEPath(c.Request.URL.Path) {
			writeDemoModeSSEReject(c)
			return
		}
		if strings.EqualFold(c.Request.Method, http.MethodGet) {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusOK, convention.Failure("A000001", demoModeRejectMessage))
	}
}

func isDemoModeSSEPath(path string) bool {
	return path == "/rag/v3/chat" || strings.HasSuffix(path, "/rag/v3/chat")
}

func writeDemoModeSSEReject(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream;charset=UTF-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	payload, _ := json.Marshal(gin.H{
		"type":    "response",
		"content": demoModeRejectMessage,
	})
	_, _ = fmt.Fprintf(c.Writer, "event: reject\ndata: %s\n\n", payload)
	_, _ = fmt.Fprint(c.Writer, "event: done\ndata: \"[DONE]\"\n\n")
	c.Abort()
}
