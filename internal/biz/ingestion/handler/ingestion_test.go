package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	serviceDto "go-base-agent/internal/biz/ingestion/dto"
	"go-base-agent/internal/biz/ingestion/model"
	"go-base-agent/internal/biz/ingestion/repo"
	"go-base-agent/internal/biz/ingestion/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestIngestionHandlers_PipelineAndTaskFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&model.IngestionPipeline{},
		&model.IngestionPipelineNode{},
		&model.IngestionTask{},
		&model.IngestionTaskNode{},
	); err != nil {
		t.Fatalf("migrate ingestion tables: %v", err)
	}

	pipelineSvc := service.NewPipelineService(repo.NewPipelineRepo(gdb), gdb)
	taskSvc := service.NewTaskService(repo.NewTaskRepo(gdb), pipelineSvc, gdb)
	taskSvc.SetExecutor(fakeTaskExecutor{chunkCount: 2})
	pipelineHandler := NewPipelineHandler(pipelineSvc)
	taskHandler := NewTaskHandler(taskSvc)

	r := gin.New()
	api := r.Group("/api/ragent")
	api.POST("/ingestion/pipelines", pipelineHandler.Create)
	api.GET("/ingestion/pipelines", pipelineHandler.List)
	api.GET("/ingestion/pipelines/:id", pipelineHandler.Get)
	api.PUT("/ingestion/pipelines/:id", pipelineHandler.Update)
	api.DELETE("/ingestion/pipelines/:id", pipelineHandler.Delete)
	api.POST("/ingestion/tasks", taskHandler.Create)
	api.POST("/ingestion/tasks/upload", taskHandler.Upload)
	api.GET("/ingestion/tasks", taskHandler.List)
	api.GET("/ingestion/tasks/:id", taskHandler.Get)
	api.GET("/ingestion/tasks/:id/nodes", taskHandler.Nodes)

	pipelineBody := `{"name":"默认流水线","description":"用于测试","nodes":[{"nodeId":"fetch","nodeType":"fetcher","nextNodeId":"parse"},{"nodeId":"parse","nodeType":"parser"}]}`
	resp := performJSON(r, http.MethodPost, "/api/ragent/ingestion/pipelines", pipelineBody)
	if resp.Code != http.StatusOK {
		t.Fatalf("create pipeline status=%d body=%s", resp.Code, resp.Body.String())
	}
	var createResult struct {
		Code string `json:"code"`
		Data struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Nodes []any  `json:"nodes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &createResult); err != nil {
		t.Fatalf("decode create pipeline: %v", err)
	}
	if createResult.Code != "0" || createResult.Data.ID == "" || createResult.Data.Name != "默认流水线" || len(createResult.Data.Nodes) != 2 {
		t.Fatalf("unexpected create pipeline response: %s", resp.Body.String())
	}

	resp = performJSON(r, http.MethodGet, "/api/ragent/ingestion/pipelines?pageNo=1&pageSize=10", "")
	if !bytes.Contains(resp.Body.Bytes(), []byte(`"records"`)) {
		t.Fatalf("expected paged pipeline records, got %s", resp.Body.String())
	}

	taskBody := `{"pipelineId":"` + createResult.Data.ID + `","source":{"type":"url","location":"https://example.com/doc","fileName":"doc.html"},"metadata":{"biz":"test"}}`
	resp = performJSON(r, http.MethodPost, "/api/ragent/ingestion/tasks", taskBody)
	if resp.Code != http.StatusOK {
		t.Fatalf("create task status=%d body=%s", resp.Code, resp.Body.String())
	}
	var taskResult struct {
		Code string `json:"code"`
		Data struct {
			TaskID     string `json:"taskId"`
			PipelineID string `json:"pipelineId"`
			Status     string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &taskResult); err != nil {
		t.Fatalf("decode task response: %v", err)
	}
	if taskResult.Code != "0" || taskResult.Data.TaskID == "" || taskResult.Data.PipelineID != createResult.Data.ID || taskResult.Data.Status != "completed" {
		t.Fatalf("unexpected task response: %s", resp.Body.String())
	}

	resp = performJSON(r, http.MethodGet, "/api/ragent/ingestion/tasks/"+taskResult.Data.TaskID+"/nodes", "")
	if !bytes.Contains(resp.Body.Bytes(), []byte(`"nodeId":"fetch"`)) || !bytes.Contains(resp.Body.Bytes(), []byte(`"status":"success"`)) {
		t.Fatalf("expected task node records, got %s", resp.Body.String())
	}

	t.Run("upload reads pipelineId from multipart form field", func(t *testing.T) {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		if err := writer.WriteField("pipelineId", createResult.Data.ID); err != nil {
			t.Fatalf("write pipelineId field: %v", err)
		}
		part, err := writer.CreateFormFile("file", "doc.md")
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := part.Write([]byte("# 会员说明")); err != nil {
			t.Fatalf("write form file: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close writer: %v", err)
		}

		req := httptest.NewRequest(http.MethodPost, "/api/ragent/ingestion/tasks/upload", body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
		}
		var uploadResult struct {
			Code string `json:"code"`
			Data struct {
				TaskID     string `json:"taskId"`
				PipelineID string `json:"pipelineId"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &uploadResult); err != nil {
			t.Fatalf("decode upload response: %v", err)
		}
		if uploadResult.Code != "0" || uploadResult.Data.TaskID == "" || uploadResult.Data.PipelineID != createResult.Data.ID {
			t.Fatalf("unexpected upload response: %s", resp.Body.String())
		}
	})
}

type fakeTaskExecutor struct {
	chunkCount int
}

func (f fakeTaskExecutor) ExecuteIngestionTask(context.Context, serviceDto.CreateTaskReq) (int, error) {
	return f.chunkCount, nil
}

func performJSON(r http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	return resp
}
