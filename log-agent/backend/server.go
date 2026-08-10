package main

import (
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func newRouter(cfg AppConfig, reader LogReader) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery(), traceMiddleware(), corsMiddleware())

	router.GET("/api/log-agent/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "trace_id": traceID(c)})
	})
	router.GET("/api/log-agent/options", func(c *gin.Context) {
		c.JSON(http.StatusOK, optionsResponse(cfg.LogReader))
	})
	router.POST("/api/log-agent/logs/search", func(c *gin.Context) {
		var req LogSearchRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求 JSON 不合法: " + err.Error(), "trace_id": traceID(c)})
			return
		}
		req.TraceID = traceID(c)
		resp, err := reader.Search(c.Request.Context(), req)
		if err != nil {
			slog.Error("log search failed", "trace_id", req.TraceID, "err", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "trace_id": req.TraceID})
			return
		}
		c.JSON(http.StatusOK, resp)
	})
	router.POST("/api/log-agent/logs/search/stream", func(c *gin.Context) {
		var req LogSearchRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求 JSON 不合法: " + err.Error(), "trace_id": traceID(c)})
			return
		}
		req.TraceID = traceID(c)
		reqCtx := c.Request.Context()
		events := make(chan LogStreamEvent, 16)
		emit := func(event LogStreamEvent) bool {
			if event.TraceID == "" {
				event.TraceID = req.TraceID
			}
			select {
			case <-reqCtx.Done():
				return false
			case events <- event:
				return true
			}
		}

		go func() {
			defer close(events)
			if _, err := streamSearchWithReader(reqCtx, reader, req, emit); err != nil {
				emit(LogStreamEvent{Type: "error", TraceID: req.TraceID, Error: err.Error()})
				return
			}
			emit(LogStreamEvent{Type: "done", TraceID: req.TraceID, Message: "完成"})
		}()

		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Stream(func(w io.Writer) bool {
			_ = w
			event, ok := <-events
			if !ok {
				return false
			}
			c.SSEvent(event.Type, event)
			return true
		})
	})
	if cfg.FrontendDir != "" {
		router.StaticFile("/", cfg.FrontendDir+"/index.html")
		router.Static("/assets", cfg.FrontendDir+"/assets")
	}
	return router
}

func traceMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		traceID := strings.TrimSpace(c.GetHeader("X-Trace-Id"))
		if traceID == "" {
			traceID = uuid.NewString()
		}
		c.Set("trace_id", traceID)
		c.Writer.Header().Set("X-Trace-Id", traceID)
		c.Next()
	}
}

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Trace-Id")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func traceID(c *gin.Context) string {
	if value, ok := c.Get("trace_id"); ok {
		if text, ok := value.(string); ok {
			return text
		}
	}
	return ""
}
