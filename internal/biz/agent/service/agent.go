package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	agentDto "go-base-agent/internal/biz/agent/dto"
	agentModel "go-base-agent/internal/biz/agent/model"
	agentRepo "go-base-agent/internal/biz/agent/repo"
	auditService "go-base-agent/internal/biz/audit/service"
)

// AgentService 智能体管理业务服务。
type AgentService struct {
	repo          *agentRepo.AgentRepo
	mode          string
	auditRecorder *auditService.BizChangeLogService
	prompts       *PromptResolver
}

// NewAgentService 创建 AgentService。
func NewAgentService(repo *agentRepo.AgentRepo, mode string) *AgentService {
	return &AgentService{
		repo: repo,
		mode: normalizeMode(mode),
	}
}

// SetAuditRecorder 设置审计记录器。
func (s *AgentService) SetAuditRecorder(recorder *auditService.BizChangeLogService) {
	s.auditRecorder = recorder
}

// SetPromptResolver 设置运行时提示词解析器。
func (s *AgentService) SetPromptResolver(resolver *PromptResolver) {
	s.prompts = resolver
}

// List 查询全部智能体。
func (s *AgentService) List(ctx context.Context) (*agentDto.AgentProfileListResp, error) {
	profiles, err := s.repo.ListProfiles(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(profiles, func(i, j int) bool {
		if profiles[i].Builtin != profiles[j].Builtin {
			return profiles[i].Builtin > profiles[j].Builtin
		}
		if !profiles[i].CreateTime.Equal(profiles[j].CreateTime) {
			return profiles[i].CreateTime.Before(profiles[j].CreateTime)
		}
		return profiles[i].ID < profiles[j].ID
	})

	configuredRefs, err := s.repo.ListConfiguredPromptRefs(ctx)
	if err != nil {
		return nil, err
	}
	configured := make(map[string][]PromptSlot, len(configuredRefs))
	for _, ref := range configuredRefs {
		if slot, ok := FindPromptSlot(ref.SlotKey); ok {
			configured[ref.AgentID] = append(configured[ref.AgentID], slot)
		}
	}

	effectiveSlotsTotal := len(EffectivePromptSlots(s.mode))
	agents := make([]agentDto.AgentProfileResp, 0, len(profiles))
	for _, profile := range profiles {
		ownSlots := configured[profile.ID]
		effectiveSlots := 0
		for _, slot := range ownSlots {
			if slot.EffectiveIn(s.mode) {
				effectiveSlots++
			}
		}
		agents = append(agents, toProfileResp(profile, effectiveSlots, len(ownSlots)-effectiveSlots))
	}

	return &agentDto.AgentProfileListResp{
		Mode:               s.mode,
		EffectiveSlotTotal: effectiveSlotsTotal,
		Agents:             agents,
	}, nil
}

// Create 创建智能体。
func (s *AgentService) Create(ctx context.Context, req agentDto.CreateAgentProfileReq) (string, error) {
	name, err := trimRequiredName(req.Name)
	if err != nil {
		return "", err
	}
	avatar, err := trimAvatar(req.Avatar)
	if err != nil {
		return "", err
	}
	description := trimToNull(req.Description)
	duplicated, err := s.repo.ProfileNameExists(ctx, name, "")
	if err != nil {
		return "", err
	}
	if duplicated {
		return "", fmt.Errorf("智能体名称已存在")
	}

	profile := &agentModel.AgentProfile{
		Name:        name,
		Description: description,
		Avatar:      avatar,
		Builtin:     0,
		Active:      0,
	}
	if err := s.repo.CreateProfile(ctx, profile); err != nil {
		return "", err
	}
	s.refreshPromptResolver(ctx)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:       auditService.BizTypeAgentProfile,
		BizID:         profile.ID,
		OperationType: auditService.OperationCreate,
		ActionDesc:    "创建智能体",
		AfterSnapshot: profile,
	})
	return profile.ID, nil
}

// Update 更新智能体。
func (s *AgentService) Update(ctx context.Context, id string, req agentDto.UpdateAgentProfileReq) (*agentDto.AgentProfileResp, error) {
	before, err := s.mustLoadEditable(ctx, id)
	if err != nil {
		return nil, err
	}
	name, err := trimRequiredUpdateName(req.Name)
	if err != nil {
		return nil, err
	}
	avatar, err := trimUpdateAvatar(req.Avatar)
	if err != nil {
		return nil, err
	}
	description := updateValue(req.Description)

	duplicated, err := s.repo.ProfileNameExists(ctx, name, id)
	if err != nil {
		return nil, err
	}
	if duplicated {
		return nil, fmt.Errorf("智能体名称已存在")
	}
	if err := s.repo.UpdateProfile(ctx, id, map[string]any{
		"name":        name,
		"description": description,
		"avatar":      avatar,
	}); err != nil {
		return nil, err
	}
	s.refreshPromptResolver(ctx)
	after, err := s.mustLoad(ctx, id)
	if err != nil {
		return nil, err
	}
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeAgentProfile,
		BizID:          id,
		OperationType:  auditService.OperationUpdate,
		ActionDesc:     "更新智能体",
		BeforeSnapshot: before,
		AfterSnapshot:  after,
	})
	resp := toProfileResp(*after, 0, 0)
	return &resp, nil
}

