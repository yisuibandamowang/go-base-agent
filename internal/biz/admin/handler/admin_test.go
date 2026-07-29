package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	adminHandler "go-base-agent/internal/biz/admin/handler"
	adminModel "go-base-agent/internal/biz/admin/model"
	adminRepo "go-base-agent/internal/biz/admin/repo"
	adminService "go-base-agent/internal/biz/admin/service"
	conversationModel "go-base-agent/internal/biz/conversation/model"
	userModel "go-base-agent/internal/biz/user/model"
	frameworkctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/framework/middleware"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSampleQuestionsResponseShapes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&adminModel.SampleQuestion{}); err != nil {
		t.Fatalf("migrate sample questions: %v", err)
	}
	now := time.Now()
	records := []adminModel.SampleQuestion{
		{Title: "A", Description: "会员权益", Question: "如何开通会员？"},
		{Title: "B", Description: "普通", Question: "如何续费会员？"},
		{Title: "C", Description: "普通", Question: "如何查看积分？"},
		{Title: "D", Description: "普通", Question: "如何使用礼品卡？"},
	}
	for i := range records {
		if err := gdb.Create(&records[i]).Error; err != nil {
			t.Fatalf("seed sample question %d: %v", i, err)
		}
	}
	if err := gdb.Model(&adminModel.SampleQuestion{}).Where("id = ?", records[0].ID).
		Updates(map[string]any{"update_time": now.Add(-3 * time.Hour)}).Error; err != nil {
		t.Fatalf("update time a: %v", err)
	}
	if err := gdb.Model(&adminModel.SampleQuestion{}).Where("id = ?", records[1].ID).
		Updates(map[string]any{"update_time": now}).Error; err != nil {
		t.Fatalf("update time b: %v", err)
	}
	if err := gdb.Model(&adminModel.SampleQuestion{}).Where("id = ?", records[2].ID).
		Updates(map[string]any{"update_time": now.Add(-1 * time.Hour)}).Error; err != nil {
		t.Fatalf("update time c: %v", err)
	}
	if err := gdb.Model(&adminModel.SampleQuestion{}).Where("id = ?", records[3].ID).
		Updates(map[string]any{"update_time": now.Add(-2 * time.Hour)}).Error; err != nil {
		t.Fatalf("update time d: %v", err)
	}

	adminRepoObj := adminRepo.NewAdminRepo(gdb)
	sampleQRepo := adminRepo.NewSampleQuestionRepo(gdb)
	svc := adminService.NewAdminService(adminRepoObj, sampleQRepo, gdb)
	h := adminHandler.NewAdminHandler(svc)

	r := gin.New()
	r.GET("/api/ragent/rag/sample-questions", h.ListRAGSampleQuestions)
	r.GET("/api/ragent/sample-questions", h.ListSampleQuestions)
	r.GET("/api/ragent/sample-questions/:id", h.GetSampleQuestion)
	r.GET("/api/ragent/admin/sample-questions", h.ListSampleQuestions)

	t.Run("sample questions page uses current and keyword", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/ragent/sample-questions?current=1&size=1&keyword=会员", nil)
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		data, ok := resp["data"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected page object, got %T: %s", resp["data"], w.Body.String())
		}
		if data["current"] != float64(1) || data["size"] != float64(1) || data["total"] != float64(2) {
			t.Fatalf("unexpected page metadata: %s", w.Body.String())
		}
		recordsData, ok := data["records"].([]interface{})
		if !ok || len(recordsData) != 1 {
			t.Fatalf("expected one record on first page, got %s", w.Body.String())
		}
		first, ok := recordsData[0].(map[string]interface{})
		if !ok || first["id"] != records[1].ID {
			t.Fatalf("expected newest matching record first, got %s", w.Body.String())
		}
	})

	t.Run("rag sample questions returns default three items", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/ragent/rag/sample-questions", nil)
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		data, ok := resp["data"].([]interface{})
		if !ok {
			t.Fatalf("expected array data, got %T: %s", resp["data"], w.Body.String())
		}
		if len(data) != 3 {
			t.Fatalf("expected 3 random sample questions, got %d: %s", len(data), w.Body.String())
		}
	})

	t.Run("sample question detail returns original record", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/ragent/sample-questions/"+records[0].ID, nil)
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		data, ok := resp["data"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected detail object, got %T: %s", resp["data"], w.Body.String())
		}
		if data["id"] != records[0].ID || data["question"] != records[0].Question {
			t.Fatalf("unexpected detail response: %s", w.Body.String())
		}
	})
}

