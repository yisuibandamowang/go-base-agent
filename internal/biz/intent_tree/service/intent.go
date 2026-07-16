package service

import (
	"context"
	"fmt"
	"log/slog"

	auditService "go-base-agent/internal/biz/audit/service"
	"go-base-agent/internal/biz/intent_tree/dto"
	"go-base-agent/internal/biz/intent_tree/model"
	"go-base-agent/internal/biz/intent_tree/repo"

	"gorm.io/gorm"
)

// IntentService 意图树业务服务。
type IntentService struct {
	intentRepo    *repo.IntentRepo
	termRepo      *repo.TermMappingRepo
	db            *gorm.DB
	auditRecorder *auditService.BizChangeLogService
}

// NewIntentService 创建 IntentService。
func NewIntentService(intentRepo *repo.IntentRepo, termRepo *repo.TermMappingRepo, db *gorm.DB) *IntentService {
	return &IntentService{intentRepo: intentRepo, termRepo: termRepo, db: db}
}

// SetAuditRecorder 设置审计日志记录器。
func (s *IntentService) SetAuditRecorder(recorder *auditService.BizChangeLogService) {
	s.auditRecorder = recorder
}

// CreateNode 创建意图节点。
func (s *IntentService) CreateNode(ctx context.Context, req dto.CreateIntentReq, userID string) (*dto.IntentNodeResp, error) {
	node := &model.IntentNode{
		KbID:                req.KbID,
		IntentCode:          req.IntentCode,
		Name:                req.Name,
		Level:               req.Level,
		ParentCode:          req.ParentCode,
		Description:         req.Description,
		Examples:            req.Examples,
		CollectionName:      req.CollectionName,
		TopK:                req.TopK,
		McpToolID:           req.McpToolID,
		Kind:                req.Kind,
		PromptSnippet:       req.PromptSnippet,
		PromptTemplate:      req.PromptTemplate,
		ParamPromptTemplate: req.ParamPromptTemplate,
		SortOrder:           req.SortOrder,
		Enabled:             req.Enabled,
		CreateBy:            userID,
	}
	if err := s.intentRepo.Create(ctx, node); err != nil {
		return nil, fmt.Errorf("创建意图节点失败: %w", err)
	}
	resp := toIntentResp(node)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:       auditService.BizTypeIntentTree,
		BizID:         resp.ID,
		OperationType: auditService.OperationCreate,
		ActionDesc:    "创建意图节点：" + resp.Name,
		AfterSnapshot: resp,
	})
	return resp, nil
}

// GetNode 获取单个意图节点。
func (s *IntentService) GetNode(ctx context.Context, id string) (*dto.IntentNodeResp, error) {
	node, err := s.intentRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return toIntentResp(node), nil
}

// UpdateNode 更新意图节点。
func (s *IntentService) UpdateNode(ctx context.Context, id string, req dto.UpdateIntentReq, userID string) (*dto.IntentNodeResp, error) {
	node, err := s.intentRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := toIntentResp(node)
	applyIntentUpdate(node, req)
	node.UpdateBy = userID
	if err := s.intentRepo.Update(ctx, node); err != nil {
		return nil, fmt.Errorf("更新意图节点失败: %w", err)
	}
	resp := toIntentResp(node)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeIntentTree,
		BizID:          resp.ID,
		OperationType:  auditService.OperationUpdate,
		ActionDesc:     "更新意图节点：" + resp.Name,
		BeforeSnapshot: before,
		AfterSnapshot:  resp,
	})
	return resp, nil
}

// DeleteNode 删除意图节点。
func (s *IntentService) DeleteNode(ctx context.Context, id string) error {
	node, err := s.intentRepo.FindByID(ctx, id)
	if err != nil {
		return err
	}
	before := toIntentResp(node)
	if err := s.intentRepo.SoftDelete(ctx, id); err != nil {
		return err
	}
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeIntentTree,
		BizID:          id,
		OperationType:  auditService.OperationDelete,
		ActionDesc:     "删除意图节点：" + before.Name,
		BeforeSnapshot: before,
	})
	return nil
}

// ToggleNode 切换意图节点启用状态。
func (s *IntentService) ToggleNode(ctx context.Context, id string, enabled int16) error {
	node, err := s.intentRepo.FindByID(ctx, id)
	if err != nil {
		return err
	}
	before := toIntentResp(node)
	if err := s.intentRepo.UpdateEnabled(ctx, id, enabled); err != nil {
		return err
	}
	node.Enabled = enabled
	after := toIntentResp(node)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeIntentTree,
		BizID:          id,
		OperationType:  operationForEnabled(enabled),
		ActionDesc:     "切换意图节点状态：" + after.Name,
		BeforeSnapshot: before,
		AfterSnapshot:  after,
	})
	return nil
}

