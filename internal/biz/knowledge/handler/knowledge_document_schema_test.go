package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	knowledgeHandler "go-base-agent/internal/biz/knowledge/handler"

	"github.com/gin-gonic/gin"
)

func TestDocumentHandler_IngestionSpecSchemaMatchesJavaContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := knowledgeHandler.NewDocumentHandler(nil, nil)
	r.GET("/api/ragent/knowledge-base/docs/ingestion-spec-schema", h.IngestionSpecSchema)

	req := httptest.NewRequest(http.MethodGet, "/api/ragent/knowledge-base/docs/ingestion-spec-schema", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.Code)
	}
	var envelope struct {
		Code string `json:"code"`
		Data struct {
			ParseProfileLabel string `json:"parseProfileLabel"`
			ParseProfiles     []struct {
				Value string `json:"value"`
				Label string `json:"label"`
				Hint  string `json:"hint"`
			} `json:"parseProfiles"`
			ParseProfileExtensions []string `json:"parseProfileExtensions"`
			BudgetFields           []struct {
				Key            string `json:"key"`
				DefaultValue   int    `json:"defaultValue"`
				Min            int    `json:"min"`
				Max            int    `json:"max"`
				RecommendedMin int    `json:"recommendedMin"`
				RecommendedMax int    `json:"recommendedMax"`
			} `json:"budgetFields"`
			WholeDocumentSentinel int `json:"wholeDocumentSentinel"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Code != "0" {
		t.Fatalf("unexpected response code: %q", envelope.Code)
	}
	if envelope.Data.ParseProfileLabel != "表格结构" {
		t.Fatalf("unexpected profile label: %q", envelope.Data.ParseProfileLabel)
	}
	if len(envelope.Data.ParseProfiles) != 2 || envelope.Data.ParseProfiles[0].Value != "fast" || envelope.Data.ParseProfiles[1].Value != "fidelity" {
		t.Fatalf("unexpected parse profiles: %+v", envelope.Data.ParseProfiles)
	}
	if len(envelope.Data.ParseProfileExtensions) != 2 || envelope.Data.ParseProfileExtensions[0] != "xls" || envelope.Data.ParseProfileExtensions[1] != "xlsx" {
		t.Fatalf("unexpected profile extensions: %+v", envelope.Data.ParseProfileExtensions)
	}
	if len(envelope.Data.BudgetFields) != 4 {
		t.Fatalf("unexpected budget field count: %d", len(envelope.Data.BudgetFields))
	}
	if got := envelope.Data.BudgetFields[0]; got.Key != "maxChars" || got.DefaultValue != 1024 || got.Min != 1 || got.Max != 8192 || got.RecommendedMin != 512 || got.RecommendedMax != 8192 {
		t.Fatalf("unexpected maxChars schema: %+v", got)
	}
	if envelope.Data.WholeDocumentSentinel != -1 {
		t.Fatalf("unexpected whole-document sentinel: %d", envelope.Data.WholeDocumentSentinel)
	}
}
