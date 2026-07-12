package handler

import (
	"net/http"
	"strconv"

	"go-base-agent/internal/biz/ingestion/dto"
	"go-base-agent/internal/biz/ingestion/service"
	"go-base-agent/internal/framework/convention"
	"go-base-agent/internal/framework/middleware"

	"github.com/gin-gonic/gin"
)

// PipelineHandler 是摄取流水线 HTTP 处理层。
type PipelineHandler struct {
	svc *service.PipelineService
}

// NewPipelineHandler 创建 PipelineHandler。
func NewPipelineHandler(svc *service.PipelineService) *PipelineHandler {
	return &PipelineHandler{svc: svc}
}

// Create POST /api/ragent/ingestion/pipelines
func (h *PipelineHandler) Create(c *gin.Context) {
	var req dto.CreatePipelineReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	resp, err := h.svc.Create(c.Request.Context(), req, userID(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// Update PUT /api/ragent/ingestion/pipelines/:id
func (h *PipelineHandler) Update(c *gin.Context) {
	var req dto.UpdatePipelineReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	resp, err := h.svc.Update(c.Request.Context(), c.Param("id"), req, userID(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// Get GET /api/ragent/ingestion/pipelines/:id
func (h *PipelineHandler) Get(c *gin.Context) {
	resp, err := h.svc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// List GET /api/ragent/ingestion/pipelines
func (h *PipelineHandler) List(c *gin.Context) {
	page, size := pagination(c)
	resp, total, err := h.svc.List(c.Request.Context(), page, size, c.Query("keyword"))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(convention.NewPageResp(resp, total, page, size)))
}

// Delete DELETE /api/ragent/ingestion/pipelines/:id
func (h *PipelineHandler) Delete(c *gin.Context) {
	if err := h.svc.Delete(c.Request.Context(), c.Param("id")); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// TaskHandler 是摄取任务 HTTP 处理层。
type TaskHandler struct {
	svc *service.TaskService
}

// NewTaskHandler 创建 TaskHandler。
func NewTaskHandler(svc *service.TaskService) *TaskHandler {
	return &TaskHandler{svc: svc}
}

// Create POST /api/ragent/ingestion/tasks
func (h *TaskHandler) Create(c *gin.Context) {
	var req dto.CreateTaskReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	resp, err := h.svc.Create(c.Request.Context(), req, userID(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// Upload POST /api/ragent/ingestion/tasks/upload
func (h *TaskHandler) Upload(c *gin.Context) {
	pipelineID := c.Query("pipelineId")
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "读取上传文件失败: "+err.Error()))
		return
	}
	resp, err := h.svc.Upload(c.Request.Context(), pipelineID, file, userID(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// Get GET /api/ragent/ingestion/tasks/:id
func (h *TaskHandler) Get(c *gin.Context) {
	resp, err := h.svc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// Nodes GET /api/ragent/ingestion/tasks/:id/nodes
func (h *TaskHandler) Nodes(c *gin.Context) {
	resp, err := h.svc.Nodes(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// List GET /api/ragent/ingestion/tasks
func (h *TaskHandler) List(c *gin.Context) {
	page, size := pagination(c)
	resp, total, err := h.svc.List(c.Request.Context(), page, size, c.Query("status"))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(convention.NewPageResp(resp, total, page, size)))
}

func pagination(c *gin.Context) (int, int) {
	page, err := strconv.Atoi(c.DefaultQuery("current", c.DefaultQuery("pageNo", c.DefaultQuery("page", "1"))))
	if err != nil || page < 1 {
		page = 1
	}
	size, err := strconv.Atoi(c.DefaultQuery("size", c.DefaultQuery("pageSize", "10")))
	if err != nil || size < 1 {
		size = 10
	}
	return page, size
}

func userID(c *gin.Context) string {
	if user := middleware.GetLoginUser(c); user != nil {
		return user.UserID
	}
	return "system"
}