// Delete 删除智能体。
func (s *AgentService) Delete(ctx context.Context, id string) error {
	var before *agentModel.AgentProfile
	err := s.repo.Transaction(ctx, func(tx *agentRepo.AgentRepo) error {
		profile, err := tx.FindProfileByID(ctx, id)
		if err != nil {
			return err
		}
		if profile == nil {
			return fmt.Errorf("智能体不存在")
		}
		if profile.Builtin == 1 {
			return fmt.Errorf("内置智能体不可编辑或删除，如需调整请复制一份新建")
		}
		if profile.Active == 1 {
			return fmt.Errorf("该智能体正在激活中，请先激活其他智能体再删除")
		}
		before = profile
		if err := tx.SoftDeletePromptsByAgentID(ctx, id); err != nil {
			return err
		}
		if err := tx.SoftDeleteProfile(ctx, profile); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.refreshPromptResolver(ctx)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeAgentProfile,
		BizID:          id,
		OperationType:  auditService.OperationDelete,
		ActionDesc:     "删除智能体",
		BeforeSnapshot: before,
	})
	return nil
}

// Activate 激活指定智能体。
func (s *AgentService) Activate(ctx context.Context, id string) error {
	var before *agentModel.AgentProfile
	err := s.repo.Transaction(ctx, func(tx *agentRepo.AgentRepo) error {
		profile, err := tx.FindProfileByID(ctx, id)
		if err != nil {
			return err
		}
		if profile == nil {
			return fmt.Errorf("智能体不存在")
		}
		before = profile
		if err := tx.ClearActiveProfiles(ctx); err != nil {
			return err
		}
		if err := tx.SetActiveProfile(ctx, id); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.refreshPromptResolver(ctx)
	after, err := s.mustLoad(ctx, id)
	if err != nil {
		return err
	}
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeAgentProfile,
		BizID:          id,
		OperationType:  auditService.OperationUpdate,
		ActionDesc:     "激活智能体",
		BeforeSnapshot: before,
		AfterSnapshot:  after,
	})
	return nil
}

// LoadPrompts 查询某智能体的槽位配置。
func (s *AgentService) LoadPrompts(ctx context.Context, id string) (*agentDto.AgentPromptConfigResp, error) {
	profile, err := s.mustLoad(ctx, id)
	if err != nil {
		return nil, err
	}
	builtin, err := s.repo.FindBuiltinProfile(ctx)
	if err != nil {
		return nil, err
	}
	own, err := s.loadOwnPrompts(ctx, id)
	if err != nil {
		return nil, err
	}
	slots := make([]agentDto.AgentPromptSlotResp, 0, len(AllPromptSlots()))
	for _, slot := range AllPromptSlots() {
		content := own[slot.Key]
		slots = append(slots, agentDto.AgentPromptSlotResp{
			SlotKey:              slot.Key,
			DisplayName:          slot.DisplayName,
			Group:                slot.Group,
			GroupName:            slot.GroupName,
			Effective:            slot.EffectiveIn(s.mode),
			InactiveReason:       inactiveReason(slot, s.mode),
			EditorHint:           slot.EditorHint,
			RequiredPlaceholders: append([]string(nil), slot.RequiredPlaceholders...),
			Content:              content,
		})
	}
	return &agentDto.AgentPromptConfigResp{
		AgentID:          profile.ID,
		AgentName:        profile.Name,
		Builtin:          profile.Builtin == 1,
		DefaultAgentName: builtinName(builtin),
		Mode:             s.mode,
		Slots:            slots,
	}, nil
}

// SavePrompt 保存单个槽位提示词。
func (s *AgentService) SavePrompt(ctx context.Context, id, slotKey string, req agentDto.SaveAgentPromptReq) error {
	if _, err := s.mustLoadEditable(ctx, id); err != nil {
		return err
	}
	slot, ok := FindPromptSlot(slotKey)
	if !ok {
		return fmt.Errorf("未知的提示词：%s", slotKey)
	}
	content := strings.TrimSpace(req.Content)
	if err := assertRequiredPlaceholders(slot, content); err != nil {
		return err
	}
	existed, err := s.repo.PromptExists(ctx, id, slot.Key)
	if err != nil {
		return err
	}
	if existed {
		if err := s.repo.UpdatePromptContent(ctx, id, slot.Key, blankToNil(content)); err != nil {
			return err
		}
	} else if content != "" {
		if err := s.repo.CreatePrompt(ctx, &agentModel.AgentPrompt{
			AgentID: id,
			SlotKey: slot.Key,
			Content: content,
		}); err != nil {
			return err
		}
	}
	s.refreshPromptResolver(ctx)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:       auditService.BizTypeAgentProfile,
		BizID:         id,
		OperationType: auditService.OperationUpdate,
		ActionDesc:    "更新智能体提示词",
	})
	return nil
}

