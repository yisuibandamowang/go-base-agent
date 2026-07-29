package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	adminDto "go-base-agent/internal/biz/admin/dto"
	adminModel "go-base-agent/internal/biz/admin/model"
	adminRepo "go-base-agent/internal/biz/admin/repo"
	auditService "go-base-agent/internal/biz/audit/service"
	userModel "go-base-agent/internal/biz/user/model"
	"go-base-agent/internal/framework/db"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// AdminService 管理后台业务服务。
type AdminService struct {
	adminRepo     *adminRepo.AdminRepo
	sampleQRepo   *adminRepo.SampleQuestionRepo
	db            *gorm.DB
	auditRecorder *auditService.BizChangeLogService
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

// SetAuditRecorder 设置审计日志记录器。
func (s *AdminService) SetAuditRecorder(recorder *auditService.BizChangeLogService) {
	s.auditRecorder = recorder
}

// GetDashboard 获取仪表盘统计。
func (s *AdminService) GetDashboard(ctx context.Context, window string) (*adminDto.DashboardResp, error) {
	stats, err := s.adminRepo.GetDashboard(ctx)
	if err != nil {
		return nil, err
	}
	rangeInfo := dashboardWindow(window, 24*time.Hour)
	usersInWindow, err := s.countByTimeRange(ctx, "t_user", "create_time", rangeInfo.Start, rangeInfo.End)
	if err != nil {
		return nil, err
	}
	sessionsInWindow, err := s.countByTimeRange(ctx, "t_conversation", "create_time", rangeInfo.Start, rangeInfo.End)
	if err != nil {
		return nil, err
	}
	sessionsPrevWindow, err := s.countByTimeRange(ctx, "t_conversation", "create_time", rangeInfo.PrevStart, rangeInfo.PrevEnd)
	if err != nil {
		return nil, err
	}
	messagesInWindow, err := s.countByTimeRange(ctx, "t_message", "create_time", rangeInfo.Start, rangeInfo.End)
	if err != nil {
		return nil, err
	}
	messagesPrevWindow, err := s.countByTimeRange(ctx, "t_message", "create_time", rangeInfo.PrevStart, rangeInfo.PrevEnd)
	if err != nil {
		return nil, err
	}
	activeUsers, err := s.countDistinctUsersByTimeRange(ctx, rangeInfo.Start, rangeInfo.End)
	if err != nil {
		return nil, err
	}
	activeUsersPrev, err := s.countDistinctUsersByTimeRange(ctx, rangeInfo.PrevStart, rangeInfo.PrevEnd)
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
		Window:             rangeInfo.Label,
		CompareWindow:      rangeInfo.CompareLabel,
		UpdatedAt:          time.Now().UnixMilli(),
		Kpis: &adminDto.DashboardKpisResp{
			TotalUsers:    dashboardKpi(stats.UserCount, usersInWindow, nil),
			ActiveUsers:   dashboardKpi(activeUsers, activeUsers-activeUsersPrev, dashboardPct(activeUsers, activeUsersPrev)),
			TotalSessions: dashboardKpi(stats.ConversationCount, sessionsInWindow, nil),
			Sessions24h:   dashboardKpi(sessionsInWindow, sessionsInWindow-sessionsPrevWindow, dashboardPct(sessionsInWindow, sessionsPrevWindow)),
			TotalMessages: dashboardKpi(stats.MessageCount, messagesInWindow, nil),
			Messages24h:   dashboardKpi(messagesInWindow, messagesInWindow-messagesPrevWindow, dashboardPct(messagesInWindow, messagesPrevWindow)),
		},
	}, nil
}

// ListTraceRuns 查询链路追踪运行记录。
func (s *AdminService) ListTraceRuns(ctx context.Context, page, size int, req adminDto.TraceRunPageReq) ([]adminDto.TraceRunResp, int64, error) {
	runs, total, err := s.adminRepo.ListTraceRuns(ctx, page, size, adminRepo.TraceRunFilter{
		TraceID:        req.TraceID,
		ConversationID: req.ConversationID,
		TaskID:         req.TaskID,
		Status:         req.Status,
	})
	if err != nil {
		return nil, 0, err
	}
	usernameMap, err := s.loadTraceUsernames(ctx, runs)
	if err != nil {
		return nil, 0, err
	}
	ttftMap, err := s.loadTraceTTFT(ctx, runs)
	if err != nil {
		return nil, 0, err
	}
	resp := make([]adminDto.TraceRunResp, 0, len(runs))
	for _, r := range runs {
		resp = append(resp, toTraceRunResp(r, usernameMap, ttftMap))
	}
	return resp, total, nil
}