func TestUserManagementRoutesRequireAdminRole(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&userModel.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	seeded := &userModel.User{Username: "target", Password: "pwd", Role: "user"}
	if err := gdb.Create(seeded).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	svc := adminService.NewAdminService(adminRepo.NewAdminRepo(gdb), adminRepo.NewSampleQuestionRepo(gdb), gdb)
	h := adminHandler.NewAdminHandler(svc)
	r := gin.New()
	api := r.Group("/api/ragent", middleware.Auth(staticTokenParser{
		users: map[string]*frameworkctx.LoginUser{
			"user-token": {UserID: "user-1", Username: "normal", Role: "user"},
		},
	}))
	api.GET("/users", h.ListUsers)
	api.POST("/users", h.CreateUser)
	api.PUT("/users/:id", h.UpdateUser)
	api.DELETE("/users/:id", h.DeleteUser)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "list", method: http.MethodGet, path: "/api/ragent/users"},
		{name: "create", method: http.MethodPost, path: "/api/ragent/users", body: `{"username":"created","password":"pwd"}`},
		{name: "update", method: http.MethodPut, path: "/api/ragent/users/" + seeded.ID, body: `{"role":"admin"}`},
		{name: "delete", method: http.MethodDelete, path: "/api/ragent/users/" + seeded.ID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer user-token")
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}
			var resp map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp["code"] == "0" || !strings.Contains(w.Body.String(), "无权限") {
				t.Fatalf("expected non-admin request to be rejected, got %s", w.Body.String())
			}
		})
	}
}

func TestUserListUsesJavaStyleCurrentKeywordAndUpdateTimeOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&userModel.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	olderAdmin := &userModel.User{Username: "older-admin", Password: "pwd", Role: "user"}
	newerRoleAdmin := &userModel.User{Username: "alice", Password: "pwd", Role: "admin"}
	ignoredUser := &userModel.User{Username: "bob", Password: "pwd", Role: "user"}
	for _, u := range []*userModel.User{olderAdmin, newerRoleAdmin, ignoredUser} {
		if err := gdb.Create(u).Error; err != nil {
			t.Fatalf("seed user %s: %v", u.Username, err)
		}
	}
	now := time.Now()
	if err := gdb.Model(&userModel.User{}).Where("id = ?", olderAdmin.ID).
		Updates(map[string]any{"create_time": now, "update_time": now.Add(-2 * time.Hour)}).Error; err != nil {
		t.Fatalf("update older admin time: %v", err)
	}
	if err := gdb.Model(&userModel.User{}).Where("id = ?", newerRoleAdmin.ID).
		Updates(map[string]any{"create_time": now.Add(-3 * time.Hour), "update_time": now}).Error; err != nil {
		t.Fatalf("update newer role admin time: %v", err)
	}

	svc := adminService.NewAdminService(adminRepo.NewAdminRepo(gdb), adminRepo.NewSampleQuestionRepo(gdb), gdb)
	h := adminHandler.NewAdminHandler(svc)
	r := gin.New()
	api := r.Group("/api/ragent", middleware.Auth(staticTokenParser{
		users: map[string]*frameworkctx.LoginUser{
			"admin-token": {UserID: "admin-1", Username: "admin", Role: "admin"},
		},
	}))
	api.GET("/users", h.ListUsers)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/users?current=1&size=1&keyword=admin", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected page data, got %T: %s", resp["data"], w.Body.String())
	}
	if data["current"] != float64(1) || data["size"] != float64(1) || data["total"] != float64(2) {
		t.Fatalf("unexpected page metadata: %s", w.Body.String())
	}
	records, ok := data["records"].([]interface{})
	if !ok || len(records) != 1 {
		t.Fatalf("expected one record, got %s", w.Body.String())
	}
	first, ok := records[0].(map[string]interface{})
	if !ok || first["id"] != newerRoleAdmin.ID {
		t.Fatalf("expected newest update_time matching user first, got %s", w.Body.String())
	}
}

type staticTokenParser struct {
	users map[string]*frameworkctx.LoginUser
}

func (p staticTokenParser) ParseToken(token string) (*frameworkctx.LoginUser, error) {
	if user, ok := p.users[token]; ok {
		return user, nil
	}
	return nil, nil
}

func (p staticTokenParser) TokenName() string {
	return "Authorization"
}

