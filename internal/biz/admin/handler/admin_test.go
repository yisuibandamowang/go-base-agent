package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	adminHandler "go-base-agent/internal/biz/admin/handler"
	adminModel "go-base-agent/internal/biz/admin/model"
	adminRepo "go-base-agent/internal/biz/admin/repo"
	adminService "go-base-agent/internal/biz/admin/service"

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
}