// ListTraceNodes 查询指定链路的节点记录。
func (s *AdminService) ListTraceNodes(ctx context.Context, traceID string) ([]adminDto.TraceNodeResp, error) {
	nodes, err := s.adminRepo.GetTraceNodes(ctx, traceID)
	if err != nil {
		return nil, err
	}
	nodeResp := make([]adminDto.TraceNodeResp, 0, len(nodes))
	for _, n := range nodes {
		nodeResp = append(nodeResp, adminDto.TraceNodeResp{
			ID: n.ID, TraceID: n.TraceID, NodeID: n.NodeID,
			ParentNodeID: n.ParentNodeID, Depth: n.Depth,
			NodeType: n.NodeType, NodeName: n.NodeName, Status: n.Status,
			ClassName: n.ClassName, MethodName: n.MethodName,
			ErrorMessage: n.ErrorMessage, StartTime: n.StartTime,
			EndTime: n.EndTime, DurationMs: n.DurationMs,
		})
	}
	return nodeResp, nil
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
	usernameMap, err := s.loadTraceUsernames(ctx, []adminRepo.TraceRun{*run})
	if err != nil {
		return nil, err
	}
	ttftMap, err := s.loadTraceTTFT(ctx, []adminRepo.TraceRun{*run})
	if err != nil {
		return nil, err
	}

	runResp := toTraceRunResp(*run, usernameMap, ttftMap)

	nodeResp := make([]adminDto.TraceNodeResp, 0, len(nodes))
	for _, n := range nodes {
		nodeResp = append(nodeResp, adminDto.TraceNodeResp{
			ID: n.ID, TraceID: n.TraceID, NodeID: n.NodeID,
			ParentNodeID: n.ParentNodeID, Depth: n.Depth,
			NodeType: n.NodeType, NodeName: n.NodeName, Status: n.Status,
			ClassName: n.ClassName, MethodName: n.MethodName,
			ErrorMessage: n.ErrorMessage, StartTime: n.StartTime,
			EndTime: n.EndTime, DurationMs: n.DurationMs,
		})
	}

	return &adminDto.TraceDetailResp{Run: &runResp, Nodes: nodeResp}, nil
}

func toTraceRunResp(run adminRepo.TraceRun, usernameMap map[string]string, ttftMap map[string]*int64) adminDto.TraceRunResp {
	return adminDto.TraceRunResp{
		ID:             run.ID,
		TraceID:        run.TraceID,
		TraceName:      run.TraceName,
		EntryMethod:    run.EntryMethod,
		ConversationID: run.ConversationID,
		TaskID:         run.TaskID,
		UserID:         run.UserID,
		Username:       usernameMap[run.UserID],
		Status:         run.Status,
		ErrorMessage:   run.ErrorMessage,
		Question:       parseTraceQuestion(run.ExtraData),
		StartTime:      run.StartTime,
		EndTime:        run.EndTime,
		DurationMs:     run.DurationMs,
		TTFTMs:         ttftMap[run.TraceID],
	}
}

func (s *AdminService) loadTraceUsernames(ctx context.Context, runs []adminRepo.TraceRun) (map[string]string, error) {
	userIDs := make([]string, 0, len(runs))
	seen := map[string]struct{}{}
	for _, run := range runs {
		userID := strings.TrimSpace(run.UserID)
		if userID == "" {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		userIDs = append(userIDs, userID)
	}
	if len(userIDs) == 0 {
		return map[string]string{}, nil
	}

	var users []struct {
		ID       string `gorm:"column:id"`
		Username string `gorm:"column:username"`
	}
	if err := s.db.WithContext(ctx).Table("t_user").
		Select("id, username").
		Where("deleted = 0 AND id IN ?", userIDs).
		Find(&users).Error; err != nil {
		return nil, fmt.Errorf("load trace usernames: %w", err)
	}
	usernameMap := make(map[string]string, len(users))
	for _, user := range users {
		usernameMap[user.ID] = user.Username
	}
	return usernameMap, nil
}

func (s *AdminService) loadTraceTTFT(ctx context.Context, runs []adminRepo.TraceRun) (map[string]*int64, error) {
	traceIDs := make([]string, 0, len(runs))
	seen := map[string]struct{}{}
	for _, run := range runs {
		traceID := strings.TrimSpace(run.TraceID)
		if traceID == "" {
			continue
		}
		if _, ok := seen[traceID]; ok {
			continue
		}
		seen[traceID] = struct{}{}
		traceIDs = append(traceIDs, traceID)
	}
	if len(traceIDs) == 0 {
		return map[string]*int64{}, nil
	}

	var rows []struct {
		TraceID    string `gorm:"column:trace_id"`
		DurationMs *int64 `gorm:"column:duration_ms"`
	}
	if err := s.db.WithContext(ctx).Table("t_rag_trace_node").
		Select("trace_id, duration_ms").
		Where("deleted = 0 AND trace_id IN ? AND node_type = ?", traceIDs, "USER_TTFT").
		Order("start_time ASC, id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load trace ttft: %w", err)
	}
	ttftMap := make(map[string]*int64, len(rows))
	for _, row := range rows {
		if row.DurationMs == nil {
			continue
		}
		if _, exists := ttftMap[row.TraceID]; !exists {
			value := *row.DurationMs
			ttftMap[row.TraceID] = &value
		}
	}
	return ttftMap, nil
}

