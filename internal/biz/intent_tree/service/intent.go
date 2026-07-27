package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	auditService "go-base-agent/internal/biz/audit/service"
	"go-base-agent/internal/biz/intent_tree/dto"
	"go-base-agent/internal/biz/intent_tree/model"
	"go-base-agent/internal/biz/intent_tree/repo"
	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/biz/rag"
	frameworkDB "go-base-agent/internal/framework/db"

	"gorm.io/gorm"
)

// IntentService 意图树业务服务。
type IntentService struct {
	intentRepo    *repo.IntentRepo
	termRepo      *repo.TermMappingRepo
	termCache     rag.QueryTermMappingCacheManager
	intentCache   rag.IntentNodeCacheManager
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

// SetQueryTermMappingCacheManager injects the optional query term mapping cache.
func (s *IntentService) SetQueryTermMappingCacheManager(cache rag.QueryTermMappingCacheManager) {
	s.termCache = cache
}

// SetIntentNodeCacheManager injects the optional intent tree cache.
func (s *IntentService) SetIntentNodeCacheManager(cache rag.IntentNodeCacheManager) {
	s.intentCache = cache
}

// CreateNode 创建意图节点。
func (s *IntentService) CreateNode(ctx context.Context, req dto.CreateIntentReq, userID string) (*dto.IntentNodeResp, error) {
	exists, err := s.intentRepo.ExistsByIntentCode(ctx, req.IntentCode)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, fmt.Errorf("意图标识已存在: %s", req.IntentCode)
	}
	if err := validateTopicKBNode(req.Level, req.Kind, req.KbID); err != nil {
		return nil, err
	}
	topK, err := normalizeCreateTopK(req.TopK, req.TopKSet)
	if err != nil {
		return nil, err
	}
	collectionName, err := s.resolveCollectionName(ctx, req.KbID, req.CollectionName)
	if err != nil {
		return nil, err
	}
	node := &model.IntentNode{
		KbID:                req.KbID,
		IntentCode:          req.IntentCode,
		Name:                req.Name,
		Level:               req.Level,
		ParentCode:          req.ParentCode,
		Description:         req.Description,
		Examples:            string(req.Examples),
		CollectionName:      collectionName,
		TopK:                topK,
		McpToolID:           req.McpToolID,
		Kind:                req.Kind,
		PromptSnippet:       req.PromptSnippet,
		PromptTemplate:      req.PromptTemplate,
		ParamPromptTemplate: req.ParamPromptTemplate,
		SortOrder:           req.SortOrder,
		Enabled:             normalizeCreateEnabled(req.Enabled, req.EnabledSet),
		CreateBy:            userID,
	}
	if err := s.intentRepo.Create(ctx, node); err != nil {
		return nil, fmt.Errorf("创建意图节点失败: %w", err)
	}
	desiredEnabled := normalizeCreateEnabled(req.Enabled, req.EnabledSet)
	if node.Enabled != desiredEnabled {
		if err := s.intentRepo.UpdateEnabled(ctx, node.ID, desiredEnabled); err != nil {
			return nil, fmt.Errorf("更新意图节点默认启用状态失败: %w", err)
		}
		node.Enabled = desiredEnabled
	}
	resp := toIntentResp(node)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:       auditService.BizTypeIntentTree,
		BizID:         resp.ID,
		OperationType: auditService.OperationCreate,
		ActionDesc:    "创建意图节点：" + resp.Name,
		AfterSnapshot: resp,
	})
	s.clearIntentNodeCache(ctx)
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
	if err := applyIntentUpdate(node, req); err != nil {
		return nil, err
	}
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
	s.clearIntentNodeCache(ctx)
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
	s.clearIntentNodeCache(ctx)
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
	s.clearIntentNodeCache(ctx)
	return nil
}

