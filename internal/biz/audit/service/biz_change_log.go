package service

import (
	"context"
	"time"

	auditModel "go-base-agent/internal/biz/audit/model"
	"go-base-agent/internal/biz/audit/repo"
)

// BizChangeLogPageReq 变更日志分页查询请求。
type BizChangeLogPageReq struct {
	BizType       string
	BizId         string
	OperationType string
	OperatorID    string
	OperatorName  string
	Success       *bool
	BeginTime     *time.Time
	EndTime       *time.Time
}

// BizChangeLogResp 变更日志响应。
type BizChangeLogResp struct {
	ID             string    `json:"id"`
	BizType        string    `json:"bizType"`
	BizId          string    `json:"bizId"`
	OperationType  string    `json:"operationType"`
	ActionDesc     string    `json:"actionDesc"`
	BeforeSnapshot string    `json:"beforeSnapshot"`
	AfterSnapshot  string    `json:"afterSnapshot"`
	ChangeDiff     string    `json:"changeDiff"`
	OperatorID     string    `json:"operatorId"`
	OperatorName   string    `json:"operatorName"`
	OperatorRole   string    `json:"operatorRole"`
	Success        bool      `json:"success"`
	ErrorMessage   string    `json:"errorMessage"`
	ClassName      string    `json:"className"`
	MethodName     string    `json:"methodName"`
	IP             string    `json:"ip"`
	UserAgent      string    `json:"userAgent"`
	CreateTime     time.Time `json:"createTime"`
}

// BizChangeLogService 业务变更日志服务。
type BizChangeLogService struct {
	repo *repo.BizChangeLogRepo
}

// NewBizChangeLogService 创建 BizChangeLogService。
func NewBizChangeLogService(repo *repo.BizChangeLogRepo) *BizChangeLogService {
	return &BizChangeLogService{repo: repo}
}

// List 分页查询变更日志。
func (s *BizChangeLogService) List(ctx context.Context, req BizChangeLogPageReq, page, size int) ([]BizChangeLogResp, int64, error) {
	items, total, err := s.repo.List(ctx, repo.BizChangeLogQuery{
		BizType:       req.BizType,
		BizId:         req.BizId,
		OperationType: req.OperationType,
		OperatorID:    req.OperatorID,
		OperatorName:  req.OperatorName,
		Success:       req.Success,
		BeginTime:     req.BeginTime,
		EndTime:       req.EndTime,
	}, page, size)
	if err != nil {
		return nil, 0, err
	}
	resp := make([]BizChangeLogResp, 0, len(items))
	for _, item := range items {
		resp = append(resp, toResp(item))
	}
	return resp, total, nil
}

// Get 查询变更日志详情。
func (s *BizChangeLogService) Get(ctx context.Context, id string) (*BizChangeLogResp, error) {
	item, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	resp := toResp(*item)
	return &resp, nil
}

func toResp(item auditModel.BizChangeLog) BizChangeLogResp {
	return BizChangeLogResp{
		ID:             item.ID,
		BizType:        item.BizType,
		BizId:          item.BizId,
		OperationType:  item.OperationType,
		ActionDesc:     item.ActionDesc,
		BeforeSnapshot: item.BeforeSnapshot,
		AfterSnapshot:  item.AfterSnapshot,
		ChangeDiff:     item.ChangeDiff,
		OperatorID:     item.OperatorID,
		OperatorName:   item.OperatorName,
		OperatorRole:   item.OperatorRole,
		Success:        item.Success,
		ErrorMessage:   item.ErrorMessage,
		ClassName:      item.ClassName,
		MethodName:     item.MethodName,
		IP:             item.IP,
		UserAgent:      item.UserAgent,
		CreateTime:     item.CreateTime,
	}
}
