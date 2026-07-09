package service

import (
	"context"
	"fmt"

	adminDto "go-base-agent/internal/biz/admin/dto"
	adminModel "go-base-agent/internal/biz/admin/model"
	adminRepo "go-base-agent/internal/biz/admin/repo"
	userModel "go-base-agent/internal/biz/user/model"
	"go-base-agent/internal/framework/db"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// AdminService 管理后台业务服务。
type AdminService struct {
	adminRepo   *adminRepo.AdminRepo
	sampleQRepo *adminRepo.SampleQuestionRepo
	db          *gorm.DB
}

// NewAdminService 创建 AdminService。
func NewAdminService(
	adminRepo *adminRepo.AdminRepo,
	sampleQRepo *adminRepo.SampleQuestionRepo,
	database *gorm.DB,
) *AdminService {
	return &AdminService{
		adminRepo:   adminRepo,
		sampleQRepo: sampleQRepo,
		db:          database,
	}
}

// GetDashboard 获取仪表盘统计。
func (s *AdminService) GetDashboard(ctx context.Context) (*adminDto.DashboardResp, error) {
	stats, err := s.adminRepo.GetDashboard(ctx)
	if err != nil {
		return nil, err
	}
	return &adminDto.DashboardResp{
		KnowledgeBaseCount: stats.KnowledgeBaseCount,
		DocumentCount:      stats.DocumentCount,
		ChunkCount:         stats.ChunkCount,
		UserCount:          stats.UserCount,
		ConversationCount:  stats.ConversationCount,
		MessageCount:       stats.MessageCount,
		VectorCount:        stats.VectorCount,
	}, nil
}

// ListTraceRuns 查询链路追踪运行记录。
func (s *AdminService) ListTraceRuns(ctx context.Context, page, size int) ([]adminDto.TraceRunResp, int64, error) {
	runs, total, err := s.adminRepo.ListTraceRuns(ctx, page, size)
	if err != nil {
		return nil, 0, err
	}
	resp := make([]adminDto.TraceRunResp, 0, len(runs))
	for _, r := range runs {
		resp = append(resp, adminDto.TraceRunResp{
			ID:             r.ID,
			TraceID:        r.TraceID,
			TraceName:      r.TraceName,
			ConversationID: r.ConversationID,
			TaskID:         r.TaskID,
			UserID:         r.UserID,
			Status:         r.Status,
			ErrorMessage:   r.ErrorMessage,
			StartTime:      r.StartTime,
			EndTime:        r.EndTime,
			DurationMs:     r.DurationMs,
		})
	}
	return resp, total, nil
}

// GetTraceDetail 获取链路详情。
func (s *AdminService) GetTraceDetail(ctx context.Context, traceID string) (*adminDto.TraceDetailResp, error) {
	run, err := s.adminRepo.GetTraceRun(ctx, traceID)
	if err != nil {
		return nil, err
	}
	nodes, err := s.adminRepo.GetTraceNodes(ctx, traceID)
	if err != nil {
		return nil, err
	}

	runResp := &adminDto.TraceRunResp{
		ID: run.ID, TraceID: run.TraceID, TraceName: run.TraceName,
		ConversationID: run.ConversationID, TaskID: run.TaskID,
		UserID: run.UserID, Status: run.Status, ErrorMessage: run.ErrorMessage,
		StartTime: run.StartTime, EndTime: run.EndTime, DurationMs: run.DurationMs,
	}

	nodeResp := make([]adminDto.TraceNodeResp, 0, len(nodes))
	for _, n := range nodes {
		nodeResp = append(nodeResp, adminDto.TraceNodeResp{
			ID: n.ID, TraceID: n.TraceID, NodeID: n.NodeID,
			ParentNodeID: n.ParentNodeID, Depth: n.Depth,
			NodeType: n.NodeType, NodeName: n.NodeName, Status: n.Status,
			ErrorMessage: n.ErrorMessage, StartTime: n.StartTime,
			EndTime: n.EndTime, DurationMs: n.DurationMs,
		})
	}

	return &adminDto.TraceDetailResp{Run: runResp, Nodes: nodeResp}, nil
}

// --- 示例问题 ---

// CreateSampleQuestion 创建示例问题。
func (s *AdminService) CreateSampleQuestion(ctx context.Context, req adminDto.CreateSampleQuestionReq) (*adminDto.SampleQuestionResp, error) {
	sq := &adminModel.SampleQuestion{
		Title:       req.Title,
		Description: req.Description,
		Question:    req.Question,
	}
	if err := s.sampleQRepo.Create(ctx, sq); err != nil {
		return nil, fmt.Errorf("创建示例问题失败: %w", err)
	}
	return toSampleQResp(sq), nil
}

// ListSampleQuestions 查询示例问题。
func (s *AdminService) ListSampleQuestions(ctx context.Context, page, size int) ([]adminDto.SampleQuestionResp, int64, error) {
	items, total, err := s.sampleQRepo.List(ctx, page, size)
	if err != nil {
		return nil, 0, err
	}
	resp := make([]adminDto.SampleQuestionResp, 0, len(items))
	for _, sq := range items {
		resp = append(resp, *toSampleQResp(&sq))
	}
	return resp, total, nil
}

// UpdateSampleQuestion 更新示例问题。
func (s *AdminService) UpdateSampleQuestion(ctx context.Context, id string, req adminDto.UpdateSampleQuestionReq) (*adminDto.SampleQuestionResp, error) {
	sq, err := s.sampleQRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if req.Title != nil {
		sq.Title = *req.Title
	}
	if req.Description != nil {
		sq.Description = *req.Description
	}
	if req.Question != nil {
		sq.Question = *req.Question
	}
	if err := s.sampleQRepo.Update(ctx, sq); err != nil {
		return nil, fmt.Errorf("更新示例问题失败: %w", err)
	}
	return toSampleQResp(sq), nil
}

// DeleteSampleQuestion 删除示例问题。
func (s *AdminService) DeleteSampleQuestion(ctx context.Context, id string) error {
	return s.sampleQRepo.SoftDelete(ctx, id)
}

// --- 用户管理 ---

// ListUsers 查询所有用户。
func (s *AdminService) ListUsers(ctx context.Context, page, size int) ([]adminDto.UserResp, int64, error) {
	var (
		users []userModel.User
		total int64
	)
	query := s.db.WithContext(ctx).Scopes(db.NotDeletedScope()).Model(&userModel.User{})
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}
	if err := query.Scopes(db.Paginate(page, size)).Order("create_time DESC").Find(&users).Error; err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}
	resp := make([]adminDto.UserResp, 0, len(users))
	for _, u := range users {
		resp = append(resp, adminDto.UserResp{
			ID: u.ID, Username: u.Username, Role: u.Role,
			Avatar: u.Avatar, CreateTime: u.CreateTime, UpdateTime: u.UpdateTime,
		})
	}
	return resp, total, nil
}