// BatchToggleNodes 批量切换意图节点启用状态。
func (s *IntentService) BatchToggleNodes(ctx context.Context, ids []string, enabled int16) error {
	targetNodes, allNodes, err := s.listAndValidateTargetNodes(ctx, ids)
	if err != nil {
		return err
	}
	if enabled == 0 {
		if err := validateBatchDisableDescendants(targetNodes, allNodes); err != nil {
			return err
		}
	}
	for _, node := range targetNodes {
		if err := s.ToggleNode(ctx, node.ID, enabled); err != nil {
			return fmt.Errorf("批量切换意图节点状态失败: %w", err)
		}
	}
	return nil
}

// BatchDeleteNodes 批量删除意图节点。
func (s *IntentService) BatchDeleteNodes(ctx context.Context, ids []string) error {
	targetNodes, allNodes, err := s.listAndValidateTargetNodes(ctx, ids)
	if err != nil {
		return err
	}
	if err := validateBatchDeleteDescendants(targetNodes, allNodes); err != nil {
		return err
	}
	for _, node := range targetNodes {
		if err := s.DeleteNode(ctx, node.ID); err != nil {
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
	sourceTerm := strings.TrimSpace(req.SourceTerm)
	if sourceTerm == "" {
		return nil, fmt.Errorf("原始词不能为空")
	}
	targetTerm := strings.TrimSpace(req.TargetTerm)
	if targetTerm == "" {
		return nil, fmt.Errorf("目标词不能为空")
	}
	matchType := req.MatchType
	if matchType == 0 {
		matchType = 1
	}
	enabled := req.Enabled
	if !req.EnabledSet && enabled == 0 {
		enabled = 1
	}
	m := &model.QueryTermMapping{
		Domain:     strings.TrimSpace(req.Domain),
		SourceTerm: sourceTerm,
		TargetTerm: targetTerm,
		MatchType:  matchType,
		Priority:   req.Priority,
		Enabled:    enabled,
		Remark:     strings.TrimSpace(req.Remark),
		CreateBy:   userID,
	}
	if err := s.termRepo.Create(ctx, m); err != nil {
		return nil, fmt.Errorf("创建关键词映射失败: %w", err)
	}
	if err := s.db.WithContext(ctx).Model(&model.QueryTermMapping{}).
		Where("id = ?", m.ID).
		Updates(map[string]any{
			"priority": req.Priority,
			"enabled":  enabled,
		}).Error; err != nil {
		return nil, fmt.Errorf("更新关键词映射默认字段失败: %w", err)
	}
	m.Priority = req.Priority
	m.Enabled = enabled
	resp := toTermResp(m)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:       auditService.BizTypeQueryTermMapping,
		BizID:         resp.ID,
		OperationType: auditService.OperationCreate,
		ActionDesc:    "创建关键词映射：" + resp.SourceTerm,
		AfterSnapshot: resp,
	})
	s.clearQueryTermMappingCache(ctx, resp.Domain)
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
	if strings.TrimSpace(m.SourceTerm) == "" {
		return nil, fmt.Errorf("原始词不能为空")
	}
	if strings.TrimSpace(m.TargetTerm) == "" {
		return nil, fmt.Errorf("目标词不能为空")
	}
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
	s.clearQueryTermMappingCache(ctx, resp.Domain)
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
	s.clearQueryTermMappingCache(ctx, before.Domain)
	return nil
}

