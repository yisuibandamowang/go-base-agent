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
	if err := gdb.Create(&adminModel.SampleQuestion{Title: "t1", Question: "q1"}).Error; err != nil {
		t.Fatalf("seed sample question: %v", err)
	}

	adminRepoObj := adminRepo.NewAdminRepo(gdb)
	sampleQRepo := adminRepo.NewSampleQuestionRepo(gdb)
	svc := adminService.NewAdminService(adminRepoObj, sampleQRepo, gdb)
	h := adminHandler.NewAdminHandler(svc)

	r := gin.New()
	r.GET("/api/ragent/rag/sample-questions", h.ListRAGSampleQuestions)
	r.GET("/api/ragent/sample-questions", h.ListRAGSampleQuestions)
	r.GET("/api/ragent/sample-questions/:id", h.GetSampleQuestion)
	r.GET("/api/ragent/admin/sample-questions", h.ListSampleQuestions)

	for _, path := range []string{
		"/api/ragent/rag/sample-questions",
		"/api/ragent/sample-questions",
	} {
		t.Run(path+" returns array data for chat page", func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}
			var resp map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp["code"] != "0" {
				t.Fatalf("expected code 0, got %v", resp["code"])
			}
			if _, ok := resp["data"].([]interface{}); !ok {
				t.Fatalf("expected data array for chat page, got %T: %s", resp["data"], w.Body.String())
			}
		})
	}

	t.Run("admin sample questions keeps paged data", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/ragent/admin/sample-questions", nil)
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
			t.Fatalf("expected data object for admin page, got %T: %s", resp["data"], w.Body.String())
		}
		if _, ok := data["records"].([]interface{}); !ok {
			t.Fatalf("expected data.records array for admin page, got %T: %s", data["records"], w.Body.String())
		}
	})

	t.Run("sample question detail returns original record", func(t *testing.T) {
		var seeded adminModel.SampleQuestion
		if err := gdb.First(&seeded).Error; err != nil {
			t.Fatalf("load seeded sample question: %v", err)
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/ragent/sample-questions/"+seeded.ID, nil)
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
		if data["id"] != seeded.ID || data["question"] != "q1" {
			t.Fatalf("unexpected detail response: %s", w.Body.String())
		}
	})
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