func parseTraceQuestion(extraData string) string {
	if strings.TrimSpace(extraData) == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(extraData), &payload); err != nil {
		return ""
	}
	question, _ := payload["question"].(string)
	return question
}

// --- 示例问题 ---

const defaultSampleQuestionLimit = 3
const defaultAdminUsername = "admin"

// CreateSampleQuestion 创建示例问题。
func (s *AdminService) CreateSampleQuestion(ctx context.Context, req adminDto.CreateSampleQuestionReq) (*adminDto.SampleQuestionResp, error) {
	question := strings.TrimSpace(req.Question)
	if question == "" {
		return nil, fmt.Errorf("示例问题内容不能为空")
	}
	sq := &adminModel.SampleQuestion{
		Title:       strings.TrimSpace(req.Title),
		Description: strings.TrimSpace(req.Description),
		Question:    question,
	}
	if err := s.sampleQRepo.Create(ctx, sq); err != nil {
		return nil, fmt.Errorf("创建示例问题失败: %w", err)
	}
	resp := toSampleQResp(sq)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:       auditService.BizTypeSampleQuestion,
		BizID:         resp.ID,
		OperationType: auditService.OperationCreate,
		ActionDesc:    "创建示例问题：" + resp.Question,
		AfterSnapshot: resp,
	})
	return resp, nil
}

// ListSampleQuestions 查询示例问题。
func (s *AdminService) ListSampleQuestions(ctx context.Context, page, size int, keyword string) ([]adminDto.SampleQuestionResp, int64, error) {
	items, total, err := s.sampleQRepo.List(ctx, page, size, keyword)
	if err != nil {
		return nil, 0, err
	}
	resp := make([]adminDto.SampleQuestionResp, 0, len(items))
	for _, sq := range items {
		resp = append(resp, *toSampleQResp(&sq))
	}
	return resp, total, nil
}

// ListRandomSampleQuestions 随机查询欢迎页示例问题。
func (s *AdminService) ListRandomSampleQuestions(ctx context.Context) ([]adminDto.SampleQuestionResp, error) {
	items, err := s.sampleQRepo.ListRandom(ctx, defaultSampleQuestionLimit)
	if err != nil {
		return nil, err
	}
	resp := make([]adminDto.SampleQuestionResp, 0, len(items))
	for _, sq := range items {
		resp = append(resp, *toSampleQResp(&sq))
	}
	return resp, nil
}

// GetSampleQuestion 查询示例问题详情。
func (s *AdminService) GetSampleQuestion(ctx context.Context, id string) (*adminDto.SampleQuestionResp, error) {
	sq, err := s.sampleQRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return toSampleQResp(sq), nil
}

// UpdateSampleQuestion 更新示例问题。
func (s *AdminService) UpdateSampleQuestion(ctx context.Context, id string, req adminDto.UpdateSampleQuestionReq) (*adminDto.SampleQuestionResp, error) {
	sq, err := s.sampleQRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := toSampleQResp(sq)
	if req.Title != nil {
		sq.Title = strings.TrimSpace(*req.Title)
	}
	if req.Description != nil {
		sq.Description = strings.TrimSpace(*req.Description)
	}
	if req.Question != nil {
		question := strings.TrimSpace(*req.Question)
		if question == "" {
			return nil, fmt.Errorf("示例问题内容不能为空")
		}
		sq.Question = question
	}
	if err := s.sampleQRepo.Update(ctx, sq); err != nil {
		return nil, fmt.Errorf("更新示例问题失败: %w", err)
	}
	resp := toSampleQResp(sq)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeSampleQuestion,
		BizID:          resp.ID,
		OperationType:  auditService.OperationUpdate,
		ActionDesc:     "更新示例问题：" + resp.Question,
		BeforeSnapshot: before,
		AfterSnapshot:  resp,
	})
	return resp, nil
}