func TestDashboardTrendsPassesQueryParameters(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Message{}); err != nil {
		t.Fatalf("migrate messages: %v", err)
	}
	messageTime := time.Now().Add(-30 * time.Minute).Truncate(time.Hour).Add(10 * time.Minute)
	if err := gdb.Create(&conversationModel.Message{
		ConversationID: "conv-trends",
		UserID:         "user-trends",
		Role:           "user",
		Content:        "hello",
	}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if err := gdb.Model(&conversationModel.Message{}).Where("conversation_id = ?", "conv-trends").
		Updates(map[string]any{"create_time": messageTime, "update_time": messageTime}).Error; err != nil {
		t.Fatalf("update message time: %v", err)
	}

	svc := adminService.NewAdminService(adminRepo.NewAdminRepo(gdb), adminRepo.NewSampleQuestionRepo(gdb), gdb)
	h := adminHandler.NewAdminHandler(svc)
	r := gin.New()
	r.GET("/api/ragent/admin/dashboard/trends", h.Trends)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/admin/dashboard/trends?metric=messages&window=24h&granularity=hour", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data object, got %T: %s", resp["data"], w.Body.String())
	}
	if data["metric"] != "messages" || data["window"] != "24h" || data["granularity"] != "hour" {
		t.Fatalf("unexpected trends metadata: %s", w.Body.String())
	}
}

func TestDashboardPerformancePassesWindowParameter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Message{}); err != nil {
		t.Fatalf("migrate messages: %v", err)
	}
	if err := gdb.Exec(`CREATE TABLE t_rag_trace_run (
		id text primary key,
		trace_id text,
		status text,
		duration_ms integer,
		deleted integer default 0,
		start_time datetime
	)`).Error; err != nil {
		t.Fatalf("create trace table: %v", err)
	}

	svc := adminService.NewAdminService(adminRepo.NewAdminRepo(gdb), adminRepo.NewSampleQuestionRepo(gdb), gdb)
	h := adminHandler.NewAdminHandler(svc)
	r := gin.New()
	r.GET("/api/ragent/admin/dashboard/performance", h.Performance)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/admin/dashboard/performance?window=12h", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data object, got %T: %s", resp["data"], w.Body.String())
	}
	if data["window"] != "12h" {
		t.Fatalf("expected window query to be forwarded, got %s", w.Body.String())
	}
}

func TestTraceNodesReturnsStoredNodes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.Exec(`CREATE TABLE t_rag_trace_node (
		id text primary key,
		trace_id text,
		node_id text,
		parent_node_id text,
		depth integer,
		node_type text,
		node_name text,
		status text,
		error_message text,
		duration_ms integer,
		deleted integer default 0,
		start_time datetime,
		end_time datetime
	)`).Error; err != nil {
		t.Fatalf("create trace node table: %v", err)
	}
	if err := gdb.Exec(`INSERT INTO t_rag_trace_node
		(id, trace_id, node_id, parent_node_id, depth, node_type, node_name, status, duration_ms, deleted)
		VALUES ('1', 'trace-1', 'retrieve', '', 1, 'retrieval', '知识库检索', 'SUCCESS', 12, 0)`).Error; err != nil {
		t.Fatalf("seed trace node: %v", err)
	}

	adminRepoObj := adminRepo.NewAdminRepo(gdb)
	sampleQRepo := adminRepo.NewSampleQuestionRepo(gdb)
	svc := adminService.NewAdminService(adminRepoObj, sampleQRepo, gdb)
	h := adminHandler.NewAdminHandler(svc)

	r := gin.New()
	r.GET("/api/ragent/rag/traces/runs/:id/nodes", h.TraceNodes)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/rag/traces/runs/trace-1/nodes", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, `"nodeId":"retrieve"`) || !strings.Contains(body, `"nodeName":"知识库检索"`) {
		t.Fatalf("expected stored trace node in response, got %s", body)
	}
}