// DefaultPrompt 查询内置智能体的默认槽位提示词。
func (s *AgentService) DefaultPrompt(ctx context.Context, slotKey string) (string, error) {
	slot, ok := FindPromptSlot(slotKey)
	if !ok {
		return "", fmt.Errorf("未知的提示词：%s", slotKey)
	}
	builtin, err := s.repo.FindBuiltinProfile(ctx)
	if err != nil {
		return "", err
	}
	if builtin == nil {
		return "", nil
	}
	prompt, err := s.repo.FindPromptByAgentAndSlot(ctx, builtin.ID, slot.Key)
	if err != nil {
		return "", err
	}
	if prompt == nil {
		return "", nil
	}
	return prompt.Content, nil
}

func (s *AgentService) loadOwnPrompts(ctx context.Context, agentID string) (map[string]string, error) {
	prompts, err := s.repo.ListPromptsByAgentID(ctx, agentID)
	if err != nil {
		return nil, err
	}
	own := make(map[string]string, len(prompts))
	for _, prompt := range prompts {
		own[prompt.SlotKey] = prompt.Content
	}
	return own, nil
}

func (s *AgentService) mustLoad(ctx context.Context, id string) (*agentModel.AgentProfile, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("智能体不存在")
	}
	profile, err := s.repo.FindProfileByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, fmt.Errorf("智能体不存在")
	}
	return profile, nil
}

func (s *AgentService) mustLoadEditable(ctx context.Context, id string) (*agentModel.AgentProfile, error) {
	profile, err := s.mustLoad(ctx, id)
	if err != nil {
		return nil, err
	}
	if profile.Builtin == 1 {
		return nil, fmt.Errorf("内置智能体不可编辑或删除，如需调整请复制一份新建")
	}
	return profile, nil
}

func (s *AgentService) recordAudit(ctx context.Context, req auditService.RecordReq) {
	if s.auditRecorder == nil {
		return
	}
	if err := s.auditRecorder.Record(ctx, req); err != nil {
		slog.Warn("audit record failed", "err", err, "biz_type", req.BizType, "biz_id", req.BizID)
	}
}

func (s *AgentService) refreshPromptResolver(ctx context.Context) {
	if s == nil || s.prompts == nil {
		return
	}
	if err := s.prompts.Refresh(ctx); err != nil {
		slog.Warn("refresh agent prompt resolver failed", "err", err)
	}
}

func toProfileResp(profile agentModel.AgentProfile, effectiveSlots, inactiveSlots int) agentDto.AgentProfileResp {
	return agentDto.AgentProfileResp{
		ID:             profile.ID,
		Name:           profile.Name,
		Description:    profile.Description,
		Avatar:         profile.Avatar,
		Builtin:        profile.Builtin == 1,
		Active:         profile.Active == 1,
		EffectiveSlots: effectiveSlots,
		InactiveSlots:  inactiveSlots,
		CreateTime:     profile.CreateTime,
		UpdateTime:     profile.UpdateTime,
	}
}

func normalizeMode(mode string) string {
	trimmed := strings.ToUpper(strings.TrimSpace(mode))
	if trimmed == "" {
		return ModeWorkflow
	}
	return trimmed
}

func trimRequiredName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		return "", fmt.Errorf("智能体名称不能为空")
	}
	return name, nil
}

func trimRequiredUpdateName(value *string) (string, error) {
	if value == nil {
		return "", fmt.Errorf("智能体名称不能为空")
	}
	return trimRequiredName(*value)
}

func trimAvatar(value string) (string, error) {
	avatar := strings.TrimSpace(value)
	if len(avatar) > 32 {
		return "", fmt.Errorf("头像标识过长")
	}
	return avatar, nil
}

func trimUpdateAvatar(value *string) (string, error) {
	if value == nil {
		return "", nil
	}
	return trimAvatar(*value)
}

func trimToNull(value string) string {
	trimmed := strings.TrimSpace(value)
	return trimmed
}

func updateValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func blankToNil(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	trimmed := strings.TrimSpace(value)
	return &trimmed
}

func inactiveReason(slot PromptSlot, mode string) string {
	if slot.EffectiveIn(mode) {
		return ""
	}
	return slot.InactiveReason
}

func builtinName(profile *agentModel.AgentProfile) string {
	if profile == nil {
		return ""
	}
	return profile.Name
}

func assertRequiredPlaceholders(slot PromptSlot, content string) error {
	if strings.TrimSpace(content) == "" || len(slot.RequiredPlaceholders) == 0 {
		return nil
	}
	missing := make([]string, 0)
	for _, placeholder := range slot.RequiredPlaceholders {
		if !strings.Contains(content, placeholder) {
			missing = append(missing, placeholder)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("「%s」缺少必需占位符：%s", slot.DisplayName, strings.Join(missing, "、"))
}