// DeleteSampleQuestion 删除示例问题。
func (s *AdminService) DeleteSampleQuestion(ctx context.Context, id string) error {
	sq, err := s.sampleQRepo.FindByID(ctx, id)
	if err != nil {
		return err
	}
	before := toSampleQResp(sq)
	if err := s.sampleQRepo.SoftDelete(ctx, id); err != nil {
		return err
	}
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeSampleQuestion,
		BizID:          id,
		OperationType:  auditService.OperationDelete,
		ActionDesc:     "删除示例问题：" + before.Question,
		BeforeSnapshot: before,
	})
	return nil
}

// --- 用户管理 ---

// ListUsers 查询所有用户。
func (s *AdminService) ListUsers(ctx context.Context, page, size int, keyword string) ([]adminDto.UserResp, int64, error) {
	var (
		users []userModel.User
		total int64
	)
	query := s.db.WithContext(ctx).Scopes(db.NotDeletedScope()).Model(&userModel.User{})
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where("username LIKE ? OR role LIKE ?", like, like)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}
	if err := query.Scopes(db.Paginate(page, size)).Order("update_time DESC").Find(&users).Error; err != nil {
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
	username := strings.TrimSpace(req.Username)
	password := strings.TrimSpace(req.Password)
	if username == "" {
		return nil, fmt.Errorf("用户名不能为空")
	}
	if password == "" {
		return nil, fmt.Errorf("密码不能为空")
	}
	if isDefaultAdminUsername(username) {
		return nil, fmt.Errorf("默认管理员用户名不可用")
	}
	if err := s.ensureUsernameAvailable(ctx, username, ""); err != nil {
		return nil, err
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("加密密码失败: %w", err)
	}
	role, err := normalizeUserRole(req.Role)
	if err != nil {
		return nil, err
	}
	u := &userModel.User{
		Username: username,
		Password: string(hashed),
		Role:     role,
		Avatar:   strings.TrimSpace(req.Avatar),
	}
	if err := s.db.WithContext(ctx).Create(u).Error; err != nil {
		return nil, fmt.Errorf("创建用户失败: %w", err)
	}
	resp := &adminDto.UserResp{
		ID: u.ID, Username: u.Username, Role: u.Role,
		Avatar: u.Avatar, CreateTime: u.CreateTime, UpdateTime: u.UpdateTime,
	}
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:       auditService.BizTypeUser,
		BizID:         resp.ID,
		OperationType: auditService.OperationCreate,
		ActionDesc:    "创建用户：" + resp.Username,
		AfterSnapshot: resp,
	})
	return resp, nil
}

// UpdateUser 管理员更新用户。
func (s *AdminService) UpdateUser(ctx context.Context, id string, req adminDto.UpdateUserReq) (*adminDto.UserResp, error) {
	var u userModel.User
	if err := s.db.WithContext(ctx).Scopes(db.NotDeletedScope()).Where("id = ?", id).First(&u).Error; err != nil {
		return nil, fmt.Errorf("用户不存在: %w", err)
	}
	if isDefaultAdminUsername(u.Username) {
		return nil, fmt.Errorf("默认管理员不允许修改或删除")
	}
	before := toUserResp(&u)
	updates := map[string]interface{}{}
	if req.Username != nil {
		username := strings.TrimSpace(*req.Username)
		if username == "" {
			return nil, fmt.Errorf("用户名不能为空")
		}
		if isDefaultAdminUsername(username) {
			return nil, fmt.Errorf("默认管理员用户名不可用")
		}
		if username != u.Username {
			if err := s.ensureUsernameAvailable(ctx, username, u.ID); err != nil {
				return nil, err
			}
		}
		updates["username"] = username
		u.Username = username
	}
	if req.Password != nil {
		password := strings.TrimSpace(*req.Password)
		if password == "" {
			return nil, fmt.Errorf("新密码不能为空")
		}
		hashed, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		updates["password"] = string(hashed)
	}
	if req.Role != nil {
		role, err := normalizeUserRole(*req.Role)
		if err != nil {
			return nil, err
		}
		updates["role"] = role
		u.Role = role
	}
	if req.Avatar != nil {
		avatar := strings.TrimSpace(*req.Avatar)
		updates["avatar"] = avatar
		u.Avatar = avatar
	}
	if len(updates) > 0 {
		updates["update_time"] = gorm.Expr("CURRENT_TIMESTAMP")
		if err := s.db.WithContext(ctx).Model(&u).Updates(updates).Error; err != nil {
			return nil, fmt.Errorf("更新用户失败: %w", err)
		}
	}
	resp := &adminDto.UserResp{
		ID: u.ID, Username: u.Username, Role: u.Role,
		Avatar: u.Avatar, CreateTime: u.CreateTime, UpdateTime: u.UpdateTime,
	}
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeUser,
		BizID:          resp.ID,
		OperationType:  auditService.OperationUpdate,
		ActionDesc:     "更新用户：" + resp.Username,
		BeforeSnapshot: before,
		AfterSnapshot:  resp,
	})
	return resp, nil
}