// BatchToggleNodes 批量切换意图节点启用状态。
func (s *IntentService) BatchToggleNodes(ctx context.Context, ids []string, enabled int16) error {
	for _, id := range ids {
		if err := s.ToggleNode(ctx, id, enabled); err != nil {
			return fmt.Errorf("批量切换意图节点状态失败: %w", err)
		}
	}
	return nil
}

// BatchDeleteNodes 批量删除意图节点。
func (s *IntentService) BatchDeleteNodes(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if err := s.DeleteNode(ctx, id); err != nil {
			return fmt.Errorf("批量删除意图节点失败: %w", err)
		}
	}
	return nil
}

// GetTree 获取意图树（以树形结构返回）。
func (s *IntentService) GetTree(ctx context.Context) ([]*dto.IntentNodeResp, error) {
	nodes, err := s.intentRepo.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	return buildTree(nodes, ""), nil
}

// ListNode 分页查询意图节点（平铺列表）。
func (s *IntentService) ListNode(ctx context.Context, page, size int) ([]dto.IntentNodeResp, int64, error) {
	nodes, total, err := s.intentRepo.ListAllPage(ctx, page, size)
	if err != nil {
		return nil, 0, err
	}
	records := make([]dto.IntentNodeResp, 0, len(nodes))
	for _, n := range nodes {
		records = append(records, *toIntentResp(&n))
	}
	return records, total, nil
}

// --- 关键词映射 ---

// CreateTermMapping 创建关键词映射。
func (s *IntentService) CreateTermMapping(ctx context.Context, req dto.CreateTermMappingReq, userID string) (*dto.TermMappingResp, error) {
	m := &model.QueryTermMapping{
		Domain:     req.Domain,
		SourceTerm: req.SourceTerm,
		TargetTerm: req.TargetTerm,
		MatchType:  req.MatchType,
		Priority:   req.Priority,
		Enabled:    req.Enabled,
		Remark:     req.Remark,
		CreateBy:   userID,
	}
	if err := s.termRepo.Create(ctx, m); err != nil {
		return nil, fmt.Errorf("创建关键词映射失败: %w", err)
	}
	resp := toTermResp(m)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:       auditService.BizTypeQueryTermMapping,
		BizID:         resp.ID,
		OperationType: auditService.OperationCreate,
		ActionDesc:    "创建关键词映射：" + resp.SourceTerm,
		AfterSnapshot: resp,
	})
	return resp, nil
}

// GetTermMapping 查询关键词映射详情。
func (s *IntentService) GetTermMapping(ctx context.Context, id string) (*dto.TermMappingResp, error) {
	m, err := s.termRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return toTermResp(m), nil
}

// UpdateTermMapping 更新关键词映射。
func (s *IntentService) UpdateTermMapping(ctx context.Context, id string, req dto.UpdateTermMappingReq, userID string) (*dto.TermMappingResp, error) {
	var m model.QueryTermMapping
	if err := s.db.WithContext(ctx).Where("id = ? AND deleted = 0", id).First(&m).Error; err != nil {
		return nil, fmt.Errorf("映射不存在: %w", err)
	}
	before := toTermResp(&m)
	applyTermUpdate(&m, req)
	m.UpdateBy = userID
	if err := s.termRepo.Update(ctx, &m); err != nil {
		return nil, fmt.Errorf("更新关键词映射失败: %w", err)
	}
	resp := toTermResp(&m)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeQueryTermMapping,
		BizID:          resp.ID,
		OperationType:  auditService.OperationUpdate,
		ActionDesc:     "更新关键词映射：" + resp.SourceTerm,
		BeforeSnapshot: before,
		AfterSnapshot:  resp,
	})
	return resp, nil
}

// DeleteTermMapping 删除关键词映射。
func (s *IntentService) DeleteTermMapping(ctx context.Context, id string) error {
	m, err := s.termRepo.FindByID(ctx, id)
	if err != nil {
		return err
	}
	before := toTermResp(m)
	if err := s.termRepo.SoftDelete(ctx, id); err != nil {
		return err
	}
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeQueryTermMapping,
		BizID:          id,
		OperationType:  auditService.OperationDelete,
		ActionDesc:     "删除关键词映射：" + before.SourceTerm,
		BeforeSnapshot: before,
	})
	return nil
}

// ListTermMappings 分页查询关键词映射。
func (s *IntentService) ListTermMappings(ctx context.Context, domain string, page, size int) ([]dto.TermMappingResp, int64, error) {
	mappings, total, err := s.termRepo.ListByDomain(ctx, domain, page, size)
	if err != nil {
		return nil, 0, err
	}
	records := make([]dto.TermMappingResp, 0, len(mappings))
	for _, m := range mappings {
		records = append(records, *toTermResp(&m))
	}
	return records, total, nil
}

