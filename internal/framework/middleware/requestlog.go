package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	appctx "github.com/nageoffer/ragent-go/internal/framework/context"
)

func RequestLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery

		c.Next()

		latency := time.Since(start)
		attrs := []any{
			"method", c.Request.Method,
			"path", path,
			"status", c.Writer.Status(),
			"latency_ms", latency.Milliseconds(),
			"client_ip", c.ClientIP(),
		}
		traceID := appctx.TraceID(c.Request.Context())
		if traceID != "" {
			attrs = append(attrs, "trace_id", traceID)
		}
		if query != "" {
			attrs = append(attrs, "query", query)
		}

		errs := c.Errors.ByType(gin.ErrorTypePrivate)
		if len(errs) > 0 {
			attrs = append(attrs, "errors", errs.String())
		}

		slog.Info("request", attrs...)
	}
}
