package main

import (
	"context"
	"fmt"
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
			searchReader := diagnosisLogReader(reader)
			resp, err := streamSearchWithReader(reqCtx, searchReader, req, emit)
			if err != nil {
				emit(LogStreamEvent{Type: "error", TraceID: req.TraceID, Error: err.Error()})
				return
			}
			codeEvidence := runDiagnosisCodeEvidenceStep(reqCtx, req, resp.Raw, cfg.Analyzer, emit)
			dbResult := runDiagnosisSQLStep(reqCtx, req, resp.Raw, codeEvidence, cfg.Analyzer.CodeRepoPath, cfg.SQL, sqlExecutor, emit)
			runDiagnosisAnalysisStep(reqCtx, reader, req, resp, codeEvidence, cfg.Analyzer, dbResult, emit)
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

func diagnosisLogReader(reader LogReader) LogReader {
	if analyzing, ok := reader.(*AnalyzingLogReader); ok && analyzing != nil && analyzing.base != nil {
		return analyzing.base
	}
	return reader
}

func runDiagnosisCodeEvidenceStep(ctx context.Context, req LogSearchRequest, raw map[string]interface{}, analyzerConf AnalyzerConfig, emit LogStreamEmitter) []CodeEvidence {
	codeRepoPath := codeRepoPathForRequest(req, analyzerConf.CodeRepoPath)
	emit(LogStreamEvent{Type: "analysis_progress", TraceID: req.TraceID, Message: "开始检索代码链路线索"})
	slog.Info("diagnosis code evidence search started", "trace_id", req.TraceID, "repo", codeRepoPath)
	codeEvidence := searchCodeEvidence(ctx, codeRepoPath, req.Service, req, raw, analyzerConf.CodeMaxLines)
	emit(LogStreamEvent{Type: "code_evidence", TraceID: req.TraceID, Message: fmt.Sprintf("代码线索检索完成，共 %d 条", len(codeEvidence)), CodeEvidence: codeEvidence})
	slog.Info("diagnosis code evidence search completed", "trace_id", req.TraceID, "count", len(codeEvidence))
	return codeEvidence
}

func runDiagnosisSQLStep(ctx context.Context, req LogSearchRequest, raw map[string]interface{}, codeEvidence []CodeEvidence, fallbackRepoPath string, sqlConf SQLConfig, sqlExecutor *SQLExecutor, emit LogStreamEmitter) *SQLQueryResponse {
	if !sqlConf.Enable {
		emit(LogStreamEvent{Type: "db_query_result", TraceID: req.TraceID, Message: "SQL 助手未启用", Error: "SQL 助手未启用"})
		return nil
	}
	queryReq := sqlQueryRequestForDiagnosisWithCodeEvidence(req, raw, codeEvidence, fallbackRepoPath)
	if strings.TrimSpace(queryReq.SQL) == "" && strings.TrimSpace(queryReq.Table) == "" && strings.TrimSpace(queryReq.CodeRepoPath) == "" {
		emit(LogStreamEvent{Type: "db_query_result", TraceID: req.TraceID, Message: "未提供 SQL、sql_table 或代码目录，跳过数据库查询"})
		return nil
	}
	emit(LogStreamEvent{Type: "db_schema_progress", TraceID: req.TraceID, Message: "开始执行只读 SQL 查询"})
	result, err := queryDiagnosisSQL(ctx, projectForRequest(req), req.Service, sqlConf, sqlExecutor, queryReq)
	if err != nil {
		emit(LogStreamEvent{Type: "db_query_result", TraceID: req.TraceID, Error: err.Error()})
		return nil
	}
	emit(LogStreamEvent{Type: "db_query_result", TraceID: req.TraceID, Message: "数据库查询完成", DBResult: result})
	return result
}

func runDiagnosisAnalysisStep(ctx context.Context, reader LogReader, req LogSearchRequest, resp *LogSearchResponse, codeEvidence []CodeEvidence, analyzerConf AnalyzerConfig, dbResult *SQLQueryResponse, emit LogStreamEmitter) {
	if resp == nil || req.ResolveOnly {
		return
	}
	analyzing, ok := reader.(*AnalyzingLogReader)
	if !ok || analyzing == nil || analyzing.analyzer == nil {
		return
	}

	input := AnalysisInput{
		Question:     req.Question,
		LogText:      logTextForAnalysis(resp),
		CodeEvidence: codeEvidence,
		DBText:       dbResultText(dbResult),
	}
	emit(LogStreamEvent{Type: "analysis_progress", TraceID: req.TraceID, Message: "开始结合日志、代码和数据库结果生成定位结论"})
	slog.Info("diagnosis analyzer stream started", "trace_id", req.TraceID, "db_result", dbResult != nil)
	var (
		analysis *AnalysisResult
		err      error
	)
	if streaming, ok := analyzing.analyzer.(StreamingAnalyzer); ok {
		analysis, err = streaming.AnalyzeStream(ctx, input, func(delta string) {
			if strings.TrimSpace(delta) == "" {
				return
			}
			emit(LogStreamEvent{Type: "analysis_delta", TraceID: req.TraceID, Delta: delta})
		})
	} else {
		analysis, err = analyzing.analyzer.Analyze(ctx, input)
	}
	if err != nil {
		slog.Error("diagnosis analyzer stream failed", "trace_id", req.TraceID, "err", err)
		resp.Analysis = &AnalysisResult{Error: err.Error(), CodeEvidence: codeEvidence}
		emit(LogStreamEvent{Type: "analysis_result", TraceID: req.TraceID, Analysis: resp.Analysis})
		return
	}
	if analysis != nil {
		analysis.CodeEvidence = codeEvidence
	}
	resp.Analysis = analysis
	emit(LogStreamEvent{Type: "analysis_result", TraceID: req.TraceID, Message: "智能分析完成", Analysis: analysis})
	slog.Info("diagnosis analyzer stream completed", "trace_id", req.TraceID)
}

func dbResultText(result *SQLQueryResponse) string {
	if result == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("sql: ")
	b.WriteString(result.SQL)
	b.WriteByte('\n')
	b.WriteString(fmt.Sprintf("row_count: %d\n", result.RowCount))
	if len(result.Columns) > 0 {
		b.WriteString("columns: ")
		b.WriteString(strings.Join(result.Columns, ", "))
		b.WriteByte('\n')
	}
	for i, row := range result.Rows {
		if i >= 10 {
			break
		}
		b.WriteString(fmt.Sprintf("row %d: ", i+1))
		parts := make([]string, 0, len(row))
		for _, column := range result.Columns {
			parts = append(parts, fmt.Sprintf("%s=%v", column, row[column]))
		}
		b.WriteString(strings.Join(parts, " "))
		b.WriteByte('\n')
	}
	return b.String()
}

func sqlQueryRequestForDiagnosis(req LogSearchRequest, raw map[string]interface{}, fallbackRepoPath string) SQLQueryRequest {
	return sqlQueryRequestForDiagnosisWithCodeEvidence(req, raw, nil, fallbackRepoPath)
}

func sqlQueryRequestForDiagnosisWithCodeEvidence(req LogSearchRequest, raw map[string]interface{}, codeEvidence []CodeEvidence, fallbackRepoPath string) SQLQueryRequest {
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
		Description:  diagnosisSQLDescription(req.Question, raw, codeEvidence),
		CodeRepoPath: codeRepoPathForRequest(req, fallbackRepoPath),
		Columns:      req.SQLColumns,
		Filters:      filters,
		Limit:        req.SQLLimit,
	}
}

func diagnosisSQLDescription(question string, raw map[string]interface{}, codeEvidence []CodeEvidence) string {
	parts := []string{strings.TrimSpace(question)}
	if raw != nil {
		lines := strings.Join(extractLogLines(raw), "\n")
		for _, finding := range deterministicLogFindings(lines) {
			parts = append(parts, finding)
		}
	}
	if text := codeEvidenceText(codeEvidence); strings.TrimSpace(text) != "" {
		parts = append(parts, text)
	}
	return strings.Join(uniqueNonEmptyStrings(parts), "\n")
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