// DeleteUser 软删除用户。
func (s *AdminService) DeleteUser(ctx context.Context, id string) error {
	var u userModel.User
	if err := s.db.WithContext(ctx).Scopes(db.NotDeletedScope()).Where("id = ?", id).First(&u).Error; err != nil {
		return fmt.Errorf("用户不存在: %w", err)
	}
	if isDefaultAdminUsername(u.Username) {
		return fmt.Errorf("默认管理员不允许修改或删除")
	}
	before := toUserResp(&u)
	if err := db.SoftDelete(s.db.WithContext(ctx), &u); err != nil {
		return err
	}
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeUser,
		BizID:          u.ID,
		OperationType:  auditService.OperationDelete,
		ActionDesc:     "删除用户：" + u.Username,
		BeforeSnapshot: before,
	})
	return nil
}

func isDefaultAdminUsername(username string) bool {
	return strings.EqualFold(strings.TrimSpace(username), defaultAdminUsername)
}

func normalizeUserRole(role string) (string, error) {
	role = strings.TrimSpace(role)
	if role == "" {
		return "user", nil
	}
	if strings.EqualFold(role, "admin") {
		return "admin", nil
	}
	if strings.EqualFold(role, "user") {
		return "user", nil
	}
	return "", fmt.Errorf("角色类型不合法")
}

