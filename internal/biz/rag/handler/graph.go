package handler

import (
	"context"
	"net/http"
	"strconv"

	"go-base-agent/internal/biz/rag"
	"go-base-agent/internal/framework/convention"

	"github.com/gin-gonic/gin"
)

// GraphHandler exposes admin graph visualization endpoints.
type GraphHandler struct {
	svc graphQueryService
}

type graphQueryService interface {
	GetGraph(ctx context.Context, entity, collection, doc string, depth, limit int) (rag.GraphView, error)
	SearchEntities(ctx context.Context, keyword string, limit int) ([]string, error)
}

// NewGraphHandler creates a graph handler.
func NewGraphHandler(svc graphQueryService) *GraphHandler {
	return &GraphHandler{svc: svc}
}

// Graph GET /api/ragent/admin/kg/graph
func (h *GraphHandler) Graph(c *gin.Context) {
	view, err := h.svc.GetGraph(
		c.Request.Context(),
		c.Query("entity"),
		c.Query("collection"),
		c.Query("doc"),
		mustAtoi(c.DefaultQuery("depth", "2")),
		mustAtoi(c.DefaultQuery("limit", "200")),
	)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(view))
}

// Labels GET /api/ragent/admin/kg/labels
func (h *GraphHandler) Labels(c *gin.Context) {
	labels, err := h.svc.SearchEntities(c.Request.Context(), c.Query("keyword"), mustAtoi(c.DefaultQuery("limit", "50")))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(labels))
}

func mustAtoi(value string) int {
	n, _ := strconv.Atoi(value)
	return n
}
