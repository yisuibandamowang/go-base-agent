package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go-base-agent/internal/biz/crawler"
	"go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/biz/knowledge/repo"
	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/framework/db"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

type scheduleFileWriter interface {
	Put(docID string, name string, data []byte)
}

type scheduleKnowledgeFileWriter interface {
	PutWithCollection(ctx context.Context, collectionName, docID, name string, data []byte) error
}

type scheduleChunkStarter interface {
	StartChunk(ctx context.Context, docID string, userID string) error
}

type scheduleChunkSynchronizer interface {
	RunChunkNow(ctx context.Context, docID string, userID string) error
}

const scheduleLeaseLostNote = "（调度锁已失效，未写回调度状态）"

// DocumentScheduleService 执行知识库文档定时刷新。
type DocumentScheduleService struct {
	db                *gorm.DB
	docRepo           *repo.KnowledgeDocumentRepo
	kbRepo            *repo.KnowledgeBaseRepo
	scheduleRepo      *repo.KnowledgeDocumentScheduleRepo
	fileStore         scheduleFileWriter
	chunkStarter      scheduleChunkStarter
	cfg               config.RAGKnowledgeScheduleConfig
	sources           map[string]crawler.Source
	owner             string
	now               func() time.Time
	parser            cron.Parser
	lockRenewObserver func(scheduleID string, lockUntil time.Time)
}

const scheduleRunningRecoveryTimeout = 10 * time.Minute

// NewDocumentScheduleService 创建文档定时刷新服务。
func NewDocumentScheduleService(
	db *gorm.DB,
	docRepo *repo.KnowledgeDocumentRepo,
	kbRepo *repo.KnowledgeBaseRepo,
	scheduleRepo *repo.KnowledgeDocumentScheduleRepo,
	fileStore scheduleFileWriter,
	chunkStarter scheduleChunkStarter,
	cfg config.RAGKnowledgeScheduleConfig,
) *DocumentScheduleService {
	host, _ := os.Hostname()
	if strings.TrimSpace(host) == "" {
		host = "ragent"
	}
	return &DocumentScheduleService{
		db:           db,
		docRepo:      docRepo,
		kbRepo:       kbRepo,
		scheduleRepo: scheduleRepo,
		fileStore:    fileStore,
		chunkStarter: chunkStarter,
		cfg:          cfg,
		sources:      make(map[string]crawler.Source),
		owner:        fmt.Sprintf("%s-%d", host, os.Getpid()),
		now:          time.Now,
		parser: cron.NewParser(
			cron.SecondOptional |
				cron.Minute |
				cron.Hour |
				cron.Dom |
				cron.Month |
				cron.Dow |
				cron.Descriptor,
		),
	}
}

// RegisterSource 注册一个远程文档来源。
func (s *DocumentScheduleService) RegisterSource(source crawler.Source) {
	if source == nil {
		return
	}
	s.sources[strings.ToLower(strings.TrimSpace(source.Name()))] = source
}

// Run 定时扫描到期文档刷新任务。
func (s *DocumentScheduleService) Run(ctx context.Context) {
	delay := time.Duration(s.cfg.ScanDelayMs) * time.Millisecond
	if delay <= 0 {
		delay = 10 * time.Second
	}
	recoveryTicker := time.NewTicker(time.Minute)
	defer recoveryTicker.Stop()
	ticker := time.NewTicker(delay)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-recoveryTicker.C:
			if recovered, err := s.RecoverStuckRunningDocuments(ctx); err != nil {
				slog.Warn("knowledge document running recovery failed", "err", err)
			} else if recovered > 0 {
				slog.Warn("reset stuck running documents", "count", recovered, "timeout", scheduleRunningRecoveryTimeout.String())
			}
		case <-ticker.C:
			if _, err := s.ScanDue(ctx); err != nil {
				slog.Warn("knowledge document schedule scan failed", "err", err)
			}
		}
	}
}