func (s *AdminService) ensureUsernameAvailable(ctx context.Context, username, excludeID string) error {
	var count int64
	query := s.db.WithContext(ctx).Scopes(db.NotDeletedScope()).Model(&userModel.User{}).
		Where("username = ?", username)
	if strings.TrimSpace(excludeID) != "" {
		query = query.Where("id <> ?", excludeID)
	}
	if err := query.Count(&count).Error; err != nil {
		return fmt.Errorf("检查用户名失败: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("用户名已存在")
	}
	return nil
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

func toUserResp(u *userModel.User) *adminDto.UserResp {
	if u == nil {
		return nil
	}
	return &adminDto.UserResp{
		ID:         u.ID,
		Username:   u.Username,
		Role:       u.Role,
		Avatar:     u.Avatar,
		CreateTime: u.CreateTime,
		UpdateTime: u.UpdateTime,
	}
}

func (s *AdminService) recordAudit(ctx context.Context, req auditService.RecordReq) {
	if s.auditRecorder == nil {
		return
	}
	if err := s.auditRecorder.Record(ctx, req); err != nil {
		slog.Warn("audit record failed", "err", err, "biz_type", req.BizType, "biz_id", req.BizID)
	}
}

type dashboardWindowRange struct {
	Start        time.Time
	End          time.Time
	PrevStart    time.Time
	PrevEnd      time.Time
	Label        string
	CompareLabel string
}

func dashboardWindow(window string, fallback time.Duration) dashboardWindowRange {
	duration := parseDashboardWindow(window, fallback)
	label := strings.TrimSpace(window)
	if label == "" {
		label = formatDashboardWindow(fallback)
	}
	end := time.Now()
	start := end.Add(-duration)
	return dashboardWindowRange{
		Start:        start,
		End:          end,
		PrevStart:    start.Add(-duration),
		PrevEnd:      start,
		Label:        label,
		CompareLabel: "prev_" + label,
	}
}

func dashboardKpi(value, delta int64, deltaPct *float64) adminDto.DashboardKpiResp {
	return adminDto.DashboardKpiResp{Value: value, Delta: delta, DeltaPct: deltaPct}
}

func dashboardPct(current, prev int64) *float64 {
	if prev <= 0 {
		return nil
	}
	value := roundDashboard1(float64(current-prev) * 100 / float64(prev))
	return &value
}

func dashboardRate(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return roundDashboard1(float64(part) * 100 / float64(total))
}

func dashboardAverage(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	var total int64
	for _, value := range values {
		total += value
	}
	return int64(math.Round(float64(total) / float64(len(values))))
}

func dashboardPercentile(values []int64, percentile float64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(math.Ceil(float64(len(sorted))*percentile)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func (s *AdminService) countByTimeRange(ctx context.Context, table, timeColumn string, start, end time.Time) (int64, error) {
	var count int64
	if err := s.db.WithContext(ctx).Table(table).
		Where("deleted = 0 AND "+timeColumn+" >= ? AND "+timeColumn+" < ?", start, end).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count dashboard %s: %w", table, err)
	}
	return count, nil
}

func (s *AdminService) countDistinctUsersByTimeRange(ctx context.Context, start, end time.Time) (int64, error) {
	var count int64
	if err := s.db.WithContext(ctx).Table("t_message").
		Where("deleted = 0 AND create_time >= ? AND create_time < ?", start, end).
		Distinct("user_id").
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count dashboard active users: %w", err)
	}
	return count, nil
}

func (s *AdminService) countAssistantMessages(ctx context.Context, start, end time.Time, exactContent string) (int64, error) {
	query := s.db.WithContext(ctx).Table("t_message").
		Where("deleted = 0 AND create_time >= ? AND create_time < ? AND role = ?", start, end, "assistant")
	if exactContent != "" {
		query = query.Where("content = ?", exactContent)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count dashboard assistant messages: %w", err)
	}
	return count, nil
}

// GetPerformance returns RAG performance metrics from trace runs.
func (s *AdminService) GetPerformance(ctx context.Context, window string) (*adminDto.PerformanceResp, error) {
	rangeInfo := dashboardWindow(window, 24*time.Hour)
	rows, err := s.listTraceRows(ctx, rangeInfo.Start, rangeInfo.End)
	if err != nil {
		return nil, err
	}
	durations := make([]int64, 0, len(rows))
	var successCount, errorCount int64
	for _, row := range rows {
		if row.Status == "SUCCESS" {
			successCount++
			if row.DurationMs > 0 {
				durations = append(durations, row.DurationMs)
			}
			continue
		}
		if row.Status == "ERROR" {
			errorCount++
		}
	}
	total := successCount + errorCount

	assistantCount, err := s.countAssistantMessages(ctx, rangeInfo.Start, rangeInfo.End, "")
	if err != nil {
		return nil, err
	}
	noDocCount, err := s.countAssistantMessages(ctx, rangeInfo.Start, rangeInfo.End, "未检索到与问题相关的文档内容。")
	if err != nil {
		return nil, err
	}
	var slowCount int64
	for _, duration := range durations {
		if duration > 20000 {
			slowCount++
		}
	}

	return &adminDto.PerformanceResp{
		Window:       rangeInfo.Label,
		AvgLatencyMs: dashboardAverage(durations),
		P95LatencyMs: dashboardPercentile(durations, 0.95),
		SuccessRate:  dashboardRate(successCount, total),
		ErrorRate:    dashboardRate(errorCount, total),
		NoDocRate:    dashboardRate(noDocCount, assistantCount),
		SlowRate:     dashboardRate(slowCount, int64(len(durations))),
		TotalTraces:  total,
	}, nil
}

// GetTrends returns dashboard time-series data.
func (s *AdminService) GetTrends(ctx context.Context, metric, window, granularity string) (*adminDto.TrendsResp, error) {
	duration := parseDashboardWindow(window, 7*24*time.Hour)
	resolvedGranularity := resolveDashboardGranularity(granularity, duration)
	windowLabel := strings.TrimSpace(window)
	if windowLabel == "" {
		windowLabel = formatDashboardWindow(duration)
	}
	start, end, buckets := dashboardTrendBuckets(time.Now(), duration, resolvedGranularity)
	normalizedMetric := strings.ToLower(strings.TrimSpace(metric))

	series := make([]adminDto.TrendSeries, 0)
	switch normalizedMetric {
	case "sessions":
		values, err := s.countByTime(ctx, "t_conversation", "create_time", start, end, resolvedGranularity)
		if err != nil {
			return nil, err
		}
		series = append(series, adminDto.TrendSeries{Name: "会话数", Data: trendPoints(buckets, values, resolvedGranularity)})
	case "messages", "":
		values, err := s.countByTime(ctx, "t_message", "create_time", start, end, resolvedGranularity)
		if err != nil {
			return nil, err
		}
		series = append(series, adminDto.TrendSeries{Name: "消息数", Data: trendPoints(buckets, values, resolvedGranularity)})
	case "activeusers":
		values, err := s.countActiveUsersByTime(ctx, start, end, resolvedGranularity)
		if err != nil {
			return nil, err
		}
		series = append(series, adminDto.TrendSeries{Name: "活跃用户", Data: trendPoints(buckets, values, resolvedGranularity)})
	case "avglatency":
		values, err := s.averageLatencyByTime(ctx, start, end, resolvedGranularity)
		if err != nil {
			return nil, err
		}
		series = append(series, adminDto.TrendSeries{Name: "平均响应时间", Data: trendPoints(buckets, values, resolvedGranularity)})
	case "quality":
		errorRate, noDocRate, err := s.qualityRatesByTime(ctx, start, end, resolvedGranularity)
		if err != nil {
			return nil, err
		}
		series = append(series,
			adminDto.TrendSeries{Name: "错误率", Data: trendPoints(buckets, errorRate, resolvedGranularity)},
			adminDto.TrendSeries{Name: "无知识率", Data: trendPoints(buckets, noDocRate, resolvedGranularity)},
		)
	}

	return &adminDto.TrendsResp{
		Metric:      metric,
		Window:      windowLabel,
		Granularity: resolvedGranularity,
		Series:      series,
	}, nil
}

type dashboardTimeRow struct {
	CreateTime time.Time `gorm:"column:create_time"`
	UserID     string    `gorm:"column:user_id"`
	Role       string    `gorm:"column:role"`
	Content    string    `gorm:"column:content"`
}

type dashboardTraceRow struct {
	StartTime  time.Time `gorm:"column:start_time"`
	Status     string    `gorm:"column:status"`
	DurationMs int64     `gorm:"column:duration_ms"`
}

func (s *AdminService) countByTime(ctx context.Context, table, timeColumn string, start, end time.Time, granularity string) (map[time.Time]float64, error) {
	var rows []dashboardTimeRow
	if err := s.db.WithContext(ctx).Table(table).
		Select(timeColumn+" AS create_time").
		Where("deleted = 0 AND "+timeColumn+" >= ? AND "+timeColumn+" < ?", start, end).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("query dashboard trend %s: %w", table, err)
	}
	values := make(map[time.Time]float64)
	for _, row := range rows {
		values[dashboardBucket(row.CreateTime, granularity)]++
	}
	return values, nil
}

func (s *AdminService) countActiveUsersByTime(ctx context.Context, start, end time.Time, granularity string) (map[time.Time]float64, error) {
	var rows []dashboardTimeRow
	if err := s.db.WithContext(ctx).Table("t_message").
		Select("create_time, user_id").
		Where("deleted = 0 AND create_time >= ? AND create_time < ?", start, end).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("query dashboard active users: %w", err)
	}
	sets := make(map[time.Time]map[string]bool)
	for _, row := range rows {
		bucket := dashboardBucket(row.CreateTime, granularity)
		if sets[bucket] == nil {
			sets[bucket] = make(map[string]bool)
		}
		sets[bucket][row.UserID] = true
	}
	values := make(map[time.Time]float64, len(sets))
	for bucket, users := range sets {
		values[bucket] = float64(len(users))
	}
	return values, nil
}