func TestTraceRunsUseJavaFiltersAndEnrichedFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&userModel.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	user := &userModel.User{Username: "trace-user", Password: "pwd", Role: "user"}
	if err := gdb.Create(user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := gdb.Exec(`CREATE TABLE t_rag_trace_run (
		id text primary key,
		trace_id text,
		trace_name text,
		entry_method text,
		conversation_id text,
		task_id text,
		user_id text,
		status text,
		error_message text,
		duration_ms integer,
		extra_data text,
		deleted integer default 0,
		start_time datetime,
		end_time datetime,
		create_time datetime
	)`).Error; err != nil {
		t.Fatalf("create trace run table: %v", err)
	}
	if err := gdb.Exec(`CREATE TABLE t_rag_trace_node (
		id text primary key,
		trace_id text,
		node_id text,
		parent_node_id text,
		depth integer,
		node_type text,
		node_name text,
		status text,
		error_message text,
		duration_ms integer,
		deleted integer default 0,
		start_time datetime,
		end_time datetime
	)`).Error; err != nil {
		t.Fatalf("create trace node table: %v", err)
	}
	now := time.Now()
	if err := gdb.Exec(`INSERT INTO t_rag_trace_run
		(id, trace_id, trace_name, entry_method, conversation_id, task_id, user_id, status, duration_ms, extra_data, deleted, start_time, create_time)
		VALUES
		('1', 'trace-success', 'rag-stream-chat', 'RAGChatController.streamChat', 'conv-1', 'task-1', ?, 'SUCCESS', 1200, '{"question":"如何开通会员？"}', 0, ?, ?),
		('2', 'trace-error', 'rag-stream-chat', 'RAGChatController.streamChat', 'conv-1', 'task-2', ?, 'ERROR', 99, '{"question":"失败问题"}', 0, ?, ?)`,
		user.ID, now, now, user.ID, now.Add(-time.Hour), now.Add(-time.Hour)).Error; err != nil {
		t.Fatalf("seed trace runs: %v", err)
	}
	if err := gdb.Exec(`INSERT INTO t_rag_trace_node
		(id, trace_id, node_id, node_type, duration_ms, deleted)
		VALUES ('n1', 'trace-success', 'ttft', 'USER_TTFT', 345, 0)`).Error; err != nil {
		t.Fatalf("seed trace ttft node: %v", err)
	}

	svc := adminService.NewAdminService(adminRepo.NewAdminRepo(gdb), adminRepo.NewSampleQuestionRepo(gdb), gdb)
	h := adminHandler.NewAdminHandler(svc)
	r := gin.New()
	r.GET("/api/ragent/rag/traces/runs", h.ListTraceRuns)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/rag/traces/runs?current=1&size=10&status=SUCCESS&conversationId=conv-1", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected page object, got %T: %s", resp["data"], w.Body.String())
	}
	if data["current"] != float64(1) || data["total"] != float64(1) {
		t.Fatalf("expected Java-style current pagination and status filter, got %s", w.Body.String())
	}
	records, ok := data["records"].([]interface{})
	if !ok || len(records) != 1 {
		t.Fatalf("expected one filtered trace run, got %s", w.Body.String())
	}
	first, ok := records[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected trace object, got %T", records[0])
	}
	if first["traceId"] != "trace-success" ||
		first["username"] != "trace-user" ||
		first["question"] != "如何开通会员？" ||
		first["ttftMs"] != float64(345) ||
		first["entryMethod"] != "RAGChatController.streamChat" {
		t.Fatalf("expected enriched Java trace fields, got %s", w.Body.String())
	}
}

func TestTraceDetailAcceptsJavaCompatibilityIDParam(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.Exec(`CREATE TABLE t_rag_trace_run (
		id text primary key,
		trace_id text,
		trace_name text,
		conversation_id text,
		task_id text,
		user_id text,
		status text,
		error_message text,
		duration_ms integer,
		deleted integer default 0,
		start_time datetime,
		end_time datetime
	)`).Error; err != nil {
		t.Fatalf("create trace run table: %v", err)
	}
	if err := gdb.Exec(`CREATE TABLE t_rag_trace_node (
		id text primary key,
		trace_id text,
		node_id text,
		parent_node_id text,
		depth integer,
		node_type text,
		node_name text,
		status text,
		error_message text,
		duration_ms integer,
		deleted integer default 0,
		start_time datetime,
		end_time datetime
	)`).Error; err != nil {
		t.Fatalf("create trace node table: %v", err)
	}
	if err := gdb.Exec(`INSERT INTO t_rag_trace_run
		(id, trace_id, trace_name, status, duration_ms, deleted)
		VALUES ('1', 'trace-detail', 'rag-stream-chat', 'SUCCESS', 12, 0)`).Error; err != nil {
		t.Fatalf("seed trace run: %v", err)
	}

	svc := adminService.NewAdminService(adminRepo.NewAdminRepo(gdb), adminRepo.NewSampleQuestionRepo(gdb), gdb)
	h := adminHandler.NewAdminHandler(svc)
	r := gin.New()
	r.GET("/api/ragent/rag/traces/runs/:id", h.TraceDetail)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/rag/traces/runs/trace-detail", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, `"traceId":"trace-detail"`) {
		t.Fatalf("expected trace detail from compatibility path, got %s", body)
	}
}
