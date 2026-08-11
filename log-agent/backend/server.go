package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func newRouter(cfg AppConfig, reader LogReader) *gin.Engine {
	return newRouterWithSQL(cfg, reader, nil)
}

func newRouterWithSQL(cfg AppConfig, reader LogReader, sqlExecutor *SQLExecutor) *gin.Engine {
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
	router.POST("/api/log-agent/sql/plan", func(c *gin.Context) {
		var req SQLQueryRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求 JSON 不合法: " + err.Error(), "trace_id": traceID(c)})
			return
		}
		req.TraceID = traceID(c)
		plan, err := planSQL(req, cfg.SQL)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "trace_id": traceID(c)})
			return
		}
		c.JSON(http.StatusOK, gin.H{"trace_id": traceID(c), "sql": plan.SQL, "args": plan.Args, "table_candidates": plan.TableCandidates})
	})
	router.POST("/api/log-agent/sql/query", func(c *gin.Context) {
		var req SQLQueryRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求 JSON 不合法: " + err.Error(), "trace_id": traceID(c)})
			return
		}
		req.TraceID = traceID(c)
		if sqlExecutor == nil || !sqlExecutor.Enabled() {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "SQL 助手未启用", "trace_id": traceID(c)})
			return
		}
		result, err := sqlExecutor.Query(c.Request.Context(), req)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "trace_id": traceID(c)})
			return
		}
		c.JSON(http.StatusOK, gin.H{"trace_id": traceID(c), "sql": result.SQL, "columns": result.Columns, "rows": result.Rows, "row_count": result.RowCount})
	})
	router.POST("/api/log-agent/diagnosis/search/stream", func(c *gin.Context) {
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
			resp, err := streamSearchWithReader(reqCtx, reader, req, emit)
			if err != nil {
				emit(LogStreamEvent{Type: "error", TraceID: req.TraceID, Error: err.Error()})
				return
			}
			runDiagnosisSQLStep(reqCtx, req, resp.Raw, cfg.Analyzer.CodeRepoPath, cfg.SQL, sqlExecutor, emit)
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
		router.GET("/", func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			c.File(cfg.FrontendDir + "/index.html")
		})
		assets := router.Group("/assets")
		assets.Use(noStoreMiddleware())
		assets.Static("/", cfg.FrontendDir+"/assets")
	}
	return router
}

func runDiagnosisSQLStep(ctx context.Context, req LogSearchRequest, raw map[string]interface{}, fallbackRepoPath string, sqlConf SQLConfig, sqlExecutor *SQLExecutor, emit LogStreamEmitter) {
	if !sqlConf.Enable {
		emit(LogStreamEvent{Type: "db_query_result", TraceID: req.TraceID, Message: "SQL 助手未启用", Error: "SQL 助手未启用"})
		return
	}
	queryReq := sqlQueryRequestForDiagnosis(req, raw, fallbackRepoPath)
	if strings.TrimSpace(queryReq.SQL) == "" && strings.TrimSpace(queryReq.Table) == "" && strings.TrimSpace(queryReq.CodeRepoPath) == "" {
		emit(LogStreamEvent{Type: "db_query_result", TraceID: req.TraceID, Message: "未提供 SQL、sql_table 或代码目录，跳过数据库查询"})
		return
	}
	emit(LogStreamEvent{Type: "db_schema_progress", TraceID: req.TraceID, Message: "开始执行只读 SQL 查询"})
	result, err := queryDiagnosisSQL(ctx, projectForRequest(req), req.Service, sqlConf, sqlExecutor, queryReq)
	if err != nil {
		emit(LogStreamEvent{Type: "db_query_result", TraceID: req.TraceID, Error: err.Error()})
		return
	}
	emit(LogStreamEvent{Type: "db_query_result", TraceID: req.TraceID, Message: "数据库查询完成", DBResult: result})
}

func sqlQueryRequestForDiagnosis(req LogSearchRequest, raw map[string]interface{}, fallbackRepoPath string) SQLQueryRequest {
	filters := req.SQLFilters
	if filters == nil {
		filters = map[string]interface{}{}
	} else {
		copied := make(map[string]interface{}, len(filters))
		for key, value := range filters {
			copied[key] = value
		}
		filters = copied
	}
	for _, keyword := range req.allKeywords() {
		field, value, ok := parseFieldValueKeyword(keyword)
		if !ok {
			continue
		}
		if _, exists := filters[field]; !exists {
			filters[field] = value
		}
	}
	if len(filters) == 0 {
		addDiagnosisSQLFilterFromPlainKeywords(filters, req)
	}
	if len(filters) == 0 {
		addDiagnosisSQLFilterFromLogs(filters, raw, req)
	}
	return SQLQueryRequest{
		TraceID:      req.TraceID,
		SQL:          req.SQL,
		Table:        req.SQLTable,
		Description:  req.Question,
		CodeRepoPath: codeRepoPathForRequest(req, fallbackRepoPath),
		Columns:      req.SQLColumns,
		Filters:      filters,
		Limit:        req.SQLLimit,
	}
}

func addDiagnosisSQLFilterFromLogs(filters map[string]interface{}, raw map[string]interface{}, req LogSearchRequest) {
	if raw == nil {
		return
	}
	preferred := preferredSQLIdentifierFields(req.Question)
	for _, fact := range extractLogFacts(strings.Join(extractLogLines(raw), "\n")) {
		for _, key := range preferred {
			if value := strings.TrimSpace(fact.Fields[key]); value != "" {
				filters[key] = value
				return
			}
		}
	}
}

func addDiagnosisSQLFilterFromPlainKeywords(filters map[string]interface{}, req LogSearchRequest) {
	question := strings.ToLower(req.Question)
	for _, keyword := range req.allKeywords() {
		if _, _, ok := parseFieldValueKeyword(keyword); ok {
			continue
		}
		keyword = strings.Trim(strings.TrimSpace(keyword), `"'`)
		if keyword == "" {
			continue
		}
		if strings.Contains(req.Question, "订单") {
			filters["order_id"] = keyword
			return
		}
		if strings.Contains(question, "kafka") || strings.Contains(req.Question, "事件") || strings.Contains(question, "event_id") {
			filters["event_id"] = keyword
			return
		}
	}
}

func preferredSQLIdentifierFields(question string) []string {
	questionLower := strings.ToLower(question)
	if strings.Contains(question, "订单") {
		return []string{"order_id", "order_no", "trade_no", "event_id", "qid", "qihoo_id", "user_id", "mid"}
	}
	if strings.Contains(questionLower, "kafka") || strings.Contains(question, "事件") || strings.Contains(questionLower, "event_id") {
		return []string{"event_id", "order_id", "order_no", "trade_no", "qid", "qihoo_id", "user_id", "mid"}
	}
	return []string{"order_id", "event_id", "order_no", "trade_no", "qid", "qihoo_id", "user_id", "mid"}
}

func noStoreMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
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