// ListTermMappings 分页查询关键词映射。
func (s *IntentService) ListTermMappings(ctx context.Context, domain, keyword string, page, size int) ([]dto.TermMappingResp, int64, error) {
	mappings, total, err := s.termRepo.ListByDomainAndKeyword(ctx, strings.TrimSpace(domain), strings.TrimSpace(keyword), page, size)
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

func applyIntentUpdate(node *model.IntentNode, req dto.UpdateIntentReq) error {
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
		node.Examples = string(*req.Examples)
	}
	if req.CollectionName != nil {
		node.CollectionName = *req.CollectionName
	}
	if req.TopK != nil {
		if err := validateTopK(*req.TopK); err != nil {
			return err
		}
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
	return nil
}

func normalizeCreateTopK(topK int, topKSet bool) (int, error) {
	if !topKSet && topK == 0 {
		return 0, nil
	}
	if err := validateTopK(topK); err != nil {
		return 0, err
	}
	return topK, nil
}

func validateTopicKBNode(level int16, kind int16, kbID string) error {
	if level == 2 && kind == 0 && strings.TrimSpace(kbID) == "" {
		return fmt.Errorf("TOPIC级别的RAG检索节点必须指定目标知识库")
	}
	return nil
}

func (s *IntentService) listAndValidateTargetNodes(ctx context.Context, ids []string) ([]model.IntentNode, []model.IntentNode, error) {
	if len(ids) == 0 {
		return nil, nil, fmt.Errorf("请至少选择一个节点")
	}
	normalizedIDs := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalizedIDs = append(normalizedIDs, id)
	}
	if len(normalizedIDs) == 0 {
		return nil, nil, fmt.Errorf("节点ID不能为空")
	}
	allNodes, err := s.intentRepo.ListAll(ctx)
	if err != nil {
		return nil, nil, err
	}
	nodeByID := make(map[string]model.IntentNode, len(allNodes))
	for _, node := range allNodes {
		nodeByID[node.ID] = node
	}
	targetNodes := make([]model.IntentNode, 0, len(normalizedIDs))
	missingIDs := make([]string, 0)
	for _, id := range normalizedIDs {
		node, ok := nodeByID[id]
		if !ok {
			missingIDs = append(missingIDs, id)
			if len(missingIDs) == 5 {
				break
			}
			continue
		}
		targetNodes = append(targetNodes, node)
	}
	if len(targetNodes) != len(normalizedIDs) {
		return nil, nil, fmt.Errorf("节点不存在或已删除: %v", missingIDs)
	}
	return targetNodes, allNodes, nil
}

func validateBatchDisableDescendants(targetNodes, allNodes []model.IntentNode) error {
	targetIDSet := intentNodeIDSet(targetNodes)
	childrenMap := buildIntentChildrenMap(allNodes)
	for _, targetNode := range targetNodes {
		descendants := collectIntentDescendants(targetNode.IntentCode, childrenMap)
		enabledButNotSelected := make([]model.IntentNode, 0)
		for _, descendant := range descendants {
			if descendant.Enabled == 1 {
				if _, ok := targetIDSet[descendant.ID]; !ok {
					enabledButNotSelected = append(enabledButNotSelected, descendant)
				}
			}
		}
		if len(enabledButNotSelected) > 0 {
			return fmt.Errorf(
				"批量停用失败：节点 [%s] 存在已启用的子节点未包含在本次操作中（如：%s），请先选择全量子节点",
				targetNode.Name,
				summarizeIntentNodeNames(enabledButNotSelected),
			)
		}
	}
	return nil
}

func validateBatchDeleteDescendants(targetNodes, allNodes []model.IntentNode) error {
	targetIDSet := intentNodeIDSet(targetNodes)
	childrenMap := buildIntentChildrenMap(allNodes)
	for _, targetNode := range targetNodes {
		descendants := collectIntentDescendants(targetNode.IntentCode, childrenMap)
		notSelectedDescendants := make([]model.IntentNode, 0)
		enabledNotSelected := make([]model.IntentNode, 0)
		for _, descendant := range descendants {
			if _, ok := targetIDSet[descendant.ID]; ok {
				continue
			}
			notSelectedDescendants = append(notSelectedDescendants, descendant)
			if descendant.Enabled == 1 {
				enabledNotSelected = append(enabledNotSelected, descendant)
			}
		}
		if len(enabledNotSelected) > 0 {
			return fmt.Errorf(
				"批量删除失败：节点 [%s] 存在已启用的子节点未包含在本次操作中（如：%s），请先停用子节点或选择全量子节点",
				targetNode.Name,
				summarizeIntentNodeNames(enabledNotSelected),
			)
		}
		if len(notSelectedDescendants) > 0 {
			return fmt.Errorf(
				"批量删除失败：节点 [%s] 未包含全量子节点（如：%s），请先勾选完整子树后再删除",
				targetNode.Name,
				summarizeIntentNodeNames(notSelectedDescendants),
			)
		}
	}
	return nil
}

