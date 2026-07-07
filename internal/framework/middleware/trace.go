package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	appctx "github.com/nageoffer/ragent-go/internal/framework/context"
)

func TraceID() gin.HandlerFunc {
	return func(c *gin.Context) {
		traceID := c.GetHeader("X-Trace-Id")
		if traceID == "" {
			traceID = uuid.New().String()
		}
		c.Header("X-Trace-Id", traceID)
		c.Request = c.Request.WithContext(appctx.WithTraceID(c.Request.Context(), traceID))
		c.Next()
	}
}