// ScanDue 扫描并执行到期的文档刷新任务。
func (s *DocumentScheduleService) ScanDue(ctx context.Context) (int, error) {
	if s == nil || s.db == nil || s.scheduleRepo == nil || s.docRepo == nil || s.fileStore == nil || s.chunkStarter == nil {
		return 0, nil
	}
	now := s.now()
	lockSeconds := s.cfg.LockSeconds
	if lockSeconds <= 0 {
		lockSeconds = 900
	}
	schedules, err := s.scheduleRepo.ClaimDue(ctx, now, s.cfg.BatchSize, s.owner, now.Add(time.Duration(lockSeconds)*time.Second))
	if err != nil {
		return 0, fmt.Errorf("claim due document schedules: %w", err)
	}
	for _, schedule := range schedules {
		if err := s.refreshOne(ctx, schedule, now); err != nil {
			slog.Warn("knowledge document schedule refresh failed", "scheduleId", schedule.ID, "docId", schedule.DocID, "err", err)
		}
	}
	return len(schedules), nil
}

// RecoverStuckRunningDocuments 将超时卡在 running 的文档重置为 failed。
func (s *DocumentScheduleService) RecoverStuckRunningDocuments(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	cutoff := s.now().Add(-scheduleRunningRecoveryTimeout)
	result := s.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Model(&model.KnowledgeDocument{}).
		Where("status = ? AND update_time < ?", "running", cutoff).
		Updates(map[string]any{
			"status":      "failed",
			"updated_by":  "system",
			"update_time": s.now(),
		})
	if result.Error != nil {
		return 0, fmt.Errorf("recover stuck running documents: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (s *DocumentScheduleService) refreshOne(ctx context.Context, schedule model.KnowledgeDocumentSchedule, startedAt time.Time) error {
	stopHeartbeat := s.startLockHeartbeat(ctx, schedule)
	if stopHeartbeat != nil {
		defer stopHeartbeat()
	}
	doc, err := s.docRepo.FindByID(ctx, schedule.DocID)
	if err != nil {
		return s.markFailed(ctx, schedule, startedAt, fmt.Errorf("find document: %w", err), nil)
	}
	if doc.ScheduleEnabled != 1 || schedule.Enabled != 1 {
		return s.markSkipped(ctx, schedule, startedAt, "定时已关闭", nil, "")
	}
	source := s.sourceForDocument(doc)
	if source == nil {
		return s.markFailed(ctx, schedule, startedAt, fmt.Errorf("document source %q is not registered", doc.SourceType), nil)
	}
	location := firstNonEmpty(documentSourceURL(doc), doc.SourceLocation, doc.FileURL)
	if strings.TrimSpace(location) == "" {
		return s.markFailed(ctx, schedule, startedAt, fmt.Errorf("document source location is empty"), nil)
	}
	remoteDoc, err := source.FetchDocument(ctx, location)
	if err != nil {
		return s.markFailed(ctx, schedule, startedAt, fmt.Errorf("fetch remote document: %w", err), nil)
	}
	if remoteDoc == nil || len(remoteDoc.Content) == 0 {
		return s.markFailed(ctx, schedule, startedAt, fmt.Errorf("remote document content is empty"), remoteDoc)
	}

	contentHash := sha256Hex(remoteDoc.Content)
	fileName := firstNonEmpty(remoteDoc.Meta.Title, doc.DocName, filepath.Base(location))
	etag := remoteDoc.Meta.Extra["etag"]
	lastModified := remoteDoc.Meta.Extra["last_modified"]
	unchanged := contentHash == schedule.LastContentHash && schedule.LastContentHash != ""
	if unchanged {
		return s.markSkipped(ctx, schedule, startedAt, "远程文件未变化", remoteDoc, contentHash)
	}
	if doc.Status == "running" {
		return s.markSkipped(ctx, schedule, startedAt, "文档正在分块中，跳过本次调度", remoteDoc, contentHash)
	}
	if writer, ok := s.fileStore.(scheduleKnowledgeFileWriter); ok && s.kbRepo != nil {
		if kb, err := s.kbRepo.FindByID(ctx, doc.KbID); err == nil && kb != nil && strings.TrimSpace(kb.CollectionName) != "" {
			if err := writer.PutWithCollection(ctx, kb.CollectionName, doc.ID, fileName, remoteDoc.Content); err != nil {
				return s.markFailed(ctx, schedule, startedAt, fmt.Errorf("save file content: %w", err), remoteDoc)
			}
		} else {
			s.fileStore.Put(doc.ID, fileName, remoteDoc.Content)
		}
	} else {
		s.fileStore.Put(doc.ID, fileName, remoteDoc.Content)
	}
	if err := s.updateDocumentAfterFetch(ctx, doc, remoteDoc, fileName); err != nil {
		return s.markFailed(ctx, schedule, startedAt, err, remoteDoc)
	}
	if syncRunner, ok := s.chunkStarter.(scheduleChunkSynchronizer); ok {
		if err := syncRunner.RunChunkNow(ctx, doc.ID, doc.CreatedBy); err != nil {
			return s.markFailed(ctx, schedule, startedAt, fmt.Errorf("run chunk now: %w", err), remoteDoc)
		}
	} else {
		if err := s.chunkStarter.StartChunk(ctx, doc.ID, doc.CreatedBy); err != nil {
			return s.markFailed(ctx, schedule, startedAt, fmt.Errorf("start chunk: %w", err), remoteDoc)
		}
	}

	nextRun, err := s.nextRunTime(schedule.CronExpr, startedAt)
	if err != nil {
		return s.markFailed(ctx, schedule, startedAt, fmt.Errorf("parse cron: %w", err), remoteDoc)
	}
	if stopHeartbeat != nil {
		stopHeartbeat()
	}
	now := s.now()
	updates := map[string]any{
		"last_run_time":     startedAt,
		"last_success_time": now,
		"last_status":       "success",
		"last_error":        "",
		"last_etag":         etag,
		"last_modified":     lastModified,
		"last_content_hash": contentHash,
		"next_run_time":     nextRun,
		"lock_owner":        "",
		"lock_until":        nil,
		"update_time":       now,
	}
	scheduleUpdated, err := s.updateScheduleIfOwned(ctx, schedule, updates)
	if err != nil {
		_ = s.recordExec(ctx, schedule, "success", "refreshed", startedAt, now, remoteDoc, contentHash, false)
		return fmt.Errorf("update schedule success: %w", err)
	}
	return s.recordExec(ctx, schedule, "success", "refreshed", startedAt, now, remoteDoc, contentHash, scheduleUpdated)
}

func (s *DocumentScheduleService) updateDocumentAfterFetch(ctx context.Context, doc *model.KnowledgeDocument, remoteDoc *crawler.Document, fileName string) error {
	updates := map[string]any{
		"doc_name":    fileName,
		"file_size":   int64(len(remoteDoc.Content)),
		"update_time": s.now(),
	}
	if remoteDoc.Meta.MimeType != "" {
		if fileType := fileTypeFromMimeOrName(remoteDoc.Meta.MimeType, fileName); fileType != "" {
			updates["file_type"] = fileType
		}
	}
	if remoteDoc.Meta.URL != "" {
		updates["source_location"] = remoteDoc.Meta.URL
		updates["file_url"] = remoteDoc.Meta.URL
	}
	if err := s.db.WithContext(ctx).Model(&model.KnowledgeDocument{}).Where("id = ?", doc.ID).Updates(updates).Error; err != nil {
		return fmt.Errorf("update fetched document: %w", err)
	}
	return nil
}

func (s *DocumentScheduleService) markFailed(ctx context.Context, schedule model.KnowledgeDocumentSchedule, startedAt time.Time, cause error, remoteDoc *crawler.Document) error {
	now := s.now()
	nextRun := startedAt.Add(time.Duration(maxInt(s.cfg.MinIntervalSeconds, 60)) * time.Second)
	scheduleUpdated, _ := s.updateScheduleIfOwned(ctx, schedule, map[string]any{
		"last_run_time": startedAt,
		"last_status":   "failed",
		"last_error":    truncateString(cause.Error(), 512),
		"next_run_time": nextRun,
		"lock_owner":    "",
		"lock_until":    nil,
		"update_time":   now,
	})
	var contentHash string
	if remoteDoc != nil {
		contentHash = sha256Hex(remoteDoc.Content)
	}
	_ = s.recordExec(ctx, schedule, "failed", truncateString(cause.Error(), 512), startedAt, now, remoteDoc, contentHash, scheduleUpdated)
	return cause
}

func (s *DocumentScheduleService) markSkipped(ctx context.Context, schedule model.KnowledgeDocumentSchedule, startedAt time.Time, message string, remoteDoc *crawler.Document, contentHash string) error {
	nextRun, err := s.nextRunTime(schedule.CronExpr, startedAt)
	if err != nil {
		return s.markFailed(ctx, schedule, startedAt, fmt.Errorf("parse cron: %w", err), remoteDoc)
	}
	now := s.now()
	updates := map[string]any{
		"last_run_time": startedAt,
		"last_status":   "skipped",
		"last_error":    truncateString(message, 512),
		"next_run_time": nextRun,
		"lock_owner":    "",
		"lock_until":    nil,
		"update_time":   now,
	}
	if remoteDoc != nil {
		updates["last_etag"] = remoteDoc.Meta.Extra["etag"]
		updates["last_modified"] = remoteDoc.Meta.Extra["last_modified"]
		updates["last_content_hash"] = contentHash
	}
	scheduleUpdated, err := s.updateScheduleIfOwned(ctx, schedule, updates)
	if err != nil {
		_ = s.recordExec(ctx, schedule, "skipped", message, startedAt, now, remoteDoc, contentHash, false)
		return fmt.Errorf("update schedule skipped: %w", err)
	}
	return s.recordExec(ctx, schedule, "skipped", message, startedAt, now, remoteDoc, contentHash, scheduleUpdated)
}

func (s *DocumentScheduleService) updateScheduleIfOwned(ctx context.Context, schedule model.KnowledgeDocumentSchedule, updates map[string]any) (bool, error) {
	query := s.db.WithContext(ctx).Model(&model.KnowledgeDocumentSchedule{}).Where("id = ?", schedule.ID)
	if schedule.LockOwner != "" {
		query = query.Where("lock_owner = ?", schedule.LockOwner)
	}
	result := query.Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (s *DocumentScheduleService) startLockHeartbeat(ctx context.Context, schedule model.KnowledgeDocumentSchedule) func() {
	if strings.TrimSpace(schedule.LockOwner) == "" {
		return nil
	}
	lockSeconds := s.cfg.LockSeconds
	if lockSeconds <= 0 {
		lockSeconds = 900
	}
	interval := time.Duration(lockSeconds) * time.Second / 3
	if interval < 500*time.Millisecond {
		interval = 500 * time.Millisecond
	}
	hbCtx, cancel := context.WithCancel(ctx)
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				renewUntil := time.Now().Add(time.Duration(lockSeconds) * time.Second)
				updated, err := s.updateScheduleIfOwned(hbCtx, schedule, map[string]any{
					"lock_until":  renewUntil,
					"update_time": s.now(),
				})
				if err != nil {
					slog.Warn("knowledge document schedule heartbeat renew failed", "scheduleId", schedule.ID, "err", err)
					continue
				}
				if !updated {
					slog.Warn("knowledge document schedule heartbeat lost", "scheduleId", schedule.ID, "lockOwner", schedule.LockOwner)
					cancel()
					return
				}
				if s.lockRenewObserver != nil {
					s.lockRenewObserver(schedule.ID, renewUntil)
				}
			}
		}
	}()
	return cancel
}