func intentNodeIDSet(nodes []model.IntentNode) map[string]struct{} {
	result := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		result[node.ID] = struct{}{}
	}
	return result
}

func buildIntentChildrenMap(nodes []model.IntentNode) map[string][]model.IntentNode {
	result := make(map[string][]model.IntentNode)
	for _, node := range nodes {
		parentCode := node.ParentCode
		if parentCode == "" {
			parentCode = "ROOT"
		}
		result[parentCode] = append(result[parentCode], node)
	}
	return result
}

func collectIntentDescendants(intentCode string, childrenMap map[string][]model.IntentNode) []model.IntentNode {
	if strings.TrimSpace(intentCode) == "" {
		return nil
	}
	result := make([]model.IntentNode, 0)
	stack := append([]model.IntentNode(nil), childrenMap[intentCode]...)
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		result = append(result, current)
		children := childrenMap[current.IntentCode]
		for i := len(children) - 1; i >= 0; i-- {
			stack = append(stack, children[i])
		}
	}
	return result
}

func summarizeIntentNodeNames(nodes []model.IntentNode) string {
	limit := len(nodes)
	if limit > 3 {
		limit = 3
	}
	names := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		name := strings.TrimSpace(nodes[i].Name)
		if name == "" {
			name = nodes[i].IntentCode
		}
		names = append(names, name)
	}
	return strings.Join(names, "、")
}

func validateTopK(topK int) error {
	if topK <= 0 {
		return fmt.Errorf("节点级 TopK 必须大于 0")
	}
	return nil
}

func normalizeCreateEnabled(enabled int16, enabledSet bool) int16 {
	if !enabledSet && enabled == 0 {
		return 1
	}
	return enabled
}

func (s *IntentService) resolveCollectionName(ctx context.Context, kbID, fallback string) (string, error) {
	kbID = strings.TrimSpace(kbID)
	if kbID == "" {
		return fallback, nil
	}
	var kb knowledgeModel.KnowledgeBase
	if err := s.db.WithContext(ctx).Scopes(frameworkDB.NotDeletedScope()).
		Where("id = ?", kbID).
		First(&kb).Error; err != nil {
		return "", fmt.Errorf("查询知识库失败: %w", err)
	}
	return kb.CollectionName, nil
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

func (s *IntentService) clearQueryTermMappingCache(ctx context.Context, domain string) {
	if s == nil || s.termCache == nil {
		return
	}
	if err := s.termCache.ClearMappings(ctx, strings.TrimSpace(domain)); err != nil {
		slog.Warn("clear query term mappings cache failed", "domain", domain, "err", err)
	}
}

func (s *IntentService) clearIntentNodeCache(ctx context.Context) {
	if s == nil || s.intentCache == nil {
		return
	}
	if err := s.intentCache.ClearNodes(ctx); err != nil {
		slog.Warn("clear intent tree cache failed", "err", err)
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
		m.Domain = strings.TrimSpace(*req.Domain)
	}
	if req.SourceTerm != nil {
		m.SourceTerm = strings.TrimSpace(*req.SourceTerm)
	}
	if req.TargetTerm != nil {
		m.TargetTerm = strings.TrimSpace(*req.TargetTerm)
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
		m.Remark = strings.TrimSpace(*req.Remark)
	}
}

// Ensure gorm import is used.
var _ = gorm.ErrRecordNotFound