func (s *AdminService) averageLatencyByTime(ctx context.Context, start, end time.Time, granularity string) (map[time.Time]float64, error) {
	rows, err := s.listTraceRows(ctx, start, end)
	if err != nil {
		return nil, err
	}
	sum := make(map[time.Time]float64)
	count := make(map[time.Time]float64)
	for _, row := range rows {
		if row.Status != "SUCCESS" || row.DurationMs <= 0 {
			continue
		}
		bucket := dashboardBucket(row.StartTime, granularity)
		sum[bucket] += float64(row.DurationMs)
		count[bucket]++
	}
	values := make(map[time.Time]float64, len(sum))
	for bucket, total := range sum {
		values[bucket] = roundDashboard1(total / count[bucket])
	}
	return values, nil
}

func (s *AdminService) qualityRatesByTime(ctx context.Context, start, end time.Time, granularity string) (map[time.Time]float64, map[time.Time]float64, error) {
	traceRows, err := s.listTraceRows(ctx, start, end)
	if err != nil {
		return nil, nil, err
	}
	success := make(map[time.Time]float64)
	failures := make(map[time.Time]float64)
	for _, row := range traceRows {
		bucket := dashboardBucket(row.StartTime, granularity)
		if row.Status == "ERROR" {
			failures[bucket]++
		} else if row.Status == "SUCCESS" {
			success[bucket]++
		}
	}

	var messageRows []dashboardTimeRow
	if err := s.db.WithContext(ctx).Table("t_message").
		Select("create_time, role, content").
		Where("deleted = 0 AND create_time >= ? AND create_time < ?", start, end).
		Scan(&messageRows).Error; err != nil {
		return nil, nil, fmt.Errorf("query dashboard quality messages: %w", err)
	}
	assistant := make(map[time.Time]float64)
	noDoc := make(map[time.Time]float64)
	for _, row := range messageRows {
		if row.Role != "assistant" {
			continue
		}
		bucket := dashboardBucket(row.CreateTime, granularity)
		assistant[bucket]++
		if row.Content == "未检索到与问题相关的文档内容。" {
			noDoc[bucket]++
		}
	}

	errorRate := make(map[time.Time]float64)
	noDocRate := make(map[time.Time]float64)
	for bucket, errCount := range failures {
		total := errCount + success[bucket]
		if total > 0 {
			errorRate[bucket] = roundDashboard1(errCount * 100 / total)
		}
	}
	for bucket, noDocCount := range noDoc {
		if assistant[bucket] > 0 {
			noDocRate[bucket] = roundDashboard1(noDocCount * 100 / assistant[bucket])
		}
	}
	return errorRate, noDocRate, nil
}