func (s *DocumentScheduleService) recordExec(ctx context.Context, schedule model.KnowledgeDocumentSchedule, status, message string, start, end time.Time, remoteDoc *crawler.Document, contentHash string, scheduleUpdated bool) error {
	if !scheduleUpdated {
		message = strings.TrimSpace(message) + scheduleLeaseLostNote
	}
	exec := &model.KnowledgeDocumentScheduleExec{
		ScheduleID:  schedule.ID,
		DocID:       schedule.DocID,
		KbID:        schedule.KbID,
		Status:      status,
		Message:     truncateString(message, 512),
		StartTime:   &start,
		EndTime:     &end,
		ContentHash: contentHash,
	}
	if remoteDoc != nil {
		exec.FileName = remoteDoc.Meta.Title
		exec.FileSize = int64(len(remoteDoc.Content))
		exec.ETag = remoteDoc.Meta.Extra["etag"]
		exec.LastModified = remoteDoc.Meta.Extra["last_modified"]
	}
	return s.scheduleRepo.CreateExec(ctx, exec)
}

func (s *DocumentScheduleService) nextRunTime(expr string, from time.Time) (time.Time, error) {
	parsed, err := s.parser.Parse(strings.TrimSpace(expr))
	if err != nil {
		return time.Time{}, err
	}
	next := parsed.Next(from)
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("cron expression has no next run")
	}
	minInterval := s.cfg.MinIntervalSeconds
	if minInterval > 0 && next.Before(from.Add(time.Duration(minInterval)*time.Second)) {
		next = from.Add(time.Duration(minInterval) * time.Second)
	}
	return next, nil
}