// CreateUser 管理员创建用户。
func (s *AdminService) CreateUser(ctx context.Context, req adminDto.CreateUserReq) (*adminDto.UserResp, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("加密密码失败: %w", err)
	}
	role := req.Role
	if role == "" {
		role = "user"
	}
	u := &userModel.User{
		Username: req.Username,
		Password: string(hashed),
		Role:     role,
		Avatar:   req.Avatar,
	}
	if err := s.db.WithContext(ctx).Create(u).Error; err != nil {
		return nil, fmt.Errorf("创建用户失败: %w", err)
	}
	return &adminDto.UserResp{
		ID: u.ID, Username: u.Username, Role: u.Role,
		Avatar: u.Avatar, CreateTime: u.CreateTime, UpdateTime: u.UpdateTime,
	}, nil
}

// UpdateUser 管理员更新用户。
func (s *AdminService) UpdateUser(ctx context.Context, id string, req adminDto.UpdateUserReq) (*adminDto.UserResp, error) {
	var u userModel.User
	if err := s.db.WithContext(ctx).Scopes(db.NotDeletedScope()).Where("id = ?", id).First(&u).Error; err != nil {
		return nil, fmt.Errorf("用户不存在: %w", err)
	}
	updates := map[string]interface{}{}
	if req.Username != nil {
		updates["username"] = *req.Username
	}
	if req.Password != nil {
		hashed, _ := bcrypt.GenerateFromPassword([]byte(*req.Password), bcrypt.DefaultCost)
		updates["password"] = string(hashed)
	}
	if req.Role != nil {
		updates["role"] = *req.Role
	}
	if req.Avatar != nil {
		updates["avatar"] = *req.Avatar
	}
	if len(updates) > 0 {
		updates["update_time"] = gorm.Expr("CURRENT_TIMESTAMP")
		if err := s.db.WithContext(ctx).Model(&u).Updates(updates).Error; err != nil {
			return nil, fmt.Errorf("更新用户失败: %w", err)
		}
	}
	return &adminDto.UserResp{
		ID: u.ID, Username: u.Username, Role: u.Role,
		Avatar: u.Avatar, CreateTime: u.CreateTime, UpdateTime: u.UpdateTime,
	}, nil
}

// DeleteUser 软删除用户。
func (s *AdminService) DeleteUser(ctx context.Context, id string) error {
	var u userModel.User
	u.ID = id
	return db.SoftDelete(s.db.WithContext(ctx), &u)
}

// --- helpers ---

func toSampleQResp(sq *adminModel.SampleQuestion) *adminDto.SampleQuestionResp {
	return &adminDto.SampleQuestionResp{
		ID:          sq.ID,
		Title:       sq.Title,
		Description: sq.Description,
		Question:    sq.Question,
		CreateTime:  sq.CreateTime,
		UpdateTime:  sq.UpdateTime,
	}
}