func (s *AdminService) listTraceRows(ctx context.Context, start, end time.Time) ([]dashboardTraceRow, error) {
	var rows []dashboardTraceRow
	if err := s.db.WithContext(ctx).Table("t_rag_trace_run").
		Select("start_time, status, duration_ms").
		Where("deleted = 0 AND start_time >= ? AND start_time < ?", start, end).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("query dashboard trace trend: %w", err)
	}
	return rows, nil
}

func trendPoints(buckets []time.Time, values map[time.Time]float64, granularity string) []adminDto.TrendPoint {
	points := make([]adminDto.TrendPoint, 0, len(buckets))
	for _, bucket := range buckets {
		points = append(points, adminDto.TrendPoint{
			Ts:    bucket.UnixMilli(),
			Value: values[dashboardBucket(bucket, granularity)],
		})
	}
	return points
}

func dashboardTrendBuckets(now time.Time, duration time.Duration, granularity string) (time.Time, time.Time, []time.Time) {
	step := 24 * time.Hour
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1)
	if granularity == "hour" {
		step = time.Hour
		end = now.Truncate(time.Hour).Add(time.Hour)
	}
	count := int(math.Ceil(duration.Hours() / step.Hours()))
	if count < 1 {
		count = 1
	}
	start := end.Add(-time.Duration(count) * step)
	buckets := make([]time.Time, 0, count)
	for cursor := start; cursor.Before(end); cursor = cursor.Add(step) {
		buckets = append(buckets, cursor)
	}
	return start, end, buckets
}

func dashboardBucket(t time.Time, granularity string) time.Time {
	t = t.In(time.Local)
	if granularity == "hour" {
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.Local)
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

func parseDashboardWindow(window string, fallback time.Duration) time.Duration {
	normalized := strings.ToLower(strings.TrimSpace(window))
	if normalized == "" {
		return fallback
	}
	unit := normalized[len(normalized)-1:]
	value := strings.TrimSpace(normalized[:len(normalized)-1])
	var number int64
	if _, err := fmt.Sscan(value, &number); err != nil || number <= 0 {
		return fallback
	}
	if unit == "h" {
		return time.Duration(number) * time.Hour
	}
	if unit == "d" {
		return time.Duration(number) * 24 * time.Hour
	}
	return fallback
}

func resolveDashboardGranularity(granularity string, duration time.Duration) string {
	normalized := strings.ToLower(strings.TrimSpace(granularity))
	if normalized == "hour" || normalized == "day" {
		return normalized
	}
	if duration <= 48*time.Hour {
		return "hour"
	}
	return "day"
}

func formatDashboardWindow(duration time.Duration) string {
	hours := int64(duration.Hours())
	if hours%24 == 0 {
		return fmt.Sprintf("%dd", hours/24)
	}
	return fmt.Sprintf("%dh", hours)
}

func roundDashboard1(value float64) float64 {
	return math.Round(value*10) / 10
}