func (s *DocumentScheduleService) sourceForDocument(doc *model.KnowledgeDocument) crawler.Source {
	keys := []string{
		strings.ToLower(strings.TrimSpace(doc.SourceType)),
	}
	location := firstNonEmpty(doc.SourceLocation, doc.FileURL)
	if isConfluenceDocumentURL(location) {
		keys = append([]string{"confluence"}, keys...)
	}
	if isFeishuDocumentURL(location) {
		keys = append([]string{"feishu"}, keys...)
	}
	if parsed, err := url.Parse(location); err == nil {
		if parsed.Scheme == "http" || parsed.Scheme == "https" {
			keys = append(keys, "url", "http")
		}
	}
	for _, key := range keys {
		if source := s.sources[key]; source != nil {
			return source
		}
	}
	return nil
}

func isFeishuDocumentURL(location string) bool {
	parsed, err := url.Parse(strings.TrimSpace(location))
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Host)
	path := strings.ToLower(parsed.Path)
	return strings.Contains(host, "feishu.cn") && (strings.Contains(path, "/wiki/") || strings.Contains(path, "/docx/") || strings.Contains(path, "/docs/"))
}

func isConfluenceDocumentURL(location string) bool {
	parsed, err := url.Parse(strings.TrimSpace(location))
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Host)
	path := strings.ToLower(parsed.Path)
	return (strings.Contains(path, "/wiki/spaces/") && strings.Contains(path, "/pages/")) ||
		(strings.Contains(host, "atlassian.net") && strings.Contains(path, "/wiki/"))
}

func sha256Hex(data []byte) string {
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash[:])
}

func fileTypeFromMimeOrName(mimeType, fileName string) string {
	switch normalizeMIMEType(mimeType) {
	case "text/markdown":
		return "md"
	case "text/html":
		return "html"
	case "text/plain":
		return "txt"
	case "application/pdf":
		return "pdf"
	case "application/json":
		return "json"
	case "application/xml", "text/xml":
		return "xml"
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return "pptx"
	case "application/vnd.ms-powerpoint":
		return "ppt"
	}
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(fileName)), ".")
}

func normalizeMIMEType(mimeType string) string {
	lower := strings.ToLower(strings.TrimSpace(mimeType))
	if idx := strings.Index(lower, ";"); idx >= 0 {
		lower = strings.TrimSpace(lower[:idx])
	}
	return lower
}

func truncateString(value string, maxLen int) string {
	if maxLen <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxLen {
		return value
	}
	return string(runes[:maxLen])
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