// --- helpers ---

func toIntentResp(node *model.IntentNode) *dto.IntentNodeResp {
	return &dto.IntentNodeResp{
		ID:                  node.ID,
		KbID:                node.KbID,
		IntentCode:          node.IntentCode,
		Name:                node.Name,
		Level:               node.Level,
		ParentCode:          node.ParentCode,
		Description:         node.Description,
		Examples:            node.Examples,
		CollectionName:      node.CollectionName,
		TopK:                node.TopK,
		McpToolID:           node.McpToolID,
		Kind:                node.Kind,
		PromptSnippet:       node.PromptSnippet,
		PromptTemplate:      node.PromptTemplate,
		ParamPromptTemplate: node.ParamPromptTemplate,
		SortOrder:           node.SortOrder,
		Enabled:             node.Enabled,
		CreateTime:          node.CreateTime,
		UpdateTime:          node.UpdateTime,
	}
}

func buildTree(nodes []model.IntentNode, parentCode string) []*dto.IntentNodeResp {
	var result []*dto.IntentNodeResp
	for _, n := range nodes {
		if n.ParentCode == parentCode {
			resp := toIntentResp(&n)
			resp.Children = buildTree(nodes, n.IntentCode)
			result = append(result, resp)
		}
	}
	return result
}

func applyIntentUpdate(node *model.IntentNode, req dto.UpdateIntentReq) {
	if req.KbID != nil {
		node.KbID = *req.KbID
	}
	if req.IntentCode != nil {
		node.IntentCode = *req.IntentCode
	}
	if req.Name != nil {
		node.Name = *req.Name
	}
	if req.Level != nil {
		node.Level = *req.Level
	}
	if req.ParentCode != nil {
		node.ParentCode = *req.ParentCode
	}
	if req.Description != nil {
		node.Description = *req.Description
	}
	if req.Examples != nil {
		node.Examples = *req.Examples
	}
	if req.CollectionName != nil {
		node.CollectionName = *req.CollectionName
	}
	if req.TopK != nil {
		node.TopK = *req.TopK
	}
	if req.McpToolID != nil {
		node.McpToolID = *req.McpToolID
	}
	if req.Kind != nil {
		node.Kind = *req.Kind
	}
	if req.PromptSnippet != nil {
		node.PromptSnippet = *req.PromptSnippet
	}
	if req.PromptTemplate != nil {
		node.PromptTemplate = *req.PromptTemplate
	}
	if req.ParamPromptTemplate != nil {
		node.ParamPromptTemplate = *req.ParamPromptTemplate
	}
	if req.SortOrder != nil {
		node.SortOrder = *req.SortOrder
	}
	if req.Enabled != nil {
		node.Enabled = *req.Enabled
	}
}

func toTermResp(m *model.QueryTermMapping) *dto.TermMappingResp {
	return &dto.TermMappingResp{
		ID:         m.ID,
		Domain:     m.Domain,
		SourceTerm: m.SourceTerm,
		TargetTerm: m.TargetTerm,
		MatchType:  m.MatchType,
		Priority:   m.Priority,
		Enabled:    m.Enabled,
		Remark:     m.Remark,
		CreateTime: m.CreateTime,
	}
}

func (s *IntentService) recordAudit(ctx context.Context, req auditService.RecordReq) {
	if s.auditRecorder == nil {
		return
	}
	if err := s.auditRecorder.Record(ctx, req); err != nil {
		slog.Warn("audit record failed", "err", err, "biz_type", req.BizType, "biz_id", req.BizID)
	}
}

func operationForEnabled(enabled int16) string {
	if enabled == 1 {
		return auditService.OperationEnable
	}
	return auditService.OperationDisable
}

func applyTermUpdate(m *model.QueryTermMapping, req dto.UpdateTermMappingReq) {
	if req.Domain != nil {
		m.Domain = *req.Domain
	}
	if req.SourceTerm != nil {
		m.SourceTerm = *req.SourceTerm
	}
	if req.TargetTerm != nil {
		m.TargetTerm = *req.TargetTerm
	}
	if req.MatchType != nil {
		m.MatchType = *req.MatchType
	}
	if req.Priority != nil {
		m.Priority = *req.Priority
	}
	if req.Enabled != nil {
		m.Enabled = *req.Enabled
	}
	if req.Remark != nil {
		m.Remark = *req.Remark
	}
}

// Ensure gorm import is used.
var _ = gorm.ErrRecordNotFound
