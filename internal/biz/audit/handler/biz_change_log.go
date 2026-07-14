package handler

import (
	"net/http"
	"strconv"
	"time"

	"go-base-agent/internal/biz/audit/service"
	"go-base-agent/internal/framework/convention"

	"github.com/gin-gonic/gin"
)

const auditTimeLayout = "2006-01-02 15:04:05"

// AuditHandler 业务变更审计日志 HTTP 处理层。
type AuditHandler struct {
	svc *service.BizChangeLogService
}

// NewAuditHandler 创建 AuditHandler。
func NewAuditHandler(svc *service.BizChangeLogService) *AuditHandler {
	return &AuditHandler{svc: svc}
}

// List GET /api/ragent/biz-change-logs。
func (h *AuditHandler) List(c *gin.Context) {
	page, size := auditPageParams(c)
	req, err := auditPageReq(c)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", err.Error()))
		return
	}
	records, total, err := h.svc.List(c.Request.Context(), req, page, size)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(convention.NewPageResp(records, total, page, size)))
}

// Get GET /api/ragent/biz-change-logs/:id。
func (h *AuditHandler) Get(c *gin.Context) {
	resp, err := h.svc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

func auditPageReq(c *gin.Context) (service.BizChangeLogPageReq, error) {
	req := service.BizChangeLogPageReq{
		BizType:       c.Query("bizType"),
		BizId:         c.Query("bizId"),
		OperationType: c.Query("operationType"),
		OperatorID:    c.Query("operatorId"),
		OperatorName:  c.Query("operatorName"),
	}
	if value := c.Query("success"); value != "" {
		success, err := strconv.ParseBool(value)
		if err != nil {
			return req, err
		}
		req.Success = &success
	}
	if value := c.Query("beginTime"); value != "" {
		beginTime, err := time.ParseInLocation(auditTimeLayout, value, time.Local)
		if err != nil {
			return req, err
		}
		req.BeginTime = &beginTime
	}
	if value := c.Query("endTime"); value != "" {
		endTime, err := time.ParseInLocation(auditTimeLayout, value, time.Local)
		if err != nil {
			return req, err
		}
		req.EndTime = &endTime
	}
	return req, nil
}

func auditPageParams(c *gin.Context) (int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("current", c.DefaultQuery("page", "1")))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "10"))
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 10
	}
	return page, size
}
